package db

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

// The probes' seconds are far later than any migration a line of work will write: whatever timestamped migrations
// are added after this test, the probes take none of their versions and sort after all of them.
const (
	managerProbe = "29991231235958_probe.sql"
	hubProbe     = "29991231235959_probe.sql"
)

// timestampedProbes are timestamped migrations as later lines of work add them, by path under migrations/. Each
// fails unless its part's last numbered migration ran before it (frozen, so the probes don't depend on a table a
// later migration may change), and neither can run twice.
var timestampedProbes = map[string]string{
	"manager/" + managerProbe: probeSQL("manager", 561, "timestamp_probe_manager"),
	"hub/" + hubProbe:         probeSQL("hub", 541, "timestamp_probe"),
}

// probeRecords are the rows the probes leave as "version name part", by the sets they run in.
var probeRecords = map[Sets][]string{
	HubSet:     {"29991231235959 " + hubProbe + " hub"},
	ManagerSet: {"29991231235958 " + managerProbe + " manager"},
	AllSets:    {"29991231235958 " + managerProbe + " manager", "29991231235959 " + hubProbe + " hub"},
}

// probeSQL makes table, after part's migration numbered last is recorded.
func probeSQL(part string, last int, table string) string {
	return fmt.Sprintf(`DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = %d AND part = '%s') THEN
		RAISE EXCEPTION 'ran before migration %04d';
	END IF;
END $$;
CREATE TABLE %s (id int)`, last, part, last, table)
}

// buildOf is a build's migrations: the embedded ones of the versions keep takes, and extra ones (path under
// migrations/: SQL).
func buildOf(t *testing.T, keep func(version int64) bool, extra map[string]string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for part, names := range setFiles(t) {
		for _, name := range names {
			v, err := migrationVersion(name)
			if err != nil {
				t.Fatal(err)
			}
			if !keep(v) {
				continue
			}
			b, err := fs.ReadFile(migrations, "migrations/"+part+"/"+name)
			if err != nil {
				t.Fatal(err)
			}
			out["migrations/"+part+"/"+name] = &fstest.MapFile{Data: b}
		}
	}
	for path, sql := range extra {
		out["migrations/"+path] = &fstest.MapFile{Data: []byte(sql)}
	}
	return out
}

// withMigrations are the embedded migrations and extra ones: a build that has them.
func withMigrations(t *testing.T, extra map[string]string) fstest.MapFS {
	t.Helper()
	return buildOf(t, func(int64) bool { return true }, extra)
}

// numberedMigrations are the embedded migrations numbered by hand: what a build from before timestamps has.
func numberedMigrations(t *testing.T) fstest.MapFS {
	t.Helper()
	return buildOf(t, func(v int64) bool { return v <= lastNumbered }, nil)
}

// stampedRecords are the records ("name part", as records has them) the timestamped migrations of fsys leave when
// sets run them.
func stampedRecords(t *testing.T, fsys fs.FS, sets Sets) []string {
	t.Helper()
	var out []string
	for _, sd := range setDirs {
		if sets&sd.set == 0 {
			continue
		}
		entries, err := fs.ReadDir(fsys, "migrations/"+sd.part)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if v, err := migrationVersion(e.Name()); err == nil && v > lastNumbered {
				out = append(out, e.Name()+" "+sd.part)
			}
		}
	}
	return out
}

