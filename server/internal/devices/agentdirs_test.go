package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func hiddenDirsHub(t *testing.T) (*Devices, *settingsStore, *Machine, http.Handler, *[]string) {
	t.Helper()
	h, st, m, mux := dirsHub(t)
	changed := &[]string{}
	h.opts.HiddenAgentDirsChanged = func(machine string) { *changed = append(*changed, machine) }
	inner := mux
	out := http.NewServeMux()
	out.HandleFunc("GET /api/v1/machines/{mid}/agent-dirs/hidden", h.withMachine(h.getHiddenAgentDirs))
	out.HandleFunc("PUT /api/v1/machines/{mid}/agent-dirs/hidden", h.withMachine(h.putHiddenAgentDirs))
	out.Handle("/", inner)
	return h, st, m, out, changed
}

// The hidden list is kept per computer beside the blocked one: GET starts as [], PUT replaces it (cleaned like the
// blocked patterns), a switched-off rule stays listed, and every real change tells the assistant once.
func TestHiddenAgentDirsRoutes(t *testing.T) {
	h, st, m, mux, changed := hiddenDirsHub(t)
	connectDirsNode(t, m)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/agent-dirs/hidden", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rules":[]`) || !strings.Contains(rec.Body.String(), `"home":"/home/me"`) {
		t.Fatalf("defaults: %d %s", rec.Code, rec.Body)
	}
	code, out := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":" ~/.automation/workspaces/ ","enabled":true},{"pattern":"*scratch*","enabled":false}]}`)
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	want := []proto.HiddenAgentDir{{Pattern: "~/.automation/workspaces", Enabled: true}, {Pattern: "*scratch*", Enabled: false}}
	var stored []proto.HiddenAgentDir
	if _, err := st.Setting(context.Background(), hiddenAgentDirsKey("m1"), &stored); err != nil || !slices.Equal(stored, want) {
		t.Fatalf("stored %+v %v", stored, err)
	}
	if !slices.Equal(*changed, []string{"m1"}) {
		t.Fatalf("told %v", *changed)
	}
	// The same list again is no change.
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.automation/workspaces","enabled":true},{"pattern":"*scratch*","enabled":false}]}`); code != 200 || len(*changed) != 1 {
		t.Fatalf("no-op PUT: %d told %v", code, *changed)
	}
	// Collection is untouched: the node is never told about hidden folders.
	if rules, home, err := h.HiddenAgentDirs(context.Background(), "m1"); err != nil || home != "/home/me" || !slices.Equal(rules, want) {
		t.Fatalf("read: %+v %q %v", rules, home, err)
	}
	// A manager that restarts reads them back.
	h2 := newTestDevices(t)
	h2.opts.Store = dirsStore{st}
	m2 := newMachine(h2, MachineConfig{ID: "m1", Name: "box"})
	h2.addEntry(m2)
	if rules, _, err := h2.HiddenAgentDirs(context.Background(), "m1"); err != nil || !slices.Equal(rules, want) {
		t.Fatalf("restart: %+v %v", rules, err)
	}
	if rules, _, err := h2.HiddenAgentDirs(context.Background(), "nope"); err != nil || rules != nil {
		t.Fatalf("unknown machine: %+v %v", rules, err)
	}
}

func TestHiddenAgentDirsRefuseBadLists(t *testing.T) {
	_, _, _, mux, changed := hiddenDirsHub(t)
	many := make([]proto.HiddenAgentDir, proto.MaxHiddenAgentDirs+1)
	for i := range many {
		many[i] = proto.HiddenAgentDir{Pattern: "/work/" + strings.Repeat("a", i+1), Enabled: true}
	}
	tooMany, _ := json.Marshal(map[string]any{"rules": many})
	for _, body := range []string{
		`{"rules":[{"pattern":"relative/dir","enabled":true}]}`,
		`{"rules":[{"pattern":"~/[","enabled":true}]}`,
		`{"rules":[{"pattern":"~/","enabled":true}]}`,
		`{"rules":[{"pattern":"*","enabled":true}]}`,
		`{"rules":[{"pattern":"/*/","enabled":false}]}`,
		`{"rules":[{"pattern":"/work","enabled":true},{"pattern":"/work/","enabled":false}]}`,
		string(tooMany),
		`nope`,
	} {
		if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", body); code != 400 {
			t.Errorf("%.60s: %d", body, code)
		}
	}
	if len(*changed) != 0 {
		t.Fatalf("a refused list told the assistant: %v", *changed)
	}
}

// An edit works on the rules as they are when it runs: a change made after the edit was planned is kept.
func TestEditHiddenAgentDirsKeepsNewerRules(t *testing.T) {
	h, _, _, mux, _ := hiddenDirsHub(t)
	ctx := context.Background()
	add := func(p string) func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
		return func(now []proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
			return append(now, proto.HiddenAgentDir{Pattern: p, Enabled: true}), nil
		}
	}
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"/other","enabled":true}]}`); code != 200 {
		t.Fatal(code)
	}
	got, err := h.EditHiddenAgentDirs(ctx, "m1", add("/work"))
	if err != nil || len(got) != 2 || got[0].Pattern != "/other" || got[1].Pattern != "/work" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := h.EditHiddenAgentDirs(ctx, "nope", add("/x")); err == nil {
		t.Fatal("edited an unknown machine")
	}
}
