package assistant

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// retryCancelled on a fixed clock: the retry of a failed run is due at retryAt, three minutes after the failure.
func TestRetryCancelledWhenTheOwnerActedAfterTheFailure(t *testing.T) {
	failedAt := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	retryAt := failedAt.Add(runRetryDelay)
	failed := ScheduleRun{RefID: "fu", At: failedAt, Status: runFailed}
	oneOff := Task{ID: "fu", Repeat: repeatOnce, At: failedAt, Enabled: false, LastRun: failedAt} // off once it was due (runDue)
	at := func(t Task, when time.Time) Task { t.At, t.Enabled = when, true; return t }
	ran := func(r ScheduleRun) []ScheduleRun { return []ScheduleRun{failed, r} }

	cases := []struct {
		name   string
		cur    Task
		found  bool
		runs   []ScheduleRun
		cancel bool
	}{
		{"nothing done since: retried", oneOff, true, []ScheduleRun{failed}, false},
		{"Run now since: not retried", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Manual: true, Status: runOK, SessionID: "s1"}), true},
		{"Run now whose turn then failed: it still went out", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Manual: true, Status: runFailed, SessionID: "s1"}), true},
		{"Run now that could not start: retried", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Manual: true, Status: runFailed}), false},
		{"Run now with lost receipt: not retried", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Manual: true, Status: runFailed, DeliveryUnknown: true}), true},
		{"Run now skipped (busy): retried", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Manual: true, Status: runSkipped, SessionID: "s1"}), false},
		{"another task's run since: retried", oneOff, true,
			ran(ScheduleRun{RefID: "other", At: failedAt.Add(time.Minute), Manual: true, Status: runOK, SessionID: "s2"}), false},
		{"a run before the failed one: retried", oneOff, true,
			[]ScheduleRun{{RefID: "fu", At: failedAt.Add(-time.Hour), Status: runOK, SessionID: "s0"}, failed}, false},
		{"moved later: not retried", at(oneOff, failedAt.Add(time.Hour)), true, []ScheduleRun{failed}, true},
		{"moved to tomorrow: not retried", at(oneOff, failedAt.Add(24*time.Hour)), true, []ScheduleRun{failed}, true},
		// Moved earlier than it was set before (#5320 counts only where the time is now): still ahead of the retry,
		// it runs then.
		{"moved earlier, still after the retry: not retried", at(Task{ID: "fu", Repeat: repeatOnce, LastRun: failedAt}, retryAt.Add(time.Minute)),
			true, []ScheduleRun{failed}, true},
		// Moved to a time before the retry: the scheduler ran it then, which is a run since the failure.
		{"moved earlier, before the retry, and run then: not retried", oneOff, true,
			ran(ScheduleRun{RefID: "fu", At: failedAt.Add(time.Minute), Status: runOK, SessionID: "s1"}), true},
		{"deleted: not retried", Task{}, false, []ScheduleRun{failed}, true},
		// A repeating task's next time is always ahead (an interval one's moved on as it ran): that is no move.
		{"interval task, its next time ahead: retried", Task{ID: "fu", Repeat: repeatInterval, EveryMinutes: 30, At: failedAt.Add(30 * time.Minute), Enabled: true},
			true, []ScheduleRun{failed}, false},
		{"daily task: retried", Task{ID: "fu", Repeat: repeatDaily, Time: "09:00", Enabled: true}, true, []ScheduleRun{failed}, false},
		{"repeating task switched off: not retried", Task{ID: "fu", Repeat: repeatDaily, Time: "09:00"}, true, []ScheduleRun{failed}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why := retryCancelled(c.cur, c.found, failed, c.runs, retryAt)
			if (why != "") != c.cancel {
				t.Fatalf("cancel %v, got %q", c.cancel, why)
			}
		})
	}
}

// The whole path (#5320's reproduction): a due one-off whose send fails is pushed and its retry armed; what the owner
// does before the retry is due decides whether it goes. The service's retry clock is pinned, so "later" does not
// depend on how fast the test runs.
func TestAOneOffRunAgainOrMovedAfterItFailedIsNotRetried(t *testing.T) {
	cases := []struct {
		name    string
		act     func(t *testing.T, h *Service, clock time.Time)
		retried bool
	}{
		{"untouched", func(*testing.T, *Service, time.Time) {}, true},
		{"run now", func(t *testing.T, h *Service, _ time.Time) {
			cur, _ := taskByID(h, "fu")
			if _, err := h.runTask(cur, time.Now(), time.Now(), true, false); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"moved later", func(_ *testing.T, h *Service, clock time.Time) { moveTask(h, "fu", clock.Add(time.Hour)) }, false},
		{"moved earlier, still after the retry", func(_ *testing.T, h *Service, clock time.Time) { moveTask(h, "fu", clock.Add(time.Second)) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := newFakeHub().withMachine()
			h := newStorelessService(t, n)
			h.runRetry = 300 * time.Millisecond
			// A day ahead of the wall clock: the moved times are never due for the real scheduler either.
			clock := time.Now().Add(24 * time.Hour).Truncate(time.Second)
			h.retryNow = func() time.Time { return clock }
			n.mu.Lock()
			n.failAssistant = errors.New("machine is offline")
			n.mu.Unlock()
			setTasks(h, Task{ID: "fu", Title: "看一眼 CI", Prompt: "看一眼 CI", Repeat: repeatOnce, At: time.Now().Add(-time.Minute), Enabled: true})
			h.runDue(context.Background(), time.Now())
			waitRuns(t, h, 1)

			n.mu.Lock()
			n.failAssistant = nil // the machine is back: a retry that goes is seen
			n.mu.Unlock()
			c.act(t, h, clock)
			time.Sleep(700 * time.Millisecond)

			runs := waitRuns(t, h, 1)
			if got := slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.Retried }); got != c.retried {
				t.Fatalf("retried %v, runs %+v", c.retried, runs)
			}
		})
	}
}

// moveTask is the owner giving a one-off another time in the app (it is on again).
func moveTask(h *Service, id string, at time.Time) {
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if i := sc.indexLocked(id); i >= 0 {
		sc.tasks[i].At, sc.tasks[i].Enabled = at.UTC(), true
	}
}
