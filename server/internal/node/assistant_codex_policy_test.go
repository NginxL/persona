package node

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexPermissionModesReachThreadAndTurn(t *testing.T) {
	for _, tc := range []struct {
		mode, approval, sandbox, turnSandbox string
		network                              bool
	}{
		{"default", "untrusted", "workspace-write", "workspaceWrite", true},
		{"acceptEdits", "untrusted", "workspace-write", "workspaceWrite", true},
		{"auto", "on-request", "workspace-write", "workspaceWrite", false},
		{"plan", "untrusted", "read-only", "readOnly", false},
		{proto.ModeBypass, "never", "danger-full-access", "dangerFullAccess", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			for _, resume := range []bool{false, true} {
				n := codexTestNode(t)
				withFakeHub(t, n)
				cwd := t.TempDir()
				n.noteModes(testThread, tc.mode, "")
				_, c, f := newFakeCodex(t, n, cwd)
				c.memDir = filepath.Join(cwd, "memory")
				method := "thread/start"
				if resume {
					method = "thread/resume"
				}
				f.results[method] = map[string]any{"thread": map[string]any{"id": testThread}}
				if err := c.start(context.Background(), testThread, cwd, resume); err != nil {
					t.Fatal(err)
				}
				var start struct{ ApprovalPolicy, Sandbox, ThreadID string }
				if err := json.Unmarshal(f.sent(method)[0].Params, &start); err != nil {
					t.Fatal(err)
				}
				if start.ApprovalPolicy != tc.approval || start.Sandbox != tc.sandbox || (resume && start.ThreadID != testThread) {
					t.Fatalf("%s: %+v", method, start)
				}
				if err := c.deliver(context.Background(), "", "policy check", nil); err != nil {
					t.Fatal(err)
				}
				var turn struct {
					ApprovalPolicy string
					SandboxPolicy  struct {
						Type          string
						NetworkAccess bool
						WritableRoots []string
					}
				}
				if err := json.Unmarshal(f.sent("turn/start")[0].Params, &turn); err != nil {
					t.Fatal(err)
				}
				if turn.ApprovalPolicy != tc.approval || turn.SandboxPolicy.Type != tc.turnSandbox || turn.SandboxPolicy.NetworkAccess != tc.network {
					t.Fatalf("turn/start: %+v", turn)
				}
				if tc.turnSandbox == "workspaceWrite" && !reflect.DeepEqual(turn.SandboxPolicy.WritableRoots, []string{c.memDir}) {
					t.Fatalf("memory root lost: %+v", turn.SandboxPolicy)
				}
			}
		})
	}
}

func TestCodexUnsetPermissionsDeferToLauncher(t *testing.T) {
	for _, resume := range []bool{false, true} {
		n := codexTestNode(t)
		withFakeHub(t, n)
		cwd := t.TempDir()
		p, c, f := newFakeCodex(t, n, cwd)
		method := "thread/start"
		if resume {
			method = "thread/resume"
		}
		f.results[method] = map[string]any{"thread": map[string]any{"id": testThread}}
		if err := c.start(context.Background(), testThread, cwd, resume); err != nil {
			t.Fatal(err)
		}
		assertNoPolicy := func(method string) {
			t.Helper()
			for _, call := range f.sent(method) {
				var params map[string]json.RawMessage
				if err := json.Unmarshal(call.Params, &params); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"approvalPolicy", "sandbox", "sandboxPolicy", "permissions"} {
					if _, ok := params[key]; ok {
						t.Fatalf("%s overrides the launcher's %s: %s", method, key, call.Params)
					}
				}
			}
		}
		assertNoPolicy(method)
		// Effective policy is display metadata, not a user's selection. In particular, an inherited
		// workspace-write/on-request policy must keep its configured network access on later turns.
		rollout := filepath.Join(cwd, "history.jsonl")
		history := []byte(`{"type":"session_meta","payload":{"id":"` + testThread + `","cwd":"` + filepath.ToSlash(cwd) + `"}}` + "\n" +
			`{"type":"turn_context","payload":{"approval_policy":"on-request","sandbox_policy":{"type":"workspace-write","network_access":true}}}` + "\n")
		if err := os.WriteFile(rollout, history, 0o600); err != nil {
			t.Fatal(err)
		}
		n.paths[testThread] = rollout
		n.state.put(testThread, sessionState{Launcher: "cx_test", Cwd: cwd})
		n.procs[testThread] = p
		if mode, _ := n.modesOf(testThread); mode != "auto" {
			t.Fatalf("effective configured policy was not read: %q", mode)
		}
		for range 2 {
			if err := c.deliver(context.Background(), "", "next turn", nil); err != nil {
				t.Fatal(err)
			}
		}
		assertNoPolicy("turn/start")
		if len(f.sent("turn/start")) != 2 {
			t.Fatal("later turns did not start")
		}
		if st, _ := n.state.get(testThread); st.AppMode != "" {
			t.Fatalf("effective policy became an explicit mode: %q", st.AppMode)
		}
		if got, err := os.ReadFile(rollout); err != nil || string(got) != string(history) {
			t.Fatal("history changed")
		}
	}
}