// versionType is schema_migrations.version's type.
func versionType(t *testing.T, d *DB) string {
	t.Helper()
	var typ string
	if err := d.pool.QueryRow(context.Background(), `SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = 'schema_migrations'::regclass AND attname = 'version'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	return typ
}

// probeRows are schema_migrations' rows of the probes as "version name part", sorted.
func probeRows(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.pool.Query(context.Background(), `SELECT version, name, coalesce(part, '-') FROM schema_migrations
		WHERE name = ANY($1)`, []string{managerProbe, hubProbe})
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var v int64
		var name, part string
		err := row.Scan(&v, &name, &part)
		return fmt.Sprintf("%d %s %s", v, name, part), err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// A database of each kind (a hub's own, a manager's own, one they share) that the numbered migrations made, its
// version an int as every build before timestamps left it: the next open widens the column to bigint and keeps the
// rows; the timestamped migrations run after the numbered ones, each once, recorded under their second; and opening
// again only reads. A set with two files of one version is refused before anything runs, in either set.
func TestMigrateTimestampedAfterNumbered(t *testing.T) {
	ctx := context.Background()
	numbered := numberedMigrations(t)
	built := withMigrations(t, timestampedProbes)
	for _, c := range []struct {
		name string
		sets Sets
	}{
		{"hub", HubSet},
		{"manager", ManagerSet},
		{"shared", AllSets},
	} {
		t.Run(c.name, func(t *testing.T) {
			url := freshDatabase(t)
			older, err := Attach(ctx, url, c.sets)
			if err != nil {
				t.Fatal(err)
			}
			if err := older.migrateFrom(ctx, numbered); err != nil {
				older.Close()
				t.Fatal(err)
			}
			if typ := versionType(t, older); typ != "bigint" {
				older.Close()
				t.Fatalf("a new database's version is %s", typ)
			}
			want := append(records(t, older), stampedRecords(t, built, c.sets)...)
			sort.Strings(want)
			_, err = older.pool.Exec(ctx, `ALTER TABLE schema_migrations ALTER COLUMN version TYPE int`)
			// Its connections keep reads of schema_migrations prepared, which Postgres refuses to run once the column
			// changes type: a pool that reads it before and after the widening is a test's only.
			older.Close()
			if err != nil {
				t.Fatal(err)
			}
			d, err := Attach(ctx, url, c.sets)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := d.migrateFrom(ctx, built); err != nil {
				t.Fatal(err)
			}
			if typ := versionType(t, d); typ != "bigint" {
				t.Fatalf("version is %s after the upgrade", typ)
			}
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("records %v, want %v", got, want)
			}
			if got := probeRows(t, d); !slices.Equal(got, probeRecords[c.sets]) {
				t.Fatalf("probe rows %v, want %v", got, probeRecords[c.sets])
			}
			traced, log := tracedDB(t, url)
			traced.sets = c.sets
			for range 2 {
				if err := traced.migrateFrom(ctx, built); err != nil {
					t.Fatalf("again: %v", err)
				}
				if got := log.take(); len(got) > 4 {
					t.Fatalf("migrating up to date took %d statements: %v", len(got), got)
				}
			}
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("records after opening again %v, want %v", got, want)
			}
			if c.sets != AllSets {
				return
			}
			// A new hub migration that runs first, and a manager one of a version a recorded one has: neither runs.
			twice := withMigrations(t, map[string]string{
				"hub/" + hubProbe:                  timestampedProbes["hub/"+hubProbe],
				"manager/" + managerProbe:          timestampedProbes["manager/"+managerProbe],
				"hub/29991231235957_before.sql":    `CREATE TABLE timestamp_before (id int)`,
				"manager/29991231235958_other.sql": `CREATE TABLE timestamp_other (id int)`,
			})
			err = d.migrateFrom(ctx, twice)
			if err == nil || !strings.Contains(err.Error(), "29991231235958_other.sql, "+managerProbe+" have the same version") ||
				!strings.Contains(err.Error(), "no migration was applied") {
				t.Fatalf("two migrations of one second: %v", err)
			}
			if tables := tablesOf(t, d); slices.Contains(tables, "timestamp_other") || slices.Contains(tables, "timestamp_before") {
				t.Fatalf("a migration ran beside two of one second: %v", tables)
			}
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("records after a refused set %v, want %v", got, want)
			}
		})
	}
	// On a new database the timestamped migrations follow the numbered ones they build on (each probe checks).
	d, err := Attach(ctx, freshDatabase(t), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.migrateFrom(ctx, built); err != nil {
		t.Fatal(err)
	}
	if got := probeRows(t, d); !slices.Equal(got, probeRecords[AllSets]) {
		t.Fatalf("probe rows %v", got)
	}
}

// previousSchemaMigrationsSplit is the catalog check of the builds from before timestamps (v0.8.2 and earlier back to
// the split): the same as schemaMigrationsSplit, without the version's type.
const previousSchemaMigrationsSplit = `SELECT coalesce((
	SELECT c.relkind = 'r' AND n.nspname = current_schema()
		AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'part' AND a.attnum > 0 AND NOT a.attisdropped)
		AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.conname = 'schema_migrations_pkey')
		AND EXISTS (SELECT 1 FROM pg_class i WHERE i.relnamespace = c.relnamespace AND i.relname = 'schema_migrations_version_part')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass('schema_migrations')), false)`

// A build from before timestamps, switched back to (a failed take-over, `personad hub switch --prev`, an update rolled
// back) on a database this one has migrated further: its catalog check finds the table as it left it, so it changes
// nothing there; it reads the bigint versions, the timestamped ones too, into a Go int as it always did; and every
// migration it has is done, so it applies and records nothing. Forward again, nothing runs twice.
//
// This replays the older build's catalog query and its scan, but its open runs this build's runner over the numbered
// files only, so it can't notice a difference in what the older runner does. The evidence for the older build itself
// is v0.8.2 run against such databases (#10618; docs/server/server.md "Migrations").
func TestOlderBuildAfterTimestampedMigrations(t *testing.T) {
	ctx := context.Background()
	url := migratedDatabase(t, AllSets)
	d, err := Attach(ctx, url, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	built := withMigrations(t, timestampedProbes)
	if err := d.migrateFrom(ctx, built); err != nil {
		t.Fatal(err)
	}
	want := records(t, d)
	var split bool
	if err := d.pool.QueryRow(ctx, previousSchemaMigrationsSplit).Scan(&split); err != nil || !split {
		t.Fatalf("an older build's catalog check: %v %v", split, err)
	}
	rows, err := d.pool.Query(ctx, `SELECT version, name, part FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	var version int // what readSchemaRecords scanned into before timestamps
	var name string
	var part *string
	var read []string
	if _, err := pgx.ForEachRow(rows, []any{&version, &name, &part}, func() error {
		if (name == managerProbe || name == hubProbe) && part != nil {
			read = append(read, fmt.Sprintf("%d %s %s", version, name, *part))
		}
		return nil
	}); err != nil {
		t.Fatalf("an older build's read: %v", err)
	}
	if sort.Strings(read); !slices.Equal(read, probeRecords[AllSets]) {
		t.Fatalf("an older build's read of the timestamped rows: %v", read)
	}
	// The older build's own migrations, the numbered ones, through this build's runner.
	traced, log := tracedDB(t, url)
	traced.sets = AllSets
	if err := traced.migrateFrom(ctx, numberedMigrations(t)); err != nil {
		t.Fatal(err)
	}
	if got := log.take(); len(got) > 4 {
		t.Fatalf("the older build's open took %d statements: %v", len(got), got)
	}
	if err := traced.migrateFrom(ctx, built); err != nil {
		t.Fatal(err)
	}
	if got := records(t, d); !slices.Equal(got, want) {
		t.Fatalf("records %v, want %v", got, want)
	}
}

