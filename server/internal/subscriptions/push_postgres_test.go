package subscriptions

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type pushRaceGate struct {
	open chan struct{}
	once sync.Once
}

func newPushRaceGate() *pushRaceGate { return &pushRaceGate{open: make(chan struct{})} }

func (g *pushRaceGate) wait(ctx context.Context) error {
	select {
	case <-g.open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *pushRaceGate) release() { g.once.Do(func() { close(g.open) }) }

type pushRaceBarrier struct {
	arrived chan struct{}
	gate    *pushRaceGate
}

func newPushRaceBarrier() *pushRaceBarrier {
	return &pushRaceBarrier{arrived: make(chan struct{}, 2), gate: newPushRaceGate()}
}

func (b *pushRaceBarrier) wait(ctx context.Context) error {
	select {
	case b.arrived <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.gate.wait(ctx)
}

func awaitPushRaceReads(t *testing.T, b *pushRaceBarrier) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-b.arrived:
		case <-timer.C:
			t.Fatal("both services did not finish their pre-commit read")
		}
	}
}

// pushRaceStore coordinates two independent services at their existing store boundaries. The read barriers make the
// old freshness race deterministic; the second commit gate also lets the test inspect each committed run separately.
type pushRaceStore struct {
	Store
	listBarrier *pushRaceBarrier
	seenBarrier *pushRaceBarrier
	commitGate  *pushRaceGate
	listOnce    sync.Once
	seenOnce    sync.Once
	listErr     error
	seenErr     error
}

func (s *pushRaceStore) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	out, err := s.Store.ListSubscriptions(ctx)
	if err == nil && s.listBarrier != nil {
		s.listOnce.Do(func() { s.listErr = s.listBarrier.wait(ctx) })
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return out, err
}

func (s *pushRaceStore) SeenKeys(ctx context.Context, id string, uids []string) (map[string]bool, error) {
	out, err := s.Store.SeenKeys(ctx, id, uids)
	if err == nil && s.seenBarrier != nil {
		s.seenOnce.Do(func() { s.seenErr = s.seenBarrier.wait(ctx) })
	}
	if s.seenErr != nil {
		return nil, s.seenErr
	}
	return out, err
}

func (s *pushRaceStore) CommitRun(ctx context.Context, c RunCommit) (RunCommit, error) {
	if s.commitGate != nil {
		if err := s.commitGate.wait(ctx); err != nil {
			return RunCommit{}, err
		}
	}
	return s.Store.CommitRun(ctx, c)
}

type pushDeliveryCounter struct {
	mu  sync.Mutex
	ids []string
}

func (c *pushDeliveryCounter) add(s Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, s.ID)
}

func (c *pushDeliveryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ids)
}

type pushOutcome struct {
	out PushResult
	err error
}

