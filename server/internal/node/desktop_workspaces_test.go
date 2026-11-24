package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func desktopCatalogJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopWorkspacesWithoutTerminalOrSessions(t *testing.T) {
	home := physicalTempDir(t)
	root := filepath.Join(home, "code", "app")
	other := filepath.Join(home, "code", "other")
	for _, dir := range []string{root, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	desktopCatalogJSON(t, filepath.Join(home, ".codex", ".codex-global-state.json"), map[string]any{
		"local-projects": map[string]codexAppProject{
			"first": {Name: "Renamed project", RootPaths: []string{root}},
			"multi": {Name: "Two directories", RootPaths: []string{other, root, other}},
			"empty": {Name: "No directory yet"},
		},
		"project-order": []string{"empty", "first", "multi"},
	})
	n := &Node{home: home}
	got, err := n.listWorkspaces(context.Background())
	if err != nil || len(got.Workspaces) != 4 || len(got.Errors) != 0 {
		t.Fatalf("desktop-only catalog = %+v, %v", got, err)
	}
	if empty := got.Workspaces[0]; empty.Title != "No directory yet" || empty.Index != 1 || empty.Cwd != "" || empty.Available {
		t.Fatalf("empty project = %+v", empty)
	}
	first := got.Workspaces[1]
	if first.Title != "Renamed project" || first.Source != "codex_app" || first.SourceID != first.ProjectID ||
		first.Cwd != root || !first.Online || !first.Available || first.Index != 2 {
		t.Fatalf("saved project = %+v", first)
	}
	if got.Workspaces[2].ProjectID != got.Workspaces[3].ProjectID || got.Workspaces[2].ID == got.Workspaces[3].ID {
		t.Fatalf("multi-root identity = %+v", got.Workspaces[2:])
	}
}

func TestDesktopWorkspacesKeepRuntimeCatalog(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	dir := resolved(t, t.TempDir())
	desktopCatalogJSON(t, filepath.Join(n.codexHome(), ".codex-global-state.json"), map[string]any{
		"local-projects": map[string]codexAppProject{"desktop": {Name: "Desktop project", RootPaths: []string{dir}}},
	})
	if got := catalog(t, n); len(got) != 1 {
		t.Fatalf("desktop catalog while the runtime is away: %+v", got)
	}
	rt := dialRuntime(t, n, paseoWorkspacesHello())
	if err := rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "wks_main", "title": "Runtime project", "cwd": dir, "kind": "checkout", "space_id": "prj_1", "space_name": "Runtime project", "repo_root": dir},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "desktop and runtime catalogs", func() bool { return len(catalog(t, n)) == 2 })
	got := catalog(t, n)
	if got["wks_main"].Source != proto.EnginePaseo || got["wks_main"].ProjectID != "prj_1" {
		t.Fatalf("runtime project lost its identity: %+v", got)
	}
	for _, row := range got {
		if row.Source == codexAppSource && row.Title == "Desktop project" && row.Cwd == dir {
			return
		}
	}
	t.Fatalf("runtime catalog hid the desktop project in the same directory: %+v", got)
}

func TestDesktopWorkspacesKeepPartialSourcesAndEmptyCatalog(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".codex", ".codex-global-state.json")
	desktopCatalogJSON(t, state, map[string]any{"local-projects": map[string]any{}})
	failed := &workspaceHost{bareHost: bareHost{kind: host.Orca}, err: errors.New("snapshot failed")}
	n := &Node{home: home, hosts: host.NewRegistry(failed)}
	got, err := n.listWorkspaces(context.Background())
	if err != nil || got.Workspaces == nil || len(got.Workspaces) != 0 || len(got.Errors) != 1 || got.Errors[0].Source != host.Orca {
		t.Fatalf("valid empty desktop source with failed host = %+v, %v", got, err)
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	// With neither a saved desktop source nor a successful engine, a failure remains a failure.
	if _, err := n.listWorkspaces(context.Background()); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("failed source without desktop = %v", err)
	}
}

