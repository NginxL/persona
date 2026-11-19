package assistant

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// delayedWaitTestStore adds revision CAS only to the durable-wait fixtures. The shared memStore deliberately keeps
// its older, PutSetting-only capabilities because other APIs use those capabilities to reject unsupported writes.
type delayedWaitTestStore struct {
	Store
	mu sync.Mutex
}

func (st *delayedWaitTestStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var current delayedWaitDoc
	if _, err := st.Store.Setting(ctx, key, &current); err != nil {
		return false, err
	}
	if current.Revision != revision {
		return false, nil
	}
	if err := st.Store.PutSetting(ctx, key, value); err != nil {
		return false, err
	}
	return true, nil
}

func withDelayedWaitCAS(st Store) Store { return &delayedWaitTestStore{Store: st} }

// These restart cases use the same store before and after replacing the manager. The PostgreSQL variants are the
// integration cases; the shared-store variants keep the regression visible in a plain unit-test run.
func TestScheduledUsageLimitRetryRestoredAfterRestart(t *testing.T) {
	t.Run("shared store", func(t *testing.T) {
		testScheduledUsageLimitRetryRestoredAfterRestart(t, withDelayedWaitCAS(&memStore{}))
	})
	t.Run("postgresql", func(t *testing.T) {
		testScheduledUsageLimitRetryRestoredAfterRestart(t, taskRecoveryDatabase(t))
	})
}

func testScheduledUsageLimitRetryRestoredAfterRestart(t *testing.T, st Store) {
	t.Helper()
	n := newFakeHub().withMachine()
	first := newServiceOn(t, n, st)
	first.runRetry = time.Hour
	first.sched.limitWait = func(time.Time, time.Time) time.Duration { return 0 } // already overdue

	// Hold the old watcher after its original wait but before it can claim or send the retry. This models losing the
	// process without letting the old manager win the race against its replacement.
	oldAtDeadline := make(chan struct{})
	releaseOld := make(chan struct{})
	oldExited := make(chan struct{})
	var once sync.Once
	first.retryNow = func() time.Time {
		once.Do(func() { close(oldAtDeadline) })
		<-releaseOld
		defer close(oldExited)
		runtime.Goexit() // abandon this old-manager watcher without dispatching it
		panic("unreachable")
	}
	t.Cleanup(func() {
		close(releaseOld)
		<-oldExited
	})

	limitRun(t, n, first, nil)
	select {
	case <-oldAtDeadline:
	case <-time.After(2 * time.Second):
		t.Fatal("the original usage-limit retry did not reach its deadline")
	}
	if got := n.sent(); got != 1 {
		t.Fatalf("the failed run sent %d times before the retry, want 1", got)
	}

	// runNow in limitRun does not advance the ordinary interval schedule. Put its next ordinary run safely in the
	// future so only restoration of the failed run's retry can dispatch on the replacement manager.
	sc := first.loadSchedule(context.Background())
	sc.mu.Lock()
	i := sc.indexLocked("p")
	if i < 0 {
		sc.mu.Unlock()
		t.Fatal("the scheduled task disappeared")
	}
	sc.tasks[i].At = time.Now().Add(time.Hour)
	err := first.saveLocked(sc, sc.tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	type send struct {
		session    string
		resumeOnly bool
	}
	sent := make(chan send, 4)
	n.mu.Lock()
	n.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		session, _ := p["session_id"].(string)
		resumeOnly, _ := p["resume_only"].(bool)
		sent <- send{session: session, resumeOnly: resumeOnly}
	}
	n.mu.Unlock()

	runCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	second := newServiceOn(t, n, st)
	second.runRetry = time.Hour
	go second.Run(runCtx)

	select {
	case got := <-sent:
		if got.session != "s1" || !got.resumeOnly {
			t.Fatalf("restored retry went to the wrong conversation: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduled usage-limit retry was not restored after manager replacement; sends=%d", n.sent())
	}
	waitFor(t, "the restored retry to be recorded", func() bool { return retried(second) })
	if got := n.sent(); got != 2 {
		t.Fatalf("restored retry sent %d total task messages, want 2", got)
	}

	// Constructing another manager from the same database must not restore an already claimed retry a second time.
	third := newServiceOn(t, n, st)
	third.runRetry = time.Hour
	go third.Run(runCtx)
	select {
	case got := <-sent:
		t.Fatalf("repeated restore dispatched the claimed retry again: %+v", got)
	case <-time.After(500 * time.Millisecond):
	}
	if got := n.sent(); got != 2 {
		t.Fatalf("repeated restore produced %d total task messages, want 2", got)
	}
}

func TestTaskReceiptRecheckRestoredAfterRestart(t *testing.T) {
	t.Run("shared store", func(t *testing.T) {
		st := withDelayedWaitCAS(planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}})
		testTaskReceiptRecheckRestoredAfterRestart(t, st)
	})
	t.Run("postgresql", func(t *testing.T) {
		testTaskReceiptRecheckRestoredAfterRestart(t, taskRecoveryDatabase(t))
	})
}

