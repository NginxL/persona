package node

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// loopTurn starts a turn on a fake app-server's process running model, as the recorded loop did (#10278).
func loopTurn(t *testing.T, model string) (*Node, *assistantProc, *codexConn, *fakeAppServer) {
	t.Helper()
	n := codexTestNode(t)
	p, c, f := newFakeCodex(t, n, t.TempDir())
	p.id, c.threadID = testThread, testThread
	p.model = modelExecution{Model: model}
	n.procs[testThread] = p
	send := assistantRecoverySend
	t.Cleanup(func() { assistantRecoverySend = send })
	assistantRecoverySend = func(context.Context, *Node, *assistantProc, string, string, modelExecution) error {
		t.Error("a stopped compaction loop entered model recovery")
		return nil
	}
	codexEvent(t, c, "turn/started", map[string]any{"threadId": testThread, "turn": map[string]any{"id": "turn-1"}})
	return n, p, c, f
}

func codexEvent(t *testing.T, c *codexConn, method string, params map[string]any) {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	c.notification(method, b)
}

// compactThenRequest is one step of the turn: Codex compacts (the item, then its usage reset), then the next request
// reports how much it sent.
func compactThenRequest(t *testing.T, c *codexConn, input int64) {
	t.Helper()
	codexEvent(t, c, "item/completed", map[string]any{"threadId": testThread, "item": map[string]any{"type": "contextCompaction", "id": "cc"}})
	codexEvent(t, c, "thread/compacted", map[string]any{"threadId": testThread, "turnId": "turn-1"})
	for _, in := range []int64{0, input} {
		codexEvent(t, c, "thread/tokenUsage/updated", map[string]any{"threadId": testThread, "turnId": "turn-1",
			"tokenUsage": map[string]any{"last": map[string]any{"inputTokens": in}, "modelContextWindow": 124518}})
	}
}

// loopStop is the reason the loop has decided to stop the turn with, "" while it has not: noteRequest decides it
// within the notification, before its interrupt goes out on a goroutine of its own.
func loopStop(c *codexConn) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loop.stop
}

func waitInterrupt(t *testing.T, f *fakeAppServer) {
	t.Helper()
	waitFor(t, "the loop's turn/interrupt", func() bool { return len(f.sent("turn/interrupt")) > 0 })
}

// The recorded loop: each request after a compaction was 166–173k tokens against a 124,518-token window. The second
// such compaction stops the turn, which ends as the user's to act on, with the reason in their language.
func TestCodexCompactionLoopStopsTheTurn(t *testing.T) {
	_, p, c, f := loopTurn(t, "small-131k")
	compactThenRequest(t, c, 173247)
	if loopStop(c) != "" || len(f.sent("turn/interrupt")) > 0 {
		t.Fatal("one compaction that fell short stopped the turn")
	}
	compactThenRequest(t, c, 170533)
	if loopStop(c) == "" {
		t.Fatal("two compactions in a row that made no room did not stop the turn")
	}
	waitInterrupt(t, f)
	codexEvent(t, c, "turn/completed", map[string]any{"threadId": testThread, "turn": map[string]any{"id": "turn-1", "status": "interrupted"}})
	p.mu.Lock()
	defer p.mu.Unlock()
	want := i18n.T(i18n.Default, "node-codex-assistant.compaction_loop_stopped")
	if p.turnError != want || p.turnErrorKind != proto.TurnErrorUser || p.busy {
		t.Fatalf("turn error %q kind %q busy %v, want %q, %q and idle", p.turnError, p.turnErrorKind, p.busy, want, proto.TurnErrorUser)
	}
}

// A compaction that makes room changes nothing, and one that fell short followed by one that worked starts the count
// again. The next turn counts afresh.
func TestCodexCompactionThatMakesRoomGoesOn(t *testing.T) {
	_, p, c, f := loopTurn(t, "small-131k")
	compactThenRequest(t, c, 30000)
	compactThenRequest(t, c, 120000) // 96% of the window: no room
	compactThenRequest(t, c, 40000)  // room again
	compactThenRequest(t, c, 118000) // 95%: one in a row only
	codexEvent(t, c, "turn/completed", map[string]any{"threadId": testThread, "turn": map[string]any{"id": "turn-1", "status": "completed"}})
	codexEvent(t, c, "turn/started", map[string]any{"threadId": testThread, "turn": map[string]any{"id": "turn-2"}})
	compactThenRequest(t, c, 119000) // the new turn's first
	// The decision is made within each notification, and no interrupt goes out without one.
	if stop := loopStop(c); stop != "" || len(f.sent("turn/interrupt")) > 0 {
		t.Fatalf("a turn whose compactions made room is stopped: %q", stop)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turnError != "" {
		t.Fatalf("turn error %q", p.turnError)
	}
}

// A request's usage with no compaction before it is not counted, however large: Codex compacts when it needs to.
func TestCodexLargeRequestWithoutCompactionIsNotALoop(t *testing.T) {
	_, _, c, f := loopTurn(t, "small-131k")
	for i := 0; i < 3; i++ {
		codexEvent(t, c, "thread/tokenUsage/updated", map[string]any{"threadId": testThread, "turnId": "turn-1",
			"tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 123000}, "modelContextWindow": 124518}})
	}
	if loopStop(c) != "" || len(f.sent("turn/interrupt")) > 0 {
		t.Fatal("requests without a compaction were taken for a loop")
	}
}

// The loop on a model the recovery switched to: the switch is dropped, so the next message goes to the configured
// model instead of the same fallback (applyModelRecoveryReturn would keep it there).
func TestCodexCompactionLoopLeavesTheFallback(t *testing.T) {
	n, _, c, f := loopTurn(t, "small-131k")
	n.state.put(testThread, sessionState{Launcher: "cx_test", IsMain: true}) // a conversation the recovery keeps
	n.recordRecoverySwitch(testThread, "cx_test", "big-1m", "", "small-131k", proto.ModelRecoveryReasonRateLimit, nil,
		[]string{"big-1m", "small-131k"})
	if !modelRecoverySwitched(n.modelRecovery(testThread)) {
		t.Fatal("no switch recorded")
	}
	compactThenRequest(t, c, 173247)
	compactThenRequest(t, c, 170533)
	waitInterrupt(t, f)
	codexEvent(t, c, "turn/completed", map[string]any{"threadId": testThread, "turn": map[string]any{"id": "turn-1", "status": "interrupted"}})
	if r := n.modelRecovery(testThread); modelRecoverySwitched(r) {
		t.Fatalf("the looping fallback is kept: %+v", r)
	}
}