// Every set's files are checked before any runs: a bad name or two files of one version in a part refuse them all, and
// the error names each file and how to fix it.
func TestPlanMigrations(t *testing.T) {
	ok := fstest.MapFS{
		"migrations/hub/0001_init.sql":                {},
		"migrations/hub/29991231235959_probe.sql":     {},
		"migrations/manager/0001_init.sql":            {},
		"migrations/manager/29991231235959_probe.sql": {}, // one second in both parts is fine
		"migrations/manager/29991231235958_probe.sql": {},
	}
	plan, err := planMigrations(ok, []string{"hub", "manager"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(plan["manager"]); got != "[{0001_init.sql 1} {29991231235958_probe.sql 29991231235958} {29991231235959_probe.sql 29991231235959}]" {
		t.Fatalf("manager plan %s", got)
	}
	bad := fstest.MapFS{
		"migrations/hub/0001_init.sql":                {},
		"migrations/hub/202611191230_minute.sql":      {},
		"migrations/manager/29991231235958_c.sql":     {},
		"migrations/manager/29991231235958_a.sql":     {},
		"migrations/manager/29991231235958_b.sql":     {},
		"migrations/manager/29991231235959_other.sql": {},
	}
	_, err = planMigrations(bad, []string{"hub", "manager"})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"no migration was applied",
		"migrations/hub: bad migration name 202611191230_minute.sql: want YYYYMMDDHHMMSS_<name>.sql",
		"migrations/manager: 29991231235958_a.sql, 29991231235958_b.sql, 29991231235958_c.sql have the same version 29991231235958",
		"keep the name of the one that merged first",
		"$(date -u +%Y%m%d%H%M%S)_<name>.sql",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want %q in it", err, want)
		}
	}
	if strings.Contains(err.Error(), "_other.sql") {
		t.Errorf("error %q names a file of a version of its own", err)
	}
	if _, err := planMigrations(bad, []string{"manager"}); err == nil || strings.Contains(err.Error(), "hub") {
		t.Fatalf("only the manager's set: %v", err)
	}
}
