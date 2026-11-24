package assistant

import (
	"context"
	"fmt"
	"time"
)

// A scope or estimate change must not push a finished, card-less review further out of sight (#12648). A supervised
// task judged finished waits in pending_review for its supervisor; when no card stands for the owner — no needs_user
// conclusion, no user_check ask, no pending worker interaction — extending the estimate or the scope with task_scope
// would hide the stalled review (the estimate_due snooze). The change is refused until the supervisor gives a review.
//
// It only fires once the review has sat past the same settle window the watch's card-less backstop uses
// (decisionSettle): inside it a card may still be on its way. A parked task is out of scope (its owner-action
// admission and legacy reconciliation live in parked.go), as is needs_user, whose own backstop is undecidableSignal.

// The hub cannot tell an owner wait from a review the supervisor is merely late on: both look like a finished task in
// pending_review with no owner card. So the refusal states only what is known (a finished review has waited past the
// window with no card) and names the whole review vocabulary, instead of implying the owner was asked anything:
// accepted/rework/cancelled when no owner input is needed, needs_user or accepted+user_check when the owner truly must
// act (#12648 review).
func (s *Service) cardlessOwnerWaitGuard(ctx context.Context, t Todo) error {
	v := t.Supervision
	if v == nil || t.DoneAt != nil || v.state(t) != reviewPending {
		return nil
	}
	since := v.UpdatedAt
	if v.PendingSince != nil {
		since = *v.PendingSince
	}
	waited := time.Now().UTC().Sub(since)
	if waited < decisionSettle {
		return nil
	}
	if a, ok := s.askCopy(t.ID); ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero() {
		return nil // an open ask already gives the owner something to answer
	}
	for _, c := range s.taskAgents(ctx, t) {
		if c.State == "needs_you" || c.PendingInteractions > 0 {
			return nil // a real pending card stands for the owner
		}
		if c.State == "working" || c.Starting {
			return nil // the work goes on; this is not a settled review wait (matches cardlessWorkersStopped)
		}
	}
	return fmt.Errorf("task %s has been waiting for review for %d minutes since its work finished, and the owner has no card to answer. "+
		"Give task_review first: accepted, rework or cancelled if no owner input is needed; "+
		"needs_user with why_owner, reason, recommend, options and cost_of_waiting, or accepted with user_check: true, "+
		"if the owner really must decide or check. A chat-only request never reaches Needs you. Call task_scope again after the review",
		t.ID, int(waited.Minutes()))
}
