package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/db/dbtest"
)

// bothSets are the migrations in both sets, frozen: from before the split, 0001 made tables for both sides (each set
// has its part), and sent_messages is a table each side keeps in its own database (the hub's sends from the apps, the
// manager's from the inbox). A new migration goes in exactly one set.
var bothSets = []string{"0001_init.sql", "0071_sent_messages.sql", "0241_sent_echo.sql"}

// sharedNumbers are numbers both sets use for different migrations, frozen: two lines of work took 421 at once
// (#5751). Harmless, since each set records its own part; no other number is in both sets.
var sharedNumbers = map[int64][2]string{421: {"0421_hub_settings.sql", "0421_machine_usage.sql"}}

// lastNumbered is the last migration numbered by hand, and numberedFiles how many numbered files each set has, both
// frozen: every migration after them is named YYYYMMDDHHMMSS_<name>.sql, the UTC second it was written (`date -u
// +%Y%m%d%H%M%S`), which another line of work adding one meanwhile is all but sure not to take too (db.go
// migrationVersion, #10618). scripts/ci/migration_freeze.py keeps the same numbered files by name.
const lastNumbered = 561

var numberedFiles = map[string]int{"hub": 35, "manager": 87}

// firstStamp is when migrations began to be timestamped, and stampAhead how far after now a timestamp may be: one
// further ahead (a typo in the year, a clock set wrong) would sort after every migration written until then, so new
// and existing databases would run those in different orders. scripts/ci/migration_freeze.py has the same bounds.
var firstStamp = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

const stampAhead = 24 * time.Hour

var migrationName = regexp.MustCompile(`^([0-9]{4}|[0-9]{14})_[a-z0-9_]+\.sql$`)

// sharedTables are the tables both sets make (a database the hub and the default manager share has one of each).
var sharedTables = []string{"sent_messages"}