// receiptSettleHub proves that the replaced manager's recheck has read its long settle delay and cannot write the
// receipt while the replacement is being tested. The delay is changed only after that read, so there is no timing race.
type receiptSettleHub struct {
	*fakeHub
	armed atomic.Bool
	once  sync.Once
	read  chan struct{}
}

func (h *receiptSettleHub) Hello(ctx context.Context) (Hello, error) {
	hello, err := h.fakeHub.Hello(ctx)
	if h.armed.Load() && hello.DoneDelayMS > 0 {
		h.once.Do(func() { close(h.read) })
	}
	return hello, err
}

func testTaskReceiptRecheckRestoredAfterRestart(t *testing.T, st Store) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`
	first := newServiceOn(t, f, st)
	startedTask(t, first, f)
	ctx := context.Background()
	first.taskWorking(ctx, "box", "s1")
	f.setItems(proto.Item{ID: "answer-1", Kind: proto.KindText, Role: "assistant", Text: "Delivery verified", TS: time.Now()})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone,
		State: proto.StateIdle, LastText: "Delivery verified", UpdatedAt: time.Now(), Rev: 1})
	if got := feedKinds(first, "conv1"); strings.Contains(got, "receipt") {
		t.Fatalf("fixture produced a receipt before the recheck: %s", got)
	}

	f.mu.Lock()
	f.hello.DoneDelayMS = int64(time.Hour / time.Millisecond)
	f.mu.Unlock()
	hub := &receiptSettleHub{fakeHub: f, read: make(chan struct{})}
	first.hub = hub
	hub.armed.Store(true)
	first.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(-time.Second)) // overdue: restore promptly
	select {
	case <-hub.read:
	case <-time.After(2 * time.Second):
		t.Fatal("the original task receipt recheck did not reach its deadline")
	}

	// The old timer is now asleep for an hour with its already-read delay. A replacement must not depend on that timer.
	f.mu.Lock()
	f.hello.DoneDelayMS = 0
	f.mu.Unlock()
	second := newServiceOn(t, f, st)
	copyReceiptFixtureTasks(first, second)
	runCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go second.Run(runCtx)
	waitFor(t, "the task receipt recheck restored after manager replacement", func() bool {
		return strings.Count(feedKinds(second, "conv1"), "receipt") == 1
	})

	waitFor(t, "the restored completion judgment", func() bool { return onlyTodo(second).DoneAt != nil })
	doneAt := *onlyTodo(second).DoneAt
	// Replaying the same closed turn after restoration is synchronous and must not add a second receipt or reopen
	// completion handling.
	second.taskTurnEnded(ctx, "box", "s1")
	if got := strings.Count(feedKinds(second, "conv1"), "receipt"); got != 1 {
		t.Fatalf("same-turn replay produced %d receipts, want 1", got)
	}
	if current := onlyTodo(second).DoneAt; current == nil || !current.Equal(doneAt) {
		t.Fatalf("same-turn replay changed completion: %v, want %v", current, doneAt)
	}
}

// copyReceiptFixtureTasks mirrors the task/link snapshot that PostgreSQL reloads. planMemStore has no to-do tables;
// the new manager's receipt and turn-ledger memory deliberately stays separate.
func copyReceiptFixtureTasks(first, second *Service) {
	first.todos.mu.RLock()
	defer first.todos.mu.RUnlock()
	second.todos.mu.Lock()
	defer second.todos.mu.Unlock()
	second.todos.todos = map[string]*Todo{}
	for id, todo := range first.todos.todos {
		copy := copyTodoLocked(todo)
		second.todos.todos[id] = &copy
	}
	second.todos.activeBySession = map[[2]string]string{}
	for ref, id := range first.todos.activeBySession {
		second.todos.activeBySession[ref] = id
	}
	second.todos.spacesByKey = map[string]string{}
	for ref, id := range first.todos.spacesByKey {
		second.todos.spacesByKey[ref] = id
	}
	second.todos.loaded = true
}
