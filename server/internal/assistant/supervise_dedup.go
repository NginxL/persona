package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

const superviseDedupKey = "assistant.supervision_dedup"
const superviseDedupWindow = 30 * time.Minute

type superviseFingerprint struct {
	Hash      string    `json:"hash"`
	At        time.Time `json:"at"`
	Permanent bool      `json:"permanent,omitempty"`
	// Todo keeps inactivity and unhandled-event fingerprints until the task closes.
	// Other non-permanent kinds retain the short semantic-deduplication window.
	Todo string `json:"todo,omitempty"`
}

func digestFingerprint(e prompts.SuperviseEntry, g superviseSignal) (string, string) {
	if g.Kind == sigPRConflict || g.Kind == sigDraftPR {
		// Keep every task/PR/head independently, including a head that returns after another one.
		key, _ := json.Marshal([]string{g.Todo, g.Kind, g.Key})
		sum := sha256.Sum256(key)
		return string(key), hex.EncodeToString(sum[:])
	}
	if g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled" {
		// An unhandled source is not new when its recipient or last reply changes.
		key, _ := json.Marshal([]string{g.Todo, g.Kind, g.Key})
		sum := sha256.Sum256(key)
		return string(key), hex.EncodeToString(sum[:])
	}
	if g.externalPRReview() {
		// Keep the latest source wake per task, independent of its Agent's answer or a manager restart.
		key, _ := json.Marshal([]string{g.Todo, g.Kind, "external_pr"})
		sum := sha256.Sum256([]byte(g.Key))
		return string(key), hex.EncodeToString(sum[:])
	}
	// State timestamps and elapsed minutes deliberately do not participate: a new
	// turn or a periodic sweep is not new information when the last summary is unchanged.
	parts := []string{g.Todo, g.Machine, e.Machine, g.Session, e.AgentID, g.Kind, g.Report}
	if g.Kind == sigAllStopped {
		parts = []string{g.Todo, g.Kind} // one latest group key per task and destination, not per member
	}
	if g.To != "" { // a card's rung (ask_route.go): each conversation hears of it; the others' keys stay as they were
		parts = append(parts, g.To)
	}
	key, _ := json.Marshal(parts)
	text := e.Last
	if text == "" {
		text = g.Detail
	}
	fields := []any{strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(e.Ask), " "), e.Said, e.Evidence, e.Step, e.Steps}
	if g.Kind == sigDecide {
		fields = append(fields, strings.Join(strings.Fields(e.Recommend), " "))
	}
	if len(e.Asks) > 0 { // the cards it lists are what is new about it
		fields = append(fields, e.Asks)
	}
	if keyIsNews(g.Kind) {
		fields = append(fields, g.Key)
	}
	if g.Kind == sigAllStopped {
		fields = []any{g.Key} // reworded triage on the same stopped turn is not a new period of inactivity
	}
	payload, _ := json.Marshal(fields)
	sum := sha256.Sum256(payload)
	return string(key), hex.EncodeToString(sum[:])
}

// keyIsNews: a signal of kind whose key names one event that is not in its agent's words, so a new key is news
// whatever the agent said last: each park's time, each decision that ended a park, each new Inbox message, each
// answer the owner gives a decision's round, each recheck or undecidable review of a decision, each start attempt's
// failure or start (#6027). The other kinds' keys move with every turn or sweep, which is not news while the agent's
// words stay the same.
func keyIsNews(kind string) bool {
	switch kind {
	case sigParkedDue, sigParkDecided, sigInbox, sigOwnerCannot, sigRecheck, sigUndecidable, sigFailed, sigStarted, sigAllStopped, sigDecisionReply, sigHandoffResult, sigEstimateDue,
		sigCheck: // a check's run, or the owner's answer to its decision: an Agent that only runs tools says the same all along
		return true
	}
	return false
}

type supervisionDelivery int

const (
	digestDelivery supervisionDelivery = iota
	leaderDelivery
	leaderResumeDelivery
)

// reserveSupervision checkpoints deduplication before dispatch. A failed dispatch
// and queue admission releases the reservation. dedupMu is held by sendDigest.
func (s *Service) reserveSupervision(ctx context.Context, entries []prompts.SuperviseEntry, signals []superviseSignal, now time.Time) ([]prompts.SuperviseEntry, []superviseSignal, func(), error) {
	return s.reserveSupervisionDelivery(ctx, entries, signals, now, digestDelivery)
}

