package assistant

import (
	"context"
	"testing"
	"time"
)

func TestNorthStarWeeklyDispatchedTasksCoverage(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"dispatch":{"enabled":true}}}`)
	loc := time.UTC
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), Build: "fixture"}))
	tasks := []dispatchTask{
		{At: time.Date(2026, 10, 27, 12, 0, 0, 0, loc)},
		{At: time.Date(2026, 10, 28, 0, 0, 0, 0, loc)},
		{At: time.Date(2026, 11, 1, 12, 0, 0, 0, loc)},
		{At: time.Date(2026, 11, 2, 12, 0, 0, 0, loc)},
	}
	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, tasks))
	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil {
		t.Fatalf("dispatched_tasks row is missing: %+v", weekly.Rows)
	}
	for i := 0; i < 3; i++ {
		if row.Cells[i] != "unknown" {
			t.Errorf("uncovered week %d = %#v, want unknown", i, row.Cells[i])
		}
	}
	if row.Cells[3] != 3 {
		t.Errorf("covered week = %#v, want 3", row.Cells[3])
	}

	capped := make([]dispatchTask, dispatchKeepMost)
	for i := range capped {
		capped[i].At = time.Date(2026, 11, 1, 0, 0, 0, 0, loc).Add(time.Duration(i) * time.Minute)
	}
	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, capped))
	weekly, err = northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row = northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil || row.Cells[3] != "unknown" {
		t.Errorf("a truncated list became a complete count: %+v", row)
	}
}

func TestNorthStarWeeklyDispatchedTasksConfigurationStates(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		name, config, want string
	}{
		{name: "disabled", config: `{}`, want: "not enabled"},
		{name: "unreadable", config: `{`, want: "unknown"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			setNorthStarWeeklyOwnConfig(t, fixture.config)
			weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
			mustDo(t, err)
			row := northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
			if row == nil {
				t.Fatal("dispatched_tasks row is missing")
			}
			for _, cell := range row.Cells {
				if cell != fixture.want {
					t.Errorf("cell = %#v, want %q", cell, fixture.want)
				}
			}
		})
	}
}

func northStarWeeklyFindRow(rows []northStarRowOut, id string) *northStarRowOut {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}
