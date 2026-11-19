package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func standingRegistryGrant() StandingAuthorization {
	return StandingAuthorization{MachineID: "box", Directory: "/code/example", Actions: []string{"merge"}, Conditions: []string{"ci_green", "no_conflicts"}, Quote: "Merge the example project when checks pass and there are no conflicts.", Source: "main conversation, user message", Reversible: true}
}
func standingJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStandingAuthorizationRegistryDatabaseAndRoutes(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	s := newServiceOn(t, newFakeHub().withMachine(), database)
	before, err := s.standingAuthorizations(ctx)
	if err != nil || before.Revision != 0 || len(before.Authorizations) != 0 {
		t.Fatalf("fresh registry: %+v %v", before, err)
	}
	grant := standingRegistryGrant()
	grant.GrantedAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Now().Add(-time.Second)
	change, err := s.setStandingAuthorizations(ctx, standingJSON(t, StandingAuthorizations{Authorizations: []StandingAuthorization{grant}}))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := change.apply(ctx); err != nil || !strings.Contains(out, `"revision":1`) {
		t.Fatalf("save: %s %v", out, err)
	}
	list, err := s.standingAuthorizations(ctx)
	if err != nil || len(list.Authorizations) != 1 || list.Revision != 1 {
		t.Fatalf("read after save: %+v %v", list, err)
	}
	unchanged, err := s.setStandingAuthorizations(ctx, standingJSON(t, list))
	if err != nil || unchanged.apply != nil {
		t.Fatalf("unchanged list asks again: %+v %v", unchanged, err)
	}
	stored := list.Authorizations[0]
	if stored.ID == "" || stored.GrantedAt.Before(start) || stored.GrantedAt.Equal(grant.GrantedAt) {
		t.Fatalf("server identity/time: %+v", stored)
	}
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	f.answerNode("git.status", func(_ string, args json.RawMessage) (any, error) {
		var params struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		root := params.Cwd
		if root == "/code/example/server" {
			root = "/code/example"
		}
		return proto.RepositoryIdentity{Version: 1, IsRepo: true, Cwd: params.Cwd, Root: root, CommonDir: root + "/.git"}, nil
	})
	s2 := newServiceOn(t, f, database)
	if got, err := s2.standingAuthorizations(ctx); err != nil || !reflect.DeepEqual(list, got) {
		t.Fatalf("restart: %+v %v", got, err)
	}
	for _, c := range []struct {
		machine, cwd string
		count        int
	}{{"box", "/code/example", 1}, {"box", "/code/example/server", 1}, {"box", "/code/example-copy", 0}, {"elsewhere", "/code/example", 0}, {"box", "/code/other", 0}, {"box", "~/code/example", 0}} {
		got, err := s2.standingForDirectory(ctx, c.machine, c.cwd)
		if err != nil || len(got) != c.count {
			t.Errorf("match %s %s: %+v %v", c.machine, c.cwd, got, err)
		}
	}
	base := "/api/v1/assistant/standing-authorizations"
	if w := serve(s2, "GET", base, ""); w.Code != 200 || !strings.Contains(w.Body.String(), stored.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{"GET", "DELETE"} {
		uri := base
		if method == "DELETE" {
			uri += "/" + stored.ID
		}
		r := httptest.NewRequest(method, uri, nil)
		r.Header.Set(RoleHeader, "answer")
		w := httptest.NewRecorder()
		s2.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("limited role %s: %d %s", method, w.Code, w.Body)
		}
	}
	for range 2 {
		if w := serve(s2, "DELETE", base+"/"+stored.ID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":2`) {
			t.Fatalf("revoke/retry: %d %s", w.Code, w.Body)
		}
	}
	if got, _ := s.standingForDirectory(ctx, "box", "/code/example"); len(got) != 0 {
		t.Fatal("old manager cached revoked grant")
	}
	if w := serve(s, "GET", base, ""); !strings.Contains(w.Body.String(), `"authorizations":[]`) {
		t.Fatalf("empty list: %s", w.Body)
	}
	t.Log("server-assigned identity/time, restart, exact scopes, full-role HTTP and idempotent revocation: PASS")
}

