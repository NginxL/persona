package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/rpc"
)

// closeoutStartNode is a node whose hub answers agents.closeout.brief with answer(); asked counts the asks.
func closeoutStartNode(t *testing.T, answer func() (string, error)) (n *Node, asked *atomic.Int64) {
	t.Helper()
	return closeoutHookNode(t, func(method string, raw json.RawMessage) (any, error) {
		var p assistantwire.CloseoutBriefRequest
		if method != assistantwire.MethodCloseoutBrief || json.Unmarshal(raw, &p) != nil || p.SessionID == "" || p.Cwd != "/code/app" {
			t.Errorf("unexpected request %s %s", method, raw)
		}
		text, err := answer()
		return assistantwire.CloseoutBriefResponse{Context: text}, err
	})
}

func closeoutHookNode(t *testing.T, answer func(string, json.RawMessage) (any, error)) (n *Node, asked *atomic.Int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	t.Cleanup(func() { cancel(); n2hW.Close(); h2nW.Close(); n2hR.Close(); h2nR.Close() })
	n, _ = testNode(t)
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	asked = &atomic.Int64{}
	hub.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		asked.Add(1)
		return answer(method, raw)
	}
	go hub.Serve(ctx, n2hR)
	go n.peer.Serve(ctx, h2nR)
	return n, asked
}

func TestDispatchedReportFormSkipsSecondSessionStart(t *testing.T) {
	n, asked := closeoutStartNode(t, func() (string, error) { return "duplicate", nil })
	for _, form := range []string{closeout.Example, closeout.MarkdownExample("English")} {
		n.state.put("s1", sessionState{TaskInstructions: "Review the samples.\n" + form})
		if got := n.closeoutStart(t.Context(), sockRequest{Pane: "w1:p1"}, startHook("s1")); got != nil || asked.Load() != 0 {
			t.Fatalf("supplied form shown twice: %v (%d calls)", got, asked.Load())
		}
	}
}

func startHook(sid string) hookInput {
	return hookInput{HookEventName: "SessionStart", SessionID: sid, Cwd: "/code/app", Source: "startup"}
}

// A starting session in a pane gets the manager's text as its additional context; the manager is asked once per
// session that it gave a text, and a start after a compaction is told again without another ask.
func TestCloseoutStartTellsOncePerSession(t *testing.T) {
	text := "the form"
	n, asked := closeoutStartNode(t, func() (string, error) { return text, nil })
	pane := sockRequest{Kind: "hook", Pane: "w1:p1"}
	for i := range 2 {
		out, _ := n.closeoutStart(t.Context(), pane, startHook("s1")).(map[string]any)
		spec, _ := out["hookSpecificOutput"].(map[string]any)
		if spec["hookEventName"] != "SessionStart" || spec["additionalContext"] != "the form" {
			t.Fatalf("start %d: %v", i, out)
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("the manager was asked %d times for one session", asked.Load())
	}
	text = "" // the switch is off: nothing is told, and it is asked again next time (the switch may be turned on)
	for range 2 {
		if out := n.closeoutStart(t.Context(), pane, startHook("s2")); out != nil {
			t.Fatalf("told with nothing to tell: %v", out)
		}
	}
	if asked.Load() != 3 {
		t.Fatalf("asked %d times, want 3", asked.Load())
	}
}

// Nothing is asked, and nothing told, for a session that only reports, a subagent, an assistant conversation, a
// session with no pane, or another event.
func TestCloseoutStartLeavesOthersAlone(t *testing.T) {
	n, asked := closeoutStartNode(t, func() (string, error) { return "the form", nil })
	const chat = "11111111-1111-4111-8111-111111111111"
	n.mu.Lock()
	n.procs[chat] = &assistantProc{id: chat, agent: "claude", exited: make(chan struct{})}
	n.mu.Unlock()
	pane := sockRequest{Kind: "hook", Pane: "w1:p1"}
	sub, prompt := startHook("s1"), startHook("s1")
	sub.AgentID, prompt.HookEventName = "a1", "UserPromptSubmit"
	for name, tc := range map[string]struct {
		req sockRequest
		in  hookInput
	}{
		"report only": {sockRequest{Kind: "hook", Pane: "w1:p1", ReportOnly: true}, startHook("s1")},
		"no pane":     {sockRequest{Kind: "hook"}, startHook("s1")},
		"a subagent":  {pane, sub},
		"a prompt":    {pane, prompt},
		"assistant":   {pane, startHook(chat)},
		"no session":  {pane, startHook("")},
	} {
		if out := n.closeoutStart(t.Context(), tc.req, tc.in); out != nil {
			t.Errorf("%s: told %v", name, out)
		}
	}
	if asked.Load() != 0 {
		t.Fatalf("the manager was asked %d times", asked.Load())
	}
	n.peer = nil
	if out := n.closeoutStart(t.Context(), pane, startHook("s1")); out != nil {
		t.Fatalf("told without a hub: %v", out)
	}
}

// A manager that fails or does not answer in time costs the start no more than closeoutStartWait, tells nothing, and
// is asked again at the session's next start.
func TestCloseoutStartDoesNotWaitForASlowManager(t *testing.T) {
	release := make(chan struct{})
	fail := true
	n, asked := closeoutStartNode(t, func() (string, error) {
		if fail {
			return "", errors.New("unavailable")
		}
		<-release
		return "late", nil
	})
	t.Cleanup(func() { close(release) })
	pane := sockRequest{Kind: "hook", Pane: "w1:p1"}
	if out := n.closeoutStart(t.Context(), pane, startHook("s1")); out != nil {
		t.Fatalf("told after an error: %v", out)
	}
	fail = false
	began := time.Now()
	if out := n.closeoutStart(t.Context(), pane, startHook("s1")); out != nil {
		t.Fatalf("told by a manager that did not answer: %v", out)
	}
	if d := time.Since(began); d > closeoutStartWait+time.Second {
		t.Fatalf("the start waited %s", d)
	}
	if asked.Load() != 2 {
		t.Fatalf("asked %d times: a failed ask must not be remembered", asked.Load())
	}
	if _, kept := n.closeoutBriefs.get("s1"); kept {
		t.Fatal("no answer was remembered as an answer")
	}
}
