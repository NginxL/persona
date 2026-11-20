package assistant

import (
	"context"
	"testing"
	"time"
)

func TestNorthStarWeeklyDispatchExactRetentionCutoff(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"dispatch":{"enabled":true}}}`)
	weekStart := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	latest := weekStart.AddDate(0, 0, 7)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, []dispatchTask{{At: latest}}))
	weekly, err := northStarWeeklyCommand(ctx, database, hub, latest.Format(time.DateOnly), "UTC", latest)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil || row.Cells[3] != "unknown" {
		t.Errorf("exactly seven-day-old opening instant was counted: %+v", row)
	}

	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, []dispatchTask{{At: latest.Add(-time.Nanosecond)}}))
	weekly, err = northStarWeeklyCommand(ctx, database, hub, latest.Format(time.DateOnly), "UTC", latest)
	mustDo(t, err)
	row = northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil || row.Cells[3] != 1 {
		t.Errorf("record just inside retention was suppressed: %+v", row)
	}
}
