package hub

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	// helloPendingLimit bounds each disconnected Store backlog. It is far above ordinary device counts and prevents one
	// connection inventing ids from consuming unbounded memory or putting an unbounded number of writes ahead of others.
	helloPendingLimit = 1024
	helloStoreTimeout = 5 * time.Second
)

// helloWriteWorkers keeps persistence off an app connection's read loop. There is one consumer for each setting
// group; its queue applies reports in message order and coalesces everything waiting into one snapshot save. Phone
// registrations additionally coalesce by device, so only the newest queued tool set for a device is written.
type helloWriteWorkers struct {
	mu                             sync.Mutex
	started, stopped               bool
	cancel                         context.CancelFunc
	appsWake, langsWake, toolsWake chan struct{}
	appsSlot, langsSlot, toolsSlot chan struct{}
	appsDrop, langsDrop, toolsDrop sync.Once
	workers                        sync.WaitGroup
}

type clientAppUpdate struct {
	cred   string
	change func(assistantwire.ClientApp, bool) assistantwire.ClientApp
}

type userLangUpdate struct {
	key  string
	lang i18n.Lang
	at   time.Time
}

// phoneToolUpdates has its own lock: phoneToolsMu covers the store write and must never be taken by the read loop.
type phoneToolUpdates struct {
	mu       sync.Mutex
	pending  map[string]db.PhoneToolRegistration
	order    []string
	flushing bool
	changed  chan struct{}
}

func (h *Hub) startHelloWriters() {
	w := &h.helloWrites
	w.mu.Lock()
	defer w.mu.Unlock()
	// A report that arrives after stop is not written; it either remains queued without a consumer or fails
	// admission. The app sends its hello and registration again when it reconnects.
	if w.started || w.stopped {
		return
	}
	w.started = true
	// The hub stops these workers explicitly after its app connections are gone. Deriving their lifetime from
	// deployCtx would strand reports read while shutdown is closing those connections.
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.appsWake, w.langsWake, w.toolsWake = make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{}, 1)
	w.appsSlot = make(chan struct{}, helloPendingLimit)
	w.langsSlot = make(chan struct{}, helloPendingLimit)
	w.toolsSlot = make(chan struct{}, helloPendingLimit)
	h.apps.changed = make(chan struct{})
	h.langs.changed = make(chan struct{})
	h.phoneToolUpdates.changed = make(chan struct{})
	start := func(wake <-chan struct{}, flush func() bool) {
		w.workers.Add(1)
		go func() {
			defer w.workers.Done()
			helloWriteLoop(ctx, wake, flush)
		}()
	}
	start(w.appsWake, h.flushClientApps)
	start(w.langsWake, h.flushUserLangs)
	start(w.toolsWake, h.flushPhoneTools)
}

func helloWriteLoop(ctx context.Context, wake <-chan struct{}, flush func() bool) {
	for ctx.Err() == nil {
		// Checking cancellation before selecting prefers it over an already signaled wake; otherwise Go could
		// randomly choose the wake and delay the transition to the drain.
		select {
		case <-ctx.Done():
		case <-wake:
			flush()
		}
	}
	// stopHelloWriters runs only after the app connections are gone. Store calls retain their own bounds; this
	// single drain covers every report accepted before that point before joining the hub's other workers.
	for flush() {
	}
}

