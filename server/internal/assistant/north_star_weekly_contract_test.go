package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyContractAndReadOnly(t *testing.T) {
	ctx := context.Background()
	_, database, _, dsn := northStarWeeklyDB(t)
	seedWeeklyRows(t, database, []time.Time{time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)})
	ro, err := attachReadOnly(ctx, dsn, db.ManagerSet)
	mustDo(t, err)
	t.Cleanup(ro.Close)
	if err := ro.PutSetting(ctx, "north-star-weekly-write-probe", 1); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("weekly connection could write: %v", err)
	}
	weekly, err := northStarWeeklyCommand(ctx, ro, ro, "2026-11-04", "UTC", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	mustDo(t, err)
	wantIDs := []string{
		"interruptions_per_finished_todo", "workspaces_of_closed_todos", "independent_review", "dispatched_tasks",
		"conversation_not_found", "continuations", "sessions_that_may_be_gone", "workers_agent_difference",
	}
	if len(weekly.Rows) != len(wantIDs) {
		t.Fatalf("got %d rows, want %d: %+v", len(weekly.Rows), len(wantIDs), weekly.Rows)
	}
	for i, id := range wantIDs {
		row := weekly.Rows[i]
		if row.ID != id {
			t.Errorf("row %d = %q, want %q", i, row.ID, id)
		}
		if id == "sessions_that_may_be_gone" {
			if len(row.Cells) != 0 || row.Snapshot == nil {
				t.Errorf("sessions row is not one snapshot: %+v", row)
			}
		} else if len(row.Cells) != 4 {
			t.Errorf("%s has %d cells, want 4", id, len(row.Cells))
		}
	}
	encoded, err := json.Marshal(weekly)
	mustDo(t, err)
	for _, private := range []string{"main-chat", "dot-chat", "/synthetic/private/path", "weekly-private-"} {
		if strings.Contains(string(encoded), private) {
			t.Errorf("weekly output contains %q: %s", private, encoded)
		}
	}
}

func TestNorthStarWeeklyMissingSchemaIsUnknown(t *testing.T) {
	for _, fixture := range []struct {
		name, sql, config, row string
	}{
		{name: "continuations", sql: `DROP TABLE task_brief_todos`, config: `{}`, row: "continuations"},
		{name: "review", sql: `ALTER TABLE todo_supervision DROP COLUMN auto_review`, config: `{"agents":{"review":{"launcher":"fixture"}}}`, row: "independent_review"},
		{name: "finished", sql: `ALTER TABLE todos DROP COLUMN start_error`, config: `{}`, row: "interruptions_per_finished_todo"},
		{name: "agents", sql: `ALTER TABLE todo_agents DROP COLUMN source`, config: `{"agents":{"review":{"launcher":"fixture"}}}`, row: "workers_agent_difference"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			_, database, hub, dsn := northStarWeeklyDB(t)
			setNorthStarWeeklyOwnConfig(t, fixture.config)
			conn, err := pgx.Connect(ctx, dsn)
			mustDo(t, err)
			_, err = conn.Exec(ctx, fixture.sql)
			mustDo(t, err)
			mustDo(t, conn.Close(ctx))
			weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
			mustDo(t, err)
			row := northStarWeeklyFindRow(weekly.Rows, fixture.row)
			if row == nil {
				t.Fatalf("%s row is missing", fixture.row)
			}
			for _, cell := range row.Cells {
				if cell != "unknown" {
					t.Errorf("cell = %#v, want unknown", cell)
				}
			}
		})
	}
}