func TestCodexPersistedModeSurvivesRestartAndAppliesNextTurn(t *testing.T) {
	n := codexTestNode(t)
	withFakeHub(t, n)
	cwd := t.TempDir()
	rollout := filepath.Join(cwd, "history.jsonl")
	history := []byte("existing conversation history\n")
	if err := os.WriteFile(rollout, history, 0o600); err != nil {
		t.Fatal(err)
	}
	n.paths[testThread] = rollout
	const other = "11111111-2222-3333-4444-555555555555"
	initial := sessionState{Launcher: "cx_test", Cwd: cwd, AppMode: "default", IsMain: true, StartedAt: time.Now().UTC()}
	n.state.put(testThread, initial)
	n.state.put(other, sessionState{Launcher: "cx_test", Cwd: cwd, AppMode: "plan"})
	untouched, _ := n.state.get(other)
	p, c, f := newFakeCodex(t, n, cwd)
	p.id, c.threadID, c.turnID = testThread, testThread, "active-turn"
	n.procs[testThread] = p
	var saved []byte
	n.state.persist = func(id string, st sessionState) {
		if id != testThread {
			t.Errorf("persisted another session: %s", id)
		}
		var err error
		saved, err = json.Marshal(st)
		if err != nil {
			t.Error(err)
		}
	}
	if err := n.setModes(context.Background(), testThread, proto.ModeBypass, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.deliver(context.Background(), "", "while active", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.sent("turn/start")) != 0 || len(f.sent("turn/steer")) != 1 {
		t.Fatal("changing a mode restarted an active turn")
	}
	var restored sessionState
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	want := initial
	want.AppMode = proto.ModeBypass
	want.AppModeExplicit = true
	if !reflect.DeepEqual(restored, want) {
		t.Fatalf("session identity changed: %+v", restored)
	}
	n.state = newState()
	n.state.merge(map[string]sessionState{testThread: restored, other: untouched})
	n.modes = map[string]sessionModes{}
	if mode, _ := n.currentModes(testThread, quickMeta{mode: "untrusted", sandbox: "workspace-write", modeAt: time.Now().Add(time.Hour)}); mode != proto.ModeBypass {
		t.Fatalf("old rollout overrode persisted mode: %s", mode)
	}
	c.mu.Lock()
	c.turnID = ""
	c.mu.Unlock()
	f.results["thread/resume"] = map[string]any{"thread": map[string]any{"id": testThread, "path": rollout}}
	if err := c.start(context.Background(), testThread, cwd, true); err != nil {
		t.Fatal(err)
	}
	var resumed struct{ ApprovalPolicy, ThreadID string }
	if err := json.Unmarshal(f.sent("thread/resume")[0].Params, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.ApprovalPolicy != "never" || resumed.ThreadID != testThread || n.transcriptPath(testThread) != rollout {
		t.Fatalf("persisted resume changed identity or policy: %+v", resumed)
	}
	for _, mode := range []string{proto.ModeBypass, "default", "auto", "plan", "acceptEdits"} {
		if err := n.setModes(context.Background(), testThread, mode, ""); err != nil {
			t.Fatal(err)
		}
		if err := c.deliver(context.Background(), "", "next turn", nil); err != nil {
			t.Fatal(err)
		}
		sent := f.sent("turn/start")
		var turn struct{ ApprovalPolicy string }
		if err := json.Unmarshal(sent[len(sent)-1].Params, &turn); err != nil {
			t.Fatal(err)
		}
		approval, _ := codexPolicy(mode)
		if turn.ApprovalPolicy != approval {
			t.Fatalf("%s next turn used %s", mode, turn.ApprovalPolicy)
		}
	}
	if got, _ := n.state.get(other); !reflect.DeepEqual(got, untouched) {
		t.Fatalf("other session changed: %+v", got)
	}
	if got, err := os.ReadFile(rollout); err != nil || string(got) != string(history) {
		t.Fatalf("history changed: %q, %v", got, err)
	}
}

func TestCodexInheritedPolicyRefusalHasNoFallback(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume", "turn/start"} {
		n := codexTestNode(t)
		withFakeHub(t, n)
		_, c, f := newFakeCodex(t, n, t.TempDir())
		c.p.id, c.threadID = testThread, testThread
		const refusal = "configured permissions are not allowed by managed requirements"
		f.mu.Lock()
		f.errors = map[string]string{method: refusal}
		f.mu.Unlock()
		var err error
		if method == "turn/start" {
			err = c.deliver(context.Background(), "", "policy check", nil)
		} else {
			err = c.start(context.Background(), testThread, c.p.cwd, method == "thread/resume")
		}
		if err == nil || !strings.Contains(err.Error(), refusal) || len(f.sent(method)) != 1 {
			t.Fatalf("%s did not propagate the inherited-policy refusal: %v", method, err)
		}
	}
}

func TestCodexFullAccessKeepsConnectorProxyRules(t *testing.T) {
	api := newFakeAPI(t)
	n, _ := gateNode(t, api.URL, proto.LarkRule{Method: "DELETE", Path: "/messages/*", Action: "deny"})
	n.modes = map[string]sessionModes{}
	n.noteModes(testThread, proto.ModeBypass, "")
	base, release, err := n.gates().open("ex", testThread)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if approval, _ := codexPolicy(proto.ModeBypass); approval != "never" {
		t.Fatal("full access is not native never")
	}
	// These requests go directly to the proxy, without a command approval callback.
	if code, _ := send(t, "GET", base+"/api/v1/me", ""); code != http.StatusOK {
		t.Fatalf("connector read: %d", code)
	}
	for _, request := range []struct{ method, path string }{
		{"POST", "/api/v1/conversations/F1/messages"},
		{"DELETE", "/api/v1/messages/M1"},
	} {
		if code, _ := send(t, request.method, base+request.path, `{"text":"hi"}`); code != http.StatusForbidden {
			t.Fatalf("unconfirmed connector write: %d", code)
		}
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != "GET" {
		t.Fatalf("connector received an unconfirmed write: %+v", seen)
	}
}

func TestCodexFullAccessManagedRefusalIsReturned(t *testing.T) {
	n := codexTestNode(t)
	withFakeHub(t, n)
	n.noteModes(testThread, proto.ModeBypass, "")
	_, c, f := newFakeCodex(t, n, t.TempDir())
	c.p.id, c.threadID = testThread, testThread
	const refusal = "approval policy never is not allowed by managed requirements"
	f.errors = map[string]string{}
	for _, method := range []string{"thread/start", "thread/resume", "turn/start"} {
		f.mu.Lock()
		f.errors[method] = refusal
		f.mu.Unlock()
		var err error
		if method == "turn/start" {
			err = c.deliver(context.Background(), "", "policy check", nil)
		} else {
			err = c.start(context.Background(), testThread, filepath.Clean(c.p.cwd), method == "thread/resume")
		}
		if err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("%s did not return managed refusal: %v", method, err)
		}
		if len(f.sent(method)) != 1 {
			t.Fatalf("%s retried a refused policy", method)
		}
	}
}
