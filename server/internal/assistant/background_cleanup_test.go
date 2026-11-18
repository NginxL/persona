package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// drainFleetBackground stops new background scheduling, then lets active calls finish before a test closes its store.
// Production Run uses the same canceled runCtx to stop new naming passes and direction-tree timers.
func drainFleetBackground(t *testing.T, s *Service, cancel context.CancelFunc) {
	t.Helper()
	cancel()

	names := &s.agentNames
	names.mu.Lock()
	if names.timer != nil {
		names.timer.Stop()
		names.timer = nil
		names.timerSeq++
	}
	names.mu.Unlock()

	tree := &s.dtree
	tree.mu.Lock()
	if tree.timer != nil && tree.timer.Stop() {
		tree.timer = nil
		tree.armed = false
		tree.runs.Done()
	}
	tree.mu.Unlock()

	// A screen-upgrade start checks runCtx under fleet.mu; taking it here closes the Add/Wait teardown window.
	// Delayed turn completions use the same gate: stop pending timers and let an already running callback finish.
	s.fleet.mu.Lock()
	s.fleet.completionStopped = true
	for timer := range s.fleet.completionTimers {
		if timer.Stop() {
			s.fleet.completionRuns.Done()
		}
		delete(s.fleet.completionTimers, timer)
	}
	s.fleet.mu.Unlock()
	// workEvent and recordSendAsync use work.mu for the same runCtx + Add scheduling gate.
	s.work.mu.Lock()
	s.work.mu.Unlock()

	waitFleetBackground(t, "turn completions", &s.fleet.completionRuns)
	waitFleetBackground(t, "agent naming", &names.runs)
	waitFleetBackground(t, "direction tree", &tree.runs)
	waitFleetBackground(t, "screen upgrades", &s.fleet.upgrades)
	waitFleetBackground(t, "hub transcription", &s.hubTr.wg)
	waitFleetBackground(t, "work events", &s.work.eventWrites)
}

// newRestartedService is a second manager on a test's store, as after a hub restart. Its background work (work events,
// screen upgrades) is drained before the database fixture closes, as newFleetTestOn does for the first one: otherwise
// it logs through the ended test, a data race under -race and a "Log in goroutine after ... has completed" panic.
func newRestartedService(t *testing.T, o Options) *Service {
	t.Helper()
	s := New(o)
	runCtx, cancel := context.WithCancel(context.Background())
	s.running.Store(&runCtx)
	t.Cleanup(func() { drainFleetBackground(t, s, cancel) })
	return s
}

func waitFleetBackground(t *testing.T, name string, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("%s did not finish before the database fixture closed", name)
	}
}

// delayedBackgroundStore lets a background database call finish only after its test has returned. A fixture that drains
// the call before closing the database sees no error; without that drain, the closed-pool error logs through the ended T.
type delayedBackgroundStore struct {
	*db.DB
	release chan struct{}
}

func newDelayedBackgroundStore(t *testing.T) *delayedBackgroundStore {
	t.Helper()
	st := &delayedBackgroundStore{DB: taskRecoveryDatabase(t), release: make(chan struct{})}
	time.AfterFunc(100*time.Millisecond, func() { close(st.release) })
	return st
}

func (s *delayedBackgroundStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == settingsKey {
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.DB.Setting(ctx, key, out)
}

func (s *delayedBackgroundStore) AddWorkEvent(ctx context.Context, e db.WorkEvent) error {
	<-s.release
	return s.DB.AddWorkEvent(ctx, e)
}

// acceptedSendStore pauses an accepted send record while it reads the conversation roles. The test can then stop new
// scheduling before releasing the record, proving that the rest of that accepted operation still writes its event.
type acceptedSendStore struct {
	*db.DB
	rolesStarted chan struct{}
	releaseRoles chan struct{}
	startedOnce  sync.Once
}

func (s *acceptedSendStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == settingsKey {
		s.startedOnce.Do(func() { close(s.rolesStarted) })
		select {
		case <-s.releaseRoles:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.DB.Setting(ctx, key, out)
}

// A database-backed fleet fixture must drain the service work it started before the test's database closes. Each case
// returns immediately: cleanup, not a sleep or an assertion inside the case, is what has to finish the background work.
func TestFleetDatabaseFixtureDrainsBackgroundWork(t *testing.T) {
	t.Run("direction_tree", func(t *testing.T) {
		ft := newFleetTestOn(t, taskRecoveryDatabase(t))
		if err := ft.s.ensureTodosLoaded(context.Background()); err != nil {
			t.Fatal(err)
		}
		ft.s.directionTreeSoon()
	})

	t.Run("agent_naming", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		ft.s.agentNamesChanged(DeviceSessions{})
	})

	t.Run("work_events", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		ft.s.workEvent("mac", "s1", "turn_finished", nil)
	})

	t.Run("work_events_after_stop", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		time.AfterFunc(50*time.Millisecond, func() { ft.s.workEvent("mac", "s1", "turn_finished", nil) })
	})

	t.Run("accepted_send_after_stop", func(t *testing.T) {
		ctx := context.Background()
		database := taskRecoveryDatabase(t)
		if err := database.PutSetting(ctx, settingsKey, Settings{Leaders: []string{"s1"}}); err != nil {
			t.Fatal(err)
		}
		st := &acceptedSendStore{DB: database, rolesStarted: make(chan struct{}), releaseRoles: make(chan struct{})}
		ft := newFleetTestOn(t, st)
		ft.s.northStarOn.Store(true)
		ft.s.recordSendAsync("mac", "s1", "resume", originManager, false, outcomeOK, "", "")
		select {
		case <-st.rolesStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("accepted send did not reach its role lookup")
		}
		ft.cancel()
		close(st.releaseRoles)
		waitFleetBackground(t, "accepted send", &ft.s.work.eventWrites)
		events, err := database.WorkEventsBetween(ctx, time.Now().Add(-time.Minute), time.Now().Add(time.Minute), []string{sendEvent})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("accepted send events after stop = %d, want 1", len(events))
		}
	})

	t.Run("synchronous_send_after_stop", func(t *testing.T) {
		ctx := context.Background()
		st := &workMemStore{}
		session := t.Name()
		if err := st.PutSetting(ctx, settingsKey, Settings{Leaders: []string{session}}); err != nil {
			t.Fatal(err)
		}
		ft := newFleetTestOn(t, st)
		ft.s.northStarOn.Store(true)
		ft.cancel()
		if ft.s.runCtx().Err() == nil {
			t.Fatal("the service context is not canceled")
		}
		// The hub's synchronous delivery report is already on its caller's stack, not a new background operation.
		ft.s.ConversationSent(ctx, ConversationSent{MachineID: "mac", SessionID: session, Outcome: assistantwire.ConversationDelivered})
		// A new asynchronous manager send remains stopped.
		ft.s.recordSendAsync("mac", session, "resume", originManager, false, outcomeOK, "", "")
		waitFleetBackground(t, "post-stop async send", &ft.s.work.eventWrites)
		st.wmu.Lock()
		got := len(st.events)
		st.wmu.Unlock()
		if got != 1 {
			t.Fatalf("synchronous send events after stop = %d, want 1", got)
		}
	})
}
