package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexExactModelIsSentOnStartResumeAndTurn(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "resume"}[resume], func(t *testing.T) {
			n := codexTestNode(t)
			withFakeHub(t, n)
			cwd := t.TempDir()
			p, c, f := newFakeCodex(t, n, cwd)
			p.model = modelExecution{Model: "specific-model"}
			method := "thread/start"
			id := ""
			if resume {
				method = "thread/resume"
				id = testThread
			}
			f.results[method] = map[string]any{"thread": map[string]any{"id": testThread}}
			f.results["turn/start"] = map[string]any{"turn": map[string]any{"id": "turn"}}
			if err := c.start(context.Background(), id, cwd, resume); err != nil {
				t.Fatal(err)
			}
			if err := c.deliver(context.Background(), "", "hello", nil); err != nil {
				t.Fatal(err)
			}
			for _, m := range []string{method, "turn/start"} {
				var params map[string]any
				_ = json.Unmarshal(f.sent(m)[0].Params, &params)
				if params["model"] != "specific-model" {
					t.Fatalf("%s did not receive model: %+v", m, params)
				}
			}
		})
	}
}

func TestModelFingerprintTracksManagedProviderChanges(t *testing.T) {
	n, _ := testNode(t)
	option := modelExecution{Model: "specific", Fingerprint: "binding"}
	n.setManaged([]proto.ManagedLauncher{{Name: "provider", Model: "default", APIKey: "key-one"}})
	first := n.executionFingerprint("provider", option)
	n.setManaged([]proto.ManagedLauncher{{Name: "provider", Model: "default", APIKey: "key-two"}})
	if second := n.executionFingerprint("provider", option); first == second {
		t.Fatal("credential change reused old process")
	}
	if first == n.executionFingerprint("provider", modelExecution{Model: "different", Fingerprint: "binding"}) {
		t.Fatal("model change reused old process")
	}
}

// Every provider field (#2455) is in the fingerprint.
func TestModelFingerprintTracksProviderFields(t *testing.T) {
	n, _ := testNode(t)
	base := proto.ManagedLauncher{Name: "p", Kind: "api", BaseURL: "https://gw.example.com", APIKey: "sk-test-k", Model: "m"}
	opt := modelExecution{Model: "m", Fingerprint: "b"}
	n.setManaged([]proto.ManagedLauncher{base})
	plain := n.executionFingerprint("p", opt)
	for name, change := range map[string]func(*proto.ManagedLauncher){
		"models":         func(l *proto.ManagedLauncher) { l.Models = []proto.ProviderModel{{ID: "m", ContextWindow: 1000000}} },
		"opus":           func(l *proto.ManagedLauncher) { l.OpusModel = "o" },
		"sonnet":         func(l *proto.ManagedLauncher) { l.SonnetModel = "s" },
		"haiku":          func(l *proto.ManagedLauncher) { l.HaikuModel = "h" },
		"fable":          func(l *proto.ManagedLauncher) { l.FableModel = "f" },
		"subagent force": func(l *proto.ManagedLauncher) { l.SubagentForce = true },
		"headers":        func(l *proto.ManagedLauncher) { l.Headers = "X-Test: 1" },
		"proxy":          func(l *proto.ManagedLauncher) { l.Proxy = "http://proxy.example.com" },
		"no proxy":       func(l *proto.ManagedLauncher) { l.NoProxy = "localhost" },
		"timeout":        func(l *proto.ManagedLauncher) { l.TimeoutMS = 1 },
		"compact":        func(l *proto.ManagedLauncher) { l.CompactWindow = 1 },
		"max context":    func(l *proto.ManagedLauncher) { l.MaxContext = 1 },
		"effort":         func(l *proto.ManagedLauncher) { l.Effort = "high" },
	} {
		l := base
		change(&l)
		n.setManaged([]proto.ManagedLauncher{l})
		if n.executionFingerprint("p", opt) == plain {
			t.Errorf("%s change reused old process", name)
		}
	}
}

