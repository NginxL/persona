package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Two writes of part_of/after at once, each valid against the to-dos as they were, can't both be stored when together
// they break the request's shape (todo_pieces.go): each is held after its caller's check, until both have passed it,
// and only then goes on to write. Every writer is covered: the apps' PATCH, todo_update, todo_create and a task's start
// (task_plan), in memory and in the store.

// holdPieceWrites holds each of the next n link writes (editTodo, insertTodo) after its caller's check until all n have
// reached that point, so they all passed against the same graph.
func holdPieceWrites(t *testing.T, s *Service, n int) {
	t.Helper()
	var mu sync.Mutex
	arrived, all := 0, make(chan struct{})
	s.pieceWriteHook = func(string) {
		mu.Lock()
		if arrived++; arrived == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(10 * time.Second): // a writer that never got here: the test's checks say which
		}
	}
	t.Cleanup(func() { s.pieceWriteHook = nil })
}

// together runs fns at once and waits for them all.
func together(fns ...func()) {
	var wg sync.WaitGroup
	for _, f := range fns {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	wg.Wait()
}

// noCycle fails t when the after links of the to-dos named close a cycle, as memory has them and (reloaded) as the
// store does.
func noCycle(t *testing.T, s *Service, ids ...string) {
	t.Helper()
	check := func(where string) {
		t.Helper()
		after := map[string][]string{}
		for _, id := range ids {
			td, ok := s.todoNow(id)
			if !ok {
				t.Fatalf("%s: %s is gone", where, id)
			}
			after[id] = td.After
		}
		for _, id := range ids {
			for _, a := range after[id] {
				if slices.Contains(after[a], id) {
					t.Fatalf("%s: a cycle was stored: %s after %v, %s after %v", where, id, after[id], a, after[a])
				}
			}
		}
	}
	check("memory")
	reloadTodos(t, s)
	check("after a reload")
}

func TestTodoPiecesConcurrentWrites(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		s := e.s
		send := func(method, path, body string) Todo {
			var res struct{ Todo Todo }
			_ = json.Unmarshal(e.must(method, path, body).Body.Bytes(), &res)
			return res.Todo
		}
		root := send("POST", "/api/v1/todos", `{"title":"Move house"}`)
		piece := func(title string) Todo {
			td := send("POST", "/api/v1/todos", `{"title":"`+title+`"}`)
			return send("PATCH", "/api/v1/todos/"+td.ID, `{"part_of":"`+root.ID+`"}`)
		}
		patch := func(id, body string) func() int {
			return func() int { return e.do("PATCH", "/api/v1/todos/"+id, body).Code }
		}
		// oneEach runs a and b together and wants exactly one of them refused.
		oneEach := func(t *testing.T, name string, a, b func() bool) {
			t.Helper()
			var okA, okB bool
			holdPieceWrites(t, s, 2)
			together(func() { okA = a() }, func() { okB = b() })
			s.pieceWriteHook = nil
			if okA == okB {
				t.Fatalf("%s: both stored %v / %v; want exactly one refused", name, okA, okB)
			}
		}

		t.Run("two PATCHes", func(t *testing.T) {
			a, b := piece("Book the van"), piece("Pack the kitchen")
			var codeA, codeB int
			holdPieceWrites(t, s, 2)
			together(
				func() { codeA = patch(a.ID, `{"after":["`+b.ID+`"]}`)() },
				func() { codeB = patch(b.ID, `{"after":["`+a.ID+`"]}`)() },
			)
			s.pieceWriteHook = nil
			if codes := []int{codeA, codeB}; !slices.Contains(codes, http.StatusOK) || !slices.Contains(codes, http.StatusBadRequest) {
				t.Fatalf("codes %v: want one 200 and one 400", codes)
			}
			noCycle(t, s, a.ID, b.ID)
		})

		t.Run("todo_update and a PATCH", func(t *testing.T) {
			a, b := piece("Change the address"), piece("Cancel the gym")
			oneEach(t, "todo_update/PATCH",
				func() bool {
					_, err := runJSON(t, s, "conv1", "todo_update", map[string]any{"id": a.ID, "after": []string{b.ID}})
					return err == nil
				},
				func() bool { return patch(b.ID, `{"after":["`+a.ID+`"]}`)() == http.StatusOK },
			)
			noCycle(t, s, a.ID, b.ID)
		})

		t.Run("a task's start and a PATCH", func(t *testing.T) {
			a, b := piece("Hire movers"), piece("Measure the sofa")
			oneEach(t, "task start/PATCH",
				func() bool {
					out, _ := s.startPieces(context.Background(), PlanTask{PartOf: root.ID, After: []string{b.ID}}, a)
					return slices.Equal(out.After, []string{b.ID}) // a link that no longer holds is left out
				},
				func() bool { return patch(b.ID, `{"after":["`+a.ID+`"]}`)() == http.StatusOK },
			)
			noCycle(t, s, a.ID, b.ID)
		})

		// Same request only, from both ends: a sibling starting to wait for X and X moved to another request, at once.
		t.Run("a sibling's after and a move", func(t *testing.T) {
			other := send("POST", "/api/v1/todos", `{"title":"Sell the car"}`)
			x, y := piece("Book the van"), piece("Pack the kitchen")
			oneEach(t, "todo_update/PATCH",
				func() bool {
					_, err := runJSON(t, s, "conv1", "todo_update", map[string]any{"id": y.ID, "after": []string{x.ID}})
					return err == nil
				},
				func() bool { return patch(x.ID, `{"part_of":"`+other.ID+`"}`)() == http.StatusOK },
			)
			afterInOwnRequest(t, s)
		})

		// One level only: a new piece of X and X made a piece of another request, at once.
		t.Run("todo_create and a PATCH", func(t *testing.T) {
			x := send("POST", "/api/v1/todos", `{"title":"Sell the car"}`)
			var newID string
			oneEach(t, "todo_create/PATCH",
				func() bool {
					_, err := runJSON(t, s, "conv1", "todo_create", map[string]any{"title": "Photograph the car", "part_of": x.ID})
					if err == nil {
						newID = mustTodo(t, s, "Photograph the car").ID
					}
					return err == nil
				},
				func() bool { return patch(x.ID, `{"part_of":"`+root.ID+`"}`)() == http.StatusOK },
			)
			reloadTodos(t, s)
			xNow, _ := s.todoNow(x.ID)
			if newID != "" {
				if xNow.PartOf != "" {
					t.Fatalf("two levels stored: %s is a piece of %s and has piece %s", x.ID, xNow.PartOf, newID)
				}
			} else if xNow.PartOf != root.ID {
				t.Fatalf("the PATCH was the one stored, yet %s: %+v", x.ID, xNow)
			}
		})
	})
}

