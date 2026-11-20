package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Every timestamp contributes once to every weekly row. Private marker strings belong
// in the source tables only, including the fields that a read-only report must omit.
func seedWeeklyRows(t *testing.T, database *db.DB, instants []time.Time) {
	t.Helper()
	ctx := context.Background()
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"review":{"launcher":"fixture"},"dispatch":{"enabled":true}}}`)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: instants[0].AddDate(0, 0, -40), Build: "fixture"}))
	const path = "/synthetic/private/path"
	older := instants[0].AddDate(0, 0, -60)
	mustDo(t, database.PutSetting(ctx, agentArchivesKey, map[string]agentArchive{
		"weekly-private-archive": {Archived: true, Session: proto.Session{UpdatedAt: older}},
	}))
	var dispatches []dispatchTask
	for i, at := range instants {
		prefix := fmt.Sprintf("weekly-private-%d-", i)
		todo := prefix + "todo"
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: todo, Title: prefix + "title",
			CreatedAt: at.Add(-time.Hour), UpdatedAt: at}))
		tasks, err := json.Marshal([]PlanTask{{TodoID: todo, WorkspaceResolved: &TaskWorkspaceResolved{Path: path, Created: true}}})
		mustDo(t, err)
		mustDo(t, database.CreateTaskPlan(ctx, db.TaskPlanRow{ID: prefix + "plan", Conversation: "main-chat",
			Tasks: tasks, State: "started", CreatedAt: at, UpdatedAt: at}))
		run := prefix + "run"
		review, err := json.Marshal(AutoReview{ID: run, State: "clean", StartedAt: at, EndedAt: &at})
		mustDo(t, err)
		mustDo(t, database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: todo, DoneWhen: []string{}, AutoReview: review}))
		mustDo(t, database.LinkTodoReviewer(ctx, todo, prefix+"machine", prefix+"reviewer", prefix+"title", path, "", run, at))
		_, err = database.LinkTodoAgentWithSource(ctx, todo, prefix+"machine", prefix+"worker", prefix+"title", path, "claude", "", "worker", prefix+"leader", at)
		mustDo(t, err)
		brief := prefix + "brief"
		_, err = database.CreateTaskBrief(ctx, db.TaskBriefRow{ID: brief, Brief: json.RawMessage(`{}`), CreatedAt: at, UpdatedAt: at})
		mustDo(t, err)
		_, err = database.LinkTaskBriefTodo(ctx, db.TaskBriefTodoRow{TodoID: todo, BriefID: brief, Continues: prefix + "previous", LinkedAt: at})
		mustDo(t, err)
		_, _, err = database.ReviewDoneTodo(ctx, todo, prefix+"reason", true, at)
		mustDo(t, err)
		dispatches = append(dispatches, dispatchTask{Session: prefix + "dispatch", Cwd: path, At: at})
		for _, event := range []db.WorkEvent{
			{At: at, MachineID: prefix + "machine", SessionID: "main-chat", Kind: notifyEvent, Detail: map[string]any{"todo": todo}},
			{At: at, MachineID: prefix + "machine", SessionID: "main-chat", Kind: sendEvent, Detail: map[string]any{
				"op": "resume", "origin": originManager, "role": roleMain, "outcome": outcomeFailed, "reason": "conversation_not_found"}},
			{At: at, Kind: todoReopenedEvent, Detail: map[string]any{"todo": todo, "after": 3600}},
		} {
			mustDo(t, database.AddWorkEvent(ctx, event))
		}
	}
	mustDo(t, database.PutSetting(ctx, dispatchSettingKey, dispatches))
}

func TestNorthStarWeeklySkippedMidnight(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	loc, err := time.LoadLocation("America/Santiago")
	mustDo(t, err)
	// Sunday 00:00 does not exist. The clock jumps from Saturday 23:59 to Sunday 01:00 at 04:00 UTC.
	boundary := time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)
	seedWeeklyRows(t, database, []time.Time{boundary.Add(-15 * time.Minute), boundary, boundary.Add(15 * time.Minute)})
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, loc)
	report, err := northStarWeeklyCommand(ctx, database, hub, "2026-09-13", loc.String(), now)
	mustDo(t, err)
	previous, last := report.Weeks[2], report.Weeks[3]
	if previous.Start != "2026-08-30" || previous.End != "2026-09-05" || last.Start != "2026-09-06" || last.End != "2026-09-12" ||
		!previous.until.Equal(boundary) || !last.since.Equal(boundary) {
		t.Errorf("expected adjacent weeks at %s, got %+v and %+v", boundary, previous, last)
	}
	fields := map[string][]string{
		"interruptions_per_finished_todo": {"interruptions", "reopened_within_24h", "n"},
		"conversation_not_found":          {"main"}, "workspaces_of_closed_todos": {"created", "n"},
		"independent_review": {"runs_started"}, "workers_agent_difference": {"not_recorded", "n"},
	}
	for _, row := range report.Rows {
		if row.ID == "sessions_that_may_be_gone" {
			if snapshot := row.Snapshot.(map[string]any); snapshot["archived"] != 1 {
				t.Errorf("archived snapshot = %v, want one", snapshot)
			}
			continue
		}
		for index, want := range []int{1, 2} {
			cell := row.Cells[index+2]
			switch row.ID {
			case "dispatched_tasks":
				// Dispatch keeps only seven days from its latest entry, which cuts through the earlier week.
				expected := []any{"unknown", 2}[index]
				if cell != expected {
					t.Errorf("%s week %d = %v, want %v", row.ID, index+2, cell, expected)
				}
			case "continuations":
				if cell != want {
					t.Errorf("%s week %d = %v, want %d", row.ID, index+2, cell, want)
				}
			default:
				counts, ok := cell.(map[string]any)
				for _, key := range fields[row.ID] {
					if !ok || counts[key] != want {
						t.Errorf("%s.%s week %d = %v, want %d", row.ID, key, index+2, cell, want)
					}
				}
			}
		}
	}
	// The reading date itself can also be the skipped midnight.
	atJump, err := northStarWeeklyCommand(ctx, database, hub, "2026-09-06", loc.String(), now)
	mustDo(t, err)
	if atJump.ReadingDay != "2026-09-06" || !atJump.Weeks[3].until.Equal(boundary) {
		t.Errorf("reading day %s ends its window at %v", atJump.ReadingDay, atJump.Weeks[3].until)
	}
	// For a Thursday reading the skipped midnight is inside the week. Saturday has one event and Sunday two.
	midweek, err := northStarWeeklyCommand(ctx, database, hub, "2026-09-10", loc.String(), now)
	mustDo(t, err)
	for id, field := range map[string]string{"conversation_not_found": "main", "interruptions_per_finished_todo": "interruptions"} {
		cell := northStarWeeklyFindRow(midweek.Rows, id).Cells[3].(map[string]any)
		if cell[field] != 3 {
			t.Errorf("%s.%s for September 3–9 = %v, want 3", id, field, cell[field])
		}
	}
}
