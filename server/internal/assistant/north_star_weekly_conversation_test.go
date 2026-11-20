package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestNorthStarWeeklyConversationNotFoundMarksUnknownWindows(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	reading := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 11, 2, 0, 0, 0, 0, loc), Build: "fixture"}))

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), reading)
	mustDo(t, err)
	for _, row := range weekly.Rows {
		if row.ID != "conversation_not_found" {
			continue
		}
		for j, cell := range row.Cells {
			if cell != "unknown" {
				t.Errorf("before recording, week %d = %#v, want unknown", j, cell)
			}
		}
	}

	late := time.Date(2026, 12, 15, 12, 0, 0, 0, loc)
	weekly, err = northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), late)
	mustDo(t, err)
	for _, row := range weekly.Rows {
		if row.ID != "conversation_not_found" {
			continue
		}
		for j, cell := range row.Cells {
			if cell != "unknown" {
				t.Errorf("outside retention, week %d = %#v, want unknown", j, cell)
			}
		}
	}
}

func TestNorthStarWeeklyConversationNotFoundMatchesSevenDailyReads(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), Build: "fixture"}))
	at := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	for _, event := range []db.WorkEvent{
		{At: at, MachineID: "machine", SessionID: "main-chat", Kind: sendEvent, Detail: map[string]any{"op": "resume", "origin": originManager, "role": roleMain, "outcome": outcomeFailed, "reason": "conversation_not_found"}},
		{At: at.Add(time.Minute), MachineID: "machine", SessionID: "dot-chat", Kind: sendEvent, Detail: map[string]any{"op": "resume", "origin": originManager, "role": roleDot, "outcome": outcomeFailed, "reason": "conversation_not_found"}},
		{At: at.Add(2 * time.Minute), MachineID: "machine", SessionID: "main-chat", Kind: sendEvent, Detail: map[string]any{"op": "resume", "origin": originManager, "role": roleMain, "outcome": outcomeFailed, "reason": "timeout"}},
	} {
		mustDo(t, database.AddWorkEvent(ctx, event))
	}

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), now)
	mustDo(t, err)
	var row *northStarRowOut
	for i := range weekly.Rows {
		if weekly.Rows[i].ID == "conversation_not_found" {
			row = &weekly.Rows[i]
		}
	}
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("conversation_not_found row: %+v", weekly.Rows)
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok {
		t.Fatalf("last cell has type %T, want an aggregate object", row.Cells[3])
	}
	if last["main"] != 1 || last["leaders"] != 1 || last["lower_bound"] != true {
		t.Fatalf("last week: %+v", last)
	}

	dailyTotal := 0
	for day := 28; day <= 31; day++ {
		ns, err := northStarCommand(ctx, database, hub, time.Date(2026, 10, day, 0, 0, 0, 0, loc).Format(time.DateOnly), "", loc.String(), now)
		mustDo(t, err)
		for _, failure := range ns.Availability.Failures {
			if failure.Reason == "conversation_not_found" {
				dailyTotal += failure.Count
			}
		}
	}
	for day := 1; day <= 3; day++ {
		ns, err := northStarCommand(ctx, database, hub, time.Date(2026, 11, day, 0, 0, 0, 0, loc).Format(time.DateOnly), "", loc.String(), now)
		mustDo(t, err)
		for _, failure := range ns.Availability.Failures {
			if failure.Reason == "conversation_not_found" {
				dailyTotal += failure.Count
			}
		}
	}
	if dailyTotal != 2 || last["main"].(int)+last["leaders"].(int) != dailyTotal {
		t.Fatalf("weekly total %v, seven daily reads %d", last, dailyTotal)
	}

	daily, err := northStarCommand(ctx, database, hub, "2026-11-01", "", loc.String(), now)
	mustDo(t, err)
	encoded, err := json.Marshal(daily)
	mustDo(t, err)
	if strings.Contains(string(encoded), "failures_by_role") {
		t.Fatalf("internal role grouping leaked into daily JSON: %s", encoded)
	}
}
