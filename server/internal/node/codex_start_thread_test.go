package node

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexStartsWithEffortOnSharedDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control socket fixture")
	}
	for _, tc := range []struct {
		name, prompt, initialEffort                    string
		legacy, refuse                                 bool
		rejectAfterCreation, ignoreUpdate, wrongReadID bool
	}{
		{name: "settings update", prompt: "first task", initialEffort: "low"},
		{name: "empty thread", initialEffort: "low"},
		{name: "start config only", prompt: "use the configured effort", initialEffort: "high", legacy: true},
		{name: "neither method", prompt: "work", initialEffort: "low", legacy: true, refuse: true},
		{name: "invalid params after creation", prompt: "work", rejectAfterCreation: true},
		{name: "acknowledged but ignored", prompt: "work", initialEffort: "low", ignoreUpdate: true, refuse: true},
		{name: "effort missing from readback", prompt: "work", ignoreUpdate: true, refuse: true},
		{name: "readback is for another thread", prompt: "work", wrongReadID: true, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _, _ := codexStartHerdr(t, 0)
			// Keep the Unix socket address under sockaddr_un's platform limit.
			home, err := os.MkdirTemp("/tmp", "cxs-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			n.home = home
			cwd := mkdir(t, filepath.Join(home, "proj"))
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			codexOnPath(t, home)
			n.codices = newCodexRegistry()
			n.configureCodex(proto.CodexConfig{})
			writeFile(t, filepath.Join(n.codexHome(), "config.toml"), "model_reasoning_effort = \"low\"\n")
			dir := filepath.Join(n.codexHome(), "app-server-control")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "app-server-control.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(socket, 0600); err != nil {
				t.Fatal(err)
			}
			rf := &remoteFixture{home: n.codexHome()}
			rf.response = func(method string, p map[string]any) (any, bool) {
				if method == "turn/start" {
					rf.mu.Lock()
					effort := rf.threadEffort
					rf.mu.Unlock()
					if effort != "high" {
						t.Errorf("first turn used %q", effort)
					}
				}
				if tc.ignoreUpdate && method == "thread/settings/update" {
					return map[string]any{}, true
				}
				if tc.wrongReadID && method == "thread/read" {
					return map[string]any{"thread": map[string]any{"id": startID, "reasoningEffort": "high"}}, true
				}
				if method != "thread/start" {
					return nil, false
				}
				rf.mu.Lock()
				rf.threads = []string{remoteRaw}
				rf.threadEffort = tc.initialEffort
				rf.mu.Unlock()
				if tc.rejectAfterCreation {
					return &codexError{Code: -32602, Message: "fixture rejected parameters after creation"}, true
				}
				return map[string]any{"thread": map[string]any{"id": remoteRaw}, "reasoningEffort": tc.initialEffort, "model": "example"}, true
			}
			if tc.legacy {
				rf.reject, rf.rejectCode = "thread/settings/update", -32601
			}
			server := &http.Server{Handler: http.HandlerFunc(rf.serve)}
			go server.Serve(listener)
			t.Cleanup(func() { server.Close() })
			endpoint := "unix://" + filepath.Join(canonicalCodexHome(n.codexHome(), n.home), "app-server-control", "app-server-control.sock")
			connectFixtureEndpoint(t, n, n.codexHome(), "local", endpoint)
			withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
					f.setScreen("w1:p2", cxReady)
					if rf.count("thread/settings/update") != 1 || rf.count("turn/start") != 0 {
						t.Error("pane opened before configuration or after a premature first turn")
					}
				}
			})
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", remoteRaw, proto.StatusDone, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
			result, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "codex", "prompt": tc.prompt, "effort": "high", "model": "example", "codex_effort_fallback": proto.CodexFallbackNotify})
			if tc.rejectAfterCreation {
				if err == nil || !strings.Contains(err.Error(), "rejected parameters after creation") || rf.count("thread/start") != 1 || rf.count("thread/settings/update") != 0 || rf.count("turn/start") != 0 || len(f.trace("tab.create")) != 0 {
					t.Fatalf("ambiguous creation must stop before retry or delivery: %v", err)
				}
				return
			}
			if tc.refuse {
				if rpcCode(err) != proto.CodeCodexEffortTerminal || rf.count("thread/unsubscribe") != 1 || rf.count("turn/start") != 0 || len(f.trace("tab.create")) != 0 {
					t.Fatalf("unexpected failed preparation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s := decodeSessions(t, []any{result["session"]})[0]
			if s.SessionID != remoteRaw || s.CodexRoute != codexRouteAppServer || !slices.Contains(s.Efforts, "high") || s.Effort != "high" || s.Capabilities == nil || s.Capabilities.Model == nil || !*s.Capabilities.Model {
				t.Fatalf("returned session %+v", s)
			}
			if !slices.Equal(ran, []string{"codex", "--remote", endpoint, "resume", remoteRaw}) {
				t.Fatalf("pane ran %q", ran)
			}
			starts := rf.sent("thread/start")
			if len(starts) != 1 || starts[0]["config"].(map[string]any)["model_reasoning_effort"] != "high" || starts[0]["model"] != "example" || starts[0]["cwd"] != cwd {
				t.Fatalf("thread start %v", starts)
			}
			if tc.prompt == "" {
				if rf.count("turn/start") != 0 {
					t.Fatal("empty start sent a turn")
				}
				if err := n.sendCodex(context.Background(), s.SessionID, "later", nil); err != nil {
					t.Fatal(err)
				}
			}
			turns := rf.sent("turn/start")
			if len(turns) != 1 || turns[0]["threadId"] != remoteRaw || turns[0]["input"].([]any)[0].(map[string]any)["text"] != firstNonEmpty(tc.prompt, "later") {
				t.Fatalf("first turn %v", turns)
			}
			if !tc.legacy {
				_, err = request(t, n, "session.modes", map[string]any{"session_id": remoteRaw, "model": "second-model", "effort": "low"})
				updates := rf.sent("thread/settings/update")
				if err != nil || len(updates) != 2 || updates[1]["model"] != "second-model" || updates[1]["effort"] != "low" {
					t.Fatalf("phone settings %v: %v", updates, err)
				}
			}
		})
	}
}

func TestCodexEffortFallbackWaitsForTaskCard(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex", "--profile", "custom"}})
	n, f, cwd, _ := codexStartHerdr(t, 0)
	p := map[string]any{"cwd": cwd, "launcher": "cx", "effort": "high", "prompt": "first", "codex_effort_fallback": proto.CodexFallbackNotify}
	if _, err := request(t, n, "session.start", p); rpcCode(err) != proto.CodeCodexEffortTerminal || len(f.trace("tab.create", "pane.send_input")) != 0 {
		t.Fatalf("start proceeded before notice: %v", err)
	}
	p["codex_effort_fallback"] = proto.CodexFallbackAnnounced
	result, err := request(t, n, "session.start", p)
	if err != nil {
		t.Fatal(err)
	}
	if s := decodeSessions(t, []any{result["session"]})[0]; s.CodexRoute != codexRouteTerminal || !slices.Equal(ran, []string{"codex", "--profile", "custom", "-c", "model_reasoning_effort=high"}) {
		t.Fatalf("fallback %s %v", s.CodexRoute, ran)
	}
}