func TestDesktopWorkspacesRespectScopeAndBlockedDirectories(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "private")
	desktopCatalogJSON(t, filepath.Join(home, ".codex", ".codex-global-state.json"), map[string]any{
		"local-projects": map[string]codexAppProject{"p": {Name: "Project", RootPaths: []string{root}}},
	})
	n := &Node{home: home, sessionScope: &nodeSessionScope{dir: root}}
	if projects, sources, errs := n.desktopWorkspaces(); len(projects) != 0 || sources != 0 || len(errs) != 0 {
		t.Fatalf("scoped node discovered desktop projects: %+v %d %+v", projects, sources, errs)
	}
	n.sessionScope = nil
	n.cdirs = &claudeDirs{blocked: []string{root}}
	if projects, sources, errs := n.desktopWorkspaces(); len(projects) != 0 || sources != 1 || len(errs) != 0 {
		t.Fatalf("blocked directory listed: %+v %d %+v", projects, sources, errs)
	}
}

func TestClaudeDesktopCatalogAndRowsShareOriginalProject(t *testing.T) {
	n, home := testNode(t)
	home = realPath(home)
	n.home = home
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	original, running := filepath.Join(home, "app"), filepath.Join(home, "worktree")
	for _, dir := range []string{original, running} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, desktop := useDesktopDirs(t)
	entry := filepath.Join(desktop, "claude-code-sessions", "account", "org", "local_"+sidA+".json")
	desktopCatalogJSON(t, entry, map[string]any{"sessionId": "local_" + sidA, "cliSessionId": sidA,
		"title": "Saved title", "cwd": running, "originCwd": original})
	desktopCatalogJSON(t, filepath.Join(filepath.Dir(entry), "local_"+sidB+".json"), map[string]any{
		"cliSessionId": sidB, "isArchived": true, "cwd": filepath.Join(home, "archived")})
	line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u1", "cwd": running,
		"entrypoint": "claude-desktop", "message": map[string]string{"role": "user", "content": "Check this project"}})
	writeTranscript(t, home, running, sidA, time.Now(), string(line),
		`{"type":"custom-title","customTitle":"Transcript title"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Ready"}]}}`)

	index := readClaudeDesktopIndex(home)
	projects, sources, errs := n.desktopWorkspaces()
	if sources != 1 || len(projects) != 1 || len(errs) != 0 {
		t.Fatalf("Claude catalog = %+v, %d, %+v", projects, sources, errs)
	}
	w := projects[0]
	if w.Source != proto.SourceClaudeDesktop || w.SourceID != projectSpace+realPath(original) || w.Cwd != original || w.Title != "app" || !w.Available {
		t.Fatalf("Claude project = %+v", w)
	}
	offline := byID(n.scanOffline())[sidA]
	if offline.SpaceID != w.SourceID || offline.SpaceKind != w.Source || offline.SourceKind != proto.SourceClaudeDesktop ||
		offline.RepoRoot != original || offline.Cwd != running || offline.Title != "Transcript title" || offline.SessionID != sidA {
		t.Fatalf("offline desktop identity = %+v", offline)
	}
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: running})
	liveRows := n.claudeSessions([]claudeProc{{SessionID: sidA, Kind: "interactive", Entrypoint: "claude-desktop", Cwd: running, Status: "busy"}}, nil, nil)
	if len(liveRows) != 1 {
		t.Fatalf("live desktop rows = %+v", liveRows)
	}
	live := liveRows[0]
	if live.SpaceID != offline.SpaceID || live.RepoRoot != offline.RepoRoot || live.Cwd != running || live.Title != "Transcript title" || live.Status != proto.StatusWorking {
		t.Fatalf("live desktop placement = %+v", live)
	}
	terminal := proto.Session{SessionID: sidA, Agent: "claude", PaneID: "w1:p1", SpaceID: "terminal", Cwd: running}
	n.decorateClaudeDesktop(&terminal, index, map[string]string{})
	if terminal.SpaceID != "terminal" || terminal.SourceKind != "" {
		t.Fatalf("desktop index took a terminal-owned session: %+v", terminal)
	}
}

