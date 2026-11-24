package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const nativeProjectSchema = `CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL, position INTEGER);
CREATE TABLE project_roots (project_id TEXT NOT NULL, position INTEGER NOT NULL, path TEXT NOT NULL);`

func projectState(t *testing.T, home string, state any) string {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".codex-global-state.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func projectSQL(t *testing.T, home, sql string) string {
	t.Helper()
	p := filepath.Join(home, "state_5.sqlite")
	cmd := exec.Command(sqlite3WithJSON(t), p)
	// Read SQL as UTF-8 bytes; Windows sqlite builds may decode command-line SQL with the active code page.
	cmd.Stdin = strings.NewReader(sql)
	hideCodexSQLiteWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	return p
}

func expireProjectCatalog(home string) {
	codexProjectCatalogs.Lock()
	c := codexProjectCatalogs.byHome[home]
	codexProjectCatalogs.Unlock()
	if c != nil {
		c.Lock()
		c.at = time.Time{}
		c.Unlock()
	}
}

func TestCodexDesktopProjectsLegacyAndEmptySources(t *testing.T) {
	home := t.TempDir()
	p, order, err := codexDesktopProjects(home)
	if err != nil || p != nil || order != nil {
		t.Fatalf("absent source = %v, %v, %v", p, order, err)
	}
	projectState(t, home, map[string]any{"local-projects": map[string]any{}})
	p, order, err = codexDesktopProjects(home)
	if err != nil || p == nil || len(p) != 0 || len(order) != 0 {
		t.Fatalf("empty source = %v, %v, %v", p, order, err)
	}
	projectState(t, home, map[string]any{
		"local-projects": map[string]any{"z": codexAppProject{Name: "Last"}, "a": codexAppProject{Name: "First"}},
		"project-order":  []string{"z", "z", "gone"},
	})
	p, order, err = codexDesktopProjects(home)
	if err != nil || len(p) != 2 || !reflect.DeepEqual(order, []string{"z", "a"}) {
		t.Fatalf("legacy projects = %v, %v, %v", p, order, err)
	}
}

func TestCodexDesktopProjectsMergeNativeAndPending(t *testing.T) {
	home := t.TempDir()
	root, other := filepath.Join(home, "project with spaces"), filepath.Join(home, "other")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	state := projectState(t, home, map[string]any{
		"local-projects": map[string]any{
			"legacy":  codexAppProject{Name: "Old name", RootPaths: []string{filepath.Join(home, "old")}},
			"pending": codexAppProject{Name: "Pending", RootPaths: []string{other}},
		},
		"project-order": []string{"legacy", "pending"},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{
			"local:" + home: map[string]string{"legacy": "native"},
			"remote:other":  map[string]string{"pending": "empty"},
		},
	})
	db := projectSQL(t, home, nativeProjectSchema+
		`INSERT INTO projects VALUES ('native', 'Renamed', 4), ('empty', 'No roots yet', 2), ('new', 'Native only', 9);`+
		`INSERT INTO project_roots VALUES ('native', 1, `+q(other)+`), ('native', 0, `+q(root)+`), ('new', 0, `+q(other)+`);`)
	beforeState, _ := os.ReadFile(state)
	beforeDB, _ := os.ReadFile(db)
	p, order, err := codexDesktopProjects(home)
	if err != nil || len(p) != 4 || !reflect.DeepEqual(order, []string{"empty", "legacy", "new", "pending"}) {
		t.Fatalf("merged = %v, %v, %v", p, order, err)
	}
	if p["legacy"].Name != "Renamed" || !reflect.DeepEqual(p["legacy"].RootPaths, []string{root, other}) {
		t.Fatalf("native values did not replace mapped row: %+v", p["legacy"])
	}
	if _, duplicate := p["native"]; duplicate || p["pending"].Name != "Pending" || p["empty"].Name != "No roots yet" {
		t.Fatalf("migration lost or duplicated project: %v", p)
	}
	afterState, _ := os.ReadFile(state)
	afterDB, _ := os.ReadFile(db)
	if string(beforeState) != string(afterState) || string(beforeDB) != string(afterDB) {
		t.Fatal("reading projects changed source state")
	}
}