var createTable = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)`)

// setFiles are each part's migration names, sorted.
func setFiles(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, sd := range setDirs {
		entries, err := fs.ReadDir(migrations, "migrations/"+sd.part)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			out[sd.part] = append(out[sd.part], e.Name())
		}
		sort.Strings(out[sd.part])
	}
	return out
}

// setTables are the tables a part's migrations make.
func setTables(t *testing.T, part string) []string {
	t.Helper()
	var out []string
	for _, name := range setFiles(t)[part] {
		b, err := fs.ReadFile(migrations, "migrations/"+part+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range createTable.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(out, strings.ToLower(m[1])) {
				out = append(out, strings.ToLower(m[1]))
			}
		}
	}
	sort.Strings(out)
	return out
}

// Every migration is in exactly one set and has a version of its own there (docs/server/server.md "Migrations"); a
// number is in both sets only for the frozen few in bothSets and sharedNumbers, and a timestamped file never is (two
// files of one second may be, one in each). The numbered ones are frozen, and the rest timestamped, after them in name
// order as in version order.
func TestMigrationSets(t *testing.T) {
	root, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range root {
		if !e.IsDir() || (e.Name() != "hub" && e.Name() != "manager") {
			t.Errorf("migrations/%s: a migration goes in migrations/hub (the hub's tables) or migrations/manager (a manager's)", e.Name())
		}
	}
	files := setFiles(t)
	for _, p := range migrationNameProblems(files, time.Now()) {
		t.Error(p)
	}
	for v, pair := range sharedNumbers {
		if !slices.Contains(files["hub"], pair[0]) || !slices.Contains(files["manager"], pair[1]) {
			t.Errorf("sharedNumbers lists %d as %v, which are not hub's and manager's files", v, pair)
		}
	}
	for _, name := range bothSets {
		if !slices.Contains(files["hub"], name) || !slices.Contains(files["manager"], name) {
			t.Errorf("%s is listed in bothSets but is not in both sets", name)
		}
	}
	hub, manager := setTables(t, "hub"), setTables(t, "manager")
	var both []string
	for _, tb := range hub {
		if slices.Contains(manager, tb) {
			both = append(both, tb)
		}
	}
	if !slices.Equal(both, sharedTables) {
		t.Errorf("tables both sets make: %v, want %v: a table is one side's (docs/server/server.md \"Migrations\")", both, sharedTables)
	}
}

// migrationNameProblems are what is wrong with the names of each part's migrations (sorted), now: a name that is not
// numbered or timestamped, a number added after they were frozen, a timestamp before firstStamp or more than
// stampAhead after now, a version two files of a part have, a number both parts have (but the frozen exceptions), a
// timestamped file both parts have, a name that sorts before an earlier version. Two files of one second may be one in
// each part: each records its own, and no record from before the split (which stands for both) has one. One file in
// both is a migration in two sets, though: on a database both sides share it runs once for each, an UPDATE or an
// INSERT twice.
func migrationNameProblems(files map[string][]string, now time.Time) []string {
	const rename = "a UTC second of its own: $(date -u +%Y%m%d%H%M%S)_<name>.sql"
	var out []string
	type file struct{ part, name string }
	numbers := map[int64]file{}
	stamped := map[string]string{} // timestamped name: the part that has it
	for _, part := range []string{"hub", "manager"} {
		seen := map[int64]string{}
		numbered := 0
		var last int64 = -1
		for _, name := range files[part] {
			if !migrationName.MatchString(name) {
				out = append(out, fmt.Sprintf("migrations/%s/%s: name a new migration by %s, in lower case", part, name, rename))
				continue
			}
			v, err := migrationVersion(name)
			if err != nil {
				out = append(out, err.Error())
				continue
			}
			if name[numberedDigits] == '_' {
				numbered++
				if v > lastNumbered {
					out = append(out, fmt.Sprintf("migrations/%s/%s: numbers stopped at %04d; name it by %s", part, name, lastNumbered, rename))
				}
				pair, known := sharedNumbers[v]
				if was, ok := numbers[v]; ok && was.part != part && (was.name != name || !slices.Contains(bothSets, name)) &&
					!(known && was.name == pair[0] && name == pair[1]) {
					out = append(out, fmt.Sprintf("migration %d is in both sets (%s, %s): a new migration goes in exactly one", v, was.name, name))
				}
				numbers[v] = file{part, name}
			} else {
				if at, _ := time.Parse(stampLayout, name[:len(stampLayout)]); at.Before(firstStamp) {
					out = append(out, fmt.Sprintf("migrations/%s/%s: a time before migrations were timestamped (%s); name it by %s",
						part, name, firstStamp.Format(time.DateOnly), rename))
				} else if at.After(now.Add(stampAhead)) {
					out = append(out, fmt.Sprintf("migrations/%s/%s: a time more than %.0f hours after now (%s UTC); name it by %s",
						part, name, stampAhead.Hours(), now.UTC().Format(stampLayout), rename))
				}
				if was, ok := stamped[name]; ok && was != part {
					out = append(out, fmt.Sprintf("migration %s is in both sets: a new migration goes in exactly one (on a database both sides share it would run once for each)", name))
				}
				stamped[name] = part
			}
			if was, ok := seen[v]; ok {
				out = append(out, fmt.Sprintf("migrations/%s: %s and %s have the same version %d: keep the name of the one that merged first and give the other %s",
					part, was, name, v, rename))
			}
			seen[v] = name
			if v < last {
				out = append(out, fmt.Sprintf("migrations/%s/%s sorts after a migration with a later version (%d)", part, name, last))
			}
			last = v
		}
		if numbered != numberedFiles[part] {
			out = append(out, fmt.Sprintf("migrations/%s has %d numbered migrations, %d when they were frozen: name a new one by %s", part, numbered, numberedFiles[part], rename))
		}
	}
	return out
}

// The guard on names: a numbered file added after the freeze (above the last number, or in a gap) fails, as do a
// timestamp that is no time, one before the switch or more than a day ahead, a second taken twice in a set, a number in
// both sets and a timestamped file in both sets; a timestamped file passes in one set, and two files of one second pass
// one in each set. The made-up files are of seconds far after any real one (now is put next to them), so the real
// timestamped files added later neither share their versions nor sort after them.
func TestMigrationNameProblems(t *testing.T) {
	now := time.Date(2999, 12, 31, 12, 0, 0, 0, time.UTC)
	with := func(add map[string][]string) map[string][]string {
		files := setFiles(t)
		for part, names := range add {
			files[part] = append(files[part], names...)
			sort.Strings(files[part])
		}
		return files
	}
	in := func(part string, names ...string) map[string][]string { return with(map[string][]string{part: names}) }
	for _, c := range []struct {
		name  string
		files map[string][]string
		want  []string // a part of each problem, in order
	}{
		{"frozen", setFiles(t), nil},
		{"timestamped", in("manager", "29991231235000_probe.sql", "29991231235001_probe_more.sql"), nil},
		{"timestamped in each set", in("hub", "29991231235002_hub_probe.sql"), nil},
		{"one second in both sets", with(map[string][]string{"hub": {"29991231235000_probe.sql"}, "manager": {"29991231235000_probe_too.sql"}}), nil},
		{"one file in both sets", with(map[string][]string{"hub": {"29991231235000_backfill.sql"}, "manager": {"29991231235000_backfill.sql"}}),
			[]string{"migration 29991231235000_backfill.sql is in both sets: a new migration goes in exactly one"}},
		{"the first second", in("hub", "20261118000000_probe.sql"), nil},
		{"a day ahead", in("hub", "30000101120000_probe.sql"), nil},
		{"next number", in("manager", "0562_probe.sql"), []string{"numbers stopped at 0561", "88 numbered migrations, 87"}},
		{"a gap", in("hub", "0559_probe.sql"), []string{"36 numbered migrations, 35"}},
		{"a number in both sets", in("hub", "0561_probe.sql"), []string{"36 numbered migrations, 35", "561 is in both sets (0561_probe.sql, 0561_model_usage.sql)"}},
		{"upper case", in("hub", "29991231235000_Probe.sql"), []string{"in lower case"}},
		{"three digits", in("hub", "562_probe.sql"), []string{"in lower case"}},
		{"a minute", in("hub", "299912312350_probe.sql"), []string{"in lower case"}},
		{"no time", in("hub", "29991131123000_probe.sql"), []string{"not a UTC time"}},
		{"before the switch", in("hub", "20261117235959_probe.sql"), []string{"before migrations were timestamped (2026-11-18)"}},
		{"more than a day ahead", in("hub", "30000101120001_probe.sql"), []string{"more than 24 hours after now (29991231120000 UTC)"}},
		{"one second twice", in("manager", "29991231235000_a.sql", "29991231235000_b.sql"),
			[]string{"29991231235000_a.sql and 29991231235000_b.sql have the same version 29991231235000: keep the name of the one that merged first"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := migrationNameProblems(c.files, now)
			if len(got) != len(c.want) {
				t.Fatalf("problems %q, want %q", got, c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problem %q, want %q in it", got[i], w)
				}
			}
		})
	}
	// The real files pass today, whatever the date: TestMigrationSets is not the only caller.
	if got := migrationNameProblems(setFiles(t), time.Now()); len(got) != 0 {
		t.Fatalf("the embedded migrations now: %q", got)
	}
}

// migrationVersion reads a number or a UTC second, and nothing else; name order is version order.
func TestMigrationVersion(t *testing.T) {
	for name, want := range map[string]int64{
		"0001_init.sql":            1,
		"0561_model_usage.sql":     561,
		"20261119123000_probe.sql": 20261119123000,
		"20991231235959_late.sql":  20991231235959,
	} {
		if got, err := migrationVersion(name); err != nil || got != want {
			t.Errorf("%s: %d %v, want %d", name, got, err, want)
		}
	}
	for _, name := range []string{"init.sql", "0001.sql", "561_x.sql", "00561_x.sql", "+561_x.sql", "202611191230_x.sql",
		"2026111912300_x.sql", "202611191230000_x.sql", "20261319123000_x.sql", "20260230123000_x.sql",
		"20261119243000_x.sql", "20261119126000_x.sql", "20261119123060_x.sql"} {
		if v, err := migrationVersion(name); err == nil {
			t.Errorf("%s: version %d, want an error", name, v)
		}
	}
	names := []string{"20261119123000_b.sql", "0561_model_usage.sql", "20261119122959_z.sql", "0001_init.sql", "20270101000000_a.sql"}
	sort.Strings(names)
	var last int64 = -1
	for _, name := range names {
		v, err := migrationVersion(name)
		if err != nil || v <= last {
			t.Fatalf("%v: %s is version %d (%v), after %d", names, name, v, err, last)
		}
		last = v
	}
}

// freshDatabase is a new empty database next to the test one, dropped afterwards.
func freshDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv(dbtest.Env)
	if base == "" {
		t.Skip(dbtest.Env + " not set")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "persona_split_t_" + hex.EncodeToString(b)
	url, err := WithDatabase(base, name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := EnsureDatabase(ctx, url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropDatabase(base, name) })
	return url
}

// dropDatabase drops a test's database name next to base, closing what is still connected to it.
func dropDatabase(base, name string) {
	admin, err := pgx.Connect(context.Background(), base)
	if err == nil {
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		_ = admin.Close(context.Background())
	}
}

// tablesOf are the database's tables but schema_migrations, sorted.
func tablesOf(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.pool.Query(context.Background(), `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'schema_migrations' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// records are schema_migrations' rows as "version name part", sorted.
func records(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.pool.Query(context.Background(), `SELECT version, name, coalesce(part, '-') FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var v int64
		var name, part string
		if err := rows.Scan(&v, &name, &part); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join([]string{name, part}, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// A hub's new database has the hub's tables only, a manager's the manager's; one they share has both, each part
// recorded once.
func TestMigrateFreshSets(t *testing.T) {
	ctx := context.Background()
	hubTables, managerTables := setTables(t, "hub"), setTables(t, "manager")
	for _, c := range []struct {
		name   string
		sets   []Sets
		tables []string
		rows   int
	}{
		{"hub", []Sets{HubSet}, hubTables, len(setFiles(t)["hub"])},
		{"manager", []Sets{ManagerSet}, managerTables, len(setFiles(t)["manager"])},
		{"shared", []Sets{HubSet, ManagerSet, AllSets}, union(hubTables, managerTables),
			len(setFiles(t)["hub"]) + len(setFiles(t)["manager"])},
	} {
		t.Run(c.name, func(t *testing.T) {
			url := freshDatabase(t)
			var d *DB
			for _, s := range c.sets {
				var err error
				if d, err = Open(ctx, url, s); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(d.Close)
			}
			if got := tablesOf(t, d); !slices.Equal(got, c.tables) {
				t.Fatalf("tables %v, want %v", got, c.tables)
			}
			if got := records(t, d); len(got) != c.rows {
				t.Fatalf("%d records, want %d: %v", len(got), c.rows, got)
			}
		})
	}
}

// unifiedUpTo are the migrations as the migrator from before the split had them, up to version until, in order, with
// their SQL: 0001 was both parts in one; the others in both sets are the same in each.
func unifiedUpTo(t *testing.T, until int64) [][2]string {
	t.Helper()
	sql := map[string]string{}
	for _, part := range []string{"hub", "manager"} {
		for _, name := range setFiles(t)[part] {
			b, err := fs.ReadFile(migrations, "migrations/"+part+"/"+name)
			if err != nil {
				t.Fatal(err)
			}
			if was, ok := sql[name]; ok && was != string(b) {
				sql[name] = was + "\n" + string(b)
			} else {
				sql[name] = string(b)
			}
		}
	}
	var out [][2]string
	for name, q := range sql {
		if v, _ := migrationVersion(name); v <= until {
			out = append(out, [2]string{name, q})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// unifiedMigrate is the migrator from before the split: one schema_migrations row per version, every migration in
// order, up to version until (419 at most: the split came after it).
func unifiedMigrate(t *testing.T, url string, until int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, m := range unifiedUpTo(t, until) {
		v, _ := migrationVersion(m[0])
		if _, err := conn.Exec(ctx, m[1]); err != nil {
			t.Fatalf("%s: %v", m[0], err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, v, m[0]); err != nil {
			t.Fatal(err)
		}
	}
}

// Opening a database that is up to date only reads schema_migrations: no ALTER TABLE, no renumbering write. So a start
// does not wait for a session that holds SHARE on the table, and takes no ACCESS EXCLUSIVE lock or transaction id. The
// migrations' part of it is four statements, whatever the sets: the lock, the catalog check, one read and the unlock
// (a check per migration took about a hundred).
func TestOpenUpToDateOnlyReadsSchemaMigrations(t *testing.T) {
	ctx := context.Background()
	url := migratedDatabase(t, AllSets)
	reader, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	if _, err := reader.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{DatabaseName(url)}.Sanitize()+` SET lock_timeout = '2s'`); err != nil {
		t.Fatal(err)
	}
	tx, err := reader.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `LOCK TABLE schema_migrations IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Sets{AllSets, HubSet, ManagerSet} {
		d, err := Open(ctx, url, s)
		if err != nil {
			t.Fatalf("open %d while a reader holds schema_migrations: %v", s, err)
		}
		d.Close()
		traced, log := tracedDB(t, url)
		traced.sets = s
		if err := traced.migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if got := log.take(); len(got) > 4 {
			t.Fatalf("migrating %d up to date took %d statements: %v", s, len(got), got)
		}
	}
}

// A record of a migration under the number it had before a merge renumbered it is moved to the new number, or only
// dropped when that is recorded too; either way the migration that has the old number now runs.
func TestMigrateRenumbered(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
	}{
		{"the old number only", `UPDATE schema_migrations SET version = 428, name = '0428_todo_source.sql' WHERE name = '0429_todo_source.sql'`},
		{"both numbers", `INSERT INTO schema_migrations (version, name, part) VALUES (428, '0428_todo_source.sql', 'manager')`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			url := freshDatabase(t)
			d, err := Open(ctx, url, ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			want := records(t, d)
			// 0428_manager_moved took the old number after this database recorded 0428_todo_source under it.
			for _, q := range []string{`DELETE FROM schema_migrations WHERE name = '0428_manager_moved.sql'`, c.sql} {
				if _, err := d.pool.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			d.Close()
			if d, err = Open(ctx, url, ManagerSet); err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("records %v, want %v", got, want)
			}
		})
	}
}

