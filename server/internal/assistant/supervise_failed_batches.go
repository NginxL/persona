package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
)

// Failed starts for different tasks of one leader conversation share a cooldown after the first failure is reserved
// for delivery. Later tasks wait in this durable document until the boundary, rather than waking the leader per task.
// Remediable starts join the same notification batch; their screen, keys and retry tools never consult this window.
// A new attempt and every main or ordinary side conversation keep their existing immediate behavior. The first
// failure is immediate; one supplementary digest carries what arrived during the cooldown. Per-attempt delivery
// deduplication in assistant.supervision_dedup remains independent.
const failedStartBatchesKey = "assistant.supervision_failed_batches"

var errFailedStartBatchConflict = errors.New("failed-start batch update kept conflicting")

type failedStartBatchDoc struct {
	Revision int64                        `json:"revision"`
	Batches  map[string]*failedStartBatch `json:"batches,omitempty"`
}

type failedStartBatch struct {
	Recipient  string                     `json:"recipient"`
	Machine    string                     `json:"machine"`
	Launcher   string                     `json:"launcher"`
	Reason     string                     `json:"reason"`
	NotifiedAt time.Time                  `json:"notified_at"` // when this delivery window was reserved, not when its send returned
	Due        time.Time                  `json:"due"`
	Notified   map[string]string          `json:"notified,omitempty"` // todo → its latest accepted attempt key in this window
	Deferred   map[string]superviseSignal `json:"deferred,omitempty"` // todo → its newest final failure not accepted for delivery yet
	Claims     map[string]string          `json:"claims,omitempty"`   // todo → manager epoch currently allowed to attempt an immediate delivery
}

type failedStartBatchCAS interface {
	CompareSetting(context.Context, string, int64, any) (bool, error)
}

// updateFailedStartBatches serializes local writers and fences another manager with the document revision. Every
// callback output must be reset on a CAS retry: the losing manager's first attempt may have looked deliverable while
// the winner's already-persisted window makes the same signal deferred. The no-store path is deliberately memory-only.
func (s *Service) updateFailedStartBatches(ctx context.Context, change func(*failedStartBatchDoc) (bool, error)) error {
	b := &s.supervise
	b.failedBatchMu.Lock()
	defer b.failedBatchMu.Unlock()
	st := s.store()
	if st == nil {
		_, err := change(&b.failedBatches)
		b.failedBatchesLoaded = true
		return err
	}
	for range 32 {
		var doc failedStartBatchDoc
		if _, err := st.Setting(ctx, failedStartBatchesKey, &doc); err != nil {
			return err
		}
		changed, err := change(&doc)
		if err != nil || !changed {
			return err
		}
		revision := doc.Revision
		doc.Revision = revision + 1
		if cas, ok := st.(failedStartBatchCAS); ok {
			saved, err := cas.CompareSetting(ctx, failedStartBatchesKey, revision, doc)
			if err != nil {
				return err
			}
			if saved {
				return nil
			}
			continue
		}
		return st.PutSetting(ctx, failedStartBatchesKey, doc)
	}
	return errFailedStartBatchConflict
}

// failedStartBatchKey identifies only failures eligible for leader cooldown grouping. Unknown facts fail open to
// the old per-task behavior: they must not merge unrelated failures or leak one direction's tasks into another's.
func (s *Service) failedStartBatchKey(ctx context.Context, to string, g superviseSignal) (string, bool) {
	if g.Kind != sigFailed || g.Recipient == "" || g.Recipient != to || g.Machine == "" || g.Launcher == "" {
		return "", false
	}
	reason := strings.Join(strings.Fields(g.Detail), " ")
	if reason == "" {
		return "", false
	}
	t, ok := s.todoNow(g.Todo)
	if !ok || t.Supervision == nil || t.Supervision.Supervisor != to || !s.isLeader(ctx, to) || t.Supervision.state(t) == reviewNeedsUser {
		return "", false
	}
	machine := g.Machine
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
	}
	if machine == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{to, machine, g.Launcher, reason}, "\x00")))
	return hex.EncodeToString(sum[:]), true
}

func (b *failedStartBatch) init() {
	if b.Notified == nil {
		b.Notified = map[string]string{}
	}
	if b.Deferred == nil {
		b.Deferred = map[string]superviseSignal{}
	}
	if b.Claims == nil {
		b.Claims = map[string]string{}
	}
}

