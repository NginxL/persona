package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// A piece that changes its request (todo_update part_of to another request, or "" to clear it) while a sibling's after
// names it is refused, naming the sibling: otherwise the sibling waits for a piece of another request, which after
// never names (docs/assistant/todos.md "Requests in pieces"). Every combination of the piece's place, its own after,
// who waits for it and the edit goes through the real tools (and the apps' PATCH), in memory and in the store, and
// after each one every after still names pieces of its own request. Ids and titles are placeholders.

// afterInOwnRequest fails t when some to-do's after names a to-do that is not a piece of the same request, as memory
// has them and, reloaded, as the store does.
func afterInOwnRequest(t *testing.T, s *Service) {
	t.Helper()
	check := func(where string) {
		t.Helper()
		s.todos.mu.RLock()
		defer s.todos.mu.RUnlock()
		for _, td := range s.todos.todos {
			for _, a := range td.After {
				if other, ok := s.todos.todos[a]; ok && (td.PartOf == "" || other.PartOf != td.PartOf) {
					t.Fatalf("%s: %s (piece of %q) waits for %s (piece of %q)", where, td.ID, td.PartOf, a, other.PartOf)
				}
			}
		}
	}
	check("memory")
	reloadTodos(t, s)
	check("after a reload")
}

func TestTodoPiecesMoveWithWaitingSiblings(t *testing.T) {
	const absent = "-" // a field the edit leaves out
	type row struct {
		name string
		// x is where the edited to-do starts: "loose" (no piece), "R" (a piece of R) or "R after W" (a piece of R
		// waiting for its sibling W).
		x string
		// waiters: how many pieces of R wait for x (after [x]); waiterDone closes the first one first.
		waiters    int
		waiterDone bool
		// partOf and after are the edit, by name: "R", "R2", "" or absent; nil after is absent, otherwise names
		// among W (a piece of R) and W2 (a piece of R2).
		partOf string
		after  []string
		patch  bool // through the apps' PATCH instead of todo_update
		// wantErr: a refusal containing it ("waits" for the waiter refusal, which the test spells out); otherwise
		// x afterwards is a piece of wantPartOf waiting for wantAfter.
		wantErr    string
		wantPartOf string
		wantAfter  []string
	}
	rows := []row{
		// Nobody waits for x: every change of request goes through, and after follows the request.
		{name: "after cleared", x: "R after W", partOf: absent, after: []string{}, wantPartOf: "R", wantAfter: nil},
		{name: "after set", x: "R", partOf: absent, after: []string{"W"}, wantPartOf: "R", wantAfter: []string{"W"}},
		{name: "same request keeps after", x: "R after W", partOf: "R", wantPartOf: "R", wantAfter: []string{"W"}},
		{name: "moved empties after", x: "R after W", partOf: "R2", wantPartOf: "R2", wantAfter: nil},
		{name: "moved with after []", x: "R after W", partOf: "R2", after: []string{}, wantPartOf: "R2", wantAfter: nil},
		{name: "moved with after in the new request", x: "R after W", partOf: "R2", after: []string{"W2"}, wantPartOf: "R2", wantAfter: []string{"W2"}},
		{name: "moved with after in the old request", x: "R", partOf: "R2", after: []string{"W"}, wantErr: "not a piece of the same request"},
		{name: "cleared empties after", x: "R after W", partOf: "", wantPartOf: "", wantAfter: nil},
		{name: "cleared with after []", x: "R after W", partOf: "", after: []string{}, wantPartOf: "", wantAfter: nil},
		{name: "cleared with after", x: "R", partOf: "", after: []string{"W"}, wantErr: "give part_of too"},
		{name: "loose made a piece", x: "loose", partOf: "R", wantPartOf: "R", wantAfter: nil},
		{name: "loose made a piece with after", x: "loose", partOf: "R", after: []string{"W"}, wantPartOf: "R", wantAfter: []string{"W"}},
		{name: "loose cleared again", x: "loose", partOf: "", wantPartOf: "", wantAfter: nil},
		{name: "moved by PATCH", x: "R after W", partOf: "R2", patch: true, wantPartOf: "R2", wantAfter: nil},
		{name: "cleared by PATCH", x: "R after W", partOf: "", patch: true, wantPartOf: "", wantAfter: nil},

		// A sibling waits for x: x may change what it waits for and stay in R, but not leave R.
		{name: "waited for: after set", x: "R", waiters: 1, partOf: absent, after: []string{"W"}, wantPartOf: "R", wantAfter: []string{"W"}},
		{name: "waited for: after cleared", x: "R after W", waiters: 1, partOf: absent, after: []string{}, wantPartOf: "R", wantAfter: nil},
		{name: "waited for: same request", x: "R after W", waiters: 1, partOf: "R", wantPartOf: "R", wantAfter: []string{"W"}},
		{name: "waited for: moved", x: "R", waiters: 1, partOf: "R2", wantErr: "waits"},
		{name: "waited for: moved, own after", x: "R after W", waiters: 1, partOf: "R2", wantErr: "waits"},
		{name: "waited for: moved with after []", x: "R after W", waiters: 1, partOf: "R2", after: []string{}, wantErr: "waits"},
		{name: "waited for: moved with after in the new request", x: "R", waiters: 1, partOf: "R2", after: []string{"W2"}, wantErr: "waits"},
		{name: "waited for: cleared", x: "R", waiters: 1, partOf: "", wantErr: "waits"},
		{name: "waited for: cleared, own after", x: "R after W", waiters: 1, partOf: "", wantErr: "waits"},
		{name: "waited for: cleared with after []", x: "R after W", waiters: 1, partOf: "", after: []string{}, wantErr: "waits"},
		{name: "waited for by a done piece: moved", x: "R", waiters: 1, waiterDone: true, partOf: "R2", wantErr: "waits"},
		{name: "waited for by a done piece: cleared", x: "R", waiters: 1, waiterDone: true, partOf: "", wantErr: "waits"},
		{name: "waited for: moved by PATCH", x: "R", waiters: 1, partOf: "R2", patch: true, wantErr: "waits"},
		{name: "waited for: cleared by PATCH", x: "R", waiters: 1, partOf: "", patch: true, wantErr: "waits"},
		{name: "waited for by two: moved", x: "R", waiters: 2, partOf: "R2", wantErr: "waits"},
		{name: "waited for by two: cleared", x: "R after W", waiters: 2, partOf: "", wantErr: "waits"},
		{name: "waited for by two: same request", x: "R", waiters: 2, partOf: "R", after: []string{"W"}, wantPartOf: "R", wantAfter: []string{"W"}},
	}

	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		s := e.s
		for i, c := range rows {
			t.Run(c.name, func(t *testing.T) {
				n := func(name string) string { return fmt.Sprintf("%d %s", i, name) } // titles unique per row
				create := func(name string, args map[string]any) Todo {
					args["title"] = n(name)
					pieceTool(t, s, "conv1", "todo_create", args)
					return mustTodo(t, s, n(name))
				}
				ids := map[string]string{"": ""}
				ids["R"] = create("R", map[string]any{}).ID
				ids["R2"] = create("R2", map[string]any{}).ID
				ids["W"] = create("W", map[string]any{"part_of": ids["R"]}).ID
				ids["W2"] = create("W2", map[string]any{"part_of": ids["R2"]}).ID
				var x Todo
				switch c.x {
				case "loose":
					x = create("X", map[string]any{})
				case "R":
					x = create("X", map[string]any{"part_of": ids["R"]})
				case "R after W":
					x = create("X", map[string]any{"part_of": ids["R"], "after": []string{ids["W"]}})
				default:
					t.Fatalf("x %q", c.x)
				}
				var waiters []string
				for w := range c.waiters {
					waiters = append(waiters, create(fmt.Sprintf("Y%d", w), map[string]any{"part_of": ids["R"], "after": []string{x.ID}}).ID)
				}
				if c.waiterDone {
					e.must("PATCH", "/api/v1/todos/"+waiters[0], `{"done":true}`)
				}
				names := func(syms []string) []string {
					out := make([]string, 0, len(syms))
					for _, sym := range syms {
						out = append(out, ids[sym])
					}
					return out
				}

				args := map[string]any{"id": x.ID}
				if c.partOf != absent {
					args["part_of"] = ids[c.partOf]
				}
				if c.after != nil {
					args["after"] = names(c.after)
				}
				var err error
				if c.patch {
					delete(args, "id")
					body, _ := json.Marshal(args)
					if w := e.do("PATCH", "/api/v1/todos/"+x.ID, string(body)); w.Code != http.StatusOK {
						if w.Code != http.StatusBadRequest {
							t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body)
						}
						var res struct{ Error string }
						_ = json.Unmarshal(w.Body.Bytes(), &res)
						err = errors.New(res.Error)
					}
				} else {
					_, err = runJSON(t, s, "conv1", "todo_update", args)
				}

				got, _ := s.todoNow(x.ID)
				if c.wantErr != "" {
					want := c.wantErr
					if want == "waits" {
						sorted := slices.Sorted(slices.Values(waiters))
						want = fmt.Sprintf("%q waits for this to-do", sorted[0])
						if len(sorted) == 2 {
							want = fmt.Sprintf("%q, %q wait for this to-do", sorted[0], sorted[1])
						}
					}
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("got %v; want a refusal with %q", err, want)
					}
					if got.PartOf != x.PartOf || !slices.Equal(got.After, x.After) {
						t.Fatalf("refused, yet changed: %+v, was part_of %q after %v", got, x.PartOf, x.After)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if got.PartOf != ids[c.wantPartOf] || !slices.Equal(got.After, names(c.wantAfter)) {
						t.Fatalf("part_of %q after %v; want %q %v", got.PartOf, got.After, ids[c.wantPartOf], names(c.wantAfter))
					}
				}
				for _, y := range waiters {
					if yNow, _ := s.todoNow(y); yNow.PartOf != ids["R"] || !slices.Equal(yNow.After, []string{x.ID}) {
						t.Fatalf("waiter %s changed: %+v", y, yNow)
					}
				}
				afterInOwnRequest(t, s)
			})
		}
	})
}