// A conversation answering or running background tasks takes a message whose model changed on what it runs: a new
// process would kill the tasks, and refusing it left the user unable to send while it kept starting more (#1554). The
// switch waits for the first message after it is idle.
func TestModelChangeWhileBusyGoesToTheRunningProcess(t *testing.T) {
	for _, mode := range []string{"", proto.ModeBypass} {
		t.Run("mode="+mode, func(t *testing.T) {
			n := codexTestNode(t)
			withFakeHub(t, n)
			// The model check would run this machine's claude through its login shell; on one where that doesn't start
			// (the hub) the send failed there, before the hold this is about.
			modelProbeCache.Lock()
			modelProbeCache.results[n.home+"\x00claude\x00new"] = modelProbeResult{at: time.Now(), agent: "claude"}
			modelProbeCache.Unlock()
			old := modelExecution{Model: "old", Fingerprint: "before"}
			in := &stdinBuf{}
			p := &assistantProc{id: testThread, agent: "claude", launcher: "claude", cwd: t.TempDir(), stdin: in, exited: make(chan struct{}), born: time.Now(),
				mode: "default", model: old, fingerprint: n.executionFingerprint("claude", old), tasks: []string{"background work"}}
			n.procs[testThread] = p
			n.state.put(testThread, sessionState{Launcher: "claude", Cwd: p.cwd, StartedAt: time.Now().UTC(), Model: "old", ModelFingerprint: "before", Tasks: p.tasks})
			if _, err := n.assistantSend(context.Background(), testThread, p.cwd, "claude", "hello", nil, mode, "", modelExecution{Model: "new", Fingerprint: "after"}); err != nil {
				t.Fatalf("a busy conversation refused the message: %v", err)
			}
			if !p.alive() || n.procs[testThread] != p {
				t.Fatal("background process killed")
			}
			if !strings.Contains(in.String(), "hello") {
				t.Fatalf("the message did not reach the running process: %q", in.String())
			}
			state, _ := n.state.get(testThread)
			if state.Model != "old" || state.ModelFingerprint != "before" || len(state.Tasks) != 1 {
				t.Fatalf("the state no longer says what the process runs: %+v", state)
			}
			// bypassPermissions is start-only: not noted as applied, so the next message after it is idle restarts it.
			if p.mode == proto.ModeBypass || strings.Contains(in.String(), "set_permission_mode") {
				t.Fatalf("bypassPermissions noted on a process started without it: %q", in.String())
			}
		})
	}
}

// After a restart the node tells a conversation it lost its background tasks by itself: on the launcher and model the
// hub runs its messages on, so the next one from the phone finds it on them (#1554); on its own with an older hub.
func TestAssistantModelComesFromTheHub(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.model = map[string]any{"launcher": "conv", "effort": "high", "model_fingerprint": "fp", "origin_known": true}
	fh.mu.Unlock()
	l, m, answered := n.assistantModelDecision(context.Background(), testThread)
	if l != "conv" || !answered || m != (modelExecution{Effort: "high", UseEffort: true, Fingerprint: "fp", OriginKnown: true}) {
		t.Fatalf("assistantModel = %q %+v answered=%v", l, m, answered)
	}
	fh.mu.Lock()
	got := fh.queried["assistant.model"]
	fh.mu.Unlock()
	if got != testThread {
		t.Fatalf("asked the hub about %q", got)
	}
	fh.mu.Lock()
	source := modelhttp.Source{Role: "background", Session: testThread}
	fh.model = map[string]any{"origin": originInbox, "origin_known": true, "inbox_permission_mode": proto.ModeBypass, "model_source": source}
	fh.mu.Unlock()
	if _, m, answered := n.assistantModelDecision(context.Background(), testThread); !answered || m.Origin != originInbox || !m.OriginKnown || m.InboxPermissionMode != proto.ModeBypass || m.Source != source {
		t.Fatalf("the hub's Inbox attestation, own mode or model source was lost: %+v answered=%v", m, answered)
	}
	fh.mu.Lock()
	fh.model = map[string]any{} // a successful scope refusal is not an ordinary-origin decision
	fh.mu.Unlock()
	if l, m, answered := n.assistantModelDecision(context.Background(), testThread); l != "" || !answered || m != (modelExecution{}) {
		t.Fatalf("with an empty hub answer: %q %+v answered=%v", l, m, answered)
	}
	fh.mu.Lock()
	fh.model = nil // an older hub: unknown method, not an ordinary-origin decision
	fh.mu.Unlock()
	if l, m, answered := n.assistantModelDecision(context.Background(), testThread); l != "" || answered || m != (modelExecution{}) {
		t.Fatalf("without the hub's answer: %q %+v answered=%v", l, m, answered)
	}
}
