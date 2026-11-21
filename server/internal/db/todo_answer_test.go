package db

import (
	"context"
	"testing"
	"time"
)

func TestCommitTodoAnswerRollsBackWholeSetAndRetriesIdempotently(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Now().UTC()
	if err := d.CreateTodo(ctx, TodoRow{ID: "todo_answer", Title: "Choose", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	first := TodoDecisionRow{ID: "answer_first", TodoID: "todo_answer", Question: "First?", Choice: "Yes",
		Source: DecisionManual, SourceMessage: "inline:first", CreatedAt: at.Add(time.Second)}
	second := first
	second.Question, second.SourceMessage = "Second?", "inline:second"
	settings := map[string]any{"assistant.test_answer": map[string]any{"answered": true}}
	// The second row conflicts on its primary key, after the first insert has run.
	if _, _, err := d.CommitTodoAnswer(ctx, first.TodoID, []TodoDecisionRow{first, second}, settings, first.CreatedAt); err == nil {
		t.Fatal("a failed second insert must roll back acceptance")
	}
	rows, err := d.ListTodoDecisions(ctx, first.TodoID, true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("no partial decisions may survive: rows=%+v err=%v", rows, err)
	}
	var stored map[string]any
	if found, err := d.Setting(ctx, "assistant.test_answer", &stored); err != nil || found {
		t.Fatalf("delivery settings must roll back too: found=%t err=%v", found, err)
	}
	todos, err := d.ListTodos(ctx)
	if err != nil || len(todos) != 1 || todos[0].DoneAt != nil {
		t.Fatalf("the rejected answer must leave the to-do open: todos=%+v err=%v", todos, err)
	}
	second.ID = "answer_second"
	for attempt, wantInserted := range []int{2, 0} {
		row, inserted, err := d.CommitTodoAnswer(ctx, first.TodoID, []TodoDecisionRow{first, second}, settings, first.CreatedAt)
		if err != nil || row == nil || row.DoneAt == nil || len(inserted) != wantInserted {
			t.Fatalf("attempt %d must atomically close with idempotent decisions: row=%+v inserted=%d err=%v",
				attempt, row, len(inserted), err)
		}
	}
	if rows, err := d.ListTodoDecisions(ctx, first.TodoID, true); err != nil || len(rows) != 2 {
		t.Fatalf("an identical retry must not duplicate decisions: rows=%+v err=%v", rows, err)
	}
}

func TestCommitTodoAnswerDoesNotReopenOrRecreateClaimedTodo(t *testing.T) {
	for _, action := range []string{"closed", "deleted"} {
		t.Run(action, func(t *testing.T) {
			d, ctx := open(t), context.Background()
			at := time.Now().UTC().Truncate(time.Microsecond)
			todo := TodoRow{ID: "todo_answer", Title: "Choose", CreatedAt: at, UpdatedAt: at}
			if action == "closed" {
				todo.DoneAt = &at
			}
			if err := d.CreateTodo(ctx, todo); err != nil {
				t.Fatal(err)
			}
			if action == "deleted" {
				if err := d.DeleteTodo(ctx, todo.ID); err != nil {
					t.Fatal(err)
				}
			}
			decision := TodoDecisionRow{ID: "answer", TodoID: todo.ID, Question: "Which?", Choice: "Yes",
				Source: DecisionManual, SourceMessage: "inline:answer", CreatedAt: at.Add(time.Second)}
			row, _, err := d.CommitTodoAnswer(ctx, todo.ID, []TodoDecisionRow{decision},
				map[string]any{"assistant.test_answer": true}, decision.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if action == "deleted" && row != nil || action == "closed" && (row == nil || row.DoneAt == nil || !row.DoneAt.Equal(at)) {
				t.Fatalf("accepting a claimed answer must preserve a %s to-do: %+v", action, row)
			}
			var accepted bool
			if found, err := d.Setting(ctx, "assistant.test_answer", &accepted); err != nil || !found || !accepted {
				t.Fatalf("the claimed answer still needs durable delivery: found=%t accepted=%t err=%v", found, accepted, err)
			}
		})
	}
}
