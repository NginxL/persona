package db

import (
	"slices"
	"testing"
)

func TestWorkspaceNamingUpgradeRepositoryMigrationCollision(t *testing.T) {
	for _, old := range []struct {
		version int
		name    string
		search  string
	}{
		{513, "0513_todo_repositories.sql", "0513_assistant_search_plain.sql"},
		{514, "0514_todo_repositories.sql", "0514_assistant_search_strong.sql"},
	} {
		for _, both := range []bool{false, true} {
			t.Run(old.name+map[bool]string{false: "/predecessor", true: "/both records"}[both], func(t *testing.T) {
				ctx := t.Context()
				url := freshDatabase(t)
				d, err := Open(ctx, url, ManagerSet)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				want := records(t, d)
				if _, err = d.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE name=$1`, old.search); err != nil {
					t.Fatal(err)
				}
				query := `UPDATE schema_migrations SET version=$1,name=$2 WHERE name='0516_todo_repositories.sql'`
				if both {
					query = `INSERT INTO schema_migrations(version,name,part) VALUES($1,$2,'manager')`
				}
				if _, err = d.pool.Exec(ctx, query, old.version, old.name); err != nil {
					t.Fatal(err)
				}
				if err = d.migrate(ctx); err != nil {
					t.Fatal(err)
				}
				if got := records(t, d); !slices.Equal(got, want) {
					t.Fatal(got, want)
				}
			})
		}
	}
}

func TestWorkspaceNamingUpgradeNumberedMigration(t *testing.T) {
	const currentName = "20261118234802_workspace_naming.sql"
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "old record", true: "both records"}[both], func(t *testing.T) {
			ctx := t.Context()
			url := freshDatabase(t)
			d, err := Open(ctx, url, ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, q := range []string{
				`INSERT INTO todos(id,title) VALUES('naming-upgrade','Original task')`,
				`INSERT INTO todo_workspace_rules(todo_id,revision,rules) VALUES('naming-upgrade',7,'{"branch_template":"custom/{task-id}"}')`,
				`INSERT INTO workspace_naming_plans(operation_id,todo_id,plan) VALUES('saved-operation','naming-upgrade','{"source":{"title":"Original task","ref":{"kind":"ref","value":"refs/pull/7/head"}},"digest":"saved"}')`,
			} {
				if _, err := d.pool.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			const saved = `SELECT jsonb_build_object('revision',r.revision,'rules',r.rules,'operation',p.operation_id,'plan',p.plan)::text
				FROM todo_workspace_rules r JOIN workspace_naming_plans p USING(todo_id) WHERE r.todo_id='naming-upgrade'`
			var before string
			if err := d.pool.QueryRow(ctx, saved).Scan(&before); err != nil {
				t.Fatal(err)
			}
			want := records(t, d)
			q := `UPDATE schema_migrations SET version=515,name='0515_workspace_naming.sql' WHERE name=$1`
			args := []any{currentName}
			if both {
				q = `INSERT INTO schema_migrations(version,name,part) VALUES(515,'0515_workspace_naming.sql','manager')`
				args = nil
			}
			changed, err := d.pool.Exec(ctx, q, args...)
			if err != nil || changed.RowsAffected() != 1 {
				t.Fatalf("legacy record fixture: %v %v", changed, err)
			}
			d.Close()
			d, err = Open(ctx, url, ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("migration records after upgrade: %v, want %v", got, want)
			}
			var after string
			if err := d.pool.QueryRow(ctx, saved).Scan(&after); err != nil || after != before {
				t.Fatalf("saved naming state changed: %s, want %s: %v", after, before, err)
			}
		})
	}
}