// A to-do deleted while an edit of it is on its way (after the caller read and checked it, before the write) stays
// deleted: the edit answers "not found" instead of writing the to-do again from the copy it read, in memory and in
// the store, whether the edit names part_of/after (checked again at the write) or not.
func TestTodoEditOfADeletedTodo(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		s := e.s
		send := func(method, path, body string) Todo {
			var res struct{ Todo Todo }
			_ = json.Unmarshal(e.must(method, path, body).Body.Bytes(), &res)
			return res.Todo
		}
		root := send("POST", "/api/v1/todos", `{"title":"Move house"}`)
		piece := func(title string) Todo {
			td := send("POST", "/api/v1/todos", `{"title":"`+title+`"}`)
			return send("PATCH", "/api/v1/todos/"+td.ID, `{"part_of":"`+root.ID+`"}`)
		}
		// deleteOnTheWay deletes id as the next edit of it is about to write
		deleteOnTheWay := func(t *testing.T, id string) {
			t.Helper()
			var once sync.Once
			s.editWriteHook = func(at string) {
				if at == id {
					once.Do(func() {
						if err := s.deleteTodo(context.Background(), id); err != nil {
							t.Errorf("delete %s: %v", id, err)
						}
					})
				}
			}
			t.Cleanup(func() { s.editWriteHook = nil })
		}
		gone := func(t *testing.T, id string) {
			t.Helper()
			if td, ok := s.todoNow(id); ok {
				t.Fatalf("memory: %s is back (title %q, part_of %q, after %v)", id, td.Title, td.PartOf, td.After)
			}
			reloadTodos(t, s)
			if td, ok := s.todoNow(id); ok {
				t.Fatalf("after a reload: %s is back (title %q)", id, td.Title)
			}
		}

		t.Run("a PATCH of after", func(t *testing.T) {
			a, b, c := piece("Book the van"), piece("Pack the kitchen"), piece("Measure the sofa")
			send("PATCH", "/api/v1/todos/"+a.ID, `{"after":["`+b.ID+`"]}`)
			deleteOnTheWay(t, a.ID)
			if code := e.do("PATCH", "/api/v1/todos/"+a.ID, `{"after":["`+c.ID+`"]}`).Code; code != http.StatusNotFound {
				t.Fatalf("PATCH of a deleted to-do: %d, want 404", code)
			}
			gone(t, a.ID)
		})

		t.Run("todo_update of part_of and after", func(t *testing.T) {
			a, b := piece("Change the address"), piece("Cancel the gym")
			deleteOnTheWay(t, a.ID)
			_, err := runJSON(t, s, "conv1", "todo_update", map[string]any{"id": a.ID, "part_of": root.ID, "after": []string{b.ID}})
			if err == nil {
				t.Fatal("todo_update of a deleted to-do succeeded")
			}
			gone(t, a.ID)
		})

		t.Run("a PATCH of the title only", func(t *testing.T) {
			x := send("POST", "/api/v1/todos", `{"title":"Hire movers"}`)
			deleteOnTheWay(t, x.ID)
			if code := e.do("PATCH", "/api/v1/todos/"+x.ID, `{"title":"Hire movers on Friday"}`).Code; code != http.StatusNotFound {
				t.Fatalf("PATCH of a deleted to-do: %d, want 404", code)
			}
			gone(t, x.ID)
		})
	})
}