func TestClaudeDesktopProjectRootUsesGitSharedDirectory(t *testing.T) {
	home := physicalTempDir(t)
	root := filepath.Join(home, "project")
	linked := filepath.Join(home, "linked")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := git(context.Background(), root, args...); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	run("init")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	run("worktree", "add", "--detach", linked)
	index := claudeDesktopIndex{about: map[string]claudeDesktopAbout{sidA: {cwd: linked}}}
	n := &Node{home: home}
	s := proto.Session{SessionID: sidA, Agent: "claude", Cwd: linked}
	n.decorateClaudeDesktop(&s, index, map[string]string{})
	if s.RepoRoot != root || s.SpaceID != projectSpace+realPath(root) || s.SpaceName != "project" || s.Cwd != linked {
		t.Fatalf("linked desktop session = %+v", s)
	}
}

func TestClaudeDesktopDirectoryProjectsExcludeHomeAndAncestors(t *testing.T) {
	home := physicalTempDir(t)
	root := filepath.Join(home, "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, desktop := useDesktopDirs(t)
	index := claudeDesktopIndex{about: map[string]claudeDesktopAbout{}}
	n := &Node{home: home}
	for _, cwd := range []string{home, filepath.Dir(home), root} {
		id := newUUID()
		index.about[id] = claudeDesktopAbout{cwd: cwd}
		desktopCatalogJSON(t, filepath.Join(desktop, "claude-code-sessions", "account", "org", "local_"+id+".json"),
			map[string]any{"cliSessionId": id, "cwd": cwd})
		s := proto.Session{SessionID: id, Agent: "claude", Cwd: cwd}
		n.decorateClaudeDesktop(&s, index, map[string]string{})
		if cwd == root {
			if s.SpaceKind != proto.SourceClaudeDesktop || s.SpaceID != projectSpace+root {
				t.Errorf("project directory placement = %+v", s)
			}
		} else if s.SpaceKind != "" || s.SpaceID != "" {
			t.Errorf("home or ancestor became a project: %+v", s)
		}
	}
	rows, sources, errs := n.desktopWorkspaces()
	if len(rows) != 1 || sources != 1 || len(errs) != 0 || rows[0].SourceID != projectSpace+root || rows[0].Cwd != root {
		t.Fatalf("directory catalog should omit home and ancestors: %+v, %d, %+v", rows, sources, errs)
	}
}

func TestClaudeDesktopDecorationPreservesOwnedSpaces(t *testing.T) {
	home := physicalTempDir(t)
	environment := claudeDesktopEnvironmentID("profile", "account", "saved")
	index := claudeDesktopIndex{
		about:        map[string]claudeDesktopAbout{sidA: {cwd: home, environment: environment}},
		environments: map[string]claudeDesktopEnvironment{environment: {Name: "Saved environment", Cwd: home}},
	}
	n := &Node{home: home}
	for _, row := range []proto.Session{
		{SessionID: sidA, Agent: "claude", SourceKind: proto.SourceClaudeDesktop, Cwd: home, SpaceKind: "herdr", SpaceID: "w1", SpaceName: "Release"},
		{SessionID: sidA, Agent: "claude", SourceKind: proto.SourceClaudeDesktop, Cwd: home, Assistant: true},
	} {
		before := row
		n.decorateClaudeDesktop(&row, index, map[string]string{})
		if row.SpaceKind != before.SpaceKind || row.SpaceID != before.SpaceID || row.SpaceName != before.SpaceName {
			t.Errorf("desktop index changed an owned space: %+v, before %+v", row, before)
		}
	}
	// An explicitly saved environment is still a project when its folder is home.
	row := proto.Session{SessionID: sidA, Agent: "claude", Cwd: home}
	n.decorateClaudeDesktop(&row, index, map[string]string{})
	if row.SpaceKind != proto.SourceClaudeDesktop || row.SpaceID != environment || row.SpaceName != "Saved environment" {
		t.Fatalf("explicit environment lost its identity: %+v", row)
	}
}
