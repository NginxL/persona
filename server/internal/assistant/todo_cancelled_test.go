package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Cancellation is its own outcome in the API even though older rows store it as done_by=cancelled and newer clients
// still need done_by=review for compatibility (#5732).
func TestCancelledTodoCarriesItsTimeAndReason(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "cancelled", "note": "用户改期，不再本轮做"})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	stored := todoNow(s, todo.ID)
	if stored.DoneBy != db.DoneByCancelled || stored.DoneAt == nil {
		t.Fatalf("stored cancellation = %+v", stored)
	}
	got := listedTodos(t, s)[todo.ID]
	if got["done_by"] != db.DoneByReview || got["cancelled_reason"] != "用户改期，不再本轮做" {
		t.Fatalf("listed cancellation = %+v", got)
	}
	if got["cancelled_at"] == nil || got["cancelled_at"] != got["done_at"] {
		t.Fatalf("cancelled_at %v, done_at %v", got["cancelled_at"], got["done_at"])
	}
	for _, task := range s.snapshotTasks(context.Background(), "https://hub.example.com") {
		if task.ID == todo.ID {
			if task.State != "已取消" || task.CancelledReason != "用户改期，不再本轮做" {
				t.Fatalf("snapshot cancellation = %+v", task)
			}
			return
		}
	}
	t.Fatal("cancelled task is missing from the snapshot")
}

// Legacy rows predate the stored completion marker: their supervision verdict still has to project the additive
// cancellation fields at the API boundary, including the existing review-note fallback (#5732).
func TestLegacyCancelledTodoCarriesItsTimeAndReason(t *testing.T) {
	s, _, _ := newSupervisedService(t)
	done := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	putTodo(s, Todo{ID: "legacy_cancelled", Title: "旧方案停止", Origin: "conv1", DoneAt: &done, DoneBy: db.DoneByReview,
		Supervision: &Supervision{Supervisor: "conv1", State: reviewCancelled,
			Reviews: []TaskReview{{Verdict: reviewCancelled, Note: "由新方案接手"}}}})
	got := listedTodos(t, s)["legacy_cancelled"]
	if got["done_by"] != db.DoneByReview || got["cancelled_reason"] != "由新方案接手" {
		t.Fatalf("legacy listed cancellation = %+v", got)
	}
	if got["cancelled_at"] == nil || got["cancelled_at"] != got["done_at"] {
		t.Fatalf("legacy cancelled_at %v, done_at %v", got["cancelled_at"], got["done_at"])
	}
}

// Reopening clears the completion marker but deliberately retains the supervision ledger. A later explicit completion
// must win over that old cancelled verdict; only a compatible legacy review marker may use the verdict as cancellation.
func TestCancelledReopenedAndCompletedIsNotCancelled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		doneBy string
		finish func(*testing.T, *Service, string)
	}{
		{name: "user", doneBy: "", finish: func(t *testing.T, s *Service, id string) {
			done := true
			if _, err := s.editTodo(context.Background(), id, db.TodoEdit{Done: &done}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "automatic", doneBy: db.DoneByAuto, finish: func(t *testing.T, s *Service, id string) {
			if ok, err := s.autoDoneTodo(context.Background(), id, "finished after reopening", time.Now()); err != nil || !ok {
				t.Fatalf("automatic completion: ok %v, err %v", ok, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, todo := newSupervisedService(t)
			raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "cancelled", "note": "先取消"})
			if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
				t.Fatal(err)
			}
			reopen := false
			if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Done: &reopen}); err != nil {
				t.Fatal(err)
			}
			if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.DoneBy != "" || cur.Supervision.State != reviewCancelled {
				t.Fatalf("the reopen fixture did not retain only the old supervision verdict: %+v", cur)
			}
			tc.finish(t, s, todo.ID)
			stored := todoNow(s, todo.ID)
			if stored.DoneAt == nil || stored.DoneBy != tc.doneBy {
				t.Fatalf("completion after reopening = %+v", stored)
			}
			if stored.Cancelled() {
				t.Errorf("the old cancelled verdict reclassified the new %s completion", tc.name)
			}
			got := listedTodos(t, s)[todo.ID]
			if got["cancelled_at"] != nil {
				t.Errorf("the API derived a stale cancelled_at after %s completion: %+v", tc.name, got)
			}
			if doneBy, _ := got["done_by"].(string); doneBy != tc.doneBy {
				t.Errorf("the API rewrote done_by after %s completion: got %q, want %q", tc.name, doneBy, tc.doneBy)
			}
		})
	}
}
