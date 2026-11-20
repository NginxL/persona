package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyWorkersAgentDifference(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"leader-same", "leader-different", "leader-empty", "worker-same", "worker-different", "worker-empty", "worker-missing", "worker-leader-empty"} {
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: id, Title: "synthetic", CreatedAt: at.Add(-time.Hour), UpdatedAt: at}))
	}
	for _, link := range []struct {
		todo, agent, via string
	}{
		{todo: "leader-same", agent: "claude"},
		{todo: "leader-different", agent: "codex"},
		{todo: "leader-empty", agent: ""},
	} {
		_, err := database.LinkTodoAgentWithSource(ctx, link.todo, "machine", "session-"+link.todo, "synthetic", "/synthetic/private/path", link.agent, "", "leader", "", at)
		mustDo(t, err)
	}
	for _, link := range []struct {
		todo, agent, via string
	}{
		{todo: "worker-same", agent: "claude", via: "leader-same"},
		{todo: "worker-different", agent: "claude", via: "leader-different"},
		{todo: "worker-empty", agent: "", via: "leader-same"},
		{todo: "worker-missing", agent: "claude", via: "leader-missing"},
		{todo: "worker-leader-empty", agent: "claude", via: "leader-empty"},
	} {
		_, err := database.LinkTodoAgentWithSource(ctx, link.todo, "machine", "session-"+link.todo, "synthetic", "/synthetic/private/path", link.agent, "", "worker", link.via, at)
		mustDo(t, err)
	}

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "workers_agent_difference")
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("workers row: %+v", weekly.Rows)
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok {
		t.Fatalf("last cell has type %T", row.Cells[3])
	}
	want := map[string]any{"same": 1, "different": 1, "not_recorded": 3, "n": 5}
	for key, value := range want {
		if last[key] != value {
			t.Errorf("%s = %#v, want %#v (cell %+v)", key, last[key], value, last)
		}
	}
	for i := 0; i < 3; i++ {
		if row.Cells[i] != "not recorded" {
			t.Errorf("week without workers %d = %#v, want not recorded", i, row.Cells[i])
		}
	}
}
