package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const requestGateKey = "request_prerequisites"

var errRequestGateRoot = errors.New("require_accepted_after can only be enabled on a request's root to-do")

// planRequestGate finds the request for a task's existing or proposed edges, including deleted enforced roots.
func (s *Service) planRequestGate(task PlanTask) string {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	book := s.todos.todos
	var root string
	var after []string
	if todo := book[firstNonEmpty(task.TodoID, task.ReuseTodo)]; todo != nil {
		if len(todo.After) != 0 && s.hasRequestGateLocked(todo.PartOf) {
			return todo.PartOf
		}
		root, after = todo.PartOf, todo.After
	}
	if task.PartOf != "" {
		if task.PartOf != root {
			after = nil
		}
		root = task.PartOf
	}
	if len(task.After) != 0 {
		after = task.After
	}
	if len(after) != 0 && s.hasRequestGateLocked(root) {
		return root
	}
	return ""
}

func (s *Service) hasRequestGateLocked(rootID string) bool {
	root := s.todos.todos[rootID]
	return root != nil && root.RequireAcceptedAfter || root == nil && s.todos.deletedRequestGates[rootID]
}

func (s *Service) hasRequestGate(rootID string) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	return s.hasRequestGateLocked(rootID)
}

// requestBlockers reads the live graph, not a plan's stale copy or the client projection of cancelled to-dos.
func (s *Service) requestBlockers(ctx context.Context, task PlanTask) []string {
	s.todos.mu.RLock()
	book := s.todos.todos
	parent, piece := book[task.RequestGate], book[task.TodoID]
	if piece != nil && piece.PartOf == task.RequestGate && len(piece.After) == 0 && len(task.After) == 0 {
		s.todos.mu.RUnlock()
		return nil
	}
	if parent == nil || parent.Cancelled() || parent.PartOf != "" || piece == nil || piece.PartOf != task.RequestGate {
		s.todos.mu.RUnlock()
		return []string{task.RequestGate}
	}
	var blockers []string
	var ownerChecks []Todo
	// Later edits may add prerequisites. Removing an approved edge cannot waive its review.
	for _, id := range cleanIDs(slices.Concat(task.After, piece.After)) {
		dep := book[id]
		if dep == nil || dep.PartOf != task.RequestGate || dep.DoneAt == nil || dep.Cancelled() || dep.StartError != "" ||
			dep.Supervision.state(*dep) != reviewAccepted || len(dep.Supervision.Reviews) == 0 {
			blockers = append(blockers, id)
			continue
		}
		review := dep.Supervision.Reviews[len(dep.Supervision.Reviews)-1]
		if review.Verdict != reviewAccepted || dep.DoneBy != db.DoneByReview {
			blockers = append(blockers, id)
		} else if review.UserCheck {
			ownerChecks = append(ownerChecks, Todo{ID: id, DoneAt: dep.DoneAt, ReopenedAt: dep.ReopenedAt})
		}
	}
	s.todos.mu.RUnlock()
	if len(ownerChecks) > 0 {
		s.accept.mu.Lock()
		defer s.accept.mu.Unlock()
		loaded := s.loadAcceptLocked(ctx)
		for _, dep := range ownerChecks {
			ask := s.accept.asks[dep.ID]
			if !loaded || ask == nil || ask.Conclusion || decideAsk(ask) || ask.ByAssistant || ask.BySupervisor || ask.ByDefault ||
				ask.AskedAt.IsZero() || acceptAskStale(ask, dep) || ask.AcceptedAt.Before(*dep.DoneAt) || ask.AcceptedAt.Before(ask.AskedAt) {
				blockers = append(blockers, dep.ID)
			}
		}
	}
	return blockers
}

func (s *Service) prerequisiteQueue(ctx context.Context, task PlanTask, blockers []string) *TaskQueue {
	now := time.Now().UTC()
	queue := &TaskQueue{Key: requestGateKey, QueuedAt: now, NextAt: now, Blockers: blockers}
	if task.Queue != nil && task.Queue.Key == requestGateKey {
		queue.QueuedAt, queue.NextAt = task.Queue.QueuedAt, task.Queue.NextAt
	}
	if len(blockers) > 0 {
		queue.Reason = i18n.T(s.lang(ctx), "assistant-tasks.request_waiting", i18n.Args{"pieces": strings.Join(blockers, ", ")})
	} else {
		queue.Reason = i18n.T(s.lang(ctx), "assistant-tasks.request_ready")
	}
	return queue
}

func (s *Service) requestStartPending(ctx context.Context, todo Todo) bool {
	plan, index, err := s.currentPlanAssignment(ctx, todo)
	return err == nil && plan.Tasks[index].RequestGate != "" && plan.Tasks[index].Queue != nil
}

