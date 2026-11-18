package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The owner's restore and Someday (#10226, docs/assistant/todos.md "Restores and Someday"), kept on the to-do so every
// device agrees, as seen is (todo_seen.go). Restore brings a to-do back from Inactive: restored_at is activity for its
// status only. Someday puts an open to-do off until someday_until (db.SomedayNoDate: no date); reopening or retrying it
// takes it out again (db.TodoEdit.Apply, db.RetryTodo). Neither mark is an edit: updated_at stays, as it does for seen.
// The refusals carry stable codes, so a client tells them from a route an older hub lacks (404 not_found there too).

// markTodo writes one of the owner's marks on todo id: with the store through write, else on the memory row through
// edit. Taken under todoBook.writing as seeTodo is, and the apps are told. db.ErrTodoNotFound when it is not there.
func (s *Service) markTodo(ctx context.Context, id string, write func(todoStore) (*db.TodoRow, error), edit func(*db.TodoRow) error) (Todo, error) {
	s.todos.writing.Lock()
	cur, ok := s.todoNow(id)
	if !ok {
		s.todos.writing.Unlock()
		return Todo{}, db.ErrTodoNotFound
	}
	var row *db.TodoRow
	if st := s.todoDB(); st != nil {
		var err error
		if row, err = write(st); err != nil {
			s.todos.writing.Unlock()
			return Todo{}, err
		}
	} else {
		r := todoRowOf(cur)
		if err := edit(&r); err != nil {
			s.todos.writing.Unlock()
			return Todo{}, err
		}
		row = &r
	}
	out, _, applied := s.setTodoRow(id, *row, nil)
	s.todos.writing.Unlock()
	if !applied {
		return Todo{}, db.ErrTodoNotFound // deleted meanwhile
	}
	s.todosChanged(ctx) // the other devices read the list again: it moves there too
	return out, nil
}

// restoreTodo records that the owner brought todo id back from Inactive now.
func (s *Service) restoreTodo(ctx context.Context, id string) (Todo, error) {
	now := time.Now().UTC()
	return s.markTodo(ctx, id,
		func(st todoStore) (*db.TodoRow, error) { return st.RestoreTodo(ctx, id, now) },
		func(r *db.TodoRow) error { r.RestoredAt = &now; return nil })
}

// setTodoSomeday puts todo id off to Someday until until, or with nil takes it out; db.ErrTodoCompleted when until is
// not nil and the to-do is closed. A failed start counts as open here: it waits on the owner to retry or close it.
func (s *Service) setTodoSomeday(ctx context.Context, id string, until *time.Time) (Todo, error) {
	return s.markTodo(ctx, id,
		func(st todoStore) (*db.TodoRow, error) { return st.SetTodoSomeday(ctx, id, until) },
		func(r *db.TodoRow) error {
			if until != nil && r.DoneAt != nil && r.StartError == "" {
				return db.ErrTodoCompleted
			}
			r.SomedayUntil = until
			return nil
		})
}

// markAllowed: the marks are for full credentials only, as seen is, once the to-dos are read; false when it answered.
func (s *Service) markAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
		return false
	}
	if err := s.ensureTodosLoaded(r.Context()); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
		return false
	}
	return true
}

// answerMark answers a mark as seen does: {"todo": Todo}, 404 for an unknown to-do, 409 for Someday on a closed one.
func answerMark(w http.ResponseWriter, t Todo, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", errors.New("todo not found"))
	case errors.Is(err, db.ErrTodoCompleted):
		apierr.WriteCode(w, http.StatusConflict, "todo_closed", i18n.Err("assistant-todos.someday_closed"))
	case err != nil:
		apierr.Write(w, http.StatusInternalServerError, err)
	default:
		apierr.WriteJSON(w, map[string]any{"todo": t})
	}
}

func (s *Service) todoMarkRoutes() {
	// PUT /api/v1/todos/{id}/restored: Restore on an inactive to-do.
	s.handle("PUT /api/v1/todos/{id}/restored", func(w http.ResponseWriter, r *http.Request) {
		if !s.markAllowed(w, r) {
			return
		}
		t, err := s.restoreTodo(r.Context(), r.PathValue("id"))
		answerMark(w, t, err)
	})
	// PUT /api/v1/todos/{id}/someday {"until"?: RFC 3339}: Someday until then, or with no body or null with no date.
	s.handle("PUT /api/v1/todos/{id}/someday", func(w http.ResponseWriter, r *http.Request) {
		if !s.markAllowed(w, r) {
			return
		}
		var in struct {
			Until *time.Time `json:"until"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		until := db.SomedayNoDate
		if in.Until != nil {
			until = in.Until.UTC()
			// Ahead, and no later than "no date": a later time (year 10000 in UTC) could not be written as JSON again.
			if !until.After(time.Now()) || until.After(db.SomedayNoDate) {
				apierr.WriteCode(w, http.StatusBadRequest, "someday_until", i18n.Err("assistant-todos.someday_until"))
				return
			}
		}
		t, err := s.setTodoSomeday(r.Context(), r.PathValue("id"), &until)
		answerMark(w, t, err)
	})
	// DELETE /api/v1/todos/{id}/someday: back from Someday.
	s.handle("DELETE /api/v1/todos/{id}/someday", func(w http.ResponseWriter, r *http.Request) {
		if !s.markAllowed(w, r) {
			return
		}
		t, err := s.setTodoSomeday(r.Context(), r.PathValue("id"), nil)
		answerMark(w, t, err)
	})
}
