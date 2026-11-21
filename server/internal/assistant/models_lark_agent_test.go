package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the actual extension conversation route and the node's launch parameters, not only resolution.
func TestLarkAgentModelOverrideAndReset(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	inherited := ModelBinding{Provider: "launcher.claude", Model: "agents-default", Effort: "medium"}
	setModel(t, s, "agent.new", inherited)
	legacy := modelRecord{Revision: 42, Binding: ModelBinding{Provider: "launcher.retired", Model: "retired", Effort: "low"}}
	if err := st.PutSetting(ctx, modelKey(purposeLarkAgent), legacy); err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "lark-custom", Model: "provider-default"}})

	assertState := func(want ModelBinding, revision int64, overridden bool) {
		t.Helper()
		p := purposeOf(t, s, purposeLarkAgent)
		if p.Effective != want || p.Revision != revision || p.CategoryID != "agent.new" || p.Parent != "agent.new" ||
			!slices.Equal(p.Fields, []string{"provider", "model", "effort", "permission_mode"}) {
			t.Fatalf("purpose: %+v; want binding %+v at revision %d", p, want, revision)
		}
		if overridden {
			if p.Override != want || p.InheritedFrom != "" {
				t.Fatalf("independent binding not exposed: %+v", p)
			}
		} else if p.Override != (ModelBinding{}) || p.InheritedFrom == "" {
			t.Fatalf("inheritance not exposed: %+v", p)
		}
		w := request(s, http.MethodPost, "/api/v1/assistant/conversations", `{"purpose":"lark.agent","prompt":"Model configuration test only"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("conversation: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Session proto.Session `json:"session"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Session.Launcher != want.Provider[len("launcher."):] {
			t.Fatalf("session launcher: %s", out.Session.Launcher)
		}
		f.mu.Lock()
		send := f.sends[len(f.sends)-1]
		f.mu.Unlock()
		if send["launcher"] != want.Provider[len("launcher."):] || send["model"] != want.Model || send["effort"] != want.Effort || send["model_fingerprint"] == "" {
			t.Fatalf("node startup: %+v; want %+v", send, want)
		}
		t.Logf("node startup launcher=%s model=%s effort=%s revision=%d", send["launcher"], send["model"], send["effort"], revision)
		var untouched modelRecord
		if _, err := st.Setting(ctx, modelKey(purposeLarkAgent), &untouched); err != nil || !reflect.DeepEqual(untouched, legacy) {
			t.Fatalf("dormant record changed: %+v %v", untouched, err)
		}
		if b, err := s.resolveModel(ctx, "inbox.agent"); err != nil || b != inherited {
			t.Fatalf("another Agents purpose changed: %+v %v", b, err)
		}
	}

	t.Run("no override ignores dormant record", func(t *testing.T) { assertState(inherited, 0, false) })
	custom := ModelBinding{Provider: "launcher.lark-custom", Model: "lark-selected", Effort: "high"}
	w := modelRequest(s, http.MethodPut, purposeLarkAgent, `{"revision":0,"provider":"launcher.lark-custom","model":"lark-selected","effort":"high"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	t.Run("explicit override reaches node", func(t *testing.T) { assertState(custom, 1, true) })
	// A sparse update keeps the provider and model; both verbs share optimistic locking.
	w = modelRequest(s, http.MethodPatch, purposeLarkAgent, `{"revision":1,"effort":"xhigh"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("sparse update: %d %s", w.Code, w.Body.String())
	}
	custom.Effort = "xhigh"
	assertState(custom, 2, true)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		w = modelRequest(s, method, purposeLarkAgent, `{"revision":0,"model":"stale"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s stale write: %d %s", method, w.Code, w.Body.String())
		}
	}
	w = modelRequest(s, http.MethodPut, purposeLarkAgent, `{"revision":2,"provider":null,"model":null,"effort":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	t.Run("reset follows Agents again", func(t *testing.T) { assertState(inherited, 3, false) })
	inherited.Model = "agents-changed"
	setModel(t, s, "agent.new", inherited)
	assertState(inherited, 3, false)
	// The saved override survives a manager restart, without restoring the retired namespace.
	s2 := newServiceOn(t, f, st)
	p := purposeOf(t, s2, purposeLarkAgent)
	if p.Revision != 3 || p.Override != (ModelBinding{}) || p.Effective != inherited {
		t.Fatalf("restart: %+v", p)
	}
}

func TestLarkAgentModelValidationAndProviderUse(t *testing.T) {
	s, _, _ := modelService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "lark-custom"}})
	for _, body := range []string{
		`{"provider":"launcher.lark-custom"}`,
		`{"revision":0,"provider":"launcher.missing"}`,
		`{"revision":0,"effort":"unsupported"}`,
		`{"revision":0,"effort":"off"}`,
		`{"revision":0,"effort":"none"}`,
	} {
		if w := modelRequest(s, http.MethodPut, purposeLarkAgent, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "lark-custom"}}, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex-custom"}})
	if w := modelRequest(s, http.MethodPut, purposeLarkAgent, `{"revision":0,"provider":"launcher.codex-custom","model":"selected","effort":"high"}`); w.Code != http.StatusOK {
		t.Fatalf("Codex effort rejected: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPut, purposeLarkAgent, `{"revision":1,"provider":"launcher.lark-custom","model":"selected"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	uses, err := s.providerModelUses(context.Background(), "launcher.lark-custom")
	if err != nil || len(uses) != 1 {
		t.Fatalf("override provider not protected: %v %v", uses, err)
	}
	if w := request(s, http.MethodDelete, "/api/v1/launchers/lark-custom", ""); w.Code == http.StatusOK {
		t.Fatalf("provider in use deleted: %s", w.Body.String())
	}
	for _, id := range []string{"inbox.judge", "translate", "schedule"} {
		p := purposeOf(t, s, id)
		w := modelRequest(s, http.MethodPut, id, fmt.Sprintf(`{"revision":%d,"model":"other"}`, p.Revision))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s became writable: %d %s", id, w.Code, w.Body.String())
		}
	}
}