func TestStandingAuthorizationSettingRequiresUserApproval(t *testing.T) {
	ctx := context.Background()
	yes, no := true, false
	for _, c := range []struct {
		name   string
		answer proto.Resolution
		saved  bool
	}{{"user approves", proto.Resolution{Allow: &yes}, true}, {"user refuses", proto.Resolution{Allow: &no}, false}, {"assistant cannot approve", proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant}, false}, {"dismissed", proto.Resolution{Dismiss: true}, false}} {
		t.Run(c.name, func(t *testing.T) {
			s := newServiceOn(t, newFakeHub().withMachine(), taskRecoveryDatabase(t))
			setSettings(t, s, func(v *Settings) { v.Main = "main-1" })
			args := standingJSON(t, map[string]any{"action": "set", "key": standingAuthorizationsKey, "value": StandingAuthorizations{Authorizations: []StandingAuthorization{standingRegistryGrant()}}})
			for _, caller := range []struct{ machine, session string }{{"server", "worker-1"}, {"box", "main-1"}} {
				r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: caller.machine, SessionID: caller.session, Arguments: args})
				if !r.IsError || !strings.Contains(r.Content[0].Text, "only the main conversation") {
					t.Fatalf("non-main: %+v", r)
				}
			}
			done := make(chan proto.ToolResult, 1)
			go func() {
				done <- s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main-1", Arguments: args})
			}()
			card, ok := awaitCard(s)
			if !ok {
				t.Fatal("registration did not ask user")
			}
			if card.Kind != proto.IxPermission || !strings.Contains(card.Summary, "/code/example") || !strings.Contains(card.Summary, standingRegistryGrant().Quote) || !strings.Contains(card.Summary, "ci_green") {
				t.Fatalf("missing grant details: %+v", card)
			}
			if list, _ := s.standingAuthorizations(ctx); len(list.Authorizations) != 0 {
				t.Fatal("grant active before approval")
			}
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: standingJSON(t, c.answer)}); err != nil {
				t.Fatal(err)
			}
			select {
			case res := <-done:
				if res.IsError {
					t.Fatalf("setting: %+v", res)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("setting did not finish")
			}
			list, err := s.standingAuthorizations(ctx)
			if err != nil || (len(list.Authorizations) > 0) != c.saved {
				t.Fatalf("save: %+v %v", list, err)
			}
		})
	}
}