// Offline resumes take minutes. Keep their reservations in memory until the send
// is confirmed so recovery never mistakes an in-flight resume for delivery.
func (s *Service) reserveSupervisionDelivery(ctx context.Context, entries []prompts.SuperviseEntry, signals []superviseSignal, now time.Time, delivery supervisionDelivery) ([]prompts.SuperviseEntry, []superviseSignal, func(), error) {
	b := &s.supervise
	history := b.dedup
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil {
			return nil, signals, nil, err
		}
	}
	if history == nil {
		history = map[string]superviseFingerprint{}
	}
	previous := maps.Clone(history)
	next := maps.Clone(history)
	for key, seen := range next {
		if !seen.Permanent && seen.Todo == "" && now.Sub(seen.At) >= superviseDedupWindow {
			delete(next, key)
		}
	}
	var out []prompts.SuperviseEntry
	var kept []superviseSignal
	var pending [][2]string
	resuming := delivery == leaderResumeDelivery
	for i, entry := range entries {
		key, hash := digestFingerprint(entry, signals[i])
		if seen, ok := next[key]; ok && seen.Hash == hash && (seen.Permanent || seen.Todo != "" || now.Sub(seen.At) < superviseDedupWindow) {
			continue
		}
		id := [2]string{key, hash}
		if _, ok := b.resuming[id]; ok && delivery != digestDelivery {
			continue
		}
		if resuming {
			if b.resuming == nil {
				b.resuming = make(map[[2]string]struct{})
			}
			b.resuming[id] = struct{}{}
			pending = append(pending, id)
		}
		fingerprint := superviseFingerprint{Hash: hash, At: now.UTC(), Permanent: signals[i].Kind == sigPRConflict || signals[i].Kind == sigDraftPR}
		if signals[i].Kind == sigAllStopped {
			fingerprint.Todo = signals[i].Todo
		}
		if g := signals[i]; g.externalPRReview() || g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled" {
			fingerprint.Todo = g.Todo
		}
		next[key] = fingerprint
		out, kept = append(out, entry), append(kept, signals[i])
	}
	if resuming {
		return out, kept, func() {
			for _, id := range pending {
				delete(b.resuming, id)
			}
		}, nil
	}
	if len(out) == 0 {
		return nil, nil, func() {}, nil
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, superviseDedupKey, next); err != nil {
			return nil, signals, nil, err
		}
	}
	b.dedup = next
	return out, kept, func() {
		b.dedup = previous
		if st := s.store(); st != nil {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := st.PutSetting(cctx, superviseDedupKey, previous); err != nil {
				s.logf("supervise: could not release deduplication reservation: %v", err)
			}
		}
	}, nil
}

// forgetSupervision takes the signals' fingerprints out of the deduplication history: their message was queued for a
// conversation that is gone and was never read, so they are told again. dedupMu is held.
func (s *Service) forgetSupervision(ctx context.Context, signals []superviseSignal, web string) {
	b := &s.supervise
	history := b.dedup
	st := s.store()
	if st != nil {
		if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil {
			s.logf("supervise: could not read the deduplication history: %v", err)
			return
		}
	}
	next := maps.Clone(history)
	for _, g := range signals {
		if g.Kind == sigAllStopped {
			key, _ := digestFingerprint(prompts.SuperviseEntry{}, g)
			delete(next, key) // a lost queue releases its key even while the participants cannot be read
			continue
		}
		if e, ok := s.digestEntry(ctx, g, web); ok {
			key, _ := digestFingerprint(e, g)
			delete(next, key)
		}
	}
	if len(next) == len(history) {
		return
	}
	if st != nil {
		if err := st.PutSetting(ctx, superviseDedupKey, next); err != nil {
			s.logf("supervise: could not release deduplication entries: %v", err)
			return
		}
	}
	b.dedup = next
}

// Task-bound fingerprints are bounded by open tasks, not elapsed time. Load once
// after restart; reservations keep the same cache current under dedupMu.
func (s *Service) pruneAllStoppedDedup(ctx context.Context) {
	b := &s.supervise
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	if b.dedup == nil {
		history := map[string]superviseFingerprint{}
		if st := s.store(); st != nil {
			if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil {
				s.logf("supervise: cannot load inactivity fingerprints: %v", err)
				return
			}
		}
		b.dedup = history
	}
	var remove []string
	s.todos.mu.RLock()
	for key, seen := range b.dedup {
		if task := s.todos.todos[seen.Todo]; seen.Todo != "" && s.todos.loaded && (task == nil || task.DoneAt != nil) {
			remove = append(remove, key)
		}
	}
	s.todos.mu.RUnlock()
	if len(remove) == 0 {
		return
	}
	next := maps.Clone(b.dedup)
	for _, key := range remove {
		delete(next, key)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, superviseDedupKey, next); err != nil {
			s.logf("supervise: cannot prune inactivity fingerprints: %v", err)
			return
		}
	}
	b.dedup = next
}

func (s *Service) restoreDigest(signals []superviseSignal, at, prev time.Time) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.Equal(at) {
		b.last = prev
	}
	for _, g := range signals {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if cur, newer := b.pending[g.id()]; !newer {
			b.pending[g.id()] = g
		} else if g.Kind == sigInbox && cur.Key != g.Key {
			b.pending[g.id()] = joinInbox(g, cur) // the newer item, with the messages of both
		}
	}
	s.retryDigestLocked(s.superviseTimings().urgent)
}