// failedGeneration names the start event independently of its queue position. queueSignal keeps At of an older
// pending failure to preserve its place, so At alone cannot decide which of two attempts is newer.
func (g superviseSignal) failedGeneration() time.Time {
	if !g.Generation.IsZero() {
		return g.Generation
	}
	return g.At // documents written by the first WIP version have no separate generation
}

func newerFailedSignal(a, b superviseSignal) bool {
	return a.failedGeneration().After(b.failedGeneration())
}

func sameFailedGeneration(a, b superviseSignal) bool {
	return a.Key == b.Key && a.failedGeneration().Equal(b.failedGeneration())
}

func pruneFailedStartBatches(doc *failedStartBatchDoc, now time.Time) bool {
	changed := false
	for key, batch := range doc.Batches {
		if batch == nil || len(batch.Deferred) == 0 && !batch.Due.IsZero() && now.Sub(batch.Due) >= superviseDedupWindow {
			delete(doc.Batches, key)
			changed = true
		}
	}
	return changed
}

// gateFailedStartBatches reserves the first message before any external send, holds new tasks during an active
// cooldown, and returns only signals this manager may deliver now. Deferred membership and an immediate-delivery
// claim are persisted before the in-memory timer is armed. A store failure fails open to the old immediate behavior.
func (s *Service) gateFailedStartBatches(ctx context.Context, to string, list []superviseSignal, now, prev time.Time) []superviseSignal {
	var out, deferred []superviseSignal
	err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		out, deferred = nil, nil
		initialRound := map[string]bool{}
		changed := pruneFailedStartBatches(doc, now)
		for _, incoming := range list {
			g := incoming
			g.NotBefore = time.Time{}
			if g.Kind == sigFailed {
				g.Recipient = to
			}
			key, eligible := s.failedStartBatchKey(ctx, to, g)
			if !eligible {
				out = append(out, g)
				continue
			}
			batch := doc.Batches[key]
			if batch == nil {
				if doc.Batches == nil {
					doc.Batches = map[string]*failedStartBatch{}
				}
				batch = &failedStartBatch{Recipient: to, Machine: g.Machine, Launcher: g.Launcher, Reason: strings.Join(strings.Fields(g.Detail), " ")}
				doc.Batches[key] = batch
			}
			batch.init()
			if batch.NotifiedAt.IsZero() || !now.Before(batch.Due) {
				// All same-group signals already gathered at this boundary form one supplementary round. Signals that
				// arrive after this CAS write join the next window instead of splitting that round per manager.
				batch.NotifiedAt = now.UTC()
				batch.Due = batch.NotifiedAt.Add(superviseDedupWindow)
				batch.Notified = map[string]string{}
				initialRound[key] = true
				changed = true
			}

			earlier, waiting := batch.Deferred[g.Todo]
			if waiting && earlier.Key != g.Key && newerFailedSignal(earlier, g) {
				// A late old attempt must not replace or delete the newer durable attempt. Put the newer one back in
				// this manager's scheduler too; the incoming stale entry has already been removed from pending.
				deferred = append(deferred, earlier)
				continue
			}
			if waiting && earlier.Key == g.Key {
				if !initialRound[key] && earlier.NotBefore.After(now) {
					g.NotBefore = earlier.NotBefore
					if g.Generation.IsZero() {
						g.Generation = earlier.Generation
					}
					deferred = append(deferred, g)
					continue
				}
				owner := batch.Claims[g.Todo]
				if owner != "" && owner != s.epoch && g.failedClaim != s.epoch {
					continue // another live manager owns this immediate attempt; its durable record survives here
				}
				batch.Claims[g.Todo] = s.epoch
				g.failedClaim = s.epoch
				batch.Deferred[g.Todo] = g
				out = append(out, g)
				changed = true
				continue
			}

			// This is a new task in the group, or a newer attempt replacing a deferred one. An already notified
			// task's new attempt is news immediately; an unrelated new task waits for the boundary.
			if !initialRound[key] && !waiting && batch.Notified[g.Todo] == g.Key {
				out = append(out, g) // an exact repeat is settled by ordinary per-attempt delivery deduplication
				continue
			}
			if !initialRound[key] && !waiting && batch.Notified[g.Todo] == "" {
				g.NotBefore = batch.Due
				delete(batch.Claims, g.Todo)
				deferred = append(deferred, g)
			} else {
				batch.Claims[g.Todo] = s.epoch
				g.failedClaim = s.epoch
				out = append(out, g)
			}
			batch.Deferred[g.Todo] = g
			changed = true

			// One todo has one live deferred generation across reason, launcher, machine and recipient batches.
			// Only the current newer attempt may remove older records; a late old delivery cannot remove this one.
			for otherKey, other := range doc.Batches {
				if otherKey == key || other == nil {
					continue
				}
				old, ok := other.Deferred[g.Todo]
				if !ok || newerFailedSignal(old, g) {
					continue
				}
				delete(other.Deferred, g.Todo)
				delete(other.Claims, g.Todo)
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		s.logf("supervise: failed-start batch unavailable; delivering immediately: %v", err)
		for i := range list {
			list[i].NotBefore = time.Time{}
			if list[i].Kind == sigFailed {
				list[i].Recipient = to
			}
		}
		return list
	}

	b := &s.supervise
	if len(out) == 0 {
		// A wholly deferred round sent nothing. A mixed round must keep now: its immediate member may be followed
		// by routine signals whose ordinary interval starts at that actual delivery, not at prev.
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	if len(deferred) == 0 {
		return out
	}
	b.mu.Lock()
	b.init()
	for _, g := range deferred {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if cur, ok := b.pending[g.id()]; ok && cur.Key != g.Key && newerFailedSignal(cur, g) {
			continue // a newer attempt arrived while the durable gate was being written
		}
		b.pending[g.id()] = g
	}
	s.scheduleDigestLocked(time.Now())
	b.mu.Unlock()
	return out
}

// dropFailedDeferred removes only an accepted/settled generation, or an older generation superseded by it. Claim
// fencing prevents an old manager's completion from deleting another manager's newer immediate delivery.
func dropFailedDeferred(doc *failedStartBatchDoc, g superviseSignal, exactOnly, checkClaim bool) bool {
	changed := false
	for _, batch := range doc.Batches {
		if batch == nil {
			continue
		}
		current, ok := batch.Deferred[g.Todo]
		if !ok || newerFailedSignal(current, g) || exactOnly && !sameFailedGeneration(current, g) {
			continue
		}
		if checkClaim && sameFailedGeneration(current, g) && g.failedClaim != "" && batch.Claims[g.Todo] != "" && batch.Claims[g.Todo] != g.failedClaim {
			continue
		}
		delete(batch.Deferred, g.Todo)
		delete(batch.Claims, g.Todo)
		changed = true
	}
	return changed
}

// finishFailedStartBatches records actual acceptance, not merely a timer or an attempted send. Accepted members
// leave the deferred outbox; an exact duplicate may settle, while another manager's claimed or newer attempt remains.
func (s *Service) finishFailedStartBatches(ctx context.Context, to string, accepted, settled []superviseSignal, now time.Time) {
	err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		changed := pruneFailedStartBatches(doc, now)
		for _, g := range accepted {
			if dropFailedDeferred(doc, g, false, true) {
				changed = true
			}
			key, eligible := s.failedStartBatchKey(ctx, to, g)
			if !eligible {
				continue
			}
			batch := doc.Batches[key]
			if batch == nil {
				if doc.Batches == nil {
					doc.Batches = map[string]*failedStartBatch{}
				}
				batch = &failedStartBatch{Recipient: to, Machine: g.Machine, Launcher: g.Launcher, Reason: strings.Join(strings.Fields(g.Detail), " ")}
				doc.Batches[key] = batch
			}
			batch.init()
			if batch.NotifiedAt.IsZero() || !now.Before(batch.Due) {
				batch.NotifiedAt = now.UTC()
				batch.Due = batch.NotifiedAt.Add(superviseDedupWindow)
				batch.Notified = map[string]string{}
			}
			batch.Notified[g.Todo] = g.Key
			changed = true
		}
		for _, g := range settled {
			if dropFailedDeferred(doc, g, true, true) {
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		s.logf("supervise: could not checkpoint failed-start batch delivery: %v", err)
	}
}

// discardFailedStartBatches settles a generation that became moot before delivery (for example its kept tab was
// retried successfully). It never removes a newer generation, regardless of which manager currently claims it.
func (s *Service) discardFailedStartBatches(ctx context.Context, list []superviseSignal, now time.Time) {
	if len(list) == 0 {
		return
	}
	err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		changed := pruneFailedStartBatches(doc, now)
		for _, g := range list {
			if dropFailedDeferred(doc, g, true, false) {
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		s.logf("supervise: could not settle failed-start batch entries: %v", err)
	}
}

// releaseFailedStartBatches leaves an unaccepted signal deferred but lets any manager claim its next attempt. This
// is the failure counterpart of finish: relay persistence errors must never turn a deferred record into accepted.
func (s *Service) releaseFailedStartBatches(ctx context.Context, list []superviseSignal) {
	if len(list) == 0 {
		return
	}
	err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		changed := false
		for _, g := range list {
			if g.failedClaim == "" {
				continue
			}
			for _, batch := range doc.Batches {
				if batch == nil {
					continue
				}
				current, ok := batch.Deferred[g.Todo]
				if ok && sameFailedGeneration(current, g) && batch.Claims[g.Todo] == g.failedClaim {
					delete(batch.Claims, g.Todo)
					changed = true
				}
			}
		}
		return changed, nil
	})
	if err != nil {
		s.logf("supervise: could not release failed-start delivery claim: %v", err)
	}
}

// restoreFailedStartBatches puts only deferred, not-yet-accepted members back into the ordinary pending scheduler.
// Accepted relay messages remain the relay's responsibility. If an old document names one todo in several batches,
// only its newest generation is restored and older duplicates are removed before any timer is armed.
func (s *Service) restoreFailedStartBatches(ctx context.Context) error {
	b := &s.supervise
	b.failedBatchMu.Lock()
	if b.failedBatchesLoaded {
		b.failedBatchMu.Unlock()
		return nil
	}
	b.failedBatchesLoaded = true // serialize competing restorers; reset below if this attempt fails
	b.failedBatchMu.Unlock()

	var restored []superviseSignal
	err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		restored = nil
		latest := map[string]superviseSignal{}
		latestBatch := map[string]string{}
		keys := make([]string, 0, len(doc.Batches))
		for key := range doc.Batches {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			batch := doc.Batches[key]
			if batch == nil {
				continue
			}
			batch.init()
			for todo, g := range batch.Deferred {
				current, ok := latest[todo]
				if !ok || newerFailedSignal(g, current) || sameFailedGeneration(g, current) && key > latestBatch[todo] {
					latest[todo], latestBatch[todo] = g, key
				}
			}
		}
		changed := false
		for key, batch := range doc.Batches {
			if batch == nil {
				continue
			}
			for todo := range batch.Deferred {
				if latestBatch[todo] != key {
					delete(batch.Deferred, todo)
					changed = true
				}
			}
			if len(batch.Claims) > 0 {
				batch.Claims = map[string]string{} // a new manager may reclaim every unaccepted immediate generation
				changed = true
			}
		}
		todos := make([]string, 0, len(latest))
		for todo := range latest {
			todos = append(todos, todo)
		}
		slices.Sort(todos)
		for _, todo := range todos {
			g := latest[todo]
			g.failedClaim = s.epoch
			restored = append(restored, g)
		}
		return changed, nil
	})
	if err != nil {
		b.failedBatchMu.Lock()
		b.failedBatchesLoaded = false
		b.failedBatchMu.Unlock()
		return err
	}
	for _, g := range restored {
		s.queueSignal(g)
	}
	return nil
}

