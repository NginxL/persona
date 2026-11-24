package subscriptions

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"
)

// Retention of a subscription's dedup keys.
const (
	seenKeep    = 5000
	seenMaxAge  = 400 * 24 * time.Hour
	RunsKept    = 20
	leaseLength = 5 * time.Minute
)

// Store keeps the subscriptions, their dedup keys and their pending items (internal/db: the manager's subscriptions,
// subscription_seen and subscription_items).
type Store interface {
	ListSubscriptions(ctx context.Context) ([]Subscription, error)
	// GetSubscription is nil when there is none with id.
	GetSubscription(ctx context.Context, id string) (*Subscription, error)
	CreateSubscription(ctx context.Context, s Subscription) error
	// UpdateSubscription writes all of s if the stored revision is prev; false when it is not.
	UpdateSubscription(ctx context.Context, s Subscription, prev int64) (bool, error)
	// DeleteSubscription removes it with its dedup keys and pending items.
	DeleteSubscription(ctx context.Context, id string) error
	// DeleteSubscriptionAt removes it only while its stored revision is prev.
	DeleteSubscriptionAt(ctx context.Context, id string, prev int64) (bool, error)
	// LeaseSubscription holds id for a run until until, if no other run holds it at now; false when one does.
	LeaseSubscription(ctx context.Context, id string, now, until time.Time) (bool, error)
	// SeenKeys says which of uids the subscription has seen.
	SeenKeys(ctx context.Context, id string, uids []string) (map[string]bool, error)
	// CommitRun writes a run's outcome in one transaction (RunCommit) and returns the outcome whose dedup keys this
	// transaction actually claimed. Its items and new counts exclude keys another manager claimed first.
	CommitRun(ctx context.Context, c RunCommit) (RunCommit, error)
	// HeldItems are the subscription's held items q picks, in the order they were stored (#2039).
	HeldItems(ctx context.Context, id string, q ItemQuery) ([]Held, error)
	// HeldItem is the subscription's held item uid (the newest stored, if several); nil when it holds none (#6198).
	HeldItem(ctx context.Context, id, uid string) (*Held, error)
	// PruneItems removes the subscription's held items stored before before, except keep's (their dedup keys stay).
	PruneItems(ctx context.Context, id string, before time.Time, keep []string) error
}

// ItemQuery picks held items: those stored at or after Since, after the stored order After, at most Limit (the
// oldest, or the newest when Newest), returned in stored order.
type ItemQuery struct {
	After  int64
	Since  time.Time
	Limit  int
	Newest bool
	// UIDs, when not nil, picks only those (a things recipe's latest answer, #13637); none when empty.
	UIDs []string
}

// Held is an item as it waits for the destinations: its place in the stored order and when its run stored it.
type Held struct {
	Seq    int64
	Stored time.Time
	Item
}

// RunCommit is what a run writes at once: the new dedup keys and items, the SinceAt cursor or bounded continuation
// with its original window, the history, and the state. The
// state (RunState, Error, FailCount, FailingSince, PausedUntil, ShapeMisses, ToolFingerprint) is written only while
// the subscription is still at Revision: a run that started before the user changed the recipe does not undo the
// change. The run's lease is released.
type RunCommit struct {
	Sub      Subscription
	Revision int64
	Seen     []Seen
	Items    []Item
	// Read are uids of held items the run read as seen in the source (#6176): marked seen, never unmarked.
	Read []string
	Now  time.Time
	// Baseline is the baseline mark the run took (Subscription.Baseline): it is cleared only while it is still that
	// one, so a recipe changed again during the run keeps its own.
	Baseline *time.Time
}

// MemStore is a Store in memory, for tests and a manager without a database.
type MemStore struct {
	mu    sync.Mutex
	subs  map[string]Subscription
	seen  map[string]map[string]time.Time
	items []Held
	seq   int64
	// FailCommit, when set, fails every CommitRun with it (a crash in the middle of a run).
	FailCommit error
}

func NewMemStore() *MemStore {
	return &MemStore{subs: map[string]Subscription{}, seen: map[string]map[string]time.Time{}}
}

