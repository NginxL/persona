package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A supervised task judged finished waits in pending_review for its supervisor's review, and a supervisor's review is
// not the owner's: it never enters the owner's Needs you by itself (docs/assistant/unified-entry.md "Needs you and In
// progress"). When such a review has sat past the settle time with nothing the owner can answer — no needs_user
// conclusion, no accepted user_check, no ask_user card, no pending worker interaction — it can hide indefinitely
// (#12648). The watch catches that state here once per review round and hands the task back to its supervisor. It
// never synthesizes a question from chat text and cannot tell an owner wait from a late review, so the rendered
// message names the whole review vocabulary; the supervisor picks the verdict the work actually needs.

// sigCardlessWait: a pending_review task has stayed with no card the owner can answer past the settle time.
const sigCardlessWait = "cardless_wait"

// cardlessSettle is how long a card-less review waits before the supervisor is told, matching decisionSettle: a card
// being written or an answer on its way is not mistaken for a silent wait.
const cardlessSettle = 10 * time.Minute

// cardlessSince is when the current pending_review round began: the ledger's PendingSince, else its last write.
func cardlessSince(t Todo) time.Time {
	v := t.Supervision
	if v == nil {
		return time.Time{}
	}
	if v.PendingSince != nil {
		return *v.PendingSince
	}
	return v.UpdatedAt
}

// cardlessWorkersStopped says none of the task's active workers still works or holds a card the owner can answer.
// It judges only the sessions it is given; callers must first prove those sessions are complete with
// cardlessAgentsReachable, because taskAgentsIn drops a machine whose session list cannot be read.
func cardlessWorkersStopped(agents []proto.Session) bool {
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return false // a real pending card stands for the owner
		}
		if c.State == proto.StateWorking || c.Starting {
			return false // the work goes on; this is not a settled review wait
		}
	}
	return true
}

// cardlessAgentsReachable reports that every machine hosting one of t's active agents answered its session list this
// pass. taskAgentsIn records a failed read in lists but drops that machine's sessions from the slice, so without this
// check a transient device/RPC failure would look like every worker having stopped and spend the round's one
// hand-back (and its CardlessToldAt stamp) on an unknown state. A failed pass simply retries on a later watch tick.
func cardlessAgentsReachable(t Todo, lists machineSessions) bool {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if l, ok := lists[a.MachineID]; ok && l.err != nil {
			return false
		}
	}
	return true
}

// cardlessRoundKey is stable across Postgres reloads, which retain microseconds rather than nanoseconds. Legacy
// reviews have no PendingSince: their UpdatedAt changes when stamped, so retain both the original time and the
// exact prospective stamp in the pending claim instead of changing the timestamp the UI uses.
func cardlessRoundKey(t Todo, now time.Time) string {
	since := cardlessSince(t).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	if t.Supervision.PendingSince == nil {
		return "cardless-legacy@" + since + "@" + now.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return "cardless@" + since
}

func legacyCardlessClaim(g superviseSignal) (since, stamp time.Time, ok bool) {
	raw, legacy := strings.CutPrefix(g.Key, "cardless-legacy@")
	if !legacy {
		return
	}
	before, after, separated := strings.Cut(raw, "@")
	since, firstErr := time.Parse(time.RFC3339Nano, before)
	stamp, secondErr := time.Parse(time.RFC3339Nano, after)
	return since, stamp, separated && firstErr == nil && secondErr == nil && !stamp.Before(since)
}

func cardlessClaimMatches(t Todo, g superviseSignal) bool {
	if t.Supervision == nil || t.DoneAt != nil || t.Supervision.state(t) != reviewPending {
		return false
	}
	if strings.HasPrefix(g.Key, "cardless-legacy@") {
		since, stamp, ok := legacyCardlessClaim(g)
		if !ok || t.Supervision.PendingSince != nil {
			return false
		}
		if told := t.Supervision.CardlessToldAt; told != nil {
			return told.Truncate(time.Microsecond).Equal(stamp.Truncate(time.Microsecond))
		}
		return since.Truncate(time.Microsecond).Equal(t.Supervision.UpdatedAt.Truncate(time.Microsecond))
	}
	// Also accept an older queued key's nanoseconds, without weakening the review-round check.
	at, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(g.Key, "cardless@"))
	if err != nil || !strings.HasPrefix(g.Key, "cardless@") {
		return false
	}
	if t.Supervision.PendingSince == nil && t.Supervision.CardlessToldAt != nil {
		// A pending signal from before recoverable claims used the round's pre-stamp UpdatedAt. Keep its existing
		// marker semantics only for that legacy round; every subsequent round has a non-nil PendingSince.
		return !at.After(*t.Supervision.CardlessToldAt)
	}
	return at.Truncate(time.Microsecond).Equal(cardlessSince(t).Truncate(time.Microsecond))
}