func TestCodexDesktopProjectsIgnoreOtherHomeMapping(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{
		"local-projects": map[string]any{"legacy": codexAppProject{Name: "Unmigrated"}},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{"local:" + t.TempDir(): map[string]string{"legacy": "native"}},
	})
	projectSQL(t, home, nativeProjectSchema+`INSERT INTO projects VALUES ('native', 'This home', 0);`)
	p, _, err := codexDesktopProjects(home)
	if err != nil || len(p) != 2 || p["legacy"].Name != "Unmigrated" || p["native"].Name != "This home" {
		t.Fatalf("borrowed another home's mapping: %v, %v", p, err)
	}
}

func TestCodexDesktopProjectsNativeWithoutLegacyState(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+`INSERT INTO projects VALUES ('empty', 'Empty project', 0);`)
	p, order, err := codexDesktopProjects(home)
	if err != nil || len(p) != 1 || p["empty"].Name != "Empty project" || !reflect.DeepEqual(order, []string{"empty"}) {
		t.Fatalf("native-only projects = %v, %v, %v", p, order, err)
	}
}

func TestCodexDesktopProjectsOldSchemaKeepsLegacy(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{"local-projects": map[string]any{"legacy": codexAppProject{Name: "Still here"}}})
	projectSQL(t, home, `CREATE TABLE threads (id TEXT);`)
	p, order, err := codexDesktopProjects(home)
	if err != nil || len(p) != 1 || p["legacy"].Name != "Still here" || !reflect.DeepEqual(order, []string{"legacy"}) {
		t.Fatalf("old schema = %v, %v, %v", p, order, err)
	}
}

func TestCodexNativeProjectsRootSchemasAndOptionalPositions(t *testing.T) {
	for _, rootColumn := range []string{"root", "path"} {
		for _, positions := range []struct {
			name          string
			project, root bool
		}{
			{name: "unordered"},
			{name: "project order", project: true},
			{name: "root order", root: true},
			{name: "both orders", project: true, root: true},
		} {
			t.Run(rootColumn+"/"+positions.name, func(t *testing.T) {
				home := t.TempDir()
				firstRoot, lastRoot := filepath.Join(home, "A root"), filepath.Join(home, "Z 中文")
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
				projectColumn, rootPositionColumn := "", ""
				projectA, projectB, rootA, rootB := "", "", "", ""
				if positions.project {
					projectColumn, projectA, projectB = ", position INTEGER", ", 2", ", 1"
				}
				if positions.root {
					rootPositionColumn, rootA, rootB = ", position INTEGER", ", 1", ", 0"
				}
				db := projectSQL(t, home, fmt.Sprintf(
					`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT%s);
CREATE TABLE project_roots (project_id TEXT, %s TEXT%s);
INSERT INTO projects VALUES ('a', 'Empty project'%s), ('b', 'Several roots'%s);
INSERT INTO project_roots VALUES ('b', %s%s), ('b', %s%s);`,
					projectColumn, rootColumn, rootPositionColumn, projectA, projectB,
					quote(lastRoot), rootB, quote(firstRoot), rootA))
				projects, order, supported, err := readCodexNativeProjects(context.Background(), db)
				wantOrder, wantRoots := []string{"a", "b"}, []string{firstRoot, lastRoot}
				if positions.project {
					wantOrder = []string{"b", "a"}
				}
				if positions.root {
					wantRoots = []string{lastRoot, firstRoot}
				}
				if err != nil || !supported || len(projects) != 2 || !reflect.DeepEqual(order, wantOrder) ||
					!reflect.DeepEqual(projects["b"].RootPaths, wantRoots) || projects["a"].Name != "Empty project" {
					t.Fatalf("schema %s = %v, %v, supported %v, %v", rootColumn, projects, order, supported, err)
				}
			})
		}
	}
}