func (m *MemStore) ListSubscriptions(context.Context) ([]Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Subscription, 0, len(m.subs))
	for _, s := range m.subs {
		out = append(out, clone(s))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemStore) GetSubscription(_ context.Context, id string) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.subs[id]
	if !ok {
		return nil, nil
	}
	c := clone(s)
	return &c, nil
}

func (m *MemStore) CreateSubscription(_ context.Context, s Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[s.ID] = clone(s)
	return nil
}

func (m *MemStore) UpdateSubscription(_ context.Context, s Subscription, prev int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.subs[s.ID]
	if !ok || cur.Revision != prev {
		return false, nil
	}
	m.subs[s.ID] = clone(s)
	return true, nil
}

func (m *MemStore) DeleteSubscription(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delete(id)
	return nil
}

func (m *MemStore) DeleteSubscriptionAt(_ context.Context, id string, prev int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.subs[id]
	if !ok || cur.Revision != prev {
		return false, nil
	}
	m.delete(id)
	return true, nil
}

func (m *MemStore) delete(id string) {
	delete(m.subs, id)
	delete(m.seen, id)
	m.items = slices.DeleteFunc(m.items, func(it Held) bool { return it.SubscriptionID == id })
}

func (m *MemStore) LeaseSubscription(_ context.Context, id string, now, until time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.subs[id]
	if !ok || s.LeaseUntil != nil && s.LeaseUntil.After(now) {
		return false, nil
	}
	s.LeaseUntil = &until
	m.subs[id] = s
	return true, nil
}

func (m *MemStore) SeenKeys(_ context.Context, id string, uids []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, u := range uids {
		if _, ok := m.seen[id][u]; ok {
			out[u] = true
		}
	}
	return out, nil
}

func (m *MemStore) CommitRun(_ context.Context, c RunCommit) (RunCommit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailCommit != nil {
		return RunCommit{}, m.FailCommit
	}
	cur, ok := m.subs[c.Sub.ID]
	if !ok {
		return claimedRun(c, nil), nil // deleted while it ran: nothing is kept
	}
	if m.seen[c.Sub.ID] == nil {
		m.seen[c.Sub.ID] = map[string]time.Time{}
	}
	claimed := map[string]bool{}
	for _, k := range c.Seen {
		if _, had := m.seen[c.Sub.ID][k.UID]; !had {
			m.seen[c.Sub.ID][k.UID] = c.Now
			claimed[k.UID] = true
		}
	}
	c = claimedRun(c, claimed)
	for _, it := range c.Items {
		it.SubscriptionID = c.Sub.ID
		m.seq++
		m.items = append(m.items, Held{Seq: m.seq, Stored: c.Now, Item: it})
	}
	for i := range m.items {
		if h := &m.items[i]; h.SubscriptionID == c.Sub.ID && slices.Contains(c.Read, h.UID) {
			h.Seen = true
		}
	}
	m.pruneSeen(c.Sub.ID, c.Now)
	applyRun(&cur, c, cur.Revision == c.Revision)
	cur.LeaseUntil = nil
	m.subs[c.Sub.ID] = cur
	return c, nil
}

// claimedRun limits a run to the dedup keys its transaction inserted. Items without a Seen candidate keep their
// legacy low-level behavior; every service run with fresh items supplies Seen candidates for them.
func claimedRun(c RunCommit, claimed map[string]bool) RunCommit {
	if len(c.Seen) == 0 {
		return c
	}
	candidates := make(map[string]bool, len(c.Seen))
	seen := make([]Seen, 0, len(c.Seen))
	for _, k := range c.Seen {
		candidates[k.UID] = true
		if claimed[k.UID] {
			seen = append(seen, k)
		}
	}
	c.Seen = seen
	items := make([]Item, 0, len(c.Items))
	for _, it := range c.Items {
		if !candidates[it.UID] || claimed[it.UID] {
			items = append(items, it)
		}
	}
	c.Items = items
	c.Sub.LastNewCount = len(items)
	c.Sub.Runs = slices.Clone(c.Sub.Runs)
	if len(c.Sub.Runs) > 0 {
		run := &c.Sub.Runs[len(c.Sub.Runs)-1]
		run.New = len(items)
		if run.Baseline > 0 {
			run.Baseline = len(seen)
		}
	}
	return c
}