// signalCardlessWait persists the pending hand-back before acknowledging its round. A crash or failed marker write
// leaves the outbox able to finish the claim; an old completed stamp without a pending entry is never reconstructed.
func (s *Service) signalCardlessWait(ctx context.Context, t Todo, agents []proto.Session, lists machineSessions, now time.Time) {
	v := t.Supervision
	if ctx.Err() != nil || v == nil || t.DoneAt != nil || v.state(t) != reviewPending || v.CardlessToldAt != nil {
		return
	}
	since := cardlessSince(t)
	if since.IsZero() || now.Sub(since) < cardlessSettle {
		return
	}
	// An acceptance/conclusion ask from the current review round that the owner has not answered already gives them
	// something to answer. acceptAskStale covers asks of an earlier round (a reopened or re-reviewed task); the ended
	// test (acceptAskEnded) is deliberately not used: a decision ask written while the task still waits for review is
	// a live card even though its ledger is not needs_user yet.
	if a, readable, ok := s.askCopyReadable(t.ID); !readable {
		// The book could not be read: "no card found" is not "no card exists". Hold off without stamping, so a later
		// pass retries once the ledger recovers instead of spending the round's one hand-back on an unknown state.
		return
	} else if ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero() {
		return
	}
	// An unreadable worker is not a stopped worker: hold the hand-back until its machine answers, rather than stamp
	// the round on an unknown state.
	if !cardlessAgentsReachable(t, lists) {
		return
	}
	if !cardlessWorkersStopped(agents) {
		return
	}
	// Match signalReplacing's stop gate. Restore before taking writeMu: restoration can acquire dedupMu, while the
	// digest holds dedupMu when it completes a recovered claim under writeMu.
	if s.reporterStopped(ctx, t.Origin) {
		return
	}
	s.restoreSupervisePending(ctx)
	g := superviseSignal{Todo: t.ID, Kind: sigCardlessWait, Key: cardlessRoundKey(t, now), Minutes: int(now.Sub(since).Minutes())}
	s.supervise.writeMu.Lock()
	defer s.supervise.writeMu.Unlock()
	current, ok := s.todoNow(t.ID)
	if ctx.Err() != nil || !ok || !cardlessClaimMatches(current, g) || current.Supervision.CardlessToldAt != nil {
		return // another watch/digest or review changed the round after this watch's snapshot
	}
	// A failed or ambiguously committed marker write retries the same claim, including its prospective legacy
	// stamp. Replacing that identity could strand an earlier committed stamp with a different pending signal.
	r := &s.relays
	r.mu.Lock()
	if s.relayLocked(ctx) {
		if pending, exists := r.st.SupervisePending[g.id()]; exists && cardlessClaimMatches(current, pending) {
			g.Key = pending.Key
		}
	}
	r.mu.Unlock()
	s.queueSignal(g)
	if ctx.Err() != nil || !s.supervisePendingDurably([]superviseSignal{g}) {
		return // no stamp until the pending signal has survived its durable write
	}
	if _, _, err := s.updateSupervisionLockedAt(ctx, t.ID, time.Now().UTC(), func(v *Supervision) bool {
		stamped := now.UTC()
		if _, claimStamp, legacy := legacyCardlessClaim(g); legacy {
			stamped = claimStamp
		}
		v.CardlessToldAt = &stamped
		return true
	}); err != nil {
		s.logf("task %s: card-less claim: %v (retried from the pending signal)", t.ID, err)
	}
}

// completeCardlessClaim finishes only an existing pending claim. It never guesses whether an old marker without
// an outbox entry was delivered. False/retry keeps transient failures pending instead of acknowledging them as moot.
func (s *Service) completeCardlessClaim(ctx context.Context, g superviseSignal) (valid, retry bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || !cardlessClaimMatches(t, g) {
		return false, false
	}
	if t.Supervision.CardlessToldAt != nil {
		return true, false
	}
	if ctx.Err() != nil || !s.supervisePendingDurably([]superviseSignal{g}) {
		return false, true
	}
	if s.reporterStopped(ctx, t.Origin) {
		return false, true
	}
	if a, ok := s.askCopy(t.ID); ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero() {
		return false, false
	}
	lists := machineSessions{}
	agents := s.taskAgentsIn(ctx, t, lists)
	if !cardlessAgentsReachable(t, lists) {
		return false, true
	}
	if !cardlessWorkersStopped(agents) {
		return false, false
	}
	s.supervise.writeMu.Lock()
	defer s.supervise.writeMu.Unlock()
	current, ok := s.todoNow(t.ID)
	if !ok || !cardlessClaimMatches(current, g) {
		return false, false
	}
	if ctx.Err() != nil {
		return false, true
	}
	// The worker read above can yield while another watch replaces or retires the pending claim. Never stamp the
	// earlier snapshot after that write; restoreDigest keeps a newer outbox member when this old attempt retries.
	if !s.supervisePendingDurably([]superviseSignal{g}) {
		return false, true
	}
	v, _, err := s.updateSupervisionLockedAt(ctx, t.ID, time.Now().UTC(), func(v *Supervision) bool {
		if v.CardlessToldAt != nil {
			return false
		}
		stamped := time.Now().UTC()
		if _, claimStamp, legacy := legacyCardlessClaim(g); legacy {
			stamped = claimStamp
		}
		v.CardlessToldAt = &stamped
		return true
	})
	if err != nil {
		return false, true
	}
	return v.CardlessToldAt != nil, false
}
