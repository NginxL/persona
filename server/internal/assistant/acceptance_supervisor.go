package assistant

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"
)

// supervisorVerified says supervised task t was closed accepted with its evidence checked, and the owner was not asked
// to look themselves: its supervisor (the main conversation or a leader) did so with task_review accepted and no
// user_check, or the done judge's spot check did (judgeClose, #3356) on anything but a big feature (high priority,
// #3205), which the owner tries. What it asked the owner is then passed for them, not left for them (#3240). A task with
// no supervisor still asks; so does a big feature's "Please verify" (verifyBySupervisor).
func supervisorVerified(t Todo) bool {
	v := t.Supervision
	if v == nil || v.Supervisor == "" || v.state(t) != reviewAccepted || len(v.Reviews) == 0 {
		return false
	}
	r := v.Reviews[len(v.Reviews)-1]
	if r.Verdict != reviewAccepted || r.UserCheck {
		return false
	}
	return r.By == "" || r.By == reviewerJudge && t.Priority != "high"
}

// verifyBySupervisor passes task id's "Please verify" for its supervisor, as the owner's "Pass" would (the task is
// verified, its cards say who passed it), and so a done or cannot conclusion its owner was to acknowledge; false when
// there is none waiting (a decision, a claim still going out, one passed already or from before the latest reopen) or
// it is a big feature's "Please verify", which the owner tries themselves (high priority, ownerVerifies, #3205).
// errAcceptStore: the asks could not be read or saved.
func (s *Service) verifyBySupervisor(ctx context.Context, id string) (bool, error) {
	t, todoOK := s.todoNow(id)
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return false, errAcceptStore
	}
	a, ok := b.asks[id]
	if !ok || decideAsk(a) || a.AskedAt.IsZero() || !a.AcceptedAt.IsZero() || todoOK && acceptAskStale(a, t) ||
		!a.Conclusion && t.Priority == "high" {
		b.mu.Unlock()
		return false, nil
	}
	a.AcceptedAt, a.BySupervisor = time.Now().UTC(), true
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		a.AcceptedAt, a.BySupervisor = time.Time{}, false
		b.mu.Unlock()
		return false, fmt.Errorf("%w: %w", errAcceptStore, err)
	}
	cp := *a
	b.mu.Unlock()
	s.askChanged(ctx)
	if cp.Conclusion {
		s.updateAskCards(ctx, id, cp) // "verified" is for deployed work: a conclusion's card only says it is checked
	} else {
		s.taskVerified(ctx, id, cp)
	}
	return true, nil
}

// verifySupervisedAsks passes every "Please verify" or done conclusion still waiting whose task its supervisor accepted
// (supervisorVerified). It finishes persisted owner Passes and restores missing personal verification asks.
func (s *Service) verifySupervisedAsks(ctx context.Context) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("acceptance: supervisor verify: %v", err)
		return
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	ids := make(map[string]struct{})
	for id, a := range b.asks {
		if !decideAsk(a) && !a.AskedAt.IsZero() {
			ids[id] = struct{}{}
		}
	}
	b.mu.Unlock()
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if t.DoneAt == nil && t.Supervision.state(*t) == reviewUserCheck {
			ids[id] = struct{}{}
		}
	}
	s.todos.mu.RUnlock()
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		s.verifySupervisedAsk(ctx, id)
	}
}

func (s *Service) verifySupervisedAsk(ctx context.Context, id string) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	t, ok := s.todoNow(id)
	if !ok {
		return
	}
	if a, ok := s.askCopy(id); ok && ownerPassPending(&a, t) {
		if _, err := s.acceptTryAsDismissalLocked(ctx, id, "", a.AskedAt, false); err != nil {
			s.logf("task %s: recover owner Pass: %v", id, err)
		}
		return
	}
	if t.Supervision != nil && t.Supervision.State == reviewUserCheck {
		if t.DoneAt == nil && userCheckAsked(t) {
			s.restoreUserCheckAsk(ctx, t)
		}
		return
	}
	if supervisorVerified(t) {
		if ok, err := s.verifyBySupervisor(ctx, id); err != nil {
			s.logf("task %s: supervisor verify: %v", id, err)
		} else if ok {
			s.logf("task %s: its \"Please verify\" passed by its supervisor's acceptance", id)
		}
	}
}

// restoreUserCheckAsk uses the stored review to recover a missing or unusable Pass while its task's action is locked.
func (s *Service) restoreUserCheckAsk(ctx context.Context, t Todo) {
	id := t.ID
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	old := b.asks[id]
	if old != nil && (old.AskedAt.IsZero() ||
		(!old.Conclusion && !decideAsk(old) && !old.ByAssistant && !old.BySupervisor &&
			old.AcceptedAt.IsZero() && !acceptAskStale(old, t))) {
		b.mu.Unlock()
		return
	}
	// Use the durable review's time so a retry belongs to that round, even if work reopens meanwhile.
	review := t.Supervision.Reviews[len(t.Supervision.Reviews)-1]
	ag := lastAgent(t)
	a := &acceptAsk{AskedAt: review.At, MachineID: ag.MachineID, SessionID: ag.SessionID, Changed: clip(oneLine(t.Title), 40)}
	if old != nil {
		a.Earlier = lastOf(slices.Concat(old.Earlier, old.Messages), acceptEarlierMost)
	}
	b.asks[id] = a
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		if old == nil {
			delete(b.asks, id)
		} else {
			b.asks[id] = old
		}
		b.mu.Unlock()
		s.logf("task %s: restore personal verification: %v", id, err)
		return
	}
	b.mu.Unlock()
	s.askChanged(ctx)
}
