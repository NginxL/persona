package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The owner's messages to Agents held while their computer is away (internal/devices held.go, #10661), as the rows and
// the conversations' undelivered routes carry them.

// heldFleet is a fleet whose machine "mac" lists the assistant's conversation conv9, with message msg-9 kept for it
// (undelivered.go), and the Agents agent1 and agent2; the devices hold held-1 for agent1.
func heldFleet(t *testing.T) (*fleetTest, proto.UndeliveredMessage, proto.UndeliveredMessage) {
	t.Helper()
	ft := newFleetTest(t)
	setSettings(t, ft.s, func(v *Settings) { v.MachineID = "mac" })
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "conv9", MachineID: "mac", Assistant: true, Cwd: "/home/me/.persona/assistant", Status: proto.StatusOffline},
		{SessionID: "agent1", MachineID: "mac", Agent: "claude", Cwd: "/home/u/project", Status: proto.StatusUnknown},
		{SessionID: "agent2", MachineID: "mac", Agent: "codex", Cwd: "/home/u/other", Status: proto.StatusUnknown},
	})
	kept, err := ft.s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "mac", SessionID: "conv9",
		Params: map[string]any{"text": "hello", "client_msg_id": "msg-9"}})
	if err != nil {
		t.Fatal(err)
	}
	held := proto.UndeliveredMessage{ID: "held-1", Text: "run the tests", At: time.Now().UTC(), State: proto.UndeliveredWaiting,
		Computer: "Mac", Error: "Will send when Mac is back"}
	ft.d.mu.Lock()
	ft.d.held = map[string]map[string][]proto.UndeliveredMessage{"mac": {
		"agent1": {held},
		// Never the case (the hub holds no message to a conversation), but if it were: the conversation's row is the
		// assistant's own list alone.
		"conv9": {{ID: "stray", Text: "not the conversation's", State: proto.UndeliveredWaiting, Computer: "Mac"}},
	}}
	ft.d.mu.Unlock()
	return ft, kept, held
}

func heldRow(rows []proto.Session, sid string) proto.Session {
	for _, r := range rows {
		if r.SessionID == sid {
			return r
		}
	}
	return proto.Session{}
}

func undeliveredJSON(u *[]proto.UndeliveredMessage) string {
	b, _ := json.Marshal(u)
	return string(b)
}

// An Agent's row carries the messages held for it, worded in the owner's language; the conversation's row keeps
// exactly its own kept list, and an Agent with none carries none. The devices are asked once for the whole list.
func TestHeldMessagesAreDrawnOnAgentRowsOnly(t *testing.T) {
	ft, kept, held := heldFleet(t)
	s := ft.s
	ft.f.mu.Lock()
	ft.f.hello.Locale = "ja"
	ft.f.mu.Unlock()
	s.fleetTakeSnapshot(s.snapshot(context.Background()))
	ft.d.mu.Lock()
	before := ft.d.heldAsked["mac"]
	ft.d.mu.Unlock()
	rows := s.publishList("mac")
	ft.d.mu.Lock()
	asked, lang := ft.d.heldAsked["mac"]-before, ft.d.heldLang["mac"]
	ft.d.mu.Unlock()
	if asked != 1 || lang != i18n.Lang("ja") {
		t.Fatalf("held messages asked %d times for one decoration (last in %q), want once in ja", asked, lang)
	}
	if got, want := undeliveredJSON(heldRow(rows, "agent1").Undelivered), undeliveredJSON(&[]proto.UndeliveredMessage{held}); got != want {
		t.Fatalf("agent1's row carries %s, want %s", got, want)
	}
	if u := heldRow(rows, "agent2").Undelivered; u != nil {
		t.Fatalf("agent2 holds nothing but its row carries %s", undeliveredJSON(u))
	}
	if got, want := undeliveredJSON(heldRow(rows, "conv9").Undelivered), undeliveredJSON(&[]proto.UndeliveredMessage{kept}); got != want {
		t.Fatalf("the conversation's row carries %s, want its own kept list %s", got, want)
	}

	// Sent: it leaves the row.
	ft.d.mu.Lock()
	delete(ft.d.held["mac"], "agent1")
	ft.d.mu.Unlock()
	if u := heldRow(s.publishList("mac"), "agent1").Undelivered; u != nil {
		t.Fatalf("still on agent1's row after it went: %s", undeliveredJSON(u))
	}
	// One row decorated alone (a finished turn's) is drawn the same way.
	ft.d.mu.Lock()
	ft.d.held["mac"]["agent2"] = []proto.UndeliveredMessage{held}
	ft.d.mu.Unlock()
	if row, ok := s.decoratedSession("mac", "agent2"); !ok || row.Undelivered == nil || len(*row.Undelivered) != 1 {
		t.Fatalf("agent2 alone: %v %s", ok, undeliveredJSON(row.Undelivered))
	}
}

