package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// scopeArgs is a task_scope call that only moves the estimate, the estimate_due response the guard must catch.
func scopeArgs(todo string) map[string]any {
	return map[string]any{"todo_id": todo, "change": "give the verification another hour",
		"why": "the estimate ran out before the owner looked", "expect_minutes": 90}
}

// agePendingReview puts the task's current pending_review round past the card-less settle window.
func agePendingReview(t *testing.T, s *Service, id string) {
	t.Helper()
	old := time.Now().Add(-(decisionSettle + time.Minute)).UTC()
	if _, _, err := s.updateSupervision(context.Background(), id, func(v *Supervision) bool {
		v.PendingSince = &old
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func briefLogLen(s *Service, id string) int {
	brief, ok := s.briefOfTodo(id)
	if !ok {
		return 0
	}
	return len(brief.Log)
}

// A card-less pending_review task cannot hide an owner wait behind an estimate extension or scope change: the call is
// refused before the brief is written and names the card to open (#12648).
func TestTaskScopeRefusedOnACardlessOwnerWait(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "做完了，你看下要不要这样发布。")
	agePendingReview(t, s, todo.ID)
	before := briefLogLen(s, todo.ID)
	res, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID))
	if err == nil {
		t.Fatalf("the estimate extension was not refused: %s", res)
	}
	for _, want := range []string{"no card to answer", "accepted", "rework", "cancelled", "needs_user", "user_check"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
	if got := briefLogLen(s, todo.ID); got != before {
		t.Fatalf("the brief was written despite the refusal: %d -> %d entries", before, got)
	}
}

// Inside the settle window the card may still be on its way, so a scope change goes through.
func TestTaskScopeAllowedInsideTheSettleWindow(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "做完了。")
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a scope change inside the settle window was refused: %v", err)
	}
}

// A worker that already holds a pending card gives the owner something to answer, so the scope change is allowed.
func TestTaskScopeAllowedWhenAWorkerCardIsPending(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "做完了。")
	agePendingReview(t, s, todo.ID)
	agentTurn(s, f, proto.StatusBlocked, "选哪个方案？", 1)
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a task with a pending worker card was refused: %v", err)
	}
}

// A worker whose session is already working again (the list changed before the ledger's supervisedWorking ran, the
// snapshot window the watch's cardlessWorkersStopped guards) means this is not a settled review wait: allow it.
func TestTaskScopeAllowedWhileAWorkerStillWorks(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "做完了。")
	agePendingReview(t, s, todo.ID)
	// Set the session directly: taskWorking would move the ledger out of pending_review before the guard sees it.
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a scope change while a worker still works was refused: %v", err)
	}
}

// An open conclusion/acceptance ask is a card too.
func TestTaskScopeAllowedWithAnOpenAsk(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "做完了。")
	agePendingReview(t, s, todo.ID)
	s.askConclusion(context.Background(), todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Reason: "要不要发布", Recommend: "发布"})
	if askOf(s, todo.ID) == nil {
		t.Fatal("the conclusion ask was not kept")
	}
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a task with an open ask was refused: %v", err)
	}
}

// Only pending_review is guarded: a task still going on (no review yet) and one parked on an external wait re-scope
// as before; parking's owner-action rules stay owned by its own admission.
func TestTaskScopeGuardLeavesOtherStatesAlone(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	// Still going on: the judge has not put it up for review.
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a going-on task was refused: %v", err)
	}
	waitsForReview(t, s, f, todo.ID, "做完了。")
	agePendingReview(t, s, todo.ID)
	parkFor(t, s, todo.ID, "Wait for the upstream release", parkExternalEvent, "3d")
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("the park did not take: %+v", cur.Supervision)
	}
	if _, err := runJSON(t, s, "conv1", "task_scope", scopeArgs(todo.ID)); err != nil {
		t.Fatalf("a parked task's scope change was refused: %v", err)
	}
}
