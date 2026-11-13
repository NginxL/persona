package assistant

import (
	"context"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
)

// inboxTaskLinkResult keeps operational outcomes distinct from a model that found no matching task.
type inboxTaskLinkResult struct {
	Status string    `json:"status"`
	Method string    `json:"method,omitempty"`
	Item   InboxItem `json:"item"`
}

func (s *Service) inboxTaskLinkRoutes() {
	s.handle("POST /api/v1/inbox/{id}/task-link/recheck", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		out, code, err := s.recheckInboxTask(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, code, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		apierr.WriteJSON(w, out)
	})
}

// recheckInboxTask runs only the task judge, including for old events with a saved draft or conversation. Unlike
// handPass it cannot sweep, escalate, start conversations, file events or send replies. Eligible links are preserved;
// a bulk notification's stale association is cleared through the normal rollback-aware item write.
func (s *Service) recheckInboxTask(ctx context.Context, id string) (inboxTaskLinkResult, int, error) {
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return inboxTaskLinkResult{}, http.StatusNotFound, errNoInboxItem
	}
	out := inboxTaskLinkResult{Item: it.listed()}
	if !it.open() {
		b.mu.Unlock()
		out.Status = "closed"
		return out, http.StatusConflict, nil
	}
	if !inboxTaskLinkEligible(it) {
		out.Status, out.Method = "excluded", "policy"
		if it.Agent == nil || it.Agent.TodoID == "" {
			b.mu.Unlock()
			return out, http.StatusOK, nil
		}
		w := beginWrite(it)
		it.Agent.TodoID = ""
		b.settle(w)
		b.mu.Unlock()
		if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its excluded task link"); failed[id] {
			return out, http.StatusServiceUnavailable, errInboxNotPersisted
		}
		b.mu.Lock()
		out.Status, out.Item = "cleared", it.listed()
		b.mu.Unlock()
		return out, http.StatusOK, nil
	}
	if it.Agent != nil && it.Agent.TodoID != "" {
		b.mu.Unlock()
		out.Status = "existing"
		return out, http.StatusOK, nil
	}
	snapshot := *it
	b.mu.Unlock()
	if !s.inboxConfig(ctx).Agent.Enabled {
		out.Status = "disabled"
		return out, http.StatusConflict, nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return out, http.StatusServiceUnavailable, err
	}
	tasks := s.activeTasks(ctx)
	out.Status = "unmatched"
	if len(tasks) == 0 {
		return out, http.StatusOK, nil
	}
	chosen, err := s.judgeInboxTask(ctx, &snapshot, tasks)
	if err != nil {
		return out, http.StatusBadGateway, err
	}
	out.Method = "judge"
	if chosen == "" {
		return out, http.StatusOK, nil
	}
	if t, ok := s.todoNow(chosen); !ok || t.DoneAt != nil || t.Cancelled() {
		out.Status = "task_closed"
		return out, http.StatusConflict, nil
	}
	b.mu.Lock()
	if b.items[id] != it || it.Last != snapshot.Last || !it.open() || !inboxTaskLinkEligible(it) {
		b.mu.Unlock()
		out.Status = "changed"
		return out, http.StatusConflict, nil
	}
	if it.Agent != nil && it.Agent.TodoID != "" {
		out.Status, out.Item = "existing", it.listed()
		b.mu.Unlock()
		return out, http.StatusOK, nil
	}
	w := beginWrite(it)
	if it.Agent == nil {
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	it.Agent.TodoID = chosen
	b.settle(w)
	b.mu.Unlock()
	if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its task link recheck"); failed[id] {
		return out, http.StatusServiceUnavailable, errInboxNotPersisted
	}
	// A draft made before the task was known now gets the same card as a newly submitted draft.
	b.mu.Lock()
	card := messageCardOf(it)
	out.Status, out.Item = "linked", it.listed()
	b.mu.Unlock()
	if card != nil {
		s.messageCard(ctx, id, *card)
	}
	s.superviseInbox(ctx, []*InboxItem{it}, map[*InboxItem]bool{it: true})
	return out, http.StatusOK, nil
}