// wakeHelloWriters lets a resumed snapshot write the reports that arrived
// while writes were paused. A non-blocking send: one pending wake covers them.
func (h *Hub) wakeHelloWriters() {
	w := &h.helloWrites
	w.mu.Lock()
	wakes := []chan struct{}{w.appsWake, w.langsWake, w.toolsWake}
	w.mu.Unlock()
	for _, wake := range wakes {
		if wake == nil {
			continue
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) stopHelloWriters() {
	w := &h.helloWrites
	w.mu.Lock()
	w.stopped = true
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.workers.Wait()
}

// waitHelloWrites is the consumers' idle signal for tests. Production counts nothing per report: each generation
// channel covers both an empty pending set and no flush in flight, under the same lock that protects that state.
// Unlike a WaitGroup or Cond, an outside worker can safely notify a waiter inside a synctest bubble.
func (h *Hub) waitHelloWrites() {
	waitHelloIdle(&h.apps.pendingMu, &h.apps.changed, func() bool { return len(h.apps.pending) > 0 || h.apps.flushing })
	waitHelloIdle(&h.langs.pendingMu, &h.langs.changed, func() bool { return len(h.langs.pending) > 0 || h.langs.flushing })
	waitHelloIdle(&h.phoneToolUpdates.mu, &h.phoneToolUpdates.changed, func() bool {
		return len(h.phoneToolUpdates.order) > 0 || h.phoneToolUpdates.flushing
	})
}

func waitHelloIdle(mu *sync.Mutex, changed *chan struct{}, busy func() bool) {
	mu.Lock()
	defer mu.Unlock()
	for busy() {
		generation := *changed
		if generation == nil {
			return
		}
		mu.Unlock()
		<-generation
		mu.Lock()
	}
}

func finishHelloFlush(mu *sync.Mutex, flushing *bool, changed *chan struct{}, pending func() bool) {
	mu.Lock()
	*flushing = false
	if !pending() && *changed != nil {
		close(*changed)
		*changed = make(chan struct{})
	}
	mu.Unlock()
}

// admitHelloReport reserves room without blocking in the ordinary case. At the bound, only the producer whose
// connection brought the next report waits, up to that write's existing Store timeout. One overflow log per consumer
// avoids turning a flood into another flood; reconnecting apps send these reports again.
func (h *Hub) admitHelloReport(slot chan struct{}, timeout time.Duration, logOnce *sync.Once, name string) bool {
	select {
	case slot <- struct{}{}:
		return true
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case slot <- struct{}{}:
		return true
	case <-ctx.Done():
		// Do not drop if capacity became available at the deadline itself.
		select {
		case slot <- struct{}{}:
			return true
		default:
		}
		logOnce.Do(func() { h.logf("%s pending queue full; dropping reports", name) })
		return false
	}
}

func releaseHelloReports(slot chan struct{}, n int) {
	for range n {
		<-slot
	}
}

func wakeHelloWriter(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (h *Hub) flushClientApps() bool {
	// A snapshot waits for writes to finish and refuses new ones; the reports
	// stay pending and a wake after resume writes them (quiesce.go).
	if !h.quiesce.begin() {
		return false
	}
	defer h.quiesce.end()
	a := &h.apps
	a.pendingMu.Lock()
	if len(a.pending) == 0 {
		a.pendingMu.Unlock()
		return false
	}
	a.flushing = true
	a.pendingMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), helloStoreTimeout)
	// Load before detaching the batch. Reports that arrive while the first load is held join this same snapshot;
	// on failure, everything detached is skipped without a save and the next report retries.
	loaded := h.prepareClientApps(ctx)
	a.pendingMu.Lock()
	batch := a.pending
	a.pending = nil
	a.pendingMu.Unlock()
	releaseHelloReports(h.helloWrites.appsSlot, len(batch))
	if loaded && len(batch) > 0 {
		h.writeClientAppsLoaded(ctx, batch)
	}
	cancel()
	finishHelloFlush(&a.pendingMu, &a.flushing, &a.changed, func() bool { return len(a.pending) > 0 })
	return true
}

func (h *Hub) flushUserLangs() bool {
	if !h.quiesce.begin() {
		return false
	}
	defer h.quiesce.end()
	g := &h.langs
	g.pendingMu.Lock()
	if len(g.pending) == 0 {
		g.pendingMu.Unlock()
		return false
	}
	g.flushing = true
	pending := append([]userLangUpdate(nil), g.pending...)
	g.pendingMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), helloStoreTimeout)
	loaded := h.prepareUserLangs(ctx, pending)
	g.pendingMu.Lock()
	batch := g.pending
	g.pending = nil
	g.pendingMu.Unlock()
	releaseHelloReports(h.helloWrites.langsSlot, len(batch))
	// A keyed report may have arrived behind an initially keyless pending batch. Cover it before applying anything.
	loaded = loaded && h.prepareUserLangs(ctx, batch)
	if loaded && len(batch) > 0 {
		h.writeUserLangsLoaded(ctx, batch)
	}
	cancel()
	finishHelloFlush(&g.pendingMu, &g.flushing, &g.changed, func() bool { return len(g.pending) > 0 })
	return true
}

func (h *Hub) flushPhoneTools() bool {
	if !h.quiesce.begin() {
		return false
	}
	defer h.quiesce.end()
	q := &h.phoneToolUpdates
	q.mu.Lock()
	if len(q.order) == 0 {
		q.mu.Unlock()
		return false
	}
	q.flushing = true
	batch := make([]db.PhoneToolRegistration, 0, len(q.order))
	for _, id := range q.order {
		batch = append(batch, q.pending[id])
	}
	clear(q.pending)
	q.order = nil
	q.mu.Unlock()
	releaseHelloReports(h.helloWrites.toolsSlot, len(batch))
	for _, registration := range batch {
		h.writePhoneTools(registration)
	}
	finishHelloFlush(&q.mu, &q.flushing, &q.changed, func() bool { return len(q.order) > 0 })
	return true
}
