package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestQueuedEffortWaitsForTheTurnAndConfirmation(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	n.noteModes(sidA, "", "low")
	// The node has cached idle, but Claude started working before the request arrived.
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	receipt, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "effort": "high", "queue_effort": true})
	if err != nil || receipt["pending_effort"] != "high" {
		t.Fatalf("busy effort receipt = %v, %v", receipt, err)
	}
	row := rowOf(t, n, sidA)
	if row.Effort != "low" || row.PendingEffort == nil || *row.PendingEffort != "high" || row.EffortChangeFailed {
		t.Fatalf("busy row = %q current, %v pending, failed=%v", row.Effort, row.PendingEffort, row.EffortChangeFailed)
	}
	code, fields := refusalOf(t, sendWaitingForEffort(t, n))
	if code != proto.CodeEffortChangePending || fields["delivered"] != deliveredNone || fields["effort"] != "high" {
		t.Fatalf("send refusal = %q %v", code, fields)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("drove the busy Claude: %q", got)
	}

	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	done := empty + "\n  ⎿  Set effort level to high (this session only)\n"
	f.screenSteps("w1:p1", func(c herdrCall) bool { return isKeys("enter")(c) || isKeys("s")(c) }, empty, listPicker, done)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	waitFor(t, "confirmation after the turn", func() bool {
		row = rowOf(t, n, sidA)
		return row.PendingEffort == nil && row.Effort == "high"
	})
	if got := f.trace("pane.send_text"); len(got) != 1 || got[0] != call("pane.send_text", map[string]any{"pane_id": "w1:p1", "text": "/effort"}) {
		t.Fatalf("picker commands = %q", got)
	}
}

func TestQueuedEffortConfirmationSurvivesReconnect(t *testing.T) {
	worker, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	share := worker.switches()
	const pane = "w1:p1"
	share.setEffortChange(sidA, effortChange{target: "high", pane: pane})
	readRow := func(n *Node, pane string, m quickMeta) proto.Session {
		row := proto.Session{SessionID: sidA, PaneID: pane, Agent: "claude"}
		n.terminalModesRow(&row, m)
		return row
	}

	// The new Node starts serving before the old connection's worker finishes the picker.
	serving, _ := testNode(t)
	serving.share.Store(share)
	share.receive(serving)
	old := quickMeta{effort: "low", effortAt: time.Now().Add(-time.Hour)}
	if row := readRow(serving, pane, old); row.PendingEffort == nil || row.Effort != "low" {
		t.Fatalf("reconnect lost the pending pick: %+v", row)
	}

	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	done := empty + "\n  ⎿  Set effort level to high (this session only)\n"
	f.screenSteps(pane, func(c herdrCall) bool { return isKeys("enter")(c) || isKeys("s")(c) }, empty, listPicker, done)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, pane, "w1:t1", "w1", cwd, "")}, nil))
	worker.tryEffortChange(sidA)
	if row := readRow(serving, pane, old); row.PendingEffort != nil || row.Effort != "high" {
		t.Fatalf("serving Node lost the confirmation: %+v", row)
	}

	later, _ := testNode(t)
	later.share.Store(share)
	share.receive(later)
	if row := readRow(later, pane, old); row.PendingEffort != nil || row.Effort != "high" {
		t.Fatalf("later connection lost the confirmation: %+v", row)
	}
	if row := readRow(later, "w1:p2", old); row.Effort != "low" {
		t.Fatalf("confirmation leaked to a different pane: %+v", row)
	}
	newer := quickMeta{mode: "plan", effort: "medium", effortAt: time.Now().Add(time.Second)}
	if row := readRow(later, pane, newer); row.Effort != "medium" || row.PermissionMode != "plan" {
		t.Fatalf("confirmation overwrote newer metadata: %+v", row)
	}
}

func TestQueuedEffortFailureRetainsTheSendBarrier(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusBlocked, nil)
	n.noteModes(sidA, "", "low")
	if err := pick(t, n, map[string]any{"effort": "high", "queue_effort": true}); err != nil {
		t.Fatal(err)
	}
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	waitFor(t, "the draft refusal on the row", func() bool {
		row := rowOf(t, n, sidA)
		return row.Effort == "low" && row.PendingEffort != nil && *row.PendingEffort == "high" && row.EffortChangeFailed
	})
	if code, _ := refusalOf(t, sendWaitingForEffort(t, n)); code != proto.CodeEffortChangePending {
		t.Fatalf("send refusal = %q", code)
	}
	if err := pick(t, n, map[string]any{"effort": "low", "queue_effort": true}); err != nil {
		t.Fatal(err)
	}
	if row := rowOf(t, n, sidA); row.PendingEffort != nil || row.EffortChangeFailed || row.Effort != "low" {
		t.Fatalf("the current effort did not clear the pending change: %+v", row)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("touched the draft: %q", got)
	}
}

func TestQueuedEffortRechecksIdleBeforeTyping(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	if err := pick(t, n, map[string]any{"effort": "high", "queue_effort": true}); err != nil {
		t.Fatal(err)
	}
	// Simulate a list built from an idle snapshot just before another turn started.
	n.wakeEffortChange(&proto.Session{SessionID: sidA, PaneID: "w1:p1", Status: proto.StatusIdle, PendingEffort: new("high")})
	waitCalls(t, f, "session.snapshot", 2)
	unlock, err := n.inputTurn(context.Background(), sidA)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if change := n.switches().effortChange(sidA); change.target != "high" || change.failed {
		t.Fatalf("the effort request did not stay pending: %+v", change)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("drove Claude during a new turn: %q", got)
	}
}

func sendWaitingForEffort(t *testing.T, n *Node) error {
	t.Helper()
	_, err := request(t, n, "session.send", map[string]any{
		"session_id": sidA, "text": "follow-up", "wait_for_effort": true,
	})
	return err
}

func TestFailedQueuedEffortClearsWhenPaneCloses(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusBlocked, nil)
	n.switches().setEffortChange(sidA, effortChange{target: "high", pane: "w1:p1", failed: true})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	n.tryEffortChange(sidA)
	if change := n.switches().effortChange(sidA); change.target != "" || change.failed {
		t.Fatalf("closed pane left a failed effort change: %+v", change)
	}
}
