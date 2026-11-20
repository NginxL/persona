package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func setNorthStarWeeklyOwnConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", home)
	dir := filepath.Join(home, "managers", "assistant")
	mustDo(t, os.MkdirAll(dir, 0o700))
	mustDo(t, os.WriteFile(filepath.Join(dir, "manager.json"), []byte(body), 0o600))
}

func TestNorthStarWeeklyIndependentReview(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"review":{"launcher":"fixture"}}}`)
	loc, err := time.LoadLocation("America/New_York")
	mustDo(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, loc)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), Build: "fixture"}))
	started := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)
	ended := started.Add(30 * time.Minute)
	for _, fixture := range []struct {
		todo, run, state string
		link             bool
	}{
		{todo: "todo-clean", run: "rv-clean", state: "clean", link: true},
		{todo: "todo-block", run: "rv-block", state: "blocking", link: true},
		{todo: "todo-failed", run: "rv-failed", state: "failed", link: false},
	} {
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: fixture.todo, Title: "synthetic", CreatedAt: started.Add(-time.Hour), UpdatedAt: started}))
		raw, err := json.Marshal(AutoReview{ID: fixture.run, State: fixture.state, StartedAt: started, EndedAt: &ended})
		mustDo(t, err)
		mustDo(t, database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: fixture.todo, DoneWhen: []string{}, AutoReview: raw}))
		if fixture.link {
			mustDo(t, database.LinkTodoReviewer(ctx, fixture.todo, "machine", "session-"+fixture.run, "synthetic", "/synthetic/private/path", "", fixture.run, started))
		}
	}
	old := time.Date(2026, 10, 27, 12, 0, 0, 0, loc)
	mustDo(t, database.LinkTodoReviewer(ctx, "todo-clean", "machine", "session-old", "synthetic", "/synthetic/private/path", "", "rv-old", old))

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", loc.String(), now)
	mustDo(t, err)
	var row *northStarRowOut
	for i := range weekly.Rows {
		if weekly.Rows[i].ID == "independent_review" {
			row = &weekly.Rows[i]
		}
	}
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("independent review row: %+v", weekly.Rows)
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok {
		t.Fatalf("last cell has type %T", row.Cells[3])
	}
	results, ok := last["by_result"].(map[string]int)
	if !ok || results["clean"] != 1 || results["blocking"] != 1 || results["failed"] != 1 || len(results) != 3 {
		t.Errorf("latest results: %+v", last["by_result"])
	}
	if last["runs_started"] != 3 || last["lower_bound"] != true {
		t.Errorf("latest week: %+v", last)
	}
	previous, ok := row.Cells[2].(map[string]any)
	if !ok || previous["runs_started"] != 1 {
		t.Fatalf("previous week: %+v", row.Cells[2])
	}
	if results, ok := previous["by_result"].(map[string]int); !ok || len(results) != 0 {
		t.Errorf("a replaced run became a result: %+v", previous["by_result"])
	}
}

func TestNorthStarWeeklyIndependentReviewCountsRunOnceAcrossWeeks(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{"agents":{"review":{"launcher":"fixture"}}}`)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	started := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	linked := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	mustDo(t, database.PutSetting(ctx, northStarSinceKey, northStarSince{At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Build: "fixture"}))
	for _, todo := range []string{"todo-stored", "todo-link-only"} {
		mustDo(t, database.CreateTodo(ctx, db.TodoRow{ID: todo, Title: "synthetic", CreatedAt: started.Add(-time.Hour), UpdatedAt: started}))
	}
	ended := started.Add(30 * time.Minute)
	raw, err := json.Marshal(AutoReview{ID: "rv-cross-week", State: "clean", StartedAt: started, EndedAt: &ended})
	mustDo(t, err)
	mustDo(t, database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: "todo-stored", DoneWhen: []string{}, AutoReview: raw}))
	mustDo(t, database.LinkTodoReviewer(ctx, "todo-stored", "machine", "session-cross-week", "synthetic", "/synthetic/private/path", "", "rv-cross-week", linked))
	mustDo(t, database.LinkTodoReviewer(ctx, "todo-link-only", "machine", "session-link-only", "synthetic", "/synthetic/private/path", "", "rv-link-only", linked))

	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	row := northStarWeeklyFindRow(weekly.Rows, "independent_review")
	if row == nil || len(row.Cells) != 4 {
		t.Fatalf("independent review row: %+v", weekly.Rows)
	}
	previous, ok := row.Cells[2].(map[string]any)
	if !ok || previous["runs_started"] != 1 {
		t.Errorf("stored start week: %+v", row.Cells[2])
	}
	last, ok := row.Cells[3].(map[string]any)
	if !ok || last["runs_started"] != 1 {
		t.Errorf("link week must contain only the link-only run: %+v", row.Cells[3])
	}
}

func TestNorthStarWeeklyIndependentReviewNotEnabled(t *testing.T) {
	ctx := context.Background()
	_, database, hub, _ := northStarWeeklyDB(t)
	setNorthStarWeeklyOwnConfig(t, `{}`)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	weekly, err := northStarWeeklyCommand(ctx, database, hub, "2026-11-04", "UTC", now)
	mustDo(t, err)
	for _, row := range weekly.Rows {
		if row.ID != "independent_review" {
			continue
		}
		for _, cell := range row.Cells {
			if cell != "not enabled" {
				t.Fatalf("disabled review cell = %#v", cell)
			}
		}
		return
	}
	t.Fatal("independent_review row is missing")
}
