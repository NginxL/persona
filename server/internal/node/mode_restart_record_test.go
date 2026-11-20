package node

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// modeRestartEvents gives n a manager at the other end of its connection and returns what it has got of the
// session.mode_restart events so far (#10297).
func modeRestartEvents(t *testing.T, n *Node) func() []proto.ModeRestartEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2mR, n2mW := io.Pipe()
	m2nR, m2nW := io.Pipe()
	n.peer = rpc.New("n", n2mW)
	manager := rpc.New("m", m2nW)
	var mu sync.Mutex
	var got []proto.ModeRestartEvent
	manager.OnEvent = func(event string, params json.RawMessage) {
		if event != proto.EventModeRestart {
			return
		}
		var e proto.ModeRestartEvent
		if json.Unmarshal(params, &e) == nil {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
	}
	go func() { _ = manager.Serve(ctx, n2mR) }()
	go func() { _ = n.peer.Serve(ctx, m2nR) }()
	t.Cleanup(func() { cancel(); _ = n2mW.Close(); _ = m2nW.Close() })
	return func() []proto.ModeRestartEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]proto.ModeRestartEvent(nil), got...)
	}
}

// outcome waits for the events to hold one for sidA with outcome and returns it.
func outcome(t *testing.T, events func() []proto.ModeRestartEvent, want string) proto.ModeRestartEvent {
	t.Helper()
	var found proto.ModeRestartEvent
	waitFor(t, "a "+want+" restart reported", func() bool {
		for _, e := range events() {
			if e.SessionID == sidA && e.Outcome == want {
				found = e
				return true
			}
		}
		return false
	})
	return found
}

// session.modes says how a pick took effect, for the manager's record (#10297): Allow All for a working claude without
// the flag waits for a restart; another mode is switched at once, and the waiting restart it drops is reported.
func TestModesAnswerSaysHowAPickTookEffect(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	knownProcess(t, n, 4101)
	events := modeRestartEvents(t, n)
	res, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": proto.ModeBypass})
	if err != nil || res["ok"] != true || res["applied"] != "restart" {
		t.Fatalf("Allow All by a restart answered %v, %v", res, err)
	}
	cycleModes(t, f, "w1:p1", "default")
	res, err = request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": "auto"})
	if err != nil || res["applied"] != "now" {
		t.Fatalf("auto answered %v, %v", res, err)
	}
	if e := outcome(t, events, proto.ModeRestartDropped); e.Mode != proto.ModeBypass || e.Reason != proto.ModeRestartReasonReplaced {
		t.Errorf("drop reported as %+v", e)
	}
	res, err = request(t, n, "session.modes", map[string]any{"session_id": sidA, "effort": "high"})
	if _, said := res["applied"]; err == nil && said {
		t.Errorf("an effort-only change says how a mode took effect: %v", res)
	}
}

// Picking the mode the session still runs in while a restart into Allow All waits calls the restart off: reported as
// cancelled, not as replaced by another switch, which a pick of a third mode still is (#11089).
func TestPickingTheModeItRunsInCancelsAWaitingRestart(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	knownProcess(t, n, 4101)
	events := modeRestartEvents(t, n)
	cycleModes(t, f, "w1:p1", "default")
	if err := pick(t, n, map[string]any{"permission_mode": "acceptEdits"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ pick, reason string }{
		{"acceptEdits", proto.ModeRestartReasonCancelled},
		{"plan", proto.ModeRestartReasonReplaced},
	} {
		if res, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": proto.ModeBypass}); err != nil || res["applied"] != "restart" {
			t.Fatalf("Allow All by a restart answered %v, %v", res, err)
		}
		before := len(events())
		if res, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": want.pick}); err != nil || res["applied"] != "now" {
			t.Fatalf("%s answered %v, %v", want.pick, res, err)
		}
		waitFor(t, "the drop reported", func() bool { return len(events()) > before })
		if e := events()[before]; e.Outcome != proto.ModeRestartDropped || e.Mode != proto.ModeBypass || e.Reason != want.reason {
			t.Errorf("picking %s while the restart waits reported %+v, want dropped for %s", want.pick, e, want.reason)
		}
		if n.switches().restartMode(sidA) != "" {
			t.Fatalf("picking %s left the restart waiting", want.pick)
		}
	}
}

// What became of a waiting restart reaches the manager: done once it ran, dropped (with why) when the claude it was
// picked for exited or it waited too long.
func TestModeRestartOutcomesAreReported(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
		knownProcess(t, n, 4101)
		events := modeRestartEvents(t, n)
		quitsToShell(f, cwd)
		comesUpWhen(f, "w1:p1", func(typed string) bool { return strings.Contains(typed, "--permission-mode "+proto.ModeBypass) })
		if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
			t.Fatal(err)
		}
		if e := outcome(t, events, proto.ModeRestartDone); e.Mode != proto.ModeBypass {
			t.Errorf("done reported as %+v", e)
		}
	})
	t.Run("process gone", func(t *testing.T) {
		n, _, _, _, _ := switchNode(t, proto.StatusWorking, nil)
		run := knownProcess(t, n, 4101)
		events := modeRestartEvents(t, n)
		if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
			t.Fatal(err)
		}
		run(4101, false)
		listSessions(t, n)
		if e := outcome(t, events, proto.ModeRestartDropped); e.Reason != proto.ModeRestartReasonProcessExited {
			t.Errorf("drop reported as %+v", e)
		}
	})
	t.Run("too old", func(t *testing.T) {
		n, _, _, _, _ := switchNode(t, proto.StatusWorking, nil)
		knownProcess(t, n, 4101)
		events := modeRestartEvents(t, n)
		if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
			t.Fatal(err)
		}
		sh := n.switches()
		waitFor(t, "the first try settled", func() bool {
			sh.mu.Lock()
			defer sh.mu.Unlock()
			r := sh.restarts[sidA]
			return r != nil && !r.applying
		})
		sh.mu.Lock()
		sh.restarts[sidA].at = time.Now().Add(-modeRestartMaxAge - time.Minute)
		sh.mu.Unlock()
		n.tryModeRestart(sidA)
		if e := outcome(t, events, proto.ModeRestartDropped); e.Reason != proto.ModeRestartReasonExpired {
			t.Errorf("drop reported as %+v", e)
		}
		if n.switches().restartMode(sidA) != "" {
			t.Error("a pick older than the limit still waits")
		}
	})
}

// A restart that did not work says how, in a code the apps put in words; one the node cannot name says nothing.
func TestRestartFailureReasons(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{refusal(proto.CodeSwitchNotStopped, "the agent did not quit", nil), proto.ModeRestartReasonNotStopped},
		{refusal(proto.CodeSwitchFailed, "did not come up", nil), proto.ModeRestartReasonStartFailed},
		{refusal(proto.CodeSwitchFailedStopped, "it has stopped", nil), proto.ModeRestartReasonStopped},
		{refusal(proto.CodeSwitchUnsupported, "no", nil), ""},
		{io.ErrUnexpectedEOF, ""},
	} {
		if got := restartFailure(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}