// The hub's client_password migration was merged as 0492, a number the manager's agent_shares already had. On a
// database both sides share, its record moves to 0505 and the manager's 0492 stays; a build from before the move that
// ran it again (it is idempotent) leaves both records, and the old one goes.
func TestMigrateRenumberedSharedNumber(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
	}{
		{"the old number only", `UPDATE schema_migrations SET version = 492, name = '0492_client_password.sql' WHERE name = '0505_client_password.sql'`},
		{"both numbers", `INSERT INTO schema_migrations (version, name, part) VALUES (492, '0492_client_password.sql', 'hub')`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			url := freshDatabase(t)
			d, err := Open(ctx, url, AllSets)
			if err != nil {
				t.Fatal(err)
			}
			want := records(t, d)
			if !slices.Contains(want, "0492_agent_shares.sql manager") || !slices.Contains(want, "0505_client_password.sql hub") {
				t.Fatalf("records %v: want the manager's 0492 and the hub's 0505", want)
			}
			if _, err := d.pool.Exec(ctx, c.sql); err != nil {
				t.Fatal(err)
			}
			d.Close()
			if d, err = Open(ctx, url, AllSets); err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if got := records(t, d); !slices.Equal(got, want) {
				t.Fatalf("records %v, want %v", got, want)
			}
		})
	}
}

