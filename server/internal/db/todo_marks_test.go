package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The owner's restore and Someday (#10226): stored and read back by every read, kept by an edit of anything else, and
// neither an edit (updated_at stays). Someday puts off only an open to-do or a failed start; taking it out works on any.
func TestTodoRestoredAndSomeday(t *testing.T) {
	d, ctx := open(t), context.Background()
	base := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"todo_open", "todo_closed", "todo_failed"} {
		if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: "Plan the next release", CreatedAt: base, UpdatedAt: base}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := d.GetTodo(ctx, "todo_open"); err != nil || got.RestoredAt != nil || got.SomedayUntil != nil {
		t.Fatalf("a new to-do: %+v %v", got, err)
	}

	restored := base.Add(time.Hour)
	row, err := d.RestoreTodo(ctx, "todo_open", restored)
	if err != nil || row.RestoredAt == nil || !row.RestoredAt.Equal(restored) || !row.UpdatedAt.Equal(base) {
		t.Fatalf("restored: %+v %v", row, err)
	}
	until := base.Add(72 * time.Hour)
	if row, err = d.SetTodoSomeday(ctx, "todo_open", &until); err != nil || row.SomedayUntil == nil ||
		!row.SomedayUntil.Equal(until) || !row.RestoredAt.Equal(restored) || !row.UpdatedAt.Equal(base) {
		t.Fatalf("put off: %+v %v", row, err)
	}
	if got, err := d.GetTodo(ctx, "todo_open"); err != nil || got.RestoredAt == nil || !got.RestoredAt.Equal(restored) ||
		got.SomedayUntil == nil || !got.SomedayUntil.Equal(until) {
		t.Fatalf("read back: %+v %v", got, err)
	}
	list, err := d.ListTodos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if (r.ID == "todo_open") != (r.SomedayUntil != nil) || (r.ID == "todo_open") != (r.RestoredAt != nil) {
			t.Fatalf("listed: %+v", r)
		}
	}
	notes := "draft the notes"
	if row, err = d.EditTodo(ctx, "todo_open", TodoEdit{Notes: &notes}, base.Add(2*time.Hour)); err != nil ||
		row.RestoredAt == nil || row.SomedayUntil == nil {
		t.Fatalf("an edit of the notes lost the marks: %+v %v", row, err)
	}
	noDate := SomedayNoDate
	if row, err = d.SetTodoSomeday(ctx, "todo_open", &noDate); err != nil || row.SomedayUntil == nil || !row.SomedayUntil.Equal(SomedayNoDate) {
		t.Fatalf("put off with no date: %+v %v", row, err)
	}
	if row, err = d.SetTodoSomeday(ctx, "todo_open", nil); err != nil || row.SomedayUntil != nil || row.RestoredAt == nil {
		t.Fatalf("taken out of Someday: %+v %v", row, err)
	}

	done := true
	if _, err := d.UpdateTodo(ctx, "todo_closed", nil, nil, &done, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetTodoSomeday(ctx, "todo_closed", &until); !errors.Is(err, ErrTodoCompleted) {
		t.Fatalf("a closed to-do put off: %v", err)
	}
	if got, err := d.GetTodo(ctx, "todo_closed"); err != nil || got.SomedayUntil != nil {
		t.Fatalf("a refused Someday was stored: %+v %v", got, err)
	}
	if row, err = d.SetTodoSomeday(ctx, "todo_closed", nil); err != nil || row.SomedayUntil != nil {
		t.Fatalf("taking a closed to-do out of Someday: %+v %v", row, err)
	}
	if _, err := d.FailTodo(ctx, "todo_failed", "no launcher", base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if row, err = d.SetTodoSomeday(ctx, "todo_failed", &until); err != nil || row.SomedayUntil == nil {
		t.Fatalf("a failed start put off: %+v %v", row, err)
	}

	if _, err := d.RestoreTodo(ctx, "todo_gone", restored); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("restoring an unknown to-do: %v", err)
	}
	for _, u := range []*time.Time{&until, nil} {
		if _, err := d.SetTodoSomeday(ctx, "todo_gone", u); !errors.Is(err, ErrTodoNotFound) {
			t.Fatalf("Someday on an unknown to-do (%v): %v", u, err)
		}
	}
}
