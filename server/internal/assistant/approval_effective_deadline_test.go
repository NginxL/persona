package assistant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// checkEffectiveDeadline checks both the snapshot a reconnecting client receives
// and the incremental card already published to connected clients.
func checkEffectiveDeadline(t *testing.T, ft *fleetTest, id string, want time.Time, minTells int) {
	t.Helper()
	pending := ft.s.pendingInteractions()
	if len(pending) != 1 || pending[0].ID != id {
		t.Errorf("current pending snapshot = %+v, want only %s", pending, id)
	} else if !pending[0].ExpiresAt.Equal(want) {
		t.Errorf("current pending snapshot expires_at = %v, want effective deadline %v", pending[0].ExpiresAt, want)
	}
	published, tells := toldCard(ft, id)
	if tells < minTells {
		t.Errorf("interaction %s was published %d times, want at least %d including its effective deadline", id, tells, minTells)
	}
	if !published.ExpiresAt.Equal(want) {
		t.Errorf("last published interaction expires_at = %v, want effective deadline %v", published.ExpiresAt, want)
	}
}

func TestClarifiedApprovalPublishesPausedAndResumedEffectiveDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldBudget := clarificationPauseBudget
		clarificationPauseBudget = 500 * time.Millisecond
		t.Cleanup(func() { clarificationPauseBudget = oldBudget })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_effective_pause_resume", 200*time.Millisecond,
			func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "setting applied", nil
			})
		original := ft.waitPending(1)[0].ExpiresAt
		elapse(50 * time.Millisecond)

		questionAt := time.Now()
		if err := ft.s.clarify(context.Background(), "ix_effective_pause_resume", "Why is this needed?", "owner-effective-1", nil); err != nil {
			t.Fatalf("clarify: %v", err)
		}
		if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
			t.Fatalf("original prepared call = %+v, %v, want clarification", got.out, got.err)
		}
		checkEffectiveDeadline(t, ft, "ix_effective_pause_resume", questionAt.Add(clarificationPauseBudget), 2)

		// The original decision deadline passes while the answer is outstanding.
		// The pause budget, not that original timestamp, is the client's deadline.
		elapse(250 * time.Millisecond)
		checkEffectiveDeadline(t, ft, "ix_effective_pause_resume", questionAt.Add(clarificationPauseBudget), 2)
		resumedAt := time.Now()
		ref := SessionRef{MachineID: "mac", SessionID: "s1"}
		ft.s.approvalAnswerTurnStarted(ref)
		ft.s.approvalAnswerTurnEnded(ref)
		remainingAtQuestion := original.Sub(questionAt)
		resumedDeadline := resumedAt.Add(remainingAtQuestion)
		checkEffectiveDeadline(t, ft, "ix_effective_pause_resume", resumedDeadline, 3)

		// The answer restored exactly the time left at the question, rather than
		// either expiring at the original deadline or starting a fresh window.
		elapse(remainingAtQuestion - time.Millisecond)
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("pending cards just before the resumed deadline = %d, want 1", got)
		}
		allow := true
		if err := ft.s.resolve("ix_effective_pause_resume", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("owner Allow after the original deadline but before the resumed deadline: %v", err)
		}
		if got := applies.Load(); got != 1 {
			t.Fatalf("owner Allow applied %d times, want exactly once", got)
		}
		ft.waitPending(0)
		if result := waitApprovalResult(t, ft, "ix_effective_pause_resume"); !strings.Contains(result, "setting applied") {
			t.Fatalf("terminal result = %q, want the applied result", result)
		}
	})
}

func TestClarifiedApprovalPublishesCumulativePauseDeadlineAndExpiresStaleCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldBudget := clarificationPauseBudget
		clarificationPauseBudget = 500 * time.Millisecond
		t.Cleanup(func() { clarificationPauseBudget = oldBudget })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_effective_repeated_questions", time.Hour,
			func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "unexpected apply", nil
			})
		if err := ft.s.clarify(context.Background(), "ix_effective_repeated_questions", "First question", "owner-effective-repeat-1", nil); err != nil {
			t.Fatalf("first clarify: %v", err)
		}
		awaitPreparedTest(t, done)
		firstPause := 150 * time.Millisecond
		elapse(firstPause)

		// Finish the first answer, then ask again in a later turn. The 150ms
		// already spent remains part of this card's cumulative pause budget.
		ref := SessionRef{MachineID: "mac", SessionID: "s1"}
		ft.s.approvalAnswerTurnStarted(ref)
		ft.s.approvalAnswerTurnEnded(ref)
		elapse(50 * time.Millisecond)
		secondQuestionAt := time.Now()
		if err := ft.s.clarify(context.Background(), "ix_effective_repeated_questions", "Second question", "owner-effective-repeat-2", nil); err != nil {
			t.Fatalf("second clarify: %v", err)
		}
		budgetDeadline := secondQuestionAt.Add(clarificationPauseBudget - firstPause)
		elapse(200 * time.Millisecond)
		if err := ft.s.clarify(context.Background(), "ix_effective_repeated_questions", "Follow-up while paused", "owner-effective-repeat-3", nil); err != nil {
			t.Fatalf("repeated clarify: %v", err)
		}
		// A repeated question in the same pause changes neither the pause start
		// nor the deadline. In particular, it must not receive another 500ms.
		checkEffectiveDeadline(t, ft, "ix_effective_repeated_questions", budgetDeadline, 4)

		// This is 400ms into the second pause and 550ms of cumulative paused
		// time, but only 200ms after the repeated question. The budget really
		// expires; a native client's late Allow cannot resurrect the card.
		elapse(200 * time.Millisecond)
		ft.waitPending(0)
		result := waitApprovalResult(t, ft, "ix_effective_repeated_questions")
		for _, want := range []string{"ix_effective_repeated_questions", "expired", "nothing was executed"} {
			if !strings.Contains(result, want) {
				t.Errorf("terminal expiry result = %q, want it to contain %q", result, want)
			}
		}
		allow := true
		if err := ft.s.resolve("ix_effective_repeated_questions", proto.Resolution{Allow: &allow}); !errors.Is(err, errNotPending) {
			t.Errorf("Allow after the cumulative pause deadline = %v, want errNotPending", err)
		}
		if got := applies.Load(); got != 0 {
			t.Errorf("expired or stale approval applied %d times, want zero", got)
		}
	})
}

func TestUnansweredApprovalPublishesWaitingWindowAsEffectiveDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_effective_unanswered", 100*time.Millisecond,
			func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "setting applied", nil
			})
		original := ft.waitPending(1)[0].ExpiresAt
		elapse(150 * time.Millisecond)
		if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
			t.Fatalf("original prepared call = %+v, %v, want unanswered waiting", got.out, got.err)
		}
		checkEffectiveDeadline(t, ft, "ix_effective_unanswered", original.Add(24*time.Hour), 2)

		allow := true
		if err := ft.s.resolve("ix_effective_unanswered", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("owner Allow during the published waiting window: %v", err)
		}
		if got := applies.Load(); got != 1 {
			t.Fatalf("waiting approval applied %d times, want exactly once", got)
		}
		ft.waitPending(0)
		waitApprovalResult(t, ft, "ix_effective_unanswered")
	})
}
