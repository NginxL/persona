package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyWorkspacesOfClosedTodos(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), Build: "fixture"}))
	closed := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)
	categories := []string{"created", "reused", "shared", "existing", "none"}
	tasks := make([]PlanTask, 0, len(categories))
	for _, category := range categories {
		id := "todo-" + category
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: id, Title: "synthetic", PlanID: "plan-workspace", DoneAt: &closed, CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed}))
		task := PlanTask{TodoID: id, WorkspaceResolved: &TaskWorkspaceResolved{Path: "/synthetic/private/path"}}
		switch category {
		case "created":
			task.Workspace = &TaskWorkspace{Mode: workspaceNew}
			task.WorkspaceResolved.Created = true
		case "reused":
			task.Workspace = &TaskWorkspace{Mode: workspaceNew}
			task.WorkspaceResolved.Reused = true
		case "shared":
			task.Workspace = &TaskWorkspace{Mode: workspaceNew}
			task.WorkspaceResolved.Shared = true
		case "existing":
			task.Workspace = &TaskWorkspace{Mode: workspaceExisting, Existing: "/synthetic/private/path"}
		case "none":
			task.Workspace = &TaskWorkspace{Mode: workspaceNone}
			task.WorkspaceResolved.Path = ""
		}
		tasks = append(tasks, task)
	}
	mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: "todo-unrecorded", Title: "synthetic", PlanID: "missing-plan", DoneAt: &closed, CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed}))
	raw, err := json.Marshal(tasks)
	mustDo(t, err)
	mustDo(t, database.CreateTaskPlan(ctx, db.TaskPlanRow{ID: "plan-workspace", Conversation: "main-chat", Tasks: raw, State: "started", CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed}))
	// The other plan was created later but ran first. Taking over cleared its execution binding only.
	former, err := json.Marshal([]PlanTask{{ReuseTodo: "todo-created", ReservedTodoID: "todo-created",
		Workspace: &TaskWorkspace{Mode: workspaceExisting}, WorkspaceResolved: &TaskWorkspaceResolved{Path: "/private/former"}}})
	mustDo(t, err)
	mustDo(t, database.CreateTaskPlan(ctx, db.TaskPlanRow{ID: "former-plan", Conversation: "main-chat", Tasks: former, State: "started", CreatedAt: closed, UpdatedAt: closed}))

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), now)
	mustDo(t, err)
	var row *northStarRowOut
	for i := range weekly.Rows {
		if weekly.Rows[i].ID == "workspaces_of_closed_todos" {
			row = &weekly.Rows[i]
		}
	}
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("workspaces row: %+v", weekly.Rows)
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok {
		t.Fatalf("last cell has type %T", row.Cells[3])
	}
	total := 0
	for _, key := range categories {
		if last[key] != 1 {
			t.Errorf("%s = %#v, want 1", key, last[key])
		}
		total += last[key].(int)
	}
	total += last["not_recorded"].(int)
	if total != 6 || last["n"] != 6 || last["still_there_and_clean"] != "not probed" {
		t.Errorf("workspace buckets do not add up: %+v", last)
	}
	encoded, err := json.Marshal(row)
	mustDo(t, err)
	for _, private := range []string{"todo-created", "plan-workspace", "/synthetic/private/path"} {
		if strings.Contains(string(encoded), private) {
			t.Errorf("aggregate output contains %q: %s", private, encoded)
		}
	}
}