// failedStartBatchRecoveryLoop restores deferred work at manager startup even when every failed to-do is closed and
// no new signal can cause sendDigest to run. A failed store read is retried rather than abandoning the durable work.
func (s *Service) failedStartBatchRecoveryLoop(ctx context.Context) {
	for {
		if err := s.restoreFailedStartBatches(ctx); err == nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// revalidateFailedStartDeferrals releases a deferred signal at once when its leader was removed, its task started
// waiting for the owner, or its current route changed while it waited. The durable gate rechecks membership again
// immediately before delivery. Only notification timing changes; remedy and retry tools remain independent.
func (s *Service) revalidateFailedStartDeferrals(ctx context.Context) {
	b := &s.supervise
	b.mu.Lock()
	var list []superviseSignal
	for _, g := range b.pending {
		if g.Kind == sigFailed && !g.NotBefore.IsZero() {
			list = append(list, g)
		}
	}
	b.mu.Unlock()
	if len(list) == 0 {
		return
	}
	cfg := s.settings(ctx)
	changed := false
	for _, g := range list {
		to := s.digestTarget(ctx, cfg, g)
		_, eligible := s.failedStartBatchKey(ctx, to, g)
		if eligible && to == g.Recipient {
			continue
		}
		b.mu.Lock()
		if cur, ok := b.pending[g.id()]; ok && cur.Key == g.Key && cur.NotBefore.Equal(g.NotBefore) {
			cur.NotBefore = time.Time{}
			b.pending[g.id()] = cur
			changed = true
		}
		b.mu.Unlock()
	}
	if changed {
		b.mu.Lock()
		s.scheduleDigestLocked(time.Now())
		b.mu.Unlock()
	}
}