func (m *MemStore) pruneSeen(id string, now time.Time) {
	keys := m.seen[id]
	type kv struct {
		k string
		t time.Time
	}
	var all []kv
	for k, t := range keys {
		if now.Sub(t) > seenMaxAge {
			delete(keys, k)
			continue
		}
		all = append(all, kv{k, t})
	}
	if len(all) <= seenKeep {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
	for _, e := range all[seenKeep:] {
		delete(keys, e.k)
	}
}

func (m *MemStore) HeldItems(_ context.Context, id string, q ItemQuery) ([]Held, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Held
	for _, it := range m.items {
		if it.SubscriptionID == id && it.Seq > q.After && !it.Stored.Before(q.Since) && (q.UIDs == nil || slices.Contains(q.UIDs, it.UID)) {
			out = append(out, it)
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		if q.Newest {
			out = out[len(out)-q.Limit:]
		} else {
			out = out[:q.Limit]
		}
	}
	return slices.Clone(out), nil
}

func (m *MemStore) HeldItem(_ context.Context, id, uid string) (*Held, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.items) - 1; i >= 0; i-- {
		if h := m.items[i]; h.SubscriptionID == id && h.UID == uid {
			return &h, nil
		}
	}
	return nil, nil
}

func (m *MemStore) PruneItems(_ context.Context, id string, before time.Time, keep []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = slices.DeleteFunc(m.items, func(it Held) bool {
		return it.SubscriptionID == id && it.Stored.Before(before) && !slices.Contains(keep, it.UID)
	})
	return nil
}

// Items are the pending items held, in order (tests).
func (m *MemStore) Items() []Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Item, len(m.items))
	for i, it := range m.items {
		out[i] = it.Item
	}
	return out
}

// SeenCount is how many dedup keys a subscription has (tests).
func (m *MemStore) SeenCount(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen[id])
}

// applyRun copies a run's outcome onto cur: the cursor, history and timings always, the state only when withState.
func applyRun(cur *Subscription, c RunCommit, withState bool) {
	out := c.Sub
	if c.Baseline != nil && sameTime(cur.Baseline, c.Baseline) {
		cur.Baseline = nil
	}
	cur.SnapshotHash, cur.Runs = out.SnapshotHash, out.Runs
	if SameRecipe(cur.Recipe, out.Recipe) {
		cur.Answer, cur.AnswerAt = slices.Clone(out.Answer), out.AnswerAt // a things recipe's latest answer (#13637)
		cur.SinceAt, cur.Continuation = out.SinceAt, cloneContinuation(out.Continuation)
		if !on(*cur) && cur.Continuation != nil && !cur.Continuation.Manual {
			cur.Continuation = nil // turned off while this page ran: the position went with it (#6310)
		}
	}
	cur.LastRunAt, cur.LastOKAt, cur.LastNewCount = out.LastRunAt, out.LastOKAt, out.LastNewCount
	cur.WeakDedup, cur.Shape, cur.NextRunAt = out.WeakDedup, out.Shape, out.NextRunAt
	cur.SourceName, cur.Icon, cur.Service, cur.SourceMin, cur.MaxCallsPerHour = out.SourceName, out.Icon, out.Service, out.SourceMin, out.MaxCallsPerHour
	// The catch-up mark of the Inbox turned on (or switched to it, #6265) as the run saw it, not of another.
	if sameTime(cur.InboxFrom, out.InboxFrom) && sameTime(cur.Migrated, out.Migrated) {
		cur.InboxCatchup = out.InboxCatchup
		if out.InboxGroup == groupOf(cur.Recipe) { // the rows' grouping, while the recipe still groups that way
			cur.InboxGroup, cur.InboxGroupFrom = out.InboxGroup, out.InboxGroupFrom
		}
	}
	if withState {
		cur.RunState, cur.Error, cur.FailCount, cur.FailingSince = out.RunState, out.Error, out.FailCount, out.FailingSince
		cur.PausedUntil, cur.ShapeMisses, cur.ToolFingerprint = out.PausedUntil, out.ShapeMisses, out.ToolFingerprint
	}
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func clone(s Subscription) Subscription {
	s.Runs = slices.Clone(s.Runs)
	s.Continuation = cloneContinuation(s.Continuation)
	return s
}