func TestCodexDesktopProjectsFailedRefreshKeepsLastCatalog(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+`INSERT INTO projects VALUES ('native', 'Keep me', 0);`)
	if _, _, err := codexDesktopProjects(home); err != nil {
		t.Fatal(err)
	}
	projectSQL(t, home, `ALTER TABLE projects RENAME COLUMN name TO other_name;`)
	expireProjectCatalog(home)
	p, order, err := codexDesktopProjects(home)
	if err == nil || p["native"].Name != "Keep me" || !reflect.DeepEqual(order, []string{"native"}) {
		t.Fatalf("failed refresh = %v, %v, %v", p, order, err)
	}
	projectSQL(t, home, `ALTER TABLE projects RENAME COLUMN other_name TO name; UPDATE projects SET name = 'Recovered';`)
	expireProjectCatalog(home)
	p, _, err = codexDesktopProjects(home)
	if err != nil || p["native"].Name != "Recovered" {
		t.Fatalf("recovered refresh = %v, %v", p, err)
	}
}

func TestCodexNativeProjectsMissingProgramKeepsLegacy(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{"local-projects": map[string]any{"legacy": codexAppProject{Name: "Available"}}})
	db := projectSQL(t, home, nativeProjectSchema)
	old := codexSQLiteFallbackDirs
	codexSQLiteFallbackDirs = func() []string { return nil }
	t.Cleanup(func() { codexSQLiteFallbackDirs = old })
	t.Setenv("PATH", t.TempDir())
	if _, _, supported, err := readCodexNativeProjects(context.Background(), db); supported || err != nil {
		t.Fatalf("missing sqlite = %v, %v", supported, err)
	}
	p, _, err := codexDesktopProjects(home)
	if err != nil || p["legacy"].Name != "Available" {
		t.Fatalf("legacy without sqlite = %v, %v", p, err)
	}
}

func TestCodexDesktopProjectsCacheAndCorruptState(t *testing.T) {
	home := t.TempDir()
	state := projectState(t, home, map[string]any{"local-projects": map[string]any{"legacy": codexAppProject{Name: "Keep me"}}})
	p, _, err := codexDesktopProjects(home)
	if err != nil || p["legacy"].Name != "Keep me" {
		t.Fatal(p, err)
	}
	codexProjectCatalogs.Lock()
	c := codexProjectCatalogs.byHome[home]
	codexProjectCatalogs.Unlock()
	c.Lock()
	before := c.at
	c.Unlock()
	codexDesktopProjects(home)
	c.Lock()
	after := c.at
	c.Unlock()
	if !before.Equal(after) {
		t.Fatal("unchanged catalog read again within refresh window")
	}
	if err := os.WriteFile(state, []byte(`{"local-projects":`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err = codexDesktopProjects(home)
	if err == nil || p["legacy"].Name != "Keep me" {
		t.Fatalf("corrupt state discarded last catalog: %v, %v", p, err)
	}
}

func TestCodexDesktopProjectsMissingRootsKeepsLastCatalog(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+`INSERT INTO projects VALUES ('native', 'Keep me', 0);`)
	if _, _, err := codexDesktopProjects(home); err != nil {
		t.Fatal(err)
	}
	projectSQL(t, home, `DROP TABLE project_roots;`)
	expireProjectCatalog(home)
	p, order, err := codexDesktopProjects(home)
	if err == nil || p["native"].Name != "Keep me" || !reflect.DeepEqual(order, []string{"native"}) {
		t.Fatalf("partial schema discarded last catalog = %v, %v, %v", p, order, err)
	}
}

const nativeProjectThreadSchema = `CREATE TABLE threads (id TEXT PRIMARY KEY, project_id TEXT, archived INTEGER NOT NULL DEFAULT 0);`

func TestCodexDesktopProjectSnapshotNativeAssignments(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{
		"local-projects": map[string]any{"legacy": codexAppProject{Name: "Before migration"}},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{
			"local:" + home: map[string]string{"legacy": "native"},
		},
	})
	projectSQL(t, home, nativeProjectSchema+nativeProjectThreadSchema+
		`INSERT INTO projects VALUES ('native', 'Migrated', 0), ('new', 'Native only', 1);`+
		`INSERT INTO threads VALUES ('migrated', 'native', 0), ('native-only', 'new', 0), ('archived', 'native', 1), `+
		`('unknown-project', 'gone', 0), ('empty-project', '', 0), ('null-project', NULL, 0);`)
	snapshot, err := codexDesktopProjectSnapshot(home)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stamp == "" || len(snapshot.Projects) != 2 || !reflect.DeepEqual(snapshot.Order, []string{"legacy", "new"}) {
		t.Fatalf("merged snapshot = %+v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.NativeProjectIDs, map[string]string{"native": "legacy", "new": "new"}) {
		t.Fatalf("native project aliases = %v", snapshot.NativeProjectIDs)
	}
	if !reflect.DeepEqual(snapshot.ThreadProjects, map[string]string{"migrated": "legacy", "native-only": "new"}) {
		t.Fatalf("active project assignments = %v", snapshot.ThreadProjects)
	}
}

func TestCodexDesktopProjectSnapshotOldThreadSchemas(t *testing.T) {
	for _, tc := range []struct {
		name, schema string
	}{
		{name: "no threads table"},
		{name: "no project column", schema: `CREATE TABLE threads (id TEXT PRIMARY KEY, archived INTEGER); INSERT INTO threads VALUES ('old', 0);`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			projectSQL(t, home, nativeProjectSchema+tc.schema+`INSERT INTO projects VALUES ('native', 'Still available', 0);`)
			snapshot, err := codexDesktopProjectSnapshot(home)
			if err != nil || snapshot.Projects["native"].Name != "Still available" || len(snapshot.ThreadProjects) != 0 {
				t.Fatalf("old assignment schema = %+v, %v", snapshot, err)
			}
		})
	}
}