// A database migrated before the split (the hub and its default manager on one) runs nothing again, whichever side
// opens it first; one left at an older state gets the rest of both sets; and a binary from before the split still
// reads and adds records.
func TestMigrateUpgradeFromUnified(t *testing.T) {
	ctx := context.Background()
	all := union(setTables(t, "hub"), setTables(t, "manager"))
	t.Run("complete", func(t *testing.T) {
		url := freshDatabase(t)
		unifiedMigrate(t, url, 419) // the last migration from before the split
		probe, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close(ctx)
		// A migration that ran again would fail on these (0001's CREATE TABLE has no IF NOT EXISTS) or change them.
		if _, err := probe.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('kept', '"yes"')`); err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, m := range unifiedUpTo(t, 419) {
			want = append(want, m[0]+" -")
		}
		sort.Strings(want)
		for _, s := range []Sets{ManagerSet, HubSet, AllSets} {
			d, err := Open(ctx, url, s)
			if err != nil {
				t.Fatalf("open %d: %v", s, err)
			}
			// The records from before stay as they were (part NULL, none added for them); only the migrations after the
			// split are new, recorded with their part.
			var old []string
			for _, r := range records(t, d) {
				if v, _ := migrationVersion(r); v <= 419 {
					old = append(old, r)
				} else if !strings.HasSuffix(r, " hub") && !strings.HasSuffix(r, " manager") {
					t.Fatalf("a migration after the split recorded without its part: %s", r)
				}
			}
			if !slices.Equal(old, want) {
				t.Fatalf("records from before the split after opening with %d: %v, want %v", s, old, want)
			}
			if s == AllSets {
				if tables := tablesOf(t, d); !slices.Equal(tables, all) {
					t.Fatalf("tables %v, want %v", tables, all)
				}
			}
			var v string
			if ok, err := d.Setting(ctx, "kept", &v); s != HubSet && (err != nil || !ok || v != "yes") { // the manager's settings
				t.Fatalf("setting kept = %q %v %v", v, ok, err)
			}
			d.Close()
		}
		// What a binary from before the split does with it (a hub switched back to an older slot): its lookup and its
		// insert still work.
		var done bool
		if err := probe.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, 1).Scan(&done); err != nil || !done {
			t.Fatalf("old lookup: %v %v", done, err)
		}
		if _, err := probe.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, 99999, "99999_later.sql"); err != nil {
			t.Fatalf("old insert: %v", err)
		}
	})
	t.Run("older", func(t *testing.T) {
		url := freshDatabase(t)
		unifiedMigrate(t, url, 300)
		for _, s := range []Sets{HubSet, ManagerSet} {
			d, err := Open(ctx, url, s)
			if err != nil {
				t.Fatalf("open %d: %v", s, err)
			}
			d.Close()
		}
		d, err := Open(ctx, url, AllSets)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if tables := tablesOf(t, d); !slices.Equal(tables, all) {
			t.Fatalf("tables %v, want %v", tables, all)
		}
		var newer int
		if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE part IS NOT NULL AND version <= 300`).Scan(&newer); err != nil || newer != 0 {
			t.Fatalf("%d migrations up to 0300 ran again, %v", newer, err)
		}
	})
}

