package assistant

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A nearly full context is compacted, not replaced (#11137): these cover the assistant's side, which asks the node
// to compact first (session.send's compact_if_full) and says what came of it; the node's side is
// node/compact_before_send_test.go.

// nearlyFull is agent c with its context at 190k of a 200k window.
func nearlyFull(c proto.Session) proto.Session {
	c.ContextTokens, c.ContextWindow = 190_000, 200_000
	return c
}

// sessionSendCalls are the session.send params the nodes were asked for, in order.
func sessionSendCalls(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method != DeviceSessionSend {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		out = append(out, p)
	}
	return out
}

// startedBeforeSent: the node was asked to start before it was asked to send.
func startedBeforeSent(f *fakeHub) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	start, send := -1, -1
	for i, c := range f.calls {
		if c.Method == DeviceSessionStart && start < 0 {
			start = i
		}
		if c.Method == DeviceSessionSend && send < 0 {
			send = i
		}
	}
	return start >= 0 && send > start
}

// The resume of a nearly full Claude Code agent is no longer refused: it is resumed without a first message, then the
// text goes through session.send with compact_if_full, which the node compacts it for first; the receipt says so.
func TestResumeOfANearlyFullAgentCompactsBeforeTheText(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", nearlyFull(stoppedBoxAgent("full0001")))
	resumingNode(f)
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "compacted": true}, nil
	})
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "full0001",
		"text": "go on with the fix", "resume": true, "resume_why": "it knows the gate machine", "task": map[string]any{"title": "接着做"}})
	if err != nil {
		t.Fatal(err)
	}
	starts := startCalls(f)
	if len(starts) != 1 || !maps.Equal(starts[0], map[string]any{"resume_session_id": "full0001", "resume_automatic": true}) {
		t.Fatalf("session.start: %v", starts)
	}
	sends := sessionSendCalls(f)
	if len(sends) != 1 || sends[0]["session_id"] != "full0001" || sends[0]["text"] != "go on with the fix" || sends[0]["compact_if_full"] != true {
		t.Fatalf("session.send: %v", sends)
	}
	if !startedBeforeSent(f) {
		t.Fatal("the text was sent before the agent was resumed")
	}
	for _, want := range []string{"Resumed full0001 on box", "nearly full (190000 of 200000 tokens)", "compacted it with its own /compact first"} {
		if !strings.Contains(res, want) {
			t.Fatalf("receipt lacks %q: %s", want, res)
		}
	}
}

// A compaction that fails leaves the text unsent: the refusal gives the node's reason, says the agent was resumed and
// rests with its context, and only now suggests a replacement.
func TestResumeCompactionFailureFallsBackWithTheReason(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", nearlyFull(stoppedBoxAgent("full0001")))
	resumingNode(f)
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: proto.CodeCompactFailed, Message: "its context is nearly full (190000 of 200000 tokens) and " +
			"compacting it failed: it finished /compact without compacting: Error during compaction: prompt is too long. The message was not sent"}
	})
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "full0001",
		"text": "go on", "resume": true, "resume_why": "needs its context", "task": map[string]any{"title": "接着做"}})
	if err == nil {
		t.Fatal("a failed compaction passed for a delivery")
	}
	for _, want := range []string{"could not compact full0001 on box", "Error during compaction: prompt is too long",
		"resumed and now rests with its context", "nothing was sent", "start a replacement", "task_plan with continues", "agent_start"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q: %v", want, err)
		}
	}
	if len(startCalls(f)) != 1 || len(sessionSendCalls(f)) != 1 {
		t.Fatalf("started %v, sent %v", startCalls(f), sessionSendCalls(f))
	}
}

// A runtime Persona cannot compact (pi here) keeps the old refusal for a nearly full resume, saying why.
func TestResumeOfANearlyFullAgentPersonaCannotCompactIsRefused(t *testing.T) {
	s, f := newQuietService(t)
	c := nearlyFull(stoppedBoxAgent("full0001"))
	c.Agent = "pi"
	f.setSessions("box", c)
	resumingNode(f)
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "full0001",
		"text": "go on", "resume": true, "resume_why": "needs its context", "task": map[string]any{"title": "接着做"}})
	if err == nil || !strings.Contains(err.Error(), "pi has no compaction Persona can run") || !strings.Contains(err.Error(), "agent_start") {
		t.Fatalf("refusal: %v", err)
	}
	if len(startCalls(f)) != 0 || len(sessionSendCalls(f)) != 0 {
		t.Fatalf("started %v, sent %v", startCalls(f), sessionSendCalls(f))
	}
}

// A message to a running nearly full agent asks its node to compact first, and the receipt says it did.
func TestSendToARunningNearlyFullAgentCompactsFirst(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", nearlyFull(boxAgent("live0001")))
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "compacted": true}, nil
	})
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "live0001", "text": "next step"})
	if err != nil {
		t.Fatal(err)
	}
	if sends := sessionSendCalls(f); len(sends) != 1 || sends[0]["compact_if_full"] != true || sends[0]["text"] != "next step" {
		t.Fatalf("session.send: %v", sends)
	}
	if !strings.Contains(res, "after compacting its nearly full context with its own /compact") {
		t.Fatalf("receipt: %s", res)
	}
}