// The way out the refusal names: take the to-do out of the waiting sibling's after, then move it (or clear it). The
// sibling can then send its own after back, and a task_plan start that would move it is left out while it is waited
// for.
func TestTodoPiecesMoveAfterTheWaitIsTaken(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		s := e.s
		for _, title := range []string{"Move house", "Sell the car", "Book the van", "Pack the kitchen", "Clean the flat"} {
			pieceTool(t, s, "conv1", "todo_create", map[string]any{"title": title})
		}
		r, r2 := mustTodo(t, s, "Move house"), mustTodo(t, s, "Sell the car")
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": mustTodo(t, s, "Book the van").ID, "part_of": r.ID})
		x := mustTodo(t, s, "Book the van")
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": mustTodo(t, s, "Pack the kitchen").ID, "part_of": r.ID, "after": []string{x.ID}})
		y := mustTodo(t, s, "Pack the kitchen")
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": mustTodo(t, s, "Clean the flat").ID, "part_of": r.ID})
		z := mustTodo(t, s, "Clean the flat")

		// A task started on x that would move it: the link is left out, as for any link that no longer holds.
		if out, err := s.startPieces(context.Background(), PlanTask{PartOf: r2.ID}, x); err != nil || out.PartOf != r.ID {
			t.Fatalf("a start moved a piece that is waited for: %+v", out)
		}
		pieceRefused(t, s, "conv1", "todo_update", map[string]any{"id": x.ID, "part_of": r2.ID}, fmt.Sprintf("%q waits for this to-do", y.ID))

		// Y sends its own after back with one more prerequisite: accepted, x is still its sibling.
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": y.ID, "after": []string{x.ID, z.ID}})
		// Taken out of Y's after, x moves; Y keeps the rest.
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": y.ID, "after": []string{z.ID}})
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": x.ID, "part_of": r2.ID})
		if got, _ := s.todoNow(x.ID); got.PartOf != r2.ID {
			t.Fatalf("moved: %+v", got)
		}
		if got, _ := s.todoNow(y.ID); !slices.Equal(got.After, []string{z.ID}) {
			t.Fatalf("Y: %+v", got)
		}
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": y.ID, "after": []string{z.ID}}) // its after sent back

		// The same for clearing: Z waited for by Y, refused until Y lets go.
		pieceRefused(t, s, "conv1", "todo_update", map[string]any{"id": z.ID, "part_of": ""}, fmt.Sprintf("%q waits for this to-do", y.ID))
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": y.ID, "after": []string{}})
		pieceTool(t, s, "conv1", "todo_update", map[string]any{"id": z.ID, "part_of": ""})
		if got, _ := s.todoNow(z.ID); got.PartOf != "" || len(got.After) != 0 {
			t.Fatalf("cleared: %+v", got)
		}
		afterInOwnRequest(t, s)
	})
}

// waitingFor counts only the to-dos that would stop being siblings: one already in the request the to-do moves to (a
// link an older hub could leave) is no reason to refuse, and moving there mends it.
func TestWaitingFor(t *testing.T) {
	book := map[string]*Todo{
		"x":  {ID: "x", PartOf: "r", After: []string{"x"}},
		"y1": {ID: "y1", PartOf: "r", After: []string{"x"}},
		"y2": {ID: "y2", PartOf: "r2", After: []string{"w", "x"}},
		"w":  {ID: "w", PartOf: "r2"},
		"z":  {ID: "z", PartOf: "r"},
	}
	for _, c := range []struct {
		partOf string
		want   []string
	}{{"r2", []string{"y1"}}, {"r", []string{"y2"}}, {"", []string{"y1", "y2"}}, {"r3", []string{"y1", "y2"}}} {
		if got := waitingFor(book, "x", c.partOf); !slices.Equal(got, c.want) {
			t.Errorf("part_of %q: %v, want %v", c.partOf, got, c.want)
		}
	}
}