func TestCodexDesktopProjectSnapshotRefreshNativeAssignments(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+nativeProjectThreadSchema+
		`INSERT INTO projects VALUES ('first', 'First', 0), ('second', 'Second', 1); INSERT INTO threads VALUES ('thread', 'first', 0);`)
	before, err := codexDesktopProjectSnapshot(home)
	if err != nil || before.ThreadProjects["thread"] != "first" {
		t.Fatalf("initial assignment = %+v, %v", before, err)
	}
	projectSQL(t, home, `UPDATE threads SET project_id = 'second';`)
	after, err := codexDesktopProjectSnapshot(home)
	if err != nil || after.ThreadProjects["thread"] != "second" || after.Stamp == before.Stamp {
		t.Fatalf("database update did not refresh snapshot = %+v, %v", after, err)
	}
	if before.ThreadProjects["thread"] != "first" {
		t.Fatal("refresh mutated the previous snapshot")
	}
	projectSQL(t, home, `UPDATE threads SET archived = 1;`)
	archived, err := codexDesktopProjectSnapshot(home)
	if err != nil || len(archived.ThreadProjects) != 0 || archived.Stamp == after.Stamp {
		t.Fatalf("archive did not refresh assignments = %+v, %v", archived, err)
	}
}

func TestCodexDesktopProjectSnapshotFailedAssignmentRefreshKeepsSnapshot(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+nativeProjectThreadSchema+
		`INSERT INTO projects VALUES ('native', 'Keep me', 0); INSERT INTO threads VALUES ('thread', 'native', 0);`)
	before, err := codexDesktopProjectSnapshot(home)
	if err != nil {
		t.Fatal(err)
	}
	projectSQL(t, home, `ALTER TABLE threads RENAME COLUMN archived TO broken_archived; UPDATE projects SET name = 'Not yet';`)
	after, err := codexDesktopProjectSnapshot(home)
	if err == nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("assignment schema failure discarded previous snapshot = %+v, %v", after, err)
	}
	projectSQL(t, home, `ALTER TABLE threads RENAME COLUMN broken_archived TO archived; UPDATE projects SET name = 'Recovered';`)
	recovered, err := codexDesktopProjectSnapshot(home)
	if err != nil || recovered.Projects["native"].Name != "Recovered" || recovered.ThreadProjects["thread"] != "native" || recovered.Stamp == before.Stamp {
		t.Fatalf("snapshot did not recover = %+v, %v", recovered, err)
	}
}