// A runtime Agent that leaves the node's list keeps a display-only row while a message is held for it. The last
// listed row supplies its identity; the real row replaces the placeholder as soon as the runtime is listed again.
func TestHeldMessageKeepsUnlistedRuntimeAgentRow(t *testing.T) {
	ft, _, held := heldFleet(t)
	ft.d.mu.Lock()
	ft.d.held["mac"] = map[string][]proto.UndeliveredMessage{"agent1": {held}}
	ft.d.mu.Unlock()
	live := proto.Session{SessionID: "agent1", MachineID: "mac", Agent: "dsh", AgentLabel: "DSH",
		SourceKind: proto.SourceRuntime, Title: "Runtime agent", Cwd: "/home/u/runtime", Status: proto.StatusWorking}
	ft.d.setSessions("mac", []proto.Session{live})
	if rows := ft.s.publishList("mac"); len(rows) != 1 || rows[0].Title != live.Title {
		t.Fatalf("seed the last-known row: %+v", rows)
	}

	ft.d.setSessions("mac", nil)
	rows := ft.s.publishList("mac")
	if len(rows) != 1 {
		t.Fatalf("unlisted Agent with a held message has %d rows, want one: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.SessionID != live.SessionID || row.MachineID != "mac" || row.Agent != live.Agent ||
		row.AgentLabel != live.AgentLabel || row.SourceKind != live.SourceKind || row.Title != live.Title || row.Cwd != live.Cwd {
		t.Fatalf("placeholder lost the runtime identity: %+v", row)
	}
	if row.Status != proto.StatusOffline || row.State != proto.StateOffline {
		t.Fatalf("placeholder is not offline: status %q, state %q", row.Status, row.State)
	}
	if got, want := undeliveredJSON(row.Undelivered), undeliveredJSON(&[]proto.UndeliveredMessage{held}); got != want {
		t.Fatalf("placeholder carries %s, want %s", got, want)
	}

	live.Title, live.Status = "Listed again", proto.StatusIdle
	ft.d.setSessions("mac", []proto.Session{live})
	rows = ft.s.publishList("mac")
	if len(rows) != 1 || rows[0].Title != live.Title || rows[0].Status != proto.StatusIdle {
		t.Fatalf("the live row did not replace its placeholder: %+v", rows)
	}

	ft.d.setSessions("mac", nil)
	ft.d.mu.Lock()
	delete(ft.d.held["mac"], "agent1")
	ft.d.mu.Unlock()
	if rows := ft.s.publishList("mac"); len(rows) != 0 {
		t.Fatalf("placeholder outlived its held message: %+v", rows)
	}
}

// After a cold manager restart there is no last-known row. The held message's machine and session IDs still make a
// minimal row, with every held message on it, until the real inventory returns or the messages are gone.
func TestHeldMessageRecreatesUnlistedAgentRowWithColdCache(t *testing.T) {
	ft, _, held := heldFleet(t)
	second := held
	second.ID, second.Text = "held-2", "run the integration tests"
	ft.d.mu.Lock()
	ft.d.held["mac"] = map[string][]proto.UndeliveredMessage{"agent1": {held, second}}
	ft.d.mu.Unlock()
	ft.d.setSessions("mac", nil)

	rows := ft.s.publishList("mac")
	if len(rows) != 1 {
		t.Fatalf("cold cache produced %d rows, want one: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.MachineID != "mac" || row.SessionID != "agent1" || row.Status != proto.StatusOffline || row.State != proto.StateOffline {
		t.Fatalf("cold placeholder: %+v", row)
	}
	if row.Agent != "" || row.AgentLabel != "" || row.SourceKind != "" || row.Title != "" || row.Cwd != "" {
		t.Fatalf("cold placeholder invented metadata: %+v", row)
	}
	if got, want := undeliveredJSON(row.Undelivered), undeliveredJSON(&[]proto.UndeliveredMessage{held, second}); got != want {
		t.Fatalf("cold placeholder carries %s, want %s", got, want)
	}

	live := proto.Session{SessionID: "agent1", Agent: "dsh", Title: "Live runtime", Status: proto.StatusIdle}
	ft.d.setSessions("mac", []proto.Session{live})
	rows = ft.s.publishList("mac")
	if len(rows) != 1 || rows[0].Title != live.Title || rows[0].Status != proto.StatusIdle {
		t.Fatalf("cold placeholder duplicated or hid the live row: %+v", rows)
	}
	ft.d.mu.Lock()
	delete(ft.d.held["mac"], "agent1")
	ft.d.mu.Unlock()
	if rows := ft.s.publishList("mac"); len(rows) != 1 || rows[0].Undelivered != nil {
		t.Fatalf("settling held messages removed or dirtied the live row: %+v", rows)
	}
	ft.d.setSessions("mac", nil)
	if rows := ft.s.publishList("mac"); len(rows) != 0 {
		t.Fatalf("unlisted row remained after its held messages settled: %+v", rows)
	}
}

// Older apps call the conversations' undelivered routes for every row's message. One the assistant keeps is its own,
// as before; any other goes to the devices' held message of that session when a person's own app asks (the hub's
// assistantwire.PersonHeader), and its answer is the route's.
func TestUndeliveredRoutesFallThroughToHeldMessages(t *testing.T) {
	ft, _, _ := heldFleet(t)
	s := ft.s
	do := func(method, path, role string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/assistant/conversations/"+path, nil)
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		req.Header.Set(assistantwire.PersonHeader, "1")
		s.ServeHTTP(rec, req)
		return rec
	}
	acts := func() []string {
		ft.d.mu.Lock()
		defer ft.d.mu.Unlock()
		return slices.Clone(ft.d.heldActs)
	}

	if rec := do(http.MethodPost, "agent1/undelivered/held-1/retry", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("retry of a held message: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "agent1/undelivered/gone/retry", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("retry of a message nobody keeps: %d %s", rec.Code, rec.Body)
	}
	if got := acts(); !slices.Equal(got, []string{"retry agent1/held-1", "retry agent1/gone"}) {
		t.Fatalf("held actions %q", got)
	}

	ft.d.mu.Lock()
	ft.d.heldErr = &rpc.Error{Code: "held_sending", Message: "It is being sent right now."}
	ft.d.mu.Unlock()
	if rec := do(http.MethodDelete, "agent1/undelivered/held-1", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being sent right now") {
		t.Fatalf("delete while it is being sent: %d %s", rec.Code, rec.Body)
	}
	ft.d.mu.Lock()
	ft.d.heldErr = nil
	ft.d.mu.Unlock()
	if rec := do(http.MethodDelete, "agent1/undelivered/held-1", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodDelete, "agent1/undelivered/held-1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d %s", rec.Code, rec.Body)
	}

	// The conversation's own kept message never reaches the devices; neither does a read-only device's request.
	n := len(acts())
	if rec := do(http.MethodPost, "conv9/undelivered/msg-9/retry", ""); rec.Code != http.StatusOK {
		t.Fatalf("retry of the conversation's kept message: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodDelete, "agent1/undelivered/held-1", "readonly"); rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only device's delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodDelete, "conv9/undelivered/msg-9", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete of the conversation's kept message: %d %s", rec.Code, rec.Body)
	}
	if q := undeliveredOf(t, s, "conv9"); len(q) != 0 {
		t.Fatalf("the conversation still keeps %+v", q)
	}
	if got := acts()[n:]; len(got) != 0 {
		t.Fatalf("the devices were asked %q", got)
	}
}

// Only a person's own app reaches a held message through these routes: without the hub's assistantwire.PersonHeader
// (the hub's own token, an extension, an older hub) the route answers 404 as before and the devices are not asked.
// The conversation's own kept messages keep today's rules, the header or not.
func TestUndeliveredRoutesReachHeldMessagesOnlyForAPerson(t *testing.T) {
	ft, _, _ := heldFleet(t)
	s := ft.s
	do := func(method, path, person string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/assistant/conversations/"+path, nil)
		if person != "" {
			req.Header.Set(assistantwire.PersonHeader, person)
		}
		s.ServeHTTP(rec, req)
		return rec
	}
	for _, person := range []string{"", "0", "yes"} {
		if rec := do(http.MethodPost, "agent1/undelivered/held-1/retry", person); rec.Code != http.StatusNotFound {
			t.Fatalf("retry with %s %q: %d %s", assistantwire.PersonHeader, person, rec.Code, rec.Body)
		}
		if rec := do(http.MethodDelete, "agent1/undelivered/held-1", person); rec.Code != http.StatusNotFound {
			t.Fatalf("delete with %s %q: %d %s", assistantwire.PersonHeader, person, rec.Code, rec.Body)
		}
	}
	ft.d.mu.Lock()
	acts, left := slices.Clone(ft.d.heldActs), len(ft.d.held["mac"]["agent1"])
	ft.d.mu.Unlock()
	if len(acts) != 0 || left != 1 {
		t.Fatalf("the devices were asked %q (%d left)", acts, left)
	}
	if rec := do(http.MethodPost, "conv9/undelivered/msg-9/retry", ""); rec.Code != http.StatusOK {
		t.Fatalf("retry of the conversation's kept message: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodDelete, "conv9/undelivered/msg-9", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete of the conversation's kept message: %d %s", rec.Code, rec.Body)
	}
}