// Mid-turn, the node holds it behind the turn and compacts once the turn ends; the receipt says so.
func TestSendToABusyNearlyFullAgentWaitsForTheTurnThenCompacts(t *testing.T) {
	s, f := newQuietService(t)
	c := nearlyFull(boxAgent("busy0001"))
	c.Status, c.State = proto.StatusWorking, proto.StateWorking
	f.setSessions("box", c)
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "queued": true, "compact_after_turn": true}, nil
	})
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "busy0001", "text": "after this"})
	if err != nil {
		t.Fatal(err)
	}
	if sends := sessionSendCalls(f); len(sends) != 1 || sends[0]["compact_if_full"] != true {
		t.Fatalf("session.send: %v", sends)
	}
	if !strings.Contains(res, "Queued for busy0001 on box") || !strings.Contains(res, "once the turn ends it is compacted with its own /compact") {
		t.Fatalf("receipt: %s", res)
	}
}

// A running agent's failed compaction: nothing sent, the reason, its context kept, and the replacement as the fallback.
func TestSendToARunningNearlyFullAgentWhoseCompactionFails(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", nearlyFull(boxAgent("live0001")))
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: proto.CodeCompactFailed, Message: "compacting it failed: it did not start compacting within 1m30s"}
	})
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "live0001", "text": "next step"})
	if err == nil {
		t.Fatal("a failed compaction passed for a delivery")
	}
	for _, want := range []string{"could not compact live0001 on box", "did not start compacting", "keeps its context as it was",
		"nothing was sent", "start a replacement"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q: %v", want, err)
		}
	}
}

// Below the threshold nothing changes: a running agent's send carries no compact_if_full, and a resume still starts
// the agent with the text as its first message and sends nothing else.
func TestNotNearlyFullBehavesAsBefore(t *testing.T) {
	s, f := newQuietService(t)
	live := boxAgent("live0001")
	live.ContextTokens, live.ContextWindow = 100_000, 200_000
	roomy := stoppedBoxAgent("roomy001")
	roomy.ContextTokens, roomy.ContextWindow = 100_000, 200_000
	f.setSessions("box", live, roomy)
	resumingNode(f)
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "live0001", "text": "next step"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	sent := f.agentSent
	f.mu.Unlock()
	if len(sent) != 1 || sent[0]["compact_if_full"] != nil || !strings.HasPrefix(res, "Sent to live0001 on box.") {
		t.Fatalf("sent %v: %s", sent, res)
	}
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "roomy001",
		"text": "go on", "resume": true, "resume_why": "needs its context", "task": map[string]any{"title": "接着做"}}); err != nil {
		t.Fatal(err)
	}
	starts := startCalls(f)
	if len(starts) != 1 || !maps.Equal(starts[0], map[string]any{"resume_session_id": "roomy001", "prompt": "go on", "resume_automatic": true}) {
		t.Fatalf("session.start: %v", starts)
	}
	if len(sessionSendCalls(f)) != 1 { // only the first test's send
		t.Fatalf("session.send: %v", sessionSendCalls(f))
	}
}

// A Codex thread nothing runs (codex_history: the node resumes it with codex resume in a new tab) is compacted like a
// Claude Code agent: resumed without a first message, then the text with compact_if_full. A Codex App chat is refused,
// saying why. (A stopped app-server thread, a Claude outside Persona's terminals or an identity conflict is read-only
// before any resume, as before.)
func TestResumeOfANearlyFullCodexCompactsBeforeTheText(t *testing.T) {
	s, f := newQuietService(t)
	c := stoppedBoxAgent("codx0001")
	c.Agent, c.SourceKind, c.ContextTokens, c.ContextWindow = "codex", "codex_history", 428111, 475000
	f.setSessions("box", c)
	resumingNode(f)
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "compacted": true}, nil
	})
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "codx0001",
		"text": "which base does the mirror have?", "resume": true, "resume_why": "it set up the gate machine", "task": map[string]any{"title": "接着做"}})
	if err != nil {
		t.Fatal(err)
	}
	starts, sends := startCalls(f), sessionSendCalls(f)
	if len(starts) != 1 || starts[0]["prompt"] != nil || len(sends) != 1 || sends[0]["compact_if_full"] != true || !startedBeforeSent(f) {
		t.Fatalf("started %v, sent %v", starts, sends)
	}
	if !strings.Contains(res, "nearly full (428111 of 475000 tokens)") {
		t.Fatalf("receipt: %s", res)
	}

	s, f = newQuietService(t)
	c.SpaceKind = "codex_app"
	f.setSessions("box", c)
	resumingNode(f)
	_, err = runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "codx0001",
		"text": "go on", "resume": true, "resume_why": "needs its context", "task": map[string]any{"title": "接着做"}})
	if err == nil || !strings.Contains(err.Error(), "Codex App chat") || !strings.Contains(err.Error(), "cannot be compacted") {
		t.Fatalf("refusal: %v", err)
	}
	if len(startCalls(f)) != 0 || len(sessionSendCalls(f)) != 0 {
		t.Fatalf("started %v, sent %v", startCalls(f), sessionSendCalls(f))
	}
}

// A running Codex on its app-server that is nearly full gets compact_if_full too (thread/compact/start on its node).
func TestSendToANearlyFullCodexOnItsAppServerCompactsFirst(t *testing.T) {
	s, f := newQuietService(t)
	c := nearlyFull(boxAgent("codx0002"))
	c.Agent, c.SourceKind = "codex", "codex_app_server"
	c.Capabilities = &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true}
	f.setSessions("box", c)
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "compacted": true}, nil
	})
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "codx0002", "text": "next"}); err != nil {
		t.Fatal(err)
	}
	if sends := sessionSendCalls(f); len(sends) != 1 || sends[0]["compact_if_full"] != true {
		t.Fatalf("session.send: %v", sends)
	}
}
