package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexNativeProjectsDriveSessionCatalogAndGrouping(t *testing.T) {
	noFakeCodexBus(t)
	n := remoteTestNode(t)
	home := n.codexHome()
	root, other := filepath.Join(n.home, "shared"), filepath.Join(n.home, "other")
	for _, dir := range []string{home, root, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	updated := time.Now().Add(-24 * time.Hour).Truncate(time.Millisecond)
	projectSQL(t, home, nativeProjectSchema+codexAppSchema+
		`INSERT INTO projects VALUES ('a', 'First project', 0), ('b', 'Second project', 1), ('multi', 'Two roots', 2);`+
		`INSERT INTO project_roots VALUES ('a', 0, `+q(root)+`), ('b', 0, `+q(root)+`), ('multi', 0, `+q(root)+`), ('multi', 1, `+q(other)+`);`+
		codexAppRow(appChat, "", root, "First chat", updated, 0)+
		codexAppRow(appOld, "", root, "Second chat", updated, 0)+
		codexAppRow(appExec, "", root, "First root chat", updated, 0)+
		codexAppRow(appDeleted, "", other, "Second root chat", updated, 0)+
		codexAppRow(appArchived, "", root, "Archived chat", updated, 1)+
		`ALTER TABLE threads ADD COLUMN project_id TEXT;`+
		`UPDATE threads SET project_id = CASE id WHEN `+q(appChat)+` THEN 'a' WHEN `+q(appOld)+` THEN 'b' WHEN `+q(appArchived)+` THEN 'a' ELSE 'multi' END;`)

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	if len(rows) != 4 {
		t.Fatalf("native-only sessions = %+v", rows)
	}
	if _, listed := rows[appArchived]; listed {
		t.Fatal("archived native thread listed")
	}
	if a, b := rows[appChat], rows[appOld]; a.SpaceID != codexDesktopProjectID(home, "a") ||
		b.SpaceID != codexDesktopProjectID(home, "b") || a.SpaceID == b.SpaceID ||
		a.SpaceName != "First project" || b.SpaceName != "Second project" || a.RepoRoot != b.RepoRoot {
		t.Fatalf("projects sharing directory = %+v, %+v", a, b)
	}
	if a, b := rows[appExec], rows[appDeleted]; a.SpaceID != codexDesktopProjectID(home, "multi") ||
		a.SpaceID != b.SpaceID || a.RepoRoot != root || b.RepoRoot != other || a.SpaceIndex != 3 || b.SpaceIndex != 3 {
		t.Fatalf("multi-root project placement = %+v, %+v", a, b)
	}
	workspaces, sources, errs := n.desktopWorkspaces()
	if len(workspaces) != 4 || sources != 1 || len(errs) != 0 {
		t.Fatalf("native-only catalog = %+v, %d, %+v", workspaces, sources, errs)
	}
	for _, row := range rows {
		if !slices.ContainsFunc(workspaces, func(w proto.Workspace) bool { return w.SourceID == row.SpaceID && w.Cwd == row.RepoRoot }) {
			t.Errorf("session has no matching saved project root: %+v", row)
		}
	}
	// A fresh state loader still finds native-only memberships after the desktop JSON has disappeared.
	codexAppStates.Delete(home)
	if app := loadCodexAppState(home); app == nil || len(app.sidebar) != 4 || app.assigned[appOld] != "b" {
		t.Fatalf("native state after cache reset = %+v", app)
	}
}

func TestCodexAppStateRefreshesNativeAssignmentsWithoutJSONChange(t *testing.T) {
	home := t.TempDir()
	state := projectState(t, home, map[string]any{
		"local-projects":                                     map[string]codexAppProject{"legacy": {Name: "Old name"}},
		"thread-project-assignments":                         map[string]any{appOld: map[string]string{"projectId": "native"}},
		"projectless-thread-ids":                             []string{appChat},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{"local:" + home: map[string]string{"legacy": "native"}},
	})
	projectSQL(t, home, nativeProjectSchema+
		`INSERT INTO projects VALUES ('native', 'First name', 0), ('other', 'Other project', 1);`+
		`CREATE TABLE threads (id TEXT PRIMARY KEY, project_id TEXT, archived INTEGER);`+
		`INSERT INTO threads VALUES ('`+appChat+`', 'native', 0);`)
	beforeJSON, _ := os.ReadFile(state)
	first := loadCodexAppState(home)
	if first == nil || first.assigned[appChat] != "legacy" || first.assigned[appOld] != "legacy" || first.inNoProject(appChat) || first.projects["legacy"].Name != "First name" {
		t.Fatalf("merged state = %+v", first)
	}
	projectSQL(t, home, `UPDATE projects SET name = 'Renamed project' WHERE id = 'native'; UPDATE threads SET project_id = 'other';`)
	second := loadCodexAppState(home)
	if second == nil || second == first || second.assigned[appChat] != "other" || second.projects["legacy"].Name != "Renamed project" {
		t.Fatalf("native changes did not refresh state: %+v", second)
	}
	afterJSON, _ := os.ReadFile(state)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("read modified legacy desktop state")
	}
	projectSQL(t, home, `ALTER TABLE projects RENAME COLUMN name TO broken_name;`)
	if stale := loadCodexAppState(home); stale != second {
		t.Fatal("failed native refresh discarded the last complete sidebar state")
	}
}

