package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyUsesRequestedZoneForDailyReads(t *testing.T) {
	ctx := context.Background()
	s, database, hub, _ := northStarWeeklyDB(t)
	setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	at := time.Date(2026, 10, 27, 23, 30, 0, 0, loc) // 2026-10-28 03:30 UTC: the previous week in the requested zone.
	mustDo(t, database.AddWorkEvent(ctx, db.WorkEvent{At: at, MachineID: "machine", SessionID: "main-chat", Kind: sendEvent, Detail: map[string]any{
		"op": "resume", "origin": originManager, "role": roleMain, "outcome": outcomeFailed, "reason": "conversation_not_found",
	}}))
	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "conversation_not_found")
	if row == nil {
		t.Fatal("conversation_not_found row is missing")
	}
	previous, ok := row.Cells[2].(map[string]any)
	if !ok || previous["main"] != 1 {
		t.Errorf("requested-zone previous week = %#v, want main 1", row.Cells[2])
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok || last["main"] != 0 {
		t.Errorf("requested-zone last week = %#v, want main 0", row.Cells[3])
	}
}

func TestNorthStarWeeklyDispatchCoveredAfterMidnight(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"dispatch":{"enabled":true}}}`)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, []dispatchTask{
		{At: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
		{At: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)},
	}))
	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil || row.Cells[3] != 2 {
		t.Errorf("covered non-midnight week = %#v, want 2", row)
	}

	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, []dispatchTask{{At: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}}))
	weekly, err = northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row = northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil || row.Cells[3] != "unknown" {
		t.Errorf("a later list that pruned the reading week = %#v, want unknown", row)
	}

	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, []dispatchTask{}))
	weekly, err = northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row = northStarWeeklyFindRow(weekly.Rows, "dispatched_tasks")
	if row == nil {
		t.Fatal("dispatched_tasks row is missing")
	}
	for i, cell := range row.Cells {
		if cell != 0 {
			t.Errorf("empty dispatch history week %d = %#v, want 0", i, cell)
		}
	}
}

func TestNorthStarWeeklyRetentionCutoffInsideWeek(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) // The retention cutoff is inside the first week.
	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	for _, id := range []string{"interruptions_per_finished_todo", "conversation_not_found"} {
		row := northStarWeeklyFindRow(weekly.Rows, id)
		if row == nil {
			t.Fatalf("%s row is missing", id)
		}
		if row.Cells[0] != "unknown" {
			t.Errorf("%s partially retained week = %#v, want unknown", id, row.Cells[0])
		}
		if row.Cells[1] == "unknown" {
			t.Errorf("%s fully retained week was suppressed: %#v", id, row.Cells[1])
		}
	}
}
