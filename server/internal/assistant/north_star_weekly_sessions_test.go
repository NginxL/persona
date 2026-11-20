package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestNorthStarWeeklySessionsThatMayBeGone(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	old := now.Add(-31 * 24 * time.Hour)
	recent := now.Add(-29 * 24 * time.Hour)
	for _, id := range []string{"todo-claude", "todo-empty", "todo-review", "todo-recent"} {
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: id, Title: "synthetic", CreatedAt: old.Add(-time.Hour), UpdatedAt: old}))
	}
	for _, link := range []struct {
		todo, agent, source, via string
		at                       time.Time
	}{
		{todo: "todo-claude", agent: "claude", at: old},
		{todo: "todo-empty", agent: "", at: old},
		{todo: "todo-review", agent: "claude", source: sourceReview, via: "rv-old", at: old},
		{todo: "todo-recent", agent: "claude", at: recent},
	} {
		_, err := database.LinkTodoAgentWithSource(ctx, link.todo, "machine", "session-"+link.todo, "synthetic", "/synthetic/private/path", link.agent, "", link.source, link.via, link.at)
		mustDo(t, err)
	}
	archives := map[string]agentArchive{
		"old":      {Archived: true, Session: proto.Session{UpdatedAt: old}},
		"recent":   {Archived: true, Session: proto.Session{UpdatedAt: recent}},
		"restored": {Archived: false, Session: proto.Session{UpdatedAt: old}},
	}
	mustDo(t, database.PutSetting(ctx, agentArchivesKey, archives))

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "sessions_that_may_be_gone")
	if row == nil {
		t.Fatalf("sessions row is missing: %+v", weekly.Rows)
	}
	if len(row.Cells) != 0 {
		t.Errorf("the reading-time snapshot was repeated by week: %+v", row.Cells)
	}
	snapshot, ok := row.Snapshot.(map[string]any)
	if !ok {
		t.Fatalf("snapshot has type %T", row.Snapshot)
	}
	want := map[string]any{"claude": 1, "agent_empty": 1, "reviewers": 1, "archived": 1, "upper_bound": true}
	for key, value := range want {
		if snapshot[key] != value {
			t.Errorf("%s = %#v, want %#v (snapshot %+v)", key, snapshot[key], value, snapshot)
		}
	}
}