// The hub's settings are its own table: a database opened for the hub reads and writes hub_settings, one opened for a
// manager settings; neither sees the other's keys.
func TestSettingsAreEachSidesOwn(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	manager, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := hub.PutSetting(ctx, "k", "hub's"); err != nil {
		t.Fatal(err)
	}
	if err := manager.PutSetting(ctx, "k", "manager's"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		d    *DB
		want string
	}{{hub, "hub's"}, {manager, "manager's"}} {
		var v string
		if ok, err := c.d.Setting(ctx, "k", &v); err != nil || !ok || v != c.want {
			t.Fatalf("got %q %v %v, want %q", v, ok, err, c.want)
		}
	}
	var v string
	if ok, err := hub.TakeSetting(ctx, "k", &v); err != nil || !ok || v != "hub's" {
		t.Fatalf("take %q %v %v", v, ok, err)
	}
	if ok, err := manager.Setting(ctx, "k", &v); err != nil || !ok || v != "manager's" {
		t.Fatalf("the hub's take reached the manager's: %q %v %v", v, ok, err)
	}
	if ok, err := manager.CompareSetting(ctx, "rev", 0, map[string]int{"revision": 1}); err != nil || !ok {
		t.Fatalf("compare: %v %v", ok, err)
	}
	if !slices.Contains(tablesOf(t, hub), "hub_settings") {
		t.Fatal("no hub_settings")
	}
	only := freshDatabase(t)
	d, err := Open(ctx, only, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if slices.Contains(tablesOf(t, d), "settings") {
		t.Fatal("a hub's database has the managers' settings table")
	}
}

// On a database the hub and the default manager shared, the hub's keys are copied once into hub_settings (the
// manager's stay theirs), so nothing the hub kept is lost.
func TestHubSettingsCopiedOnUpgrade(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	unifiedMigrate(t, url, 419)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('hub_id', '"h-1"'), ('mutes', '{"spaces":["m/s"]}'),
		('codex_inventory/lap', '{"x":1}'), ('hub.warm_machines', '{}'), ('assistant.tasks', '[]')`); err != nil {
		t.Fatal(err)
	}
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	for _, k := range []string{"hub_id", "mutes", "codex_inventory/lap"} {
		var v any
		if ok, err := hub.Setting(ctx, k, &v); err != nil || !ok {
			t.Fatalf("%s not copied: %v %v", k, ok, err)
		}
	}
	for _, k := range []string{"hub.warm_machines", "assistant.tasks"} {
		var v any
		if ok, err := hub.Setting(ctx, k, &v); err != nil || ok {
			t.Fatalf("the manager's %s is the hub's now: %v %v", k, ok, err)
		}
	}
	manager, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	var tasks []any
	if ok, err := manager.Setting(ctx, "assistant.tasks", &tasks); err != nil || !ok {
		t.Fatalf("the manager's setting: %v %v", ok, err)
	}
}

// A NULL-part row after the split (a build without the split that had one of the 421 files) stands only for its own
// file: the other set's 0421 still runs.
func TestNullRowAfterTheSplitIsItsFileOnly(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	unifiedMigrate(t, url, 419)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES (421, '0421_machine_usage.sql')`); err != nil {
		t.Fatal(err)
	}
	d, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !slices.Contains(tablesOf(t, d), "hub_settings") {
		t.Fatal("the hub's 0421 was taken as done by the manager's row")
	}
}

