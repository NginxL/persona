package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func newDelayedRetryFixture(t *testing.T, st Store) (*Service, *fakeHub, delayedWait) {
	t.Helper()
	n := newFakeHub().withMachine()
	s := newServiceOn(t, n, st)
	now := time.Now()
	task := Task{ID: "p", Title: "Example", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30,
		At: now.Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"}
	sc := setTasks(s, task)
	sc.mu.Lock()
	err := s.saveLocked(sc, sc.tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	failedAt := now.Add(-time.Hour)
	run := ScheduleRun{RefID: "p", At: failedAt, MachineID: "server", SessionID: "s1", Status: runFailed, Error: "usage limit"}
	run.ID = s.recordRun(run)
	w := delayedWait{ID: "limit_" + run.ID, Kind: delayedScheduledRetry, At: now.Add(-time.Second),
		Retry: &scheduledLimitRetry{Run: run, RunOf: failedAt, MachineName: "Example computer", LimitTurn: failedAt}}
	if err := s.persistScheduledRetry(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	n.setSessions("server", proto.Session{MachineID: "server", SessionID: "s1", Assistant: true, Cwd: "/assistant",
		Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: failedAt})
	return s, n, w
}

func TestRestoredScheduledRetryKeepsDeadline(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	w.At = time.Now().Add(500 * time.Millisecond)
	if err := first.updateDelayedWaits(context.Background(), func(doc *delayedWaitDoc) (bool, error) {
		doc.Waits[0].At = w.At
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	second := newServiceOn(t, n, st)
	sent := make(chan map[string]any, 2)
	n.mu.Lock()
	n.onAssistantSend = func(_ *fakeHub, p map[string]any) { sent <- p }
	n.mu.Unlock()
	second.restoreDelayedWaits(context.Background())
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || !doc.Waits[0].At.Equal(w.At) {
		t.Fatalf("restored deadline = %+v, %v; want %v", doc.Waits, err, w.At)
	}
	select {
	case p := <-sent:
		t.Fatalf("retry ran before its original deadline: %+v", p)
	case <-time.After(75 * time.Millisecond):
	}
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not run at its original deadline")
	}
	waitFor(t, "the completed retry to leave the backlog", func() bool {
		doc, err := second.readDelayedWaits(context.Background())
		return err == nil && len(doc.Waits) == 0
	})
	third := newServiceOn(t, n, st)
	third.restoreDelayedWaits(context.Background())
	if got := n.sent(); got != 1 {
		t.Fatalf("repeated restore produced %d sends, want 1", got)
	}
}

type ambiguousSaveStore struct {
	Store
	fail bool
}

func (st *ambiguousSaveStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	ok, err := st.Store.(delayedWaitCAS).CompareSetting(ctx, key, revision, value)
	if err == nil && ok && st.fail {
		st.fail = false
		return false, errors.New("save acknowledgment lost")
	}
	return ok, err
}

func TestScheduledRetryDoesNotDispatchUnclaimedAfterAmbiguousSave(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	if err := st.PutSetting(context.Background(), delayedWaitsKey, delayedWaitDoc{}); err != nil {
		t.Fatal(err)
	}
	ambiguous := &ambiguousSaveStore{Store: st, fail: true}
	first.store = func() Store { return ambiguous }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first.runScheduledLimitRetry(ctx, w, false)
	if got := n.sent(); got != 0 {
		t.Fatalf("an unclaimed retry dispatched %d times after an ambiguous save", got)
	}
	second := newServiceOn(t, n, st)
	second.restoreDelayedWaits(context.Background())
	if got := n.sent(); got != 0 {
		t.Fatalf("restored retry ran before its persisted backoff: %d sends", got)
	}
	if err := second.updateDelayedWaits(context.Background(), func(doc *delayedWaitDoc) (bool, error) {
		doc.Waits[0].At = time.Now().Add(-time.Second)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	third := newServiceOn(t, n, st)
	third.restoreDelayedWaits(context.Background())
	waitFor(t, "the persistently saved retry to dispatch once", func() bool { return n.sent() == 1 })
}

func TestClaimedScheduledRetryRecoversUnknownWithoutResend(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	_, ok, err := first.claimDelayedWait(context.Background(), w.ID, "interrupted", time.Now())
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	before := len(n.pushed())
	second := newServiceOn(t, n, st)
	second.restoreDelayedWaits(context.Background())
	if got := n.sent(); got != 0 {
		t.Fatalf("claimed retry was sent again %d times", got)
	}
	body := i18n.T(second.lang(context.Background()), "assistant-schedule.retry_unknown")
	pushes := n.pushed()
	if len(pushes) != before+1 || pushes[len(pushes)-1].Body != body {
		t.Fatalf("unknown-outcome pushes = %+v", pushes)
	}
	sc := second.loadSchedule(context.Background())
	sc.mu.Lock()
	found := false
	for _, run := range sc.runs {
		if run.ID == w.Retry.Run.ID && run.Error == body {
			found = true
		}
	}
	sc.mu.Unlock()
	if !found {
		t.Fatal("unknown outcome was not recorded in schedule history")
	}
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 0 {
		t.Fatalf("recovered claim remains in backlog: %+v, %v", doc.Waits, err)
	}
	third := newServiceOn(t, n, st)
	third.restoreDelayedWaits(context.Background())
	if n.sent() != 0 || len(n.pushed()) != len(pushes) {
		t.Fatal("repeated recovery resent or reported the same claim again")
	}
}

func TestDelayedWaitClaimIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   func(*testing.T) Store
	}{
		{name: "shared store", st: func(*testing.T) Store { return withDelayedWaitCAS(&memStore{}) }},
		{name: "postgresql", st: func(t *testing.T) Store { return taskRecoveryDatabase(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st(t)
			first := newServiceOn(t, newFakeHub(), st)
			second := newServiceOn(t, newFakeHub(), st)
			w := delayedWait{ID: "limit_once", Kind: delayedScheduledRetry, At: time.Now().Add(-time.Second),
				Retry: &scheduledLimitRetry{}}
			if err := first.persistScheduledRetry(context.Background(), w); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			type result struct {
				ok  bool
				err error
			}
			results := make(chan result, 2)
			for _, tc := range []struct {
				s     *Service
				token string
			}{{first, "first"}, {second, "second"}} {
				go func() {
					<-start
					_, ok, err := tc.s.claimDelayedWait(context.Background(), w.ID, tc.token, time.Now())
					results <- result{ok: ok, err: err}
				}()
			}
			close(start)
			claims := 0
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.ok {
					claims++
				}
			}
			if claims != 1 {
				t.Fatalf("%d managers claimed one retry, want exactly one", claims)
			}
		})
	}
}

func TestRestoredScheduledRetryCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel func(*Service, *fakeHub, delayedWait)
	}{
		{name: "deleted", cancel: func(s *Service, _ *fakeHub, _ delayedWait) { setTasks(s) }},
		{name: "disabled", cancel: func(s *Service, _ *fakeHub, _ delayedWait) {
			setTasks(s, Task{ID: "p", Repeat: repeatInterval, EveryMinutes: 30, Enabled: false})
		}},
		{name: "moved one-shot", cancel: func(s *Service, _ *fakeHub, _ delayedWait) {
			setTasks(s, Task{ID: "p", Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true})
		}},
		{name: "later run", cancel: func(s *Service, _ *fakeHub, w delayedWait) {
			s.recordRun(ScheduleRun{RefID: "p", At: w.Retry.Run.At.Add(time.Minute), MachineID: "server", SessionID: "s1", Status: runOK})
		}},
		{name: "advanced conversation", cancel: func(_ *Service, n *fakeHub, _ delayedWait) {
			n.setSessions("server", proto.Session{MachineID: "server", SessionID: "s1", Status: proto.StatusDone,
				State: proto.StateIdle, UpdatedAt: time.Now()})
		}},
		{name: "working conversation", cancel: func(_ *Service, n *fakeHub, _ delayedWait) {
			n.setSessions("server", proto.Session{MachineID: "server", SessionID: "s1", Status: proto.StatusWorking,
				State: proto.StateWorking, UpdatedAt: time.Now()})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, w := newDelayedRetryFixture(t, withDelayedWaitCAS(&memStore{}))
			tc.cancel(s, n, w)
			s.runScheduledLimitRetry(context.Background(), w, true)
			if got := n.sent(); got != 0 {
				t.Fatalf("cancelled retry sent %d times", got)
			}
			doc, err := s.readDelayedWaits(context.Background())
			if err != nil || len(doc.Waits) != 0 {
				t.Fatalf("cancelled wait remains: %+v, %v", doc.Waits, err)
			}
		})
	}
}

type compareGateStore struct {
	Store
	calls  int
	before func(int) error
}

func (st *compareGateStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	st.calls++
	if st.before != nil {
		if err := st.before(st.calls); err != nil {
			return false, err
		}
	}
	return st.Store.(delayedWaitCAS).CompareSetting(ctx, key, revision, value)
}

func TestScheduledRetryRechecksCancellationAfterClaim(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	started := make(chan struct{})
	release := make(chan struct{})
	gate := &compareGateStore{Store: st}
	gate.before = func(call int) error {
		if call == 1 {
			close(started)
			<-release
		}
		return nil
	}
	first.store = func() Store { return gate }
	done := make(chan struct{})
	go func() {
		defer close(done)
		first.runScheduledLimitRetry(context.Background(), w, true)
	}()
	<-started
	setTasks(first, Task{ID: "p", Repeat: repeatInterval, EveryMinutes: 30, Enabled: false})
	close(release)
	<-done
	if got := n.sent(); got != 0 {
		t.Fatalf("task disabled during the claim was dispatched %d times", got)
	}
}

func TestScheduledRetryRearmsWhenSessionReadFails(t *testing.T) {
	first, n, w := newDelayedRetryFixture(t, withDelayedWaitCAS(&memStore{}))
	n.answerNode(DeviceSessionGet, func(string, json.RawMessage) (any, error) { return nil, errors.New("offline") })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first.runScheduledLimitRetry(ctx, w, true)
	if got := n.sent(); got != 0 {
		t.Fatalf("session-read failure allowed %d stale dispatches", got)
	}
	doc, err := first.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 {
		t.Fatalf("session-read failure did not retain the wait: %+v, %v", doc.Waits, err)
	}
}

func TestReceiptRecheckNewGenerationSupersedesPending(t *testing.T) {
	now := time.Now()
	first, f, st, _ := newReceiptWaitFixture(t, now.Add(-2*time.Second))
	id := onlyTodo(first).ID
	first.todos.mu.Lock()
	reopened := now
	first.todos.todos[id].ReopenedAt = &reopened
	first.todos.mu.Unlock()
	newer := first.newReceiptRecheck(context.Background(), "box", "s1", now.Add(-time.Second))
	selected, arm, err := first.persistReceiptRecheck(context.Background(), newer)
	if err != nil || !arm {
		t.Fatalf("persist reopened recheck = %v, %v", arm, err)
	}
	doc, err := first.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || doc.Waits[0].ID != selected.ID {
		t.Fatalf("reopened recheck did not supersede its old generation: %+v, %v", doc.Waits, err)
	}
	second := newServiceOn(t, f, st)
	copyReceiptFixtureTasks(first, second)
	second.restoreDelayedWaits(context.Background())
	waitFor(t, "the reopened task's receipt", func() bool {
		return strings.Count(feedKinds(second, "conv1"), "receipt") == 1
	})
}

func TestReceiptRecheckNewGenerationReplacesLocalTimer(t *testing.T) {
	oldAt := time.Now().Add(150 * time.Millisecond)
	first, _, _, _ := newReceiptWaitFixture(t, oldAt)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first.running.Store(&ctx)
	first.taskTurnEndedAfter(ctx, "box", "s1", oldAt)
	id := onlyTodo(first).ID
	first.todos.mu.Lock()
	reopened := time.Now()
	first.todos.todos[id].ReopenedAt = &reopened
	first.todos.mu.Unlock()
	first.taskTurnEndedAfter(ctx, "box", "s1", oldAt.Add(150*time.Millisecond))
	waitFor(t, "the new generation's local recheck timer", func() bool {
		return strings.Count(feedKinds(first, "conv1"), "receipt") == 1
	})
}

type failOnePutStore struct {
	Store
	key    string
	failed bool
}

func (st *failOnePutStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == st.key && !st.failed {
		st.failed = true
		return errors.New("run history write failed")
	}
	return st.Store.PutSetting(ctx, key, value)
}

func TestUnknownRetryOutcomeRetriesFailedRunSave(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	if _, ok, err := first.claimDelayedWait(context.Background(), w.ID, "interrupted", time.Now()); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	failedWrite := &failOnePutStore{Store: st, key: runsKey}
	second := newServiceOn(t, n, failedWrite)
	second.restoreDelayedWaits(context.Background())
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || !doc.Waits[0].Unknown {
		t.Fatalf("unknown wait was dropped after its history write failed: %+v, %v", doc.Waits, err)
	}
	before := len(n.pushed())
	third := newServiceOn(t, n, st)
	third.restoreDelayedWaits(context.Background())
	waitFor(t, "the unknown outcome to be recorded and cleared", func() bool {
		doc, err := third.readDelayedWaits(context.Background())
		return err == nil && len(doc.Waits) == 0
	})
	if n.sent() != 0 || len(n.pushed()) != before+1 {
		t.Fatalf("recovery resent or failed to report the unknown outcome: sends=%d pushes=%d", n.sent(), len(n.pushed()))
	}
}

func TestUnknownRetryOutcomeSurvivesHistoryTruncation(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	if _, ok, err := first.claimDelayedWait(context.Background(), w.ID, "interrupted", time.Now()); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	sc := first.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = nil
	err := st.PutSetting(context.Background(), runsKey, sc.runs)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	second := newServiceOn(t, n, st)
	second.restoreDelayedWaits(context.Background())
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 0 {
		t.Fatalf("unknown outcome was not recovered after history truncation: %+v, %v", doc.Waits, err)
	}
	sc = second.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.runs) != 1 || sc.runs[0].ID != w.Retry.Run.ID || n.sent() != 0 {
		t.Fatalf("unknown history = %+v, sends=%d", sc.runs, n.sent())
	}
}

type releaseSessionsHub struct {
	*fakeHub
	once    sync.Once
	read    chan struct{}
	release chan struct{}
	limited proto.Session
}

func (h *releaseSessionsHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionsList {
		h.once.Do(func() {
			close(h.read)
			<-h.release
		})
		return json.Marshal(map[string]any{"sessions": []proto.Session{h.limited}})
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestPartialRestoreDoesNotRequeueLiveReceiptClaim(t *testing.T) {
	st := withDelayedWaitCAS(planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}})
	firstReceipt, f, st, receiptWait := newReceiptWaitFixtureOn(t, st, time.Now().Add(-time.Second))
	scheduled, _, retryWait := newDelayedRetryFixture(t, st)
	if _, ok, err := scheduled.claimDelayedWait(context.Background(), retryWait.ID, "scheduled-crash", time.Now()); err != nil || !ok {
		t.Fatalf("scheduled claim = %v, %v", ok, err)
	}
	firstClaim := make(chan string, 1)
	unknownFailed := false
	receiptClaims := 0
	valueGate := &compareValueGate{Store: st}
	valueGate.before = func(doc delayedWaitDoc) error {
		for _, w := range doc.Waits {
			if w.Kind == delayedScheduledRetry && w.Unknown && !unknownFailed {
				unknownFailed = true
				return errors.New("scheduled recovery CAS failed")
			}
			if w.Kind == delayedReceiptRecheck && w.Claim != "" {
				receiptClaims++
				if receiptClaims == 1 {
					firstClaim <- w.Claim
				}
			}
		}
		return nil
	}
	second := newServiceOn(t, f, valueGate)
	copyReceiptFixtureTasks(firstReceipt, second)
	limited := limitTurn("s1", time.Now().Add(time.Hour), true)
	limited.MachineID, limited.Agent, limited.UpdatedAt = "box", "claude", time.Now()
	hub := &releaseSessionsHub{fakeHub: f, read: make(chan struct{}), release: make(chan struct{}), limited: limited}
	second.hub = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second.running.Store(&ctx)
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(hub.release) }) }
	t.Cleanup(resume)
	if second.restoreDelayedWaits(ctx) {
		t.Fatal("the injected scheduled recovery failure unexpectedly succeeded")
	}
	liveToken := <-firstClaim
	select {
	case <-hub.read:
	case <-time.After(2 * time.Second):
		t.Fatal("the live receipt handler did not reach settlement")
	}
	second.restoreDelayedWaits(ctx)
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) == 0 {
		t.Fatalf("live receipt wait disappeared: %+v, %v", doc.Waits, err)
	}
	var current delayedWait
	for _, w := range doc.Waits {
		if w.ID == receiptWait.ID {
			current = w
		}
	}
	if current.Claim != liveToken {
		t.Fatalf("partial restore replaced the live receipt claim %q with %q", liveToken, current.Claim)
	}
	resume()
	waitFor(t, "the settled receipt's successor", func() bool {
		doc, err := second.readDelayedWaits(context.Background())
		if err != nil {
			return false
		}
		for _, w := range doc.Waits {
			if w.ID == receiptWait.ID {
				return false
			}
		}
		return true
	})
}

