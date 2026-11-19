package repositorystore

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingConcurrentOperationSnapshots(t *testing.T) {
	for _, name := range []string{"same input", "different input", "different todo"} {
		t.Run(name, func(t *testing.T) {
			d := open(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ids := [2]string{"naming-first", "naming-first"}
			if name == "different todo" {
				ids[1] = "naming-second"
			}
			now := time.Now().UTC()
			for i, id := range ids {
				if i == 1 && id == ids[0] {
					continue
				}
				if err := d.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Example task", CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			plans := [2]workspacenaming.Plan{}
			for i, id := range ids {
				snapshot, err := repository.Snapshot("assistant", id, 0, nil, []string{})
				if err != nil {
					t.Fatal(err)
				}
				source := workspacenaming.Source{Manager: "assistant", TodoID: id, Title: "Example task", MachineID: "computer", PlannedAt: now.Add(time.Duration(i) * time.Hour), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repository.LayoutContainer, Repositories: snapshot}
				if name == "different input" && i == 1 {
					source.MachineID = "other-computer"
				}
				preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"})
				if preview.Plan == nil {
					t.Fatal(preview)
				}
				plans[i] = *preview.Plan
			}
			connection, err := pgx.Connect(ctx, os.Getenv(dbtest.Env))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close(context.Background())
			gate, err := connection.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			// Both requests can read the absent operation. Inserts must wait here;
			// same-Todo requests additionally serialize on the existing Todo lock.
			if _, err := gate.Exec(ctx, `LOCK TABLE workspace_naming_plans IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			type result struct {
				id   string
				plan *workspacenaming.Plan
				err  error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			finished := 0
			defer func() {
				_ = gate.Rollback(context.Background())
				cancel()
				for finished < 2 {
					select {
					case <-results:
						finished++
					case <-time.After(5 * time.Second):
						t.Error("database writers did not stop")
						return
					}
				}
			}()
			for i := range 2 {
				go func() {
					<-start
					plan, err := d.SaveWorkspaceNamingPlan(ctx, ids[i], "concurrent-example-operation", 0, plans[i])
					results <- result{ids[i], plan, err}
				}()
			}
			close(start)
			// Observe actual server lock waiters, not elapsed time or a mock.
			// Cross-Todo requests must both reach INSERT before the gate opens.
			for {
				var waiting, inserting int
				if _, err := gate.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
					t.Fatal(err)
				}
				err = gate.QueryRow(ctx, `SELECT count(DISTINCT l.pid), count(DISTINCT l.pid) FILTER (WHERE l.mode='RowExclusiveLock' AND l.relation='workspace_naming_plans'::regclass)
 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE NOT l.granted AND a.datname=current_database()`).Scan(&waiting, &inserting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting == 2 {
					want := 1
					if name == "different todo" {
						want = 2
					}
					if inserting != want {
						t.Fatalf("waiting inserts %d, want %d", inserting, want)
					}
					break
				}
				runtime.Gosched()
			}
			if err := gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			passed, conflicts := 0, 0
			var winner *workspacenaming.Plan
			for finished < 2 {
				got := <-results
				finished++
				if errors.Is(got.err, repository.Problem("revision_conflict")) {
					if got.plan != nil {
						t.Fatal("conflict exposed an operation snapshot")
					}
					conflicts++
					continue
				}
				if got.err != nil || got.plan == nil {
					t.Fatalf("save %s: plan %+v, error %v", got.id, got.plan, got.err)
				}
				passed++
				if winner != nil && winner.Digest != got.plan.Digest {
					t.Fatal("same operation returned different immutable snapshots")
				}
				winner = got.plan
			}
			wantPassed := 1
			if name == "same input" {
				wantPassed = 2
			}
			if passed != wantPassed || conflicts != 2-wantPassed {
				t.Fatal("outcomes", passed, conflicts)
			}
			var count int
			if err := d.QueryRow(ctx, `SELECT count(*) FROM workspace_naming_plans`).Scan(&count); err != nil || count != 1 {
				t.Fatal("persisted operations", count, err)
			}
			stored, err := d.WorkspaceNamingPlan(ctx, winner.Source.TodoID, "concurrent-example-operation")
			if err != nil || stored == nil || stored.Digest != winner.Digest {
				t.Fatal("winner changed", stored, err)
			}
		})
	}
}
