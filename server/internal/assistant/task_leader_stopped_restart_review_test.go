package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A worker's completed turn is still undelivered when its stopped leader refuses
// the first send. A manager restart must not consume the persisted turn signal.
func TestStoppedLeaderReviewSurvivesManagerRestart(t *testing.T) {
	checkLeaderReviewDelivery(t, proto.StatusInterrupted, false)
}

func TestOfflineLeaderReviewSurvivesManagerRestart(t *testing.T) {
	checkLeaderReviewDelivery(t, proto.StatusOffline, false)
}

func TestOfflineLeaderReviewIsToldAfterConfirmation(t *testing.T) {
	checkLeaderReviewDelivery(t, proto.StatusOffline, true)
}

func checkLeaderReviewDelivery(t *testing.T, status string, confirmed bool) {
	t.Helper()
	ctx := context.Background()
	database := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	s, f := newQuietServiceOn(t, database)
	s.leaderResume = time.Hour // keep the in-memory retry out of the restart assertion
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID
	// Initialize the persisted turn ledger before the worker's turn, as the
	// manager's normal supervision sweep does.
	s.superviseWatch(ctx, time.Now())

	f.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	s.taskWorking(ctx, "box", worker, startRow{})
	stopLeader(f, worker)
	f.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: status, Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, UpdatedAt: time.Now()},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了，PR https://github.com/o/r/pull/7 已合并", UpdatedAt: time.Now()})
	entered, release := make(chan struct{}), make(chan struct{})
	releaseSend := sync.OnceFunc(func() { close(release) })
	defer releaseSend()
	if status == proto.StatusOffline {
		f.mu.Lock()
		f.beforeSend = func() {
			close(entered)
			<-release
		}
		f.mu.Unlock()
		if confirmed {
			failSends(f, nil)
		} else {
			failSends(f, &rpc.Error{Code: "peer_unconfirmed", Message: "not taken", Data: json.RawMessage(`{"delivered":"unknown"}`)})
		}
	}
	s.taskTurnEnded(ctx, "box", worker)
	turnsQuiet(t, s, w.ID)
	if status == proto.StatusOffline {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("offline leader did not start resuming")
		}
		s.supervise.dedupMu.Lock()
		s.supervise.dedupMu.Unlock()
		turn, ok := s.turnOf(ctx, w.ID)
		if !ok || turn.Signal == nil || turn.Signal.Told != nil {
			t.Fatalf("while the send was in flight, Told changed: %+v", turn.Signal)
		}
		var history map[string]superviseFingerprint
		if _, err := database.Setting(ctx, superviseDedupKey, &history); err != nil {
			t.Fatal(err)
		}
		for key := range history {
			if strings.Contains(key, w.ID) && strings.Contains(key, `"review"`) {
				t.Fatalf("the in-flight review was recorded in persistent deduplication: %s", key)
			}
		}
		releaseSend()
	}
	if confirmed {
		waitFor(t, "the confirmed review to be persisted as Told", func() bool {
			var ledger taskTurnsDoc
			_, err := database.Setting(ctx, taskTurnsKey, &ledger)
			return err == nil && ledger.Turns[w.ID].Signal != nil && ledger.Turns[w.ID].Signal.Told != nil
		})
		if got := deliveredTo(f, "s1"); len(got) != 1 {
			t.Fatalf("the leader should have received the review once: %q", got)
		}
		return
	}
	waitFor(t, "the first send to the leader", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
	waitFor(t, "the failed delivery to escalate", func() bool { return leaderEscalations(f) == 1 })
	// Wait for the digest to finish its bookkeeping.
	s.supervise.dedupMu.Lock()
	s.supervise.dedupMu.Unlock()
	if got := deliveredTo(f, "s1"); len(got) != 0 {
		t.Fatalf("setup: the refused leader received a message: %q", got)
	}

	// Stop the old manager's digest timer, leaving its process-local retry and
	// pending queue behind just as a process restart would.
	s.supervise.mu.Lock()
	if s.supervise.timer != nil {
		s.supervise.timer.Stop()
		s.supervise.timer = nil
	}
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Lock()
	s.supervise.dedupMu.Unlock()
	beforeTurn, beforeOK := s.turnOf(ctx, w.ID)
	s.supervise.mu.Lock()
	beforePending, beforePendingOK := s.supervise.pending[w.ID+"|review"]
	beforeSent := s.supervise.sent[w.ID+"|review"]
	s.supervise.mu.Unlock()
	if !beforeOK || beforeTurn.Signal == nil || beforeTurn.Signal.Told != nil {
		t.Fatalf("an unconfirmed delivery changed Told: %+v", beforeTurn.Signal)
	}
	t.Logf("before restart: turn=%+v ok=%v signal=%+v pending=%+v pendingOK=%v sent=%q", beforeTurn, beforeOK, beforeTurn.Signal, beforePending, beforePendingOK, beforeSent)

	restarted, hub := restartedOn(t, database)
	// profileTaskStore persists settings and plans but not the todo tables.
	// Seed the new manager with the same todo rows so this fixture exercises
	// the persisted turn and deduplication ledgers.
	s.todos.mu.RLock()
	restarted.todos.todos = make(map[string]*Todo, len(s.todos.todos))
	for id, row := range s.todos.todos {
		cloned := copyTodoLocked(row)
		restarted.todos.todos[id] = &cloned
	}
	restarted.todos.loaded = true
	s.todos.mu.RUnlock()
	hub.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: time.Now()},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了，PR https://github.com/o/r/pull/7 已合并", UpdatedAt: time.Now()})
	restarted.superviseWatch(ctx, time.Now())
	turnsQuiet(t, restarted, w.ID)
	afterTurn, afterOK := restarted.turnOf(ctx, w.ID)
	restarted.supervise.mu.Lock()
	afterPending, afterPendingOK := restarted.supervise.pending[w.ID+"|review"]
	afterSent := restarted.supervise.sent[w.ID+"|review"]
	restarted.supervise.mu.Unlock()
	t.Logf("after restart: turn=%+v ok=%v signal=%+v pending=%+v pendingOK=%v sent=%q supervision=%+v", afterTurn, afterOK, afterTurn.Signal, afterPending, afterPendingOK, afterSent, todoNow(restarted, w.ID).Supervision)
	waitFor(t, "the restarted manager to deliver the undelivered review", func() bool {
		for _, m := range deliveredTo(hub, "s1") {
			if strings.Contains(m, "waits for your review") && strings.Contains(m, w.ID) {
				return true
			}
		}
		return false
	})
}
