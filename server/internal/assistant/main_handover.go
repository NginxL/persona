package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/db"
)

// Replacing the main conversation (#4866, docs/assistant/assistant.md "New main chat"): the open tasks of the main
// conversation that was put away (POST /api/v1/assistant/main/new) or replaced in the settings go to the main one now.
// Their supervisor and their origin move together, as a closed leader's workers are rehomed (task_leader.go): every
// check that compares the supervisor with a conversation (agent_send's provenance, the done judge's, agent_supervise,
// task_review) then counts the new main conversation as the one that handed the task out, and the task's receipts,
// reviews and reports go to its feed instead of an archived one nobody opens. The ledger is kept as it is (criteria,
// reviews, round, parking, waits on the owner); a task waiting for review is signalled again to the new main
// conversation. Only a previously assigned legacy background notice may still finish in the old background conversation;
// no new background route is created (#5732). The card of a task the new main supervises is put there (the old
// conversation keeps its history). A task a leader supervises keeps its leader; closed tasks and the owner's own
// to-dos are not touched.

// handOverMainTasks moves the open tasks of conversation from, a main conversation that was replaced, to conversation
// to, the main one now; it answers how many moved. Nothing moves when from is a leader now (it supervises its tasks).
func (s *Service) handOverMainTasks(ctx context.Context, from, to string) int {
	if from == "" || to == "" || from == to || s.isLeaderRef(from) || s.isLeader(ctx, from) {
		return 0
	}
	moved := 0
	for _, t := range s.todosOf(ctx, "*") {
		if t.DoneAt != nil || t.Cancelled() {
			continue
		}
		heldBy := func(v *Supervision) bool { return v.Supervisor == from || v.Supervisor == "" && t.Origin == from }
		ledger := t.Supervision != nil && heldBy(t.Supervision)
		if t.Origin != from && !ledger {
			continue
		}
		if s.handOverTask(ctx, t, from, to, heldBy) {
			moved++
		}
	}
	if moved > 0 {
		s.logf("assistant: %d open task(s) of the replaced main conversation %s handed to %s", moved, short(from), short(to))
	}
	return moved
}

// handOverTask moves task t from conversation from to conversation to (handOverMainTasks): its origin when it is
// from, its supervisor when heldBy says the ledger is from's. Its card goes to the new conversation with its supervision
// (a leader keeps the card it has, and a task "Move to…" already moved has its card there). False when nothing moved
// (a write failed: the next pass tries again).
func (s *Service) handOverTask(ctx context.Context, t Todo, from, to string, heldBy func(*Supervision) bool) bool {
	moved, supervised := false, false
	if t.Origin == from {
		if _, err := s.editTodo(ctx, t.ID, db.TodoEdit{Origin: &to}); err != nil {
			s.logf("task %s: hand over to the main conversation: origin: %v", t.ID, err)
			return false
		}
		s.todos.mu.Lock() // the stored row moved; memory follows (setTodoRow leaves the origin alone for every other writer)
		if m := s.todos.todos[t.ID]; m != nil {
			m.Origin = to
		}
		s.todos.mu.Unlock()
		s.todosChanged(ctx)
		moved = true
	}
	if t.Supervision != nil {
		pending := false
		v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			if !heldBy(v) {
				return false // a leader's, or moved meanwhile
			}
			v.Supervisor = to
			pending = v.state(t) == reviewPending
			return true
		})
		switch {
		case err != nil:
			s.logf("task %s: hand over to the main conversation: %v", t.ID, err)
		case changed:
			moved, supervised = true, true
			if pending { // the old conversation was told; the new one has not been
				s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Detail: "主对话换了，这个任务转给你验收"}) // i18n-ignore: the supervision message is the assistant's prompt
			}
		}
	}
	if supervised {
		s.taskCard(ctx, to, t.ID, wakeActive) // its card follows it (the old conversation keeps its history)
	}
	if moved {
		s.logf("task %s: the main conversation %s was replaced; %s takes it over", t.ID, short(from), short(to))
	}
	return moved
}

// handOverFormerMainTasks moves what a former main conversation (Settings.Former) still holds to the main one: switches
// made before tasks moved with the main conversation, and a move a restart or a failed write cut short. It runs from the
// supervision loop and is idempotent: a moved task is no longer the former conversation's.
func (s *Service) handOverFormerMainTasks(ctx context.Context) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || len(cfg.Former) == 0 {
		return
	}
	for _, f := range cfg.Former {
		if f.SessionID != cfg.Main {
			s.handOverMainTasks(ctx, f.SessionID, cfg.Main)
		}
	}
}