func openPushRacePostgres(t *testing.T) (context.Context, *db.DB, *db.DB) {
	t.Helper()
	url := os.Getenv(dbtest.Env)
	if url == "" {
		t.Skip(dbtest.Env + " not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	first, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	return ctx, first, second
}

func newPushRaceService(store Store, clock Clock, delivered *pushDeliveryCounter) *Service {
	return New(Options{Store: store, Clock: clock, Delivered: delivered.add})
}

func awaitPushOutcome(t *testing.T, outcomes <-chan pushOutcome) PushResult {
	t.Helper()
	select {
	case outcome := <-outcomes:
		if outcome.err != nil {
			t.Fatalf("push: %v", outcome.err)
		}
		return outcome.out
	case <-time.After(10 * time.Second):
		t.Fatal("push did not finish")
		return PushResult{}
	}
}

func TestPushSourceCreationAcrossServicesPostgres(t *testing.T) {
	ctx, first, second := openPushRacePostgres(t)
	clock := &fakeClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	barrier := newPushRaceBarrier()
	t.Cleanup(barrier.gate.release)
	delivered := &pushDeliveryCounter{}
	actor := Actor{ID: "device:push-create-race", Name: "Test Phone"}
	services := []*Service{
		newPushRaceService(&pushRaceStore{Store: NewDBStore(first), listBarrier: barrier}, clock, delivered),
		newPushRaceService(&pushRaceStore{Store: NewDBStore(second), listBarrier: barrier}, clock, delivered),
	}
	outcomes := make(chan pushOutcome, 2)
	for _, svc := range services {
		go func() {
			out, err := svc.Push(ctx, actor, PushRequest{})
			outcomes <- pushOutcome{out: out, err: err}
		}()
	}
	awaitPushRaceReads(t, barrier)
	barrier.gate.release()
	one, two := awaitPushOutcome(t, outcomes), awaitPushOutcome(t, outcomes)
	if one.Subscription.ID != two.Subscription.ID {
		t.Fatalf("services made different push sources: %s and %s", one.Subscription.ID, two.Subscription.ID)
	}
	rows, err := first.Subscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var matched int
	for _, row := range rows {
		if row.Connector == PushConnector && row.Source == actor.ID {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("stored %d push sources for one device, want 1", matched)
	}
}

func TestPushFreshnessAcrossServicesPostgres(t *testing.T) {
	ctx, first, second := openPushRacePostgres(t)
	clock := &fakeClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	actor := Actor{ID: "device:push-commit-race", Name: "Test Phone"}
	setup := newPushRaceService(NewDBStore(first), clock, &pushDeliveryCounter{})
	created, err := setup.Push(ctx, actor, PushRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := setup.Update(ctx, created.Subscription.ID, Patch{Revision: created.Subscription.Revision, ToInbox: ptr(true)}, actor)
	if err != nil {
		t.Fatal(err)
	}

	reads := newPushRaceBarrier()
	secondCommit := newPushRaceGate()
	t.Cleanup(reads.gate.release)
	t.Cleanup(secondCommit.release)
	delivered := &pushDeliveryCounter{}
	firstService := newPushRaceService(&pushRaceStore{Store: NewDBStore(first), seenBarrier: reads}, clock, delivered)
	secondService := newPushRaceService(&pushRaceStore{Store: NewDBStore(second), seenBarrier: reads, commitGate: secondCommit}, clock, delivered)
	item := PushItem{Key: "shared", Title: "One notification", Body: "Deliver this once", App: "Example"}
	firstOutcome := make(chan pushOutcome, 1)
	secondOutcome := make(chan pushOutcome, 1)
	go func() {
		out, err := firstService.Push(ctx, actor, PushRequest{Items: []PushItem{item}})
		firstOutcome <- pushOutcome{out: out, err: err}
	}()
	go func() {
		out, err := secondService.Push(ctx, actor, PushRequest{Items: []PushItem{item}})
		secondOutcome <- pushOutcome{out: out, err: err}
	}()

	// Both services have now read the UID as fresh. Let only the first commit, then check its run before releasing the
	// second stale decision through the same shared PostgreSQL row lock.
	awaitPushRaceReads(t, reads)
	reads.gate.release()
	one := awaitPushOutcome(t, firstOutcome)
	if one.New != 1 || one.Count != 1 {
		t.Fatalf("first push = new %d count %d, want 1 and 1", one.New, one.Count)
	}
	items, err := first.SubscriptionItems(ctx, sub.ID, db.SubscriptionItemsQuery{})
	if err != nil || len(items) != 1 {
		t.Fatalf("after first push: %d items, %v", len(items), err)
	}
	_, runs, err := firstService.Get(ctx, sub.ID)
	if err != nil || len(runs) == 0 || runs[0].New != 1 {
		t.Fatalf("first committed run: %+v, %v", runs, err)
	}
	if delivered.count() != 1 {
		t.Fatalf("first push delivered %d times, want 1", delivered.count())
	}

	secondCommit.release()
	two := awaitPushOutcome(t, secondOutcome)
	if two.New != 0 || two.Count != 1 {
		t.Fatalf("competing push = new %d count %d, want 0 and 1", two.New, two.Count)
	}
	items, err = first.SubscriptionItems(ctx, sub.ID, db.SubscriptionItemsQuery{})
	if err != nil || len(items) != 1 || items[0].UID != "push:shared" {
		t.Fatalf("after competing push: %+v, %v", items, err)
	}
	_, runs, err = firstService.Get(ctx, sub.ID)
	if err != nil || len(runs) == 0 || runs[0].New != 0 {
		t.Fatalf("competing committed run: %+v, %v", runs, err)
	}
	if delivered.count() != 1 {
		t.Fatalf("shared UID delivered %d times, want 1", delivered.count())
	}
}