func newReceiptWaitFixtureOn(t *testing.T, st Store, at time.Time) (*Service, *fakeHub, Store, delayedWait) {
	t.Helper()
	first, f := newTaskServiceOn(t, st)
	startedTask(t, first, f)
	first.taskWorking(context.Background(), "box", "s1")
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`
	f.setItems(proto.Item{ID: "answer-1", Kind: proto.KindText, Role: "assistant", Text: "Delivery verified", TS: time.Now()})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone,
		State: proto.StateIdle, LastText: "Delivery verified", UpdatedAt: time.Now(), Rev: 1})
	w := first.newReceiptRecheck(context.Background(), "box", "s1", at)
	selected, arm, err := first.persistReceiptRecheck(context.Background(), w)
	if err != nil || !arm {
		t.Fatalf("persist receipt recheck = %v, %v", arm, err)
	}
	return first, f, st, selected
}

type compareValueGate struct {
	Store
	before func(delayedWaitDoc) error
}

func (st *compareValueGate) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	if st.before != nil {
		if err := st.before(value.(delayedWaitDoc)); err != nil {
			return false, err
		}
	}
	return st.Store.(delayedWaitCAS).CompareSetting(ctx, key, revision, value)
}

type failFirstSettingStore struct {
	Store
	failed chan struct{}
	fail   bool
}

func (st *failFirstSettingStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == delayedWaitsKey && st.fail {
		st.fail = false
		close(st.failed)
		return false, errors.New("database unavailable")
	}
	return st.Store.Setting(ctx, key, out)
}

func TestDelayedWaitsRecoveryLoopRetriesStoreFailure(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	_, n, _ := newDelayedRetryFixture(t, st)
	flaky := &failFirstSettingStore{Store: st, failed: make(chan struct{}), fail: true}
	second := newServiceOn(t, n, flaky)
	setTasks(second, Task{ID: "p", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30,
		At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go second.Run(ctx)
	<-flaky.failed
	waitFor(t, "the restored retry after the store recovers", func() bool { return n.sent() == 1 })
}

func TestReceiptRecheckRetriesReleaseFailure(t *testing.T) {
	first, f, st, w := newReceiptWaitFixture(t, time.Now().Add(-time.Second))
	gate := &compareGateStore{Store: st}
	gate.before = func(call int) error {
		if call == 2 {
			return errors.New("release failed")
		}
		return nil
	}
	second := newServiceOn(t, f, gate)
	copyReceiptFixtureTasks(first, second)
	attempts, backoff, maximum := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 1, time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = attempts, backoff, maximum })
	f.mu.Lock()
	f.sessionsFail["box"] = 100
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second.running.Store(&ctx)
	second.runReceiptRecheck(ctx, w, true)
	waitFor(t, "the claimed recheck to become pending after the release recovers", func() bool {
		doc, err := second.readDelayedWaits(context.Background())
		return err == nil && len(doc.Waits) == 1 && doc.Waits[0].Claim == ""
	})
}

func TestScheduledRetryRearmPersistsBackoffDeadline(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	n.answerNode(DeviceSessionGet, func(string, json.RawMessage) (any, error) { return nil, errors.New("offline") })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first.runScheduledLimitRetry(ctx, w, true)
	doc, err := first.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || !doc.Waits[0].At.After(w.At) {
		t.Fatalf("rearm did not persist its backoff deadline: %+v, %v", doc.Waits, err)
	}
	n.mu.Lock()
	delete(n.nodes, DeviceSessionGet)
	n.mu.Unlock()
	second := newServiceOn(t, n, st)
	second.restoreDelayedWaits(context.Background())
	select {
	case <-time.After(75 * time.Millisecond):
	}
	if got := n.sent(); got != 0 {
		t.Fatalf("restored retry ran before its backoff: %d sends", got)
	}
	if err := second.updateDelayedWaits(context.Background(), func(doc *delayedWaitDoc) (bool, error) {
		doc.Waits[0].At = time.Now().Add(-time.Second)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	third := newServiceOn(t, n, st)
	third.restoreDelayedWaits(context.Background())
	waitFor(t, "the retry after its backoff expires", func() bool { return n.sent() == 1 })
}

func TestScheduledRetryCancellationIsSerializedWithClaim(t *testing.T) {
	st := withDelayedWaitCAS(&memStore{})
	first, n, w := newDelayedRetryFixture(t, st)
	setTasks(first, Task{ID: "p", Repeat: repeatInterval, EveryMinutes: 30, Enabled: false})
	gate := &compareGateStore{Store: st}
	gate.before = func(int) error {
		setTasks(first, Task{ID: "p", Title: "Example", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30,
			At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server"})
		return nil
	}
	first.store = func() Store { return gate }
	first.runScheduledLimitRetry(context.Background(), w, true)
	second := newServiceOn(t, n, st)
	second.runScheduledLimitRetry(context.Background(), w, true)
	if got := n.sent(); got != 1 {
		t.Fatalf("an unclaimed cancellation deleted the wait another manager needed: %d sends", got)
	}
}

func TestReceiptRecheckClaimErrorReplacesOlderTimer(t *testing.T) {
	first, f, st, w := newReceiptWaitFixture(t, time.Now().Add(-time.Second))
	gate := &compareGateStore{Store: st}
	gate.before = func(call int) error {
		if call == 1 {
			return errors.New("claim store unavailable")
		}
		return nil
	}
	second := newServiceOn(t, f, gate)
	copyReceiptFixtureTasks(first, second)
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	second.tasks.mu.Lock()
	second.tasks.limitLooks = map[SessionRef]time.Time{ref: time.Now().Add(-time.Hour)}
	second.tasks.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second.running.Store(&ctx)
	second.runReceiptRecheck(ctx, w, true)
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || !doc.Waits[0].At.After(w.At) {
		t.Fatalf("claim error did not persist the retry deadline: %+v, %v", doc.Waits, err)
	}
	second.tasks.mu.Lock()
	localAt := second.tasks.limitLooks[ref]
	second.tasks.mu.Unlock()
	if !localAt.Equal(doc.Waits[0].At) {
		t.Fatalf("older map entry suppressed the rearm: local=%v durable=%v", localAt, doc.Waits[0].At)
	}
	if err := second.updateDelayedWaits(context.Background(), func(doc *delayedWaitDoc) (bool, error) {
		doc.Waits[0].At = time.Now().Add(-time.Second)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	doc, err = second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 {
		t.Fatal(err)
	}
	second.armReceiptRecheck(doc.Waits[0], true)
	waitFor(t, "the receipt after the claim store recovers", func() bool {
		return strings.Count(feedKinds(second, "conv1"), "receipt") == 1
	})
}

func newReceiptWaitFixture(t *testing.T, at time.Time) (*Service, *fakeHub, Store, delayedWait) {
	t.Helper()
	st := withDelayedWaitCAS(planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}})
	return newReceiptWaitFixtureOn(t, st, at)
}

func TestRestoredReceiptRecheckRejectsStaleBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service, string)
	}{
		{name: "closed", change: func(s *Service, id string) {
			now := time.Now()
			s.todos.todos[id].DoneAt = &now
		}},
		{name: "deleted", change: func(s *Service, id string) {
			delete(s.todos.todos, id)
			for ref, todoID := range s.todos.activeBySession {
				if todoID == id {
					delete(s.todos.activeBySession, ref)
				}
			}
		}},
		{name: "reopened", change: func(s *Service, id string) {
			now := time.Now()
			s.todos.todos[id].ReopenedAt = &now
		}},
		{name: "relinked", change: func(s *Service, id string) {
			for i := range s.todos.todos[id].Agents {
				s.todos.todos[id].Agents[i].LinkedAt = s.todos.todos[id].Agents[i].LinkedAt.Add(time.Minute)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, f, st, w := newReceiptWaitFixture(t, time.Now().Add(-time.Second))
			first.todos.mu.Lock()
			tc.change(first, w.Receipt.TodoID)
			first.todos.mu.Unlock()
			second := newServiceOn(t, f, st)
			copyReceiptFixtureTasks(first, second)
			second.restoreDelayedWaits(context.Background())
			waitFor(t, "the stale recheck to be consumed", func() bool {
				doc, err := second.readDelayedWaits(context.Background())
				return err == nil && len(doc.Waits) == 0
			})
			if got := strings.Count(feedKinds(second, "conv1"), "receipt"); got != 0 {
				t.Fatalf("stale recheck produced %d receipts", got)
			}
		})
	}
}

func TestRestoredReceiptRecheckKeepsDeadline(t *testing.T) {
	at := time.Now().Add(500 * time.Millisecond)
	first, f, st, _ := newReceiptWaitFixture(t, at)
	second := newServiceOn(t, f, st)
	copyReceiptFixtureTasks(first, second)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second.running.Store(&ctx)
	second.restoreDelayedWaits(ctx)
	select {
	case <-time.After(75 * time.Millisecond):
	}
	if got := strings.Count(feedKinds(second, "conv1"), "receipt"); got != 0 {
		t.Fatalf("recheck ran before its deadline and produced %d receipts", got)
	}
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || !doc.Waits[0].At.Equal(at) {
		t.Fatalf("receipt deadline changed: %+v, %v", doc.Waits, err)
	}
}

func TestReceiptRecheckRetainsUnreachableAgent(t *testing.T) {
	first, f, st, w := newReceiptWaitFixture(t, time.Now().Add(-time.Second))
	second := newServiceOn(t, f, st)
	copyReceiptFixtureTasks(first, second)
	attempts, backoff, maximum := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 1, time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = attempts, backoff, maximum })
	f.mu.Lock()
	f.sessionsFail["box"] = agentReachAttempts
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second.running.Store(&ctx)
	second.runReceiptRecheck(ctx, w, true)
	doc, err := second.readDelayedWaits(context.Background())
	if err != nil || len(doc.Waits) != 1 || doc.Waits[0].Claim != "" || !doc.Waits[0].At.After(w.At) {
		t.Fatalf("unreachable agent was not left retryable: %+v, %v", doc.Waits, err)
	}
	if got := strings.Count(feedKinds(second, "conv1"), "receipt"); got != 0 {
		t.Fatalf("unreachable agent produced %d receipts", got)
	}
}
