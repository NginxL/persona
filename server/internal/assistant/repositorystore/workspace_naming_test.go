package repositorystore

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingPersistenceCASAndOperationSnapshot(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := d.CreateTodo(ctx, db.TodoRow{ID: "naming", Title: "Original", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			_, err := d.SetTodoWorkspaceRules(ctx, "naming", 0, json.RawMessage(`{"branch_template":"custom/{task-id}"}`))
			results <- err
		})
	}
	group.Wait()
	close(results)
	passed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if err == repository.Problem("revision_conflict") {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflicts != 1 {
		t.Fatal(passed, conflicts)
	}
	record, err := d.TodoWorkspaceRules(ctx, "naming")
	if err != nil || record.Revision != 1 {
		t.Fatal(record, err)
	}
	snapshot, err := repository.Snapshot("assistant", "naming", 0, nil, []string{})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := workspacenaming.Effective(workspacenaming.Rules{}, record.Rules)
	source := workspacenaming.Source{Manager: "assistant", TodoID: "naming", Title: "Original", MachineID: "computer", PlannedAt: now, Timezone: "UTC", Rules: rules, Layout: repository.LayoutContainer, Repositories: snapshot}
	preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"})
	if preview.Plan == nil {
		t.Fatal(preview)
	}
	plan, err := d.SaveWorkspaceNamingPlan(ctx, "naming", "operation-one", record.Revision, *preview.Plan)
	if err != nil {
		t.Fatal(err)
	}
	// Neither an operation snapshot nor its preparation consumer seals business metadata.
	if _, err = d.SetTodoWorkspaceRules(ctx, "naming", 1, json.RawMessage(`{"branch_template":"next/{task-id}"}`)); err != nil {
		t.Fatal(err)
	}
	title := "Tomorrow"
	if _, err = d.EditTodo(ctx, "naming", db.TodoEdit{Title: &title}, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var adopted workspacenaming.Plan
	err = d.Transaction(ctx, func(tx pgx.Tx) error {
		var err error
		adopted, err = ReadWorkspaceNamingPlanTx(ctx, tx, "naming", "operation-one")
		return err
	})
	if err != nil || adopted.Digest != plan.Digest {
		t.Fatal(adopted, err)
	}
	if adopted.Source.Title != "Original" || !adopted.Source.PlannedAt.Equal(now) {
		t.Fatal("operation input recomputed", adopted)
	}
	// A stale candidate cannot be saved after an override edit.
	if _, err = d.SaveWorkspaceNamingPlan(ctx, "naming", "operation-two", 1, *preview.Plan); err != repository.Problem("revision_conflict") {
		t.Fatal(err)
	}
	if _, err = d.SetTodoWorkspaceRules(ctx, "naming", 2, json.RawMessage(`{"branch_template":""}`)); err == nil {
		t.Fatal("empty override accepted")
	}
	got, _ := d.TodoWorkspaceRules(ctx, "naming")
	if got.Revision != 2 {
		t.Fatal("partial write", got)
	}
	if err = d.DeleteTodo(ctx, "naming"); err != nil {
		t.Fatal(err)
	}
	if got, err := d.WorkspaceNamingPlan(ctx, "naming", "operation-one"); err != nil || got != nil {
		t.Fatal("orphan", got, err)
	}
}
