package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// The owner's restore and Someday (todo_marks.go, #10226): only a full device sets them, an unknown to-do is 404,
// Someday is refused on a closed to-do; neither counts as activity, the apps read both in the list and are told they
// changed. Times read back from the store keep microseconds, hence sameInstant.

// markAnswer is the to-do a mark's 200 answers with.
func markAnswer(t *testing.T, w *httptest.ResponseRecorder) Todo {
	t.Helper()
	var res struct{ Todo Todo }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Todo.ID == "" {
		t.Fatalf("not {\"todo\": Todo}: %s %v", w.Body, err)
	}
	return res.Todo
}

func TestTodoRestoredRequests(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		id := e.create("Ship the release").ID
		before := e.get(id)
		if before == nil || before.RestoredAt != nil {
			t.Fatalf("a new to-do reads restored: %+v", before)
		}
		if w := e.doAs("answer", "PUT", "/api/v1/todos/"+id+"/restored", ""); w.Code != http.StatusForbidden {
			t.Fatalf("as a read-only role: %d", w.Code)
		}
		if w := e.do("PUT", "/api/v1/todos/todo_gone/restored", ""); w.Code != http.StatusNotFound {
			t.Fatalf("an unknown to-do: %d", w.Code)
		}

		events := todosEvents(e)
		start := time.Now().Add(-time.Second)
		answered := markAnswer(t, e.must("PUT", "/api/v1/todos/"+id+"/restored", ""))
		if answered.ID != id || answered.RestoredAt == nil {
			t.Fatalf("answered: %+v", answered)
		}
		got := e.get(id)
		if got == nil || got.RestoredAt == nil || got.RestoredAt.Before(start) || !sameInstant(*got.RestoredAt, *answered.RestoredAt) {
			t.Fatalf("after Restore: %+v", got)
		}
		if !sameInstant(got.UpdatedAt, before.UpdatedAt) {
			t.Fatalf("Restore counted as an edit: updated_at %v, was %v", got.UpdatedAt, before.UpdatedAt)
		}
		if todosEvents(e) == events {
			t.Fatal("the apps were not told the to-dos changed")
		}
	})
}

func TestTodoSomedayRequests(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		id := e.create("Learn to bake sourdough").ID
		before := e.get(id)
		if before == nil || before.SomedayUntil != nil {
			t.Fatalf("a new to-do reads put off: %+v", before)
		}
		for _, method := range []string{"PUT", "DELETE"} {
			if w := e.doAs("answer", method, "/api/v1/todos/"+id+"/someday", ""); w.Code != http.StatusForbidden {
				t.Fatalf("%s as a read-only role: %d", method, w.Code)
			}
			if w := e.do(method, "/api/v1/todos/todo_gone/someday", ""); w.Code != http.StatusNotFound {
				t.Fatalf("%s an unknown to-do: %d", method, w.Code)
			}
		}
		someday := func(want *time.Time) {
			t.Helper()
			got := e.get(id)
			if got == nil || (got.SomedayUntil == nil) != (want == nil) || want != nil && !sameInstant(*got.SomedayUntil, *want) {
				t.Fatalf("someday_until %v, want %v", got.SomedayUntil, want)
			}
			if !sameInstant(got.UpdatedAt, before.UpdatedAt) {
				t.Fatalf("Someday counted as an edit: updated_at %v, was %v", got.UpdatedAt, before.UpdatedAt)
			}
		}

		// No body, or null: Someday with no date.
		events := todosEvents(e)
		if a := markAnswer(t, e.must("PUT", "/api/v1/todos/"+id+"/someday", "")); a.SomedayUntil == nil || !a.SomedayUntil.Equal(db.SomedayNoDate) {
			t.Fatalf("answered: %+v", a)
		}
		someday(&db.SomedayNoDate)
		if todosEvents(e) == events {
			t.Fatal("the apps were not told the to-dos changed")
		}
		until := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
		e.must("PUT", "/api/v1/todos/"+id+"/someday", `{"until":"`+until.Format(time.RFC3339)+`"}`)
		someday(&until)
		e.must("PUT", "/api/v1/todos/"+id+"/someday", `{"until":null}`)
		someday(&db.SomedayNoDate)
		// A date that is not ahead, one later than "no date" (year 10000 in UTC could not be written as JSON again), one
		// that is no RFC 3339 time and a broken body are refused and change nothing; the list still reads.
		for _, body := range []string{`{"until":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}`,
			`{"until":"9999-12-31T23:59:59-01:00"}`, `{"until":"9999-12-31T12:00:00Z"}`, `{"until":"tomorrow"}`, `{`} {
			if w := e.do("PUT", "/api/v1/todos/"+id+"/someday", body); w.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d", body, w.Code)
			}
		}
		someday(&db.SomedayNoDate)
		if w := e.must("GET", "/api/v1/todos", ""); !json.Valid(w.Body.Bytes()) || w.Body.Len() == 0 {
			t.Fatalf("the list after the refusals: %q", w.Body)
		}

		events = todosEvents(e)
		if a := markAnswer(t, e.must("DELETE", "/api/v1/todos/"+id+"/someday", "")); a.SomedayUntil != nil {
			t.Fatalf("answered: %+v", a)
		}
		someday(nil)
		if todosEvents(e) == events {
			t.Fatal("the apps were not told the to-dos changed")
		}

		// Only an open to-do is put off; a failed start still waits on the owner, so it is open here.
		failed := e.create("Rebuild the index").ID
		if err := e.s.failTodo(context.Background(), failed, "did not start"); err != nil {
			t.Fatal(err)
		}
		if a := markAnswer(t, e.must("PUT", "/api/v1/todos/"+failed+"/someday", "")); a.SomedayUntil == nil {
			t.Fatalf("a failed start put off: %+v", a)
		}
		e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
		before = e.get(id)
		if w := e.do("PUT", "/api/v1/todos/"+id+"/someday", `{"until":"`+until.Format(time.RFC3339)+`"}`); w.Code != http.StatusConflict {
			t.Fatalf("a closed to-do put off: %d %s", w.Code, w.Body)
		}
		someday(nil)
		e.must("DELETE", "/api/v1/todos/"+id+"/someday", "")
	})
}