func TestStandingAuthorizationStaleApprovalCannotRestoreRevocation(t *testing.T) {
	ctx := context.Background()
	s := newServiceOn(t, newFakeHub().withMachine(), taskRecoveryDatabase(t))
	first, err := s.setStandingAuthorizations(ctx, standingJSON(t, StandingAuthorizations{Authorizations: []StandingAuthorization{standingRegistryGrant()}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.apply(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := s.standingAuthorizations(ctx)
	next := standingRegistryGrant()
	next.Directory = "/code/another"
	pending := before
	pending.Authorizations = append(append([]StandingAuthorization(nil), before.Authorizations...), next)
	change, err := s.setStandingAuthorizations(ctx, standingJSON(t, pending))
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(s, "DELETE", "/api/v1/assistant/standing-authorizations/"+before.Authorizations[0].ID, ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if _, err := change.apply(ctx); !errors.Is(err, errStandingConflict) {
		t.Fatalf("stale phone approval: %v", err)
	}
	list, _ := s.standingAuthorizations(ctx)
	if len(list.Authorizations) != 0 || list.Revision != 2 {
		t.Fatalf("stale approval restored grant: %+v", list)
	}
	if _, err := s.setStandingAuthorizations(ctx, standingJSON(t, pending)); !errors.Is(err, errStandingConflict) {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestStandingAuthorizationConflictReportsDefinitelyNotApplied(t *testing.T) {
	ctx := context.Background()
	s := newServiceOn(t, newFakeHub().withMachine(), taskRecoveryDatabase(t))
	first, err := s.setStandingAuthorizations(ctx, standingJSON(t, StandingAuthorizations{Authorizations: []StandingAuthorization{standingRegistryGrant()}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.apply(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := s.standingAuthorizations(ctx)
	next := standingRegistryGrant()
	next.Directory = "/code/another"
	pending := before
	pending.Authorizations = append(append([]StandingAuthorization(nil), before.Authorizations...), next)
	var setting ownSetting
	for _, o := range s.ownSettings() {
		if o.Key == standingAuthorizationsKey {
			setting = o
		}
	}
	done := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := s.ownSettingAction(ctx, ToolCall{MachineID: "server", SessionID: "main-1"}, setting, "set", standingJSON(t, pending))
		done <- struct {
			text string
			err  error
		}{text, err}
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("the standing authorization change did not wait for approval")
	}
	if w := serve(s, "DELETE", "/api/v1/assistant/standing-authorizations/"+before.Authorizations[0].ID, ""); w.Code != 200 {
		t.Fatalf("revoke while the card waits: %d %s", w.Code, w.Body)
	}
	allow := true
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: standingJSON(t, proto.Resolution{Allow: &allow})}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(got.text, "Nothing was applied") || strings.Contains(got.text, "uncertain") {
			t.Fatalf("stale standing authorization outcome = %q, %v", got.text, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stale standing authorization approval did not finish")
	}
	list, _ := s.standingAuthorizations(ctx)
	if len(list.Authorizations) != 0 || list.Revision != 2 {
		t.Fatalf("stale approval restored a revoked grant: %+v", list)
	}
}

func TestStandingAuthorizationRejectsBroadOrMutatedGrants(t *testing.T) {
	for _, dir := range []string{"/", "/code", "~/code/example", "relative/path", "/code/*", "/code/app/../other", "/code/example/"} {
		grant := standingRegistryGrant()
		grant.Directory = dir
		if _, err := prepareStandingChange(StandingAuthorizations{}, StandingAuthorizations{Authorizations: []StandingAuthorization{grant}}, time.Now()); err == nil {
			t.Errorf("broad/ambiguous: %q", dir)
		}
	}
	for _, field := range []string{"machine", "actions", "quote", "source"} {
		grant := standingRegistryGrant()
		switch field {
		case "machine":
			grant.MachineID = ""
		case "actions":
			grant.Actions = []string{"all"}
		case "quote":
			grant.Quote = ""
		case "source":
			grant.Source = ""
		}
		if err := checkStandingGrant(grant); err == nil {
			t.Errorf("invalid %s", field)
		}
	}
	grant := standingRegistryGrant()
	grant.ID = "sa_existing"
	grant.GrantedAt = time.Now()
	before := StandingAuthorizations{Revision: 1, Authorizations: []StandingAuthorization{grant}}
	grant.Actions = []string{"merge", "deploy-production"}
	if _, err := prepareStandingChange(before, StandingAuthorizations{Revision: 1, Authorizations: []StandingAuthorization{grant}}, time.Now()); err == nil {
		t.Fatal("existing grant silently expanded")
	}
}
func TestGoldenStandingAuthorizationSettingsDescription(t *testing.T) {
	golden(t, "standing_authorizations.settings_description", prompts.Tool("persona_settings").Description)
}

type unavailableStandingStore struct{ Store }

func (st unavailableStandingStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == standingAuthorizationsKey {
		return false, errors.New("authorization registry unavailable")
	}
	return st.Store.Setting(ctx, key, out)
}

func TestStandingAuthorizationReadFailureIsNotEmptyRegistry(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s := newServiceOn(t, newFakeHub().withMachine(), unavailableStandingStore{Store: database})
	if _, err := s.standingForDirectory(context.Background(), "box", "/code/example"); err == nil {
		t.Fatal("failed registry read treated as no grants")
	}
	for _, request := range []struct{ method, path string }{{"GET", "/api/v1/assistant/standing-authorizations"}, {"DELETE", "/api/v1/assistant/standing-authorizations/sa_example"}} {
		if w := serve(s, request.method, request.path, ""); w.Code != 503 {
			t.Fatalf("%s failed read: %d %s", request.method, w.Code, w.Body)
		}
	}
}
