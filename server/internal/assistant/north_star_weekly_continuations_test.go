package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyContinuations(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"todo-before", "todo-one", "todo-two", "todo-independent"} {
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: id, Title: "synthetic", CreatedAt: at.Add(-time.Hour), UpdatedAt: at}))
	}
	for _, id := range []string{"brief-one", "brief-two", "brief-independent"} {
		_, err := database.CreateTaskBrief(ctx, db.TaskBriefRow{ID: id, Brief: json.RawMessage(`{}`), CreatedAt: at.Add(-time.Hour), UpdatedAt: at})
		mustDo(t, err)
	}
	for _, link := range []db.TaskBriefTodoRow{
		{TodoID: "todo-one", BriefID: "brief-one", Continues: "todo-before", LinkedAt: at},
		{TodoID: "todo-two", BriefID: "brief-two", Continues: "todo-one", LinkedAt: at.Add(time.Minute)},
		{TodoID: "todo-independent", BriefID: "brief-independent", LinkedAt: at.Add(2 * time.Minute)},
	} {
		_, err := database.LinkTaskBriefTodo(ctx, link)
		mustDo(t, err)
	}

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "continuations")
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("continuations row: %+v", weekly.Rows)
	}
	if row.Cells[3] != 2 {
		t.Errorf("last week = %#v, want 2", row.Cells[3])
	}
	for i := 0; i < 3; i++ {
		if row.Cells[i] != "not recorded" {
			t.Errorf("zero continuation week %d = %#v, want not recorded", i, row.Cells[i])
		}
	}
}
