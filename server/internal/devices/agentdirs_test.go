package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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
	// Saved without an action (the hiding-only version, an older client): hide, what such a rule did.
	want := []proto.HiddenAgentDir{{Pattern: "~/.automation/workspaces", Enabled: true, Action: proto.AgentDirHide}, {Pattern: "*scratch*", Enabled: false, Action: proto.AgentDirHide}}
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
	// Collection is untouched: the node is never told about hidden folders (only blocking ones, below).
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

// A rule's action: block or hide, nothing else. The node is told the blocking patterns of the rules switched on,
// when it connects and again when they change; a change that leaves them as they were tells it nothing (#10053).
func TestBlockedAgentDirsReachTheNode(t *testing.T) {
	_, st, m, mux, changed := hiddenDirsHub(t)
	f := connectDirsNode(t, m)
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"/work","enabled":true,"action":"delete"}]}`); code != 400 {
		t.Fatalf("an unknown action: %d", code)
	}
	told := func() [][]string {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out [][]string
		for _, c := range f.configure {
			if raw, ok := c["agent_dirs_blocked"]; ok {
				var p []string
				_ = json.Unmarshal(raw, &p)
				out = append(out, p)
			}
		}
		return out
	}
	code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs","enabled":true,"action":"block"},{"pattern":"/scratch","enabled":true,"action":"hide"},{"pattern":"/off","enabled":false,"action":"block"}]}`)
	if code != 200 {
		t.Fatal(code)
	}
	eventually(t, 5*time.Second, "the node told the blocking patterns", func() bool {
		got := told()
		return len(got) == 1 && slices.Equal(got[0], []string{"~/.bots/runs"})
	})
	var stored []proto.HiddenAgentDir
	if _, err := st.Setting(context.Background(), hiddenAgentDirsKey("m1"), &stored); err != nil || stored[0].Action != proto.AgentDirBlock {
		t.Fatalf("stored %+v %v", stored, err)
	}
	// Only a hide rule switched off: the node's patterns are the same, it is not told again.
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs","enabled":true,"action":"block"},{"pattern":"/scratch","enabled":false,"action":"hide"},{"pattern":"/off","enabled":false,"action":"block"}]}`); code != 200 {
		t.Fatal(code)
	}
	// Switching the rule to hide unblocks it on the node.
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs","enabled":true,"action":"hide"}]}`); code != 200 {
		t.Fatal(code)
	}
	eventually(t, 5*time.Second, "the node told it is unblocked", func() bool {
		got := told()
		return len(got) == 2 && len(got[1]) == 0
	})
	if len(*changed) != 3 {
		t.Fatalf("the assistant told %v", *changed)
	}
	// A connection's own configure carries them too.
	if p := m.blockedAgentDirsParam(context.Background()); p == nil || len(p) != 0 {
		t.Fatalf("param %#v", p)
	}
}

// failingRulesStore fails reads of the Agent folder rules while fail is set, like a database away at connect time.
type failingRulesStore struct {
	dirsStore
	fail *bool
}

func (s failingRulesStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if *s.fail && strings.HasPrefix(key, "agent_dirs_hidden/") {
		return false, errors.New("database away")
	}
	return s.dirsStore.Setting(ctx, key, out)
}

// A node keeps the block list it was given, also across restarts. A manager that cannot read the rules when it
// configures the node (a cold cache and a failed read) says nothing of them (null): sending [] would unblock every
// folder on that computer. Only a successful read clears them, an empty rule list included.
func TestBlockedAgentDirsColdReadFailureKeepsTheNodesBlocks(t *testing.T) {
	_, st, m, _ := dirsHub(t)
	fail := true
	m.d.opts.Store = failingRulesStore{dirsStore{st}, &fail}
	f := connectDirsNode(t, m)
	ctx := context.Background()
	sent := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, ok := f.configure[len(f.configure)-1]["agent_dirs_blocked"]
		if !ok {
			return "(absent)"
		}
		return string(raw)
	}
	m.configureNode(ctx, m.peer, nil, true)
	if got := sent(); got != "null" {
		t.Fatalf("a failed rules read sent agent_dirs_blocked %s; the node would replace its blocks with it", got)
	}
	fail = false
	m.hiddenDirs.mu.Lock()
	m.hiddenDirs.failedAt = time.Time{} // the retry is due
	m.hiddenDirs.mu.Unlock()
	m.configureNode(ctx, m.peer, nil, true)
	if got := sent(); got != "[]" {
		t.Fatalf("a successful read of no rules sent agent_dirs_blocked %s, want [] (clears the node's blocks)", got)
	}
}

// An app from before blocking sends its whole list without actions. A rule it lists that already exists keeps its
// action: switching another rule from such an app must not turn every block into a hide (the node would start
// collecting those folders again). A new rule from it hides, as before blocking.
func TestHiddenAgentDirsPutWithoutActionKeepsBlocks(t *testing.T) {
	_, st, _, mux, _ := hiddenDirsHub(t)
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs","enabled":true,"action":"block"},{"pattern":"/scratch","enabled":true,"action":"hide"}]}`); code != 200 {
		t.Fatal(code)
	}
	// The older app switches /scratch off and adds /new, sending no action at all.
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs/","enabled":true},{"pattern":"/scratch","enabled":false},{"pattern":"/new","enabled":true}]}`); code != 200 {
		t.Fatal(code)
	}
	var stored []proto.HiddenAgentDir
	if _, err := st.Setting(context.Background(), hiddenAgentDirsKey("m1"), &stored); err != nil {
		t.Fatal(err)
	}
	want := []proto.HiddenAgentDir{{Pattern: "~/.bots/runs", Enabled: true, Action: proto.AgentDirBlock},
		{Pattern: "/scratch", Enabled: false, Action: proto.AgentDirHide}, {Pattern: "/new", Enabled: true, Action: proto.AgentDirHide}}
	if !slices.Equal(stored, want) {
		t.Fatalf("stored %+v, want %+v", stored, want)
	}
	// An app that names the action still switches it.
	if code, _ := put(t, mux, "/api/v1/machines/m1/agent-dirs/hidden", `{"rules":[{"pattern":"~/.bots/runs","enabled":true,"action":"hide"}]}`); code != 200 {
		t.Fatal(code)
	}
	if _, err := st.Setting(context.Background(), hiddenAgentDirsKey("m1"), &stored); err != nil || stored[0].Action != proto.AgentDirHide {
		t.Fatalf("an explicit action: %+v %v", stored, err)
	}
}
