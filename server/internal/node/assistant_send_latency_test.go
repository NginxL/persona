package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// Both inputs are required: a delayed snapshot cannot delay the independent
// permission ACK, and that ACK cannot let the user prompt escape the snapshot.
func TestAssistantSendOverlapsContextAndAcknowledgedModes(t *testing.T) {
	f := cliTest(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("HOME"), ".claude"))
	controlFile(t, f, "control-hold", "")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.fh.mu.Lock()
	f.fh.onContext = func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	f.fh.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "unique user prompt", nil, "plan", "", cliExec())
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot was not requested")
	}
	// The real node and its subprocess protocol are used; only claude and the model are
	// replaced by the local shell fixture. No model, config or token is contacted.
	controlSeen := time.NewTicker(10 * time.Millisecond)
	defer controlSeen.Stop()
	controlDeadline := time.NewTimer(3 * time.Second) // shorter than the unchanged 8-second snapshot timeout
	defer controlDeadline.Stop()
	for f.file("control-waiting") == "" {
		select {
		case <-controlSeen.C:
		case <-controlDeadline.C:
			t.Fatal("permission control waited for the blocked snapshot")
		}
	}
	if strings.Contains(f.file("stdin.jsonl"), "unique user prompt") {
		t.Fatal("user input escaped before the snapshot and permission ACK")
	}
	if err := os.Remove(filepath.Join(f.dir, "control-hold")); err != nil {
		t.Fatal(err)
	}
	cliWait(t, "acknowledged mode", func() bool {
		f.n.mu.Lock()
		var p *assistantProc
		for _, candidate := range f.n.procs {
			p = candidate
		}
		f.n.mu.Unlock()
		if p == nil {
			return false
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.mode == "plan"
	})
	if strings.Contains(f.file("stdin.jsonl"), "unique user prompt") {
		t.Fatal("permission ACK bypassed the pending snapshot")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send did not finish after both dependencies completed")
	}
	cliWait(t, "the fake CLI to consume the delivered input", func() bool { return strings.Contains(f.file("stdin.jsonl"), "unique user prompt") })
	input := f.file("stdin.jsonl")
	if !strings.Contains(input, "snapshot") || !strings.Contains(input, "unique user prompt") {
		t.Fatalf("context or original prompt lost: %s", input)
	}
}

// A public mention turn must not pull the owner's private live snapshot into the main conversation. This is per send:
// the ordinary-send control above keeps requesting the snapshot, and Public never becomes process/model state (#11130).
func TestAssistantPublicSendSkipsOwnerSnapshot(t *testing.T) {
	f := cliTest(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("HOME"), ".claude"))
	calls := 0
	f.fh.mu.Lock()
	f.fh.onContext = func(context.Context) { calls++ }
	f.fh.mu.Unlock()
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "public mention request", nil, "", "", modelExecution{Public: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.n.stopAssistant(id) })
	input := f.file("stdin.jsonl")
	if !strings.Contains(input, "public mention request") || strings.Contains(input, "snapshot") {
		t.Fatalf("public input carries an owner snapshot: %s", input)
	}
	f.fh.mu.Lock()
	defer f.fh.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the public send requested %d owner snapshots", calls)
	}
}

// Opt-in wall-clock probe, also usable unchanged against the base with go
// -overlay. These delays simulate RPC/runtime latency; this is not real model
// latency. The production Node and a real local claude-shaped subprocess run.
func TestAssistantSendIsolatedLatency(t *testing.T) {
	if os.Getenv("PERSONA_SEND_LATENCY_PROBE") == "" {
		t.Skip("set PERSONA_SEND_LATENCY_PROBE=1 for the isolated wall-clock probe")
	}
	timeout := os.Getenv("PERSONA_SEND_LATENCY_TIMEOUT") != ""
	delay := "3"
	if timeout {
		delay = "30" // no ACK inside the unchanged 15-second control budget
	}
	fakeClaudeHome(t, `echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      sleep `+delay+`
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*) echo '{"type":"result","subtype":"success"}' ;;
  esac
done
`)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("HOME"), ".claude"))
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.onContext = func(ctx context.Context) {
		if timeout {
			<-ctx.Done() // the production context RPC budget remains 8 seconds
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
		}
	}
	fh.mu.Unlock()
	start := time.Now()
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "plan", "")
	elapsed := time.Since(start)
	if id != "" {
		defer n.stopAssistant(id)
	}
	if (err != nil) != timeout {
		t.Fatalf("timeout=%v err=%v", timeout, err)
	}
	t.Logf("isolated real Node / fake CLI: timeout=%v final=%s err=%v", timeout, elapsed, err)
}

// TestSendSocketNodeProcess serves the production Node handler in a separate
// process. Only its CLI and herdr socket are replaced; neither can reach a real
// model or terminal. The hub socket probe builds and launches this test binary.
func TestSendSocketNodeProcess(t *testing.T) {
	dir := os.Getenv("PERSONA_SEND_SOCKET_DIR")
	if dir == "" {
		t.Skip("helper process for TestSendReceiptOverHTTPAndWebSocket")
	}
	fakeClaudeHome(t, `echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      sleep 3
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$PERSONA_SEND_SOCKET_DIR/runtime-input.jsonl"
      echo '{"type":"result","subtype":"success"}' ;;
  esac
done
`)
	n, terminal, home, cwd := guideNode(t, true)
	n.akeys = map[string]*assistantProc{}
	n.logger = log.New(os.Stderr, "", 0)
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	for name, data := range map[string]string{".zshrc": "", ".claude.json": `{"hasCompletedOnboarding":true}`} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const mainID = "0d000000-0000-4000-8000-00000000000b"
	mainDir := filepath.Join(home, ".persona", "assistant")
	if err := os.MkdirAll(mainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	n.state.put(mainID, sessionState{Cwd: mainDir, Launcher: "claude", AppMode: "plan"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	peer := rpc.New("probe-node", os.Stdout)
	n.peer = peer
	stage := func(kind, phase string) {
		_ = peer.Notify("probe.stage", map[string]any{"kind": kind, "phase": phase, "at": time.Now().UTC()})
	}
	terminal.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	terminal.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			<-time.After(1200 * time.Millisecond) // deliberate fake terminal delay, never a real engine
			stage("agent", "terminal_handoff")
			appendTranscript(t, home, cwd, sidA, promptLine(cwd, str(c.Params["text"]), 0))
		}
	})
	peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "probe.info" {
			return map[string]any{"main": mainID, "agent": sidA, "main_cwd": mainDir, "agent_cwd": cwd, "home": home}, nil
		}
		if method == "session.send" {
			var p struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(params, &p)
			kind := "agent"
			if p.SessionID == mainID {
				kind = "main"
			}
			stage(kind, "node_received")
			defer stage(kind, "node_returned")
		}
		return n.handle(ctx, method, params)
	}
	t.Cleanup(func() {
		n.mu.Lock()
		ids := make([]string, 0, len(n.procs))
		for id := range n.procs {
			ids = append(ids, id)
		}
		n.mu.Unlock()
		for _, id := range ids {
			n.stopAssistant(id)
		}
	})
	if err := peer.Serve(ctx, os.Stdin); err != nil && ctx.Err() == nil && err != io.EOF {
		t.Fatal(err)
	}
}
