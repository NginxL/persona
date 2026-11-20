package assistant

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyInterruptionsPerFinishedTodo(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), Build: "fixture"}))
	closed := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)
	for i := 0; i < 10; i++ {
		row := db.TodoRow{ID: fmt.Sprintf("todo-%02d", i), Title: "synthetic", DoneAt: &closed, CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed}
		if i == 0 {
			row.Kind = db.KindConcern
			row.DoneBy = db.DoneByReported
		}
		mustDo(t, database.CreateTodo(ctx, row))
	}
	for _, row := range []db.TodoRow{
		{ID: "todo-failed", Title: "synthetic", DoneAt: &closed, StartError: "did not start", CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed},
		{ID: "todo-cancelled", Title: "synthetic", DoneAt: &closed, DoneBy: db.DoneByCancelled, CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed},
		{ID: "todo-cancelled-state", Title: "synthetic", DoneAt: &closed, CreatedAt: closed.Add(-time.Hour), UpdatedAt: closed},
		{ID: "todo-next-day", Title: "synthetic", DoneAt: &now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
	} {
		mustDo(t, database.CreateTodo(ctx, row))
	}
	mustDo(t, database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: "todo-cancelled-state", DoneWhen: []string{}, State: "cancelled"}))

	events := []db.WorkEvent{
		{At: closed, MachineID: "machine", SessionID: "worker", Kind: notifyEvent, Detail: map[string]any{"todo": "todo-00"}},
		{At: closed.Add(time.Minute), MachineID: "machine", SessionID: "worker", Kind: notifyEvent, Detail: map[string]any{"todo": "todo-01"}},
		{At: closed.Add(2 * time.Minute), MachineID: "machine", SessionID: "worker", Kind: notifyEvent, Detail: map[string]any{"todo": "todo-02"}},
		{At: closed.Add(3 * time.Minute), MachineID: "machine", SessionID: "main-chat", Kind: notifyEvent, Detail: map[string]any{}},
		{At: closed.Add(4 * time.Minute), Kind: todoReopenedEvent, Detail: map[string]any{"todo": "todo-00", "after": 86400.0}},
		{At: closed.Add(5 * time.Minute), Kind: todoReopenedEvent, Detail: map[string]any{"todo": "todo-00", "after": 100.0}},
		{At: closed.Add(6 * time.Minute), Kind: todoReopenedEvent, Detail: map[string]any{"todo": "todo-01", "after": 86401.0}},
		{At: closed.Add(7 * time.Minute), Kind: todoReopenedEvent, Detail: map[string]any{"todo": "todo-02", "after": 100.0}},
	}
	for _, event := range events {
		mustDo(t, database.AddWorkEvent(ctx, event))
	}
	for _, prior := range []Todo{
		{ID: "todo-cancelled", DoneAt: &closed, DoneBy: db.DoneByCancelled},
		{ID: "todo-failed", DoneAt: &closed, StartError: "private launcher detail"},
	} {
		retry := closed.Add(time.Hour)
		detail := reopenedDetail(&prior, db.TodoRow{UpdatedAt: retry})
		mustDo(t, database.AddWorkEvent(ctx, db.WorkEvent{At: retry, Kind: todoReopenedEvent, Detail: detail}))
	}

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), now)
	mustDo(t, err)
	var row *northStarRowOut
	for i := range weekly.Rows {
		if weekly.Rows[i].ID == "interruptions_per_finished_todo" {
			row = &weekly.Rows[i]
		}
	}
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("interruptions row: %+v", weekly.Rows)
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok {
		t.Fatalf("last cell has type %T", row.Cells[3])
	}
	want := map[string]any{
		"interruptions":       4,
		"ratio":               0.4,
		"from_tasks":          3,
		"share_from_tasks":    0.75,
		"reopened_within_24h": 2,
		"n":                   10,
	}
	for key, value := range want {
		if last[key] != value {
			t.Errorf("%s = %#v, want %#v (cell %+v)", key, last[key], value, last)
		}
	}
	early, ok := row.Cells[0].(map[string]any)
	if !ok || early["n"] != 0 || early["ratio"] != "too few" {
		t.Errorf("fewer than ten finished todos produced a ratio: %+v", row.Cells[0])
	}
	dailyTotal, dailyTasks := 0, 0
	for day := 0; day < 7; day++ {
		date := time.Date(2026, 10, 28, 0, 0, 0, 0, loc).AddDate(0, 0, day).Format(time.DateOnly)
		ns, err := northStarCommand(ctx, database, hub, date, "", loc.String(), now)
		mustDo(t, err)
		dailyTotal += ns.Interruptions.Total
		for _, category := range ns.Interruptions.Categories {
			dailyTasks += category.BySource[nsFromTask]
		}
	}
	if dailyTotal != 4 || dailyTasks != 3 || last["interruptions"] != dailyTotal || last["from_tasks"] != dailyTasks {
		t.Errorf("weekly cell %+v, seven daily reads total %d tasks %d", last, dailyTotal, dailyTasks)
	}
}