// The marks stay through the owner's other edits, and reopening or retrying a to-do takes it out of Someday: the owner is
// on it again (#10226).
func TestTodoMarksAcrossEdits(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		id := e.create("Renew the passport").ID
		e.must("PUT", "/api/v1/todos/"+id+"/restored", "")
		e.must("PUT", "/api/v1/todos/"+id+"/someday", "")
		e.must("PATCH", "/api/v1/todos/"+id, `{"notes":"the photo booth on the corner"}`)
		if got := e.get(id); got == nil || got.RestoredAt == nil || got.SomedayUntil == nil {
			t.Fatalf("after editing the notes: %+v", got)
		}
		e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
		if got := e.get(id); got == nil || got.RestoredAt == nil || got.SomedayUntil == nil {
			t.Fatalf("after closing it: %+v", got)
		}
		e.must("PATCH", "/api/v1/todos/"+id, `{"done":false}`)
		if got := e.get(id); got == nil || got.RestoredAt == nil || got.SomedayUntil != nil {
			t.Fatalf("after reopening it: %+v", got)
		}

		failed := e.create("Rebuild the index").ID
		if err := e.s.failTodo(context.Background(), failed, "did not start"); err != nil {
			t.Fatal(err)
		}
		e.must("PUT", "/api/v1/todos/"+failed+"/someday", "")
		if _, ok, err := e.s.reopenFailedTodo(context.Background(), failed); err != nil || !ok {
			t.Fatalf("retry: %v %v", ok, err)
		}
		if got := e.get(failed); got == nil || got.SomedayUntil != nil || got.DoneAt != nil {
			t.Fatalf("after a retry: %+v", got)
		}
	})
}

func TestTodoRestoredAndSomedaySurviveRestart(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	id := e.create("Ship the release").ID
	e.must("PUT", "/api/v1/todos/"+id+"/restored", "")
	e.must("PUT", "/api/v1/todos/"+id+"/someday", "")
	again := &todoEnv{t: t, s: newServiceOn(t, e.f, database), f: e.f}
	if got := again.get(id); got == nil || got.RestoredAt == nil || got.SomedayUntil == nil || !got.SomedayUntil.Equal(db.SomedayNoDate) {
		t.Fatalf("after a restart: %+v", got)
	}
}