// A switch back to a hub build from before hub_settings and forward again: what that build wrote in settings
// meanwhile is taken up (only where it is newer), and its event log hand-over is moved, not left for a later switch
// back to resume.
func TestHubFollowsAnOlderBuild(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	unifiedMigrate(t, url, 419)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('mutes', '{"spaces":["a"]}'), ('look', '"one"')`); err != nil {
		t.Fatal(err)
	}
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.PutSetting(ctx, "look", "two"); err != nil { // this build's own change, after the copy
		t.Fatal(err)
	}
	// It is kept for an older build too, with the same time: a switch back starts from it, and it is not taken again.
	var same bool
	if err := conn.QueryRow(ctx, `SELECT s.value = h.value AND s.updated_at = h.updated_at FROM settings s JOIN hub_settings h
		USING (key) WHERE key = 'look'`).Scan(&same); err != nil || !same {
		t.Fatalf("look kept for an older build: %v %v", same, err)
	}
	if err := hub.PutSetting(ctx, "hub.devices_warm", map[string]int{}); err != nil { // not a key an older build knows
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key = 'hub.devices_warm'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a new key in settings: %d %v", n, err)
	}
	hub.Close()
	// The older build serves for a while: it writes the hub's keys to settings.
	for _, q := range []string{
		`UPDATE settings SET value = '{"spaces":["a","b"]}', updated_at = now() + interval '1 second' WHERE key = 'mutes'`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('sessions.deleted', '{"m/s":"x"}', now() + interval '1 second')`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('codex_inventory/lap', '{"n":1}', now() + interval '1 second')`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('hub.event_log', '{"stream":"old"}', now() + interval '1 second')`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('assistant.tasks', '[]', now() + interval '1 second')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	hub, err = Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	var mutes struct {
		Spaces []string `json:"spaces"`
	}
	if ok, err := hub.Setting(ctx, "mutes", &mutes); err != nil || !ok || len(mutes.Spaces) != 2 {
		t.Fatalf("mutes the older build changed: %+v %v %v", mutes, ok, err)
	}
	var look string
	if ok, err := hub.Setting(ctx, "look", &look); err != nil || !ok || look != "two" {
		t.Fatalf("this build's newer value was replaced: %q %v %v", look, ok, err)
	}
	for _, k := range []string{"sessions.deleted", "codex_inventory/lap"} {
		var v any
		if ok, err := hub.Setting(ctx, k, &v); err != nil || !ok {
			t.Fatalf("%s the older build wrote: %v %v", k, ok, err)
		}
	}
	var v any
	if ok, err := hub.Setting(ctx, "assistant.tasks", &v); err != nil || ok {
		t.Fatalf("a manager's key taken: %v %v", ok, err)
	}
	var log struct {
		Stream string `json:"stream"`
	}
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || !ok || log.Stream != "old" {
		t.Fatalf("the older build's event log: %+v %v %v", log, ok, err)
	}
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key = 'hub.event_log'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the event log is still in settings (%d, %v): a switch back would resume it", left, err)
	}
	// This build's own hand-over is there for an older build too, and taking it takes both.
	if err := hub.PutSetting(ctx, "hub.event_log", map[string]string{"stream": "new"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key = 'hub.event_log' AND value->>'stream' = 'new'`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("this build's hand-over not kept for an older one: %d %v", left, err)
	}
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || !ok || log.Stream != "new" {
		t.Fatalf("take: %+v %v %v", log, ok, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key = 'hub.event_log'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("taken, still in settings: %d %v", left, err)
	}
}

// followOlderHub takes up the keys 0421 copied: the two lists are one.
func TestHubKeysAreThoseCopied(t *testing.T) {
	b, err := fs.ReadFile(migrations, "migrations/hub/0421_hub_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	in := regexp.MustCompile(`(?s)key IN \((.*?)\)`).FindStringSubmatch(string(b))
	if in == nil {
		t.Fatal("no key list in 0421")
	}
	var copied []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(in[1], -1) {
		copied = append(copied, m[1])
	}
	want := slices.Clone(hubKeys)
	sort.Strings(copied)
	sort.Strings(want)
	if !slices.Equal(copied, want) {
		t.Fatalf("0421 copies %v, followOlderHub takes %v", copied, want)
	}
	if !strings.Contains(string(b), `codex\_inventory/%`) || !slices.Equal(hubKeyPrefixes, []string{"codex_inventory/"}) {
		t.Fatal("the key families differ")
	}
}

// A fresh install: the hub opens the database before its default manager makes the settings table. Once it is there,
// the hub's keys (those written before it too) are kept there for a build from before hub_settings.
func TestHubKeysKeptForAnOlderBuildOnAFreshInstall(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if err := hub.PutSetting(ctx, "hub_id", "h-1"); err != nil { // no settings table yet
		t.Fatal(err)
	}
	manager, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := hub.PutSetting(ctx, "mutes", map[string][]string{"spaces": {"m/a"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hub_id", "mutes"} {
		var n int
		if err := manager.pool.QueryRow(ctx, `SELECT count(*) FROM settings s JOIN hub_settings h USING (key)
			WHERE key = $1 AND s.value = h.value`, k).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s not kept for an older build: %d %v", k, n, err)
		}
	}
}

// The sync with an older build's settings locks the shared table before it touches hub_settings, as a write of a hub
// key starts there: a hub-key write while another process (a CLI, a starting hub) syncs makes the sync wait, holding
// nothing the write needs next, whether the write's row was there already or is new.
func TestHubKeySyncLocksTheSharedTableFirst(t *testing.T) {
	for _, tc := range []struct{ name, shared, own string }{
		{"a row that is there", `UPDATE settings SET value = '{"spaces":["b"]}', updated_at = now() WHERE key = 'mutes'`,
			`UPDATE hub_settings SET value = '{"spaces":["b"]}', updated_at = now() WHERE key = 'mutes'`},
		{"a new row", `INSERT INTO settings (key, value) VALUES ('codex_inventory/new', '{}')`,
			`INSERT INTO hub_settings (key, value) VALUES ('codex_inventory/new', '{}')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			url := freshDatabase(t)
			unifiedMigrate(t, url, 419)
			conn, err := pgx.Connect(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(ctx)
			if _, err := conn.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('mutes', '{"spaces":["a"]}')`); err != nil {
				t.Fatal(err)
			}
			hub, err := Open(ctx, url, HubSet)
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			// A write of a hub key on its way (PutSetting): the shared table first, then hub_settings.
			put, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer put.Rollback(ctx)
			if _, err := put.Exec(ctx, tc.shared); err != nil {
				t.Fatal(err)
			}
			synced := make(chan error, 1)
			go func() { synced <- hub.syncOlderHub(ctx) }()
			var pid int
			for deadline := time.Now().Add(10 * time.Second); ; {
				err := hub.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname = current_database()
					AND wait_event_type = 'Lock'`).Scan(&pid)
				if err == nil {
					break
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				if time.Now().After(deadline) {
					t.Fatal("the sync does not wait for the write")
				}
				time.Sleep(20 * time.Millisecond)
			}
			var held int
			if err := hub.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid = $1 AND relation = 'hub_settings'::regclass`,
				pid).Scan(&held); err != nil || held != 0 {
				t.Fatalf("the waiting sync holds %d locks on hub_settings (%v)", held, err)
			}
			if _, err := put.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
				t.Fatal(err)
			}
			if _, err := put.Exec(ctx, tc.own); err != nil {
				t.Fatalf("the write waits for the sync on hub_settings: %v", err)
			}
			if err := put.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-synced; err != nil {
				t.Fatalf("sync: %v", err)
			}
		})
	}
}

// This build's event log hand-over, kept in settings for an older build, was taken by one that then served and
// stopped without handing over: the copy left in hub_settings is stale and not resumed. One the older build handed
// over is; one waiting for this build stays in both tables until taken, however often the database is opened first.
func TestHubDropsAHandOverAnOlderBuildTook(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	unifiedMigrate(t, url, 419)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	open := func() *DB {
		t.Helper()
		hub, err := Open(ctx, url, HubSet)
		if err != nil {
			t.Fatal(err)
		}
		return hub
	}
	var log struct {
		Stream string `json:"stream"`
	}
	hub := open()
	if err := hub.PutSetting(ctx, "hub.event_log", map[string]string{"stream": "new"}); err != nil { // it stops
		t.Fatal(err)
	}
	hub.Close()
	if _, err := conn.Exec(ctx, `DELETE FROM settings WHERE key = 'hub.event_log'`); err != nil { // the older build takes it
		t.Fatal(err)
	}
	hub = open()
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || ok {
		t.Fatalf("a hand-over an older build took is resumed: %+v %v %v", log, ok, err)
	}
	// This build stops again; the older build takes it and hands its own over.
	if err := hub.PutSetting(ctx, "hub.event_log", map[string]string{"stream": "new"}); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	if _, err := conn.Exec(ctx, `UPDATE settings SET value = '{"stream":"old"}', updated_at = now() + interval '1 second'
		WHERE key = 'hub.event_log'`); err != nil {
		t.Fatal(err)
	}
	hub = open()
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || !ok || log.Stream != "old" {
		t.Fatalf("the older build's hand-over: %+v %v %v", log, ok, err)
	}
	// This build stops; the database is opened (a CLI, a start that failed) before the next start takes it.
	if err := hub.PutSetting(ctx, "hub.event_log", map[string]string{"stream": "new"}); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	open().Close()
	open().Close()
	hub = open()
	defer hub.Close()
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || !ok || log.Stream != "new" {
		t.Fatalf("this build's hand-over after the database was opened in between: %+v %v %v", log, ok, err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key = 'hub.event_log'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("taken, still in settings: %d %v", n, err)
	}
}

// A fresh install: the hub hands its event log over before its default manager made the settings table, and the table
// appears while the hub is down. That hand-over was never there for an older build to take: it is resumed.
func TestHubKeepsAHandOverFromBeforeTheSharedTable(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.PutSetting(ctx, "hub.event_log", map[string]string{"stream": "first"}); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	manager, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	hub, err = Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	var log struct {
		Stream string `json:"stream"`
	}
	if ok, err := hub.TakeSetting(ctx, "hub.event_log", &log); err != nil || !ok || log.Stream != "first" {
		t.Fatalf("the hand-over from before the shared table: %+v %v %v", log, ok, err)
	}
}

// The shared settings table dropped while the hub runs: its keys go on in hub_settings alone, a take first as well as a
// write.
func TestHubKeysAfterTheSharedTableIsDropped(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	unifiedMigrate(t, url, 419)
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if err := hub.PutSetting(ctx, "look", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.pool.Exec(ctx, `DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	var look string
	if ok, err := hub.TakeSetting(ctx, "look", &look); err != nil || !ok || look != "one" {
		t.Fatalf("take: %q %v %v", look, ok, err)
	}
	if err := hub.PutSetting(ctx, "look", "two"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if ok, err := hub.Setting(ctx, "look", &look); err != nil || !ok || look != "two" {
		t.Fatalf("read: %q %v %v", look, ok, err)
	}
	// A database handle that still takes the table for there: the write.
	hub.olderHub.Store(true)
	if err := hub.PutSetting(ctx, "look", "three"); err != nil {
		t.Fatalf("put on a stale flag: %v", err)
	}
	if ok, err := hub.Setting(ctx, "look", &look); err != nil || !ok || look != "three" {
		t.Fatalf("read: %q %v %v", look, ok, err)
	}
}