func TestCodexAppStateFindsNativeProjectsWhenSQLiteBecomesAvailable(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{
		"local-projects": map[string]codexAppProject{"legacy": {Name: "Old name"}},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{"local:" + home: map[string]string{"legacy": "native"}},
	})
	projectSQL(t, home, nativeProjectSchema+`INSERT INTO projects VALUES ('native', 'Current name', 0);`)
	previousDirs, previousPath := codexSQLiteFallbackDirs, os.Getenv("PATH")
	codexSQLiteFallbackDirs = func() []string { return nil }
	t.Cleanup(func() { codexSQLiteFallbackDirs = previousDirs })
	t.Setenv("PATH", t.TempDir())
	if first := loadCodexAppState(home); first == nil || first.projects["legacy"].Name != "Old name" {
		t.Fatalf("legacy state without SQLite = %+v", first)
	}
	codexSQLiteFallbackDirs = previousDirs
	t.Setenv("PATH", previousPath)
	expireProjectCatalog(home)
	if recovered := loadCodexAppState(home); recovered == nil || recovered.projects["legacy"].Name != "Current name" {
		t.Fatalf("reader recovery with unchanged files = %+v", recovered)
	}
}

func TestCodexAppStateClearsOnlyMigratedNativeUnassignments(t *testing.T) {
	home := t.TempDir()
	projectState(t, home, map[string]any{
		"local-projects": map[string]codexAppProject{"legacy": {Name: "Saved project"}},
		"thread-project-assignments": map[string]any{
			appChat: map[string]string{"projectId": "legacy"},
			appOld:  map[string]string{"projectId": "legacy"},
		},
		"app-server-project-id-by-legacy-project-id-by-host": map[string]any{"local:" + home: map[string]string{"legacy": "native"}},
		"app-server-projects-migration-by-host": map[string]any{"local:" + home: map[string]any{
			"threadAssignmentsMigrated": true, "threadAssignmentsReadMigrated": true, "pendingThreadAssignmentIds": []string{appOld},
		}},
	})
	projectSQL(t, home, nativeProjectSchema+
		`INSERT INTO projects VALUES ('native', 'Saved project', 0);`+
		`CREATE TABLE threads (id TEXT PRIMARY KEY, project_id TEXT, archived INTEGER);`+
		`INSERT INTO threads VALUES ('`+appChat+`', NULL, 0), ('`+appOld+`', NULL, 0), ('`+appExec+`', NULL, 0);`)
	state := loadCodexAppState(home)
	if state == nil || state.assigned[appChat] != "" || !state.inNoProject(appChat) ||
		state.assigned[appOld] != "legacy" || state.inNoProject(appOld) || state.listed(appExec) {
		t.Fatalf("migration-aware unassignment = %+v", state)
	}
}