type requestBlocked struct{ ids []string }

func (*requestBlocked) Error() string { return "request prerequisites are not accepted" }

func (s *Service) checkRequestStart(ctx context.Context, task PlanTask) error {
	if task.RequestGate == "" {
		return nil
	}
	if todo, ok := s.todoNow(task.TodoID); !ok || todo.DoneAt != nil || todo.Cancelled() ||
		task.Queue != nil && todo.ReopenedAt != nil && todo.ReopenedAt.After(task.Queue.QueuedAt) {
		return &requestBlocked{[]string{task.TodoID}}
	}
	if blockers := s.requestBlockers(ctx, task); len(blockers) != 0 {
		return &requestBlocked{blockers}
	}
	return nil
}

func (s *Service) claimRequestStart(ctx context.Context, task *PlanTask) bool {
	if !task.OwnerStarted || task.Queue == nil || task.Queue.Claimed {
		return false
	}
	if err := s.checkRequestStart(ctx, *task); err != nil {
		blocked := err.(*requestBlocked)
		if !slices.Equal(task.Queue.Blockers, blocked.ids) || task.Queue.Key != requestGateKey {
			s.requeueRequestStart(ctx, *task, blocked.ids)
		}
		return false
	}
	claimed := *task.Queue
	claimed.Claimed, claimed.Blockers = true, nil
	claimed.Reason = i18n.T(s.lang(ctx), "assistant-tasks.request_unconfirmed")
	if !s.replaceRequestQueue(ctx, *task, &claimed) {
		return false
	}
	task.Queue = &claimed
	return true
}

func (s *Service) requeueRequestStart(ctx context.Context, task PlanTask, blockers []string) {
	s.replaceRequestQueue(ctx, task, s.prerequisiteQueue(ctx, task, blockers))
}

// replaceRequestQueue compares the exact queue entry, not only the plan state. The launch claim is committed
// before any remote mutations. An unanswered launch or a crash after the claim is never replayed automatically.
func (s *Service) replaceRequestQueue(ctx context.Context, task PlanTask, next *TaskQueue) bool {
	return s.replaceTaskQueue(ctx, task, next, queueOwnerStart)
}

type queueCASPolicy int

const (
	queueCAS queueCASPolicy = iota
	queueOwnerStart
	queueQuotaStart
)

func (s *Service) replaceTaskQueue(ctx context.Context, task PlanTask, next *TaskQueue, policy queueCASPolicy) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	book := &s.tasks
	book.planEdits.acquire(task.planID)
	defer book.planEdits.release(task.planID)
	index := task.planIndex
	plan, err := s.loadPlan(ctx, task.planID)
	if err != nil || plan.State != planStarted || index < 0 || index >= len(plan.Tasks) {
		return false
	}
	current := &plan.Tasks[index]
	expected, _ := json.Marshal(task.Queue)
	actual, _ := json.Marshal(current.Queue)
	if current.TodoID != task.TodoID || current.RequestGate != task.RequestGate || policy == queueOwnerStart && !current.OwnerStarted || string(expected) != string(actual) {
		return false
	}
	current.Queue = next
	var changed bool
	st := s.store()
	if policy == queueQuotaStart && st != nil {
		store, ok := st.(gatewayQueueStore)
		if !ok || task.Queue.Gateway == nil {
			return false
		}
		plan.UpdatedAt = time.Now().UTC()
		replacement, _ := json.Marshal(next)
		changed, err = store.ClaimUnstartedTaskQueue(ctx, plan.ID, index, task.TodoID,
			task.Queue.Gateway.Since, expected, replacement, plan.UpdatedAt)
	} else if store, ok := st.(interface {
		CompareTaskQueue(context.Context, string, int, string, json.RawMessage, json.RawMessage, time.Time) (bool, error)
	}); ok {
		plan.UpdatedAt = time.Now().UTC()
		replacement, _ := json.Marshal(next)
		changed, err = store.CompareTaskQueue(ctx, plan.ID, index, task.TodoID, expected, replacement, plan.UpdatedAt)
	} else {
		if policy == queueQuotaStart {
			ready, err := s.gatewayTaskReadyAt(ctx, task, task.Queue.Gateway.Since)
			if err != nil || !ready {
				return false
			}
		}
		changed, err = s.movePlan(ctx, plan, planStarted)
	}
	if err != nil {
		s.logf("task %s: compare task queue: %v", task.TodoID, err)
		return false
	}
	if changed {
		s.event(ctx, map[string]any{"type": "task_plan", "plan": s.planWithSpace(ctx, plan)})
	}
	return changed
}
