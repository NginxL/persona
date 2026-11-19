package assistant

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// A node recovering an assistant turn keeps it working, including while its latest upstream attempt has an error. The
// scheduled watcher must not fail or rerun the whole task between attempts; only the final fallback answer ends the run.
func TestTaskModelRecoveryFailsOnlyAfterTheFinalTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		s := New(Options{Hub: f, Logf: testLogf(t)})
		s.turnPoll, s.runRetry = time.Second, time.Minute
		task := Task{ID: "p", Title: "Example check", Prompt: "Check the example", Repeat: repeatInterval, EveryMinutes: 30,
			At: time.Now(), Enabled: true, MachineID: "server"}
		setTasks(s, task)
		f.items = []proto.Item{{ID: "failure", Role: "assistant", Kind: proto.KindText, Text: "API Error: 503 no eligible upstream"}}
		working := proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
			TurnError: "API Error: 503 no eligible upstream", TurnErrorKind: proto.TurnErrorTransient}
		f.setSessions("server", working)
		run := ScheduleRun{RefID: task.ID, Status: runOK, SessionID: "s1"}
		run.ID = s.recordRun(run)
		done := make(chan struct{})
		go func() {
			s.watchScheduledRun(run, task, Device{ID: "server", Name: "Example computer"})
			close(done)
		}()
		synctest.Wait()
		<-time.After(4 * s.turnPoll) // several polls with a recoverable failure still in progress
		synctest.Wait()
		// A durable backoff publishes a stopped turn rather than holding the
		// execution process busy. It still must not restart the task prompt.
		working.Status, working.State = proto.StatusDone, proto.StateUnread
		f.setSessions("server", working)
		<-time.After(4 * s.turnPoll)
		synctest.Wait()
		sc := s.loadSchedule(context.Background())
		sc.mu.Lock()
		failedEarly := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.Status == runFailed })
		sc.mu.Unlock()
		if failedEarly || f.sent() != 0 || retried(s) {
			t.Fatal("an in-progress model recovery failed or reran the scheduled task")
		}

		f.items = []proto.Item{{ID: "answer", Role: "assistant", Kind: proto.KindText, Text: "Recovered answer"}}
		f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, Assistant: true,
			Model: "fallback-model", LastText: "Recovered answer", UpdatedAt: time.Now()})
		select {
		case <-done:
		case <-time.After(10 * s.turnPoll):
			t.Fatal("the final fallback answer did not end the watch")
		}
		synctest.Wait()
		again := retried(s)
		sc = s.loadSchedule(context.Background())
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if len(sc.runs) != 1 || sc.runs[0].Status != runOK || sc.runs[0].Error != "" {
			t.Fatalf("a recovered run was recorded as failed: %+v", sc.runs)
		}
		if f.sent() != 0 || again {
			t.Fatal("a recovered run was sent again as a scheduled retry")
		}
	})
}

func TestTaskWithoutPendingModelRetryDoesNotHoldRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		s := New(Options{Hub: f, Logf: testLogf(t)})
		s.turnPoll, s.runRetry = time.Second, 5*time.Second
		task := Task{ID: "p", Title: "Example check", Prompt: "Check the example", Repeat: repeatInterval, EveryMinutes: 30,
			At: time.Now(), Enabled: true, MachineID: "server"}
		setTasks(s, task)
		f.items = []proto.Item{{ID: "failure", Role: "assistant", Kind: proto.KindText, Text: "API Error: 503 no eligible upstream"}}
		working := proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
			TurnError: "API Error: 503 no eligible upstream", TurnErrorKind: proto.TurnErrorTransient, UpdatedAt: time.Now()}
		f.setSessions("server", working)
		turnAt := time.Now().UTC()
		s.putWork(context.Background(), db.SessionWork{MachineID: "server", SessionID: "s1", TurnAt: turnAt,
			Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, TurnAt: turnAt, At: turnAt}})
		run := ScheduleRun{RefID: task.ID, Status: runOK, SessionID: "s1", Retried: true}
		run.ID = s.recordRun(run)
		done := make(chan struct{})
		go func() {
			s.watchScheduledRun(run, task, Device{ID: "server", Name: "Example computer"})
			close(done)
		}()
		synctest.Wait()
		<-time.After(2 * s.turnPoll)
		working.Status, working.State = proto.StatusDone, proto.StateUnread
		f.setSessions("server", working)
		synctest.Wait()
		select {
		case <-done:
		case <-time.After(10 * s.turnPoll):
			t.Fatal("a transient work entry with no pending wake held the scheduled run")
		}
	})
}
