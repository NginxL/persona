package assistant

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The To-dos view on the wire (#10226, docs/assistant/todo-status-sop.md "The hub's view"): every row of
// GET /api/v1/todos carries its view and the list its counts, GET /api/v1/todos/{id} answers one row the same way, and
// GET /api/v1/todos/places the sidebar's places. Each read works the view out once, over the whole list, through the
// adapter (todo_projection.go): a row's rank, its steps and a superseded failed start are read against the rest of the
// list, so a row reads the same in the list and on its own. Every time in a view is absolute, so the same state gives
// the same bytes and the list's ETag (#9590) holds while nothing changes. The manager names CapabilityTodoView and
// CapabilityTodoPlaces in its hello; the hub adds the counts and places of several managers up.

// todosList is GET /api/v1/todos's answer: the rows, each with its view, and the list's counts.
type todosList struct {
	Todos  []listedTodo             `json:"todos"`
	Counts assistantwire.TodoCounts `json:"counts"`
}

// listedTodos are the to-dos as GET /api/v1/todos lists them, newest created first: each formatted, with its decision
// review, its ask while one waits (todo_ask.go) and, while open and nobody works on it, why (tasks_orphan.go); with
// briefs also what was asked and who carried it (task_brief.go), which the view does not read. Without views: viewed
// adds them. An error when the to-dos or their decision reviews cannot be read.
func (s *Service) listedTodos(ctx context.Context, briefs bool) ([]listedTodo, error) {
	return s.listedTodosOf(ctx, briefs, nil)
}

// listedTodosOf is listedTodos of the to-dos whose ids are in ids alone; nil is every to-do.
func (s *Service) listedTodosOf(ctx context.Context, briefs bool, ids map[string]bool) ([]listedTodo, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	s.todos.mu.RLock()
	n := len(s.todos.todos)
	if ids != nil {
		n = len(ids)
	}
	list := make([]listedTodo, 0, n)
	for _, t := range s.todos.todos {
		if ids == nil || ids[t.ID] {
			list = append(list, listedTodo{Todo: formatTodo(copyTodoLocked(t))})
		}
	}
	s.todos.mu.RUnlock()
	if err := s.listDecisionReviews(ctx, list); err != nil {
		return nil, err
	}
	asks := s.todoAsks(ctx) // once the todos are let go: it reads them itself
	var links briefLinks
	if briefs {
		links = s.briefLinksNow() // every lineage below is read from these, not from all the links each time
	}
	for i := range list {
		if briefs {
			list[i].Todo = s.withBriefFrom(list[i].Todo, links)
		}
		if a, ok := asks[list[i].ID]; ok {
			list[i].Ask = &a
		}
		if o, ok := s.orphanOf(list[i].ID); ok && list[i].DoneAt == nil {
			list[i].Orphaned = &o
		}
	}
	// Newest first; equal times by id, so unchanged state is the same bytes whatever order the map gave (the ETag).
	slices.SortFunc(list, func(a, b listedTodo) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return list, nil
}

// viewed gives every row of list its view at now and answers the list's projection.
func (s *Service) viewed(ctx context.Context, list []listedTodo, now time.Time) todoProjection {
	p := s.todoProjectionOf(ctx, list, now)
	for i := range list {
		list[i].View = &p.views[i]
	}
	return p
}

// todosViewed is every to-do as GET /api/v1/todos lists it, each with its view now (the views' clock,
// todo_view_push.go), and the list's projection. What each row is filed under is held against what the apps were
// told, and any that reads differently tells them again (todoViewsSeen, todoViewDiffers).
func (s *Service) todosViewed(ctx context.Context, briefs bool) ([]listedTodo, todoProjection, error) {
	gen := s.todoViewGen() // before the to-dos are read: a push from then on makes these views old
	list, err := s.listedTodos(ctx, briefs)
	if err != nil {
		return nil, todoProjection{}, err
	}
	p := s.viewed(ctx, list, s.todoViewNow())
	if s.todoViewsSeen(gen, p, nil) {
		s.todoViewDiffers()
	}
	return list, p, nil
}

func (s *Service) todoViewRoutes() {
	// GET /api/v1/todos/{id}: one to-do as GET /api/v1/todos lists it, its view included → {"todo": row}; an unknown
	// id 404 todo_not_found.
	s.handle("GET /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		s.todos.mu.RLock()
		_, known := s.todos.todos[id]
		s.todos.mu.RUnlock()
		if !known { // before the whole list's pass, which only a known id needs
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", errors.New("todo not found"))
			return
		}
		list, _, err := s.todosViewed(r.Context(), false)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		for _, t := range list {
			if t.ID == id {
				t.Todo = s.withBrief(t.Todo) // as withBriefFrom gives it in the list
				apierr.WriteJSON(w, map[string]any{"todo": t})
				return
			}
		}
		apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", errors.New("todo not found")) // deleted meanwhile
	})
	// GET /api/v1/todos/places: the To-dos sidebar's places with the list's counts (assistantwire.TodoPlaces).
	s.handle("GET /api/v1/todos/places", func(w http.ResponseWriter, r *http.Request) {
		_, p, err := s.todosViewed(r.Context(), false)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		apierr.WriteJSON(w, s.todoPlaces(r.Context(), p))
	})
}