func TestCodexDesktopProjectSnapshotProjectlessMigrationAuthority(t *testing.T) {
	home := t.TempDir()
	projectSQL(t, home, nativeProjectSchema+nativeProjectThreadSchema+
		`INSERT INTO projects VALUES ('native', 'Native', 0);`+
		`INSERT INTO threads VALUES ('legacy-null', NULL, 0), ('legacy-empty', '', 0), ('projectless-null', NULL, 0), `+
		`('live-native', 'native', 0), ('unrelated-null', NULL, 0), ('unrelated-empty', '', 0), `+
		`('archived', NULL, 1), ('missing-project', 'gone', 0);`)
	complete := codexProjectMigration{ThreadAssignmentsMigrated: true, ThreadAssignmentsReadMigrated: true}
	for _, tc := range []struct {
		name     string
		host     string
		marker   codexProjectMigration
		explicit []string
	}{
		{name: "no migration marker"},
		{name: "assignment migration only", host: "local:" + home, marker: codexProjectMigration{ThreadAssignmentsMigrated: true}},
		{name: "read migration only", host: "local:" + home, marker: codexProjectMigration{ThreadAssignmentsReadMigrated: true}},
		{name: "complete migration", host: "local:" + home, marker: complete, explicit: []string{"legacy-null", "legacy-empty", "projectless-null"}},
		{name: "pending memberships", host: "local:" + home, marker: codexProjectMigration{
			ThreadAssignmentsMigrated: true, ThreadAssignmentsReadMigrated: true,
			PendingThreadAssignmentIDs: []string{"legacy-null", "projectless-null"},
		}, explicit: []string{"legacy-empty"}},
		{name: "other local home", host: "local:" + t.TempDir(), marker: complete},
		{name: "remote host", host: "remote:" + home, marker: complete},
		{name: "relative home", host: "local:relative", marker: complete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			migration := map[string]codexProjectMigration{}
			if tc.host != "" {
				migration[tc.host] = tc.marker
			}
			projectState(t, home, map[string]any{
				"local-projects": map[string]any{"legacy": codexAppProject{Name: "Legacy"}},
				"thread-project-assignments": map[string]any{
					"legacy-null":  map[string]string{"projectKind": "local", "projectId": "legacy"},
					"legacy-empty": map[string]string{"projectKind": "local", "projectId": "legacy"},
					"archived":     map[string]string{"projectKind": "local", "projectId": "legacy"},
				},
				"projectless-thread-ids":                []string{"projectless-null"},
				"app-server-projects-migration-by-host": migration,
			})
			snapshot, err := codexDesktopProjectSnapshot(home)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"live-native": "native"}
			for _, thread := range tc.explicit {
				want[thread] = ""
			}
			if !reflect.DeepEqual(snapshot.ThreadProjects, want) {
				t.Fatalf("projectless authority = %v, want %v", snapshot.ThreadProjects, want)
			}
		})
	}
}

func TestCodexDesktopProjectSnapshotLegacyMigrationMarkers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		local    any
		complete bool
	}{
		{name: "legacy numeric host"},
		{name: "numeric current host", local: 1},
		{name: "unknown current host format", local: []any{true}},
		{name: "malformed current host fields", local: map[string]any{"threadAssignmentsMigrated": "yes"}},
		{name: "complete current host ignores foreign markers", local: codexProjectMigration{
			ThreadAssignmentsMigrated: true, ThreadAssignmentsReadMigrated: true,
		}, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			markers := map[string]any{
				"local": 1, "remote:example": []any{1}, "local:" + t.TempDir(): "legacy",
			}
			if tc.local != nil {
				markers["local:"+home] = tc.local
			}
			projectState(t, home, map[string]any{
				"local-projects":                        map[string]any{"legacy": codexAppProject{Name: "Keep assignment"}},
				"thread-project-assignments":            map[string]any{"old-null": map[string]string{"projectId": "legacy"}},
				"app-server-projects-migration-by-host": markers,
			})
			projectSQL(t, home, nativeProjectSchema+nativeProjectThreadSchema+
				`INSERT INTO projects VALUES ('native', 'Native project', 0);
INSERT INTO threads VALUES ('old-null', NULL, 0), ('native-chat', 'native', 0);`)
			snapshot, err := codexDesktopProjectSnapshot(home)
			if err != nil || snapshot.Projects["legacy"].Name != "Keep assignment" || snapshot.Projects["native"].Name != "Native project" {
				t.Fatalf("markers discarded project catalog = %+v, %v", snapshot, err)
			}
			want := map[string]string{"native-chat": "native"}
			if tc.complete {
				want["old-null"] = ""
			}
			if !reflect.DeepEqual(snapshot.ThreadProjects, want) {
				t.Fatalf("marker authority = %v, want %v", snapshot.ThreadProjects, want)
			}
		})
	}
}
