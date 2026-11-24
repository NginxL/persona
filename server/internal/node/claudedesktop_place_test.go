package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// writeTranscriptIn writes a Claude transcript that ran in cwd, of the desktop app or of the command line.
func writeTranscriptIn(t *testing.T, dir, id, cwd, entrypoint string) {
	t.Helper()
	where, _ := json.Marshal(cwd)
	body := fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-11-05T07:31:12Z","cwd":%s,"entrypoint":%q,"message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n"+
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-05T07:31:20Z","entrypoint":%q,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n", where, entrypoint, entrypoint)
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(body+strings.Repeat(" ", 4096)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, at, at)
}

// The desktop app's sessions are in their project's space (#8521): one the app ran in a worktree it made
// (<repo>/.claude/worktrees/<name>) is in its repository's, the space the Codex App's chats of that repository are in,
// whether or not the app's index names the folder it was started in (the repository is the one the list's Git places
// read: no git runs for it); one in a folder that is no repository is in the folder the app started it in, unless that
// is the home folder. A command line's session gets no space, and neither does a desktop session some host placed.
func TestDesktopSessionsAreInTheirProject(t *testing.T) {
	const (
		inTree   = "abcdabcd-0000-4000-8000-000000000001" // in a worktree the app made, its index naming the repository
		noOrigin = "abcdabcd-0000-4000-8000-000000000002" // the same, its entry without originCwd (an older app)
		inNotes  = "abcdabcd-0000-4000-8000-000000000003" // in a folder below the one the app started it in, no repository
		cli      = "abcdabcd-0000-4000-8000-000000000004" // the command line's, in the same worktree
	)
	base := physicalTempDir(t)
	repo := filepath.Join(base, "src", "app")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := git(context.Background(), dir, args...); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	run(repo, "init")
	run(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	tree := filepath.Join(repo, ".claude", "worktrees", "task-one")
	run(repo, "worktree", "add", "--detach", tree)
	notes := filepath.Join(base, "notes")
	drafts := filepath.Join(notes, "drafts")
	if err := os.MkdirAll(drafts, 0o700); err != nil {
		t.Fatal(err)
	}

	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	forgetOfflineScan()
	t.Cleanup(forgetOfflineScan)
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTranscriptIn(t, dir, inTree, tree, "claude-desktop")
	writeTranscriptIn(t, dir, noOrigin, tree, "claude-desktop")
	writeTranscriptIn(t, dir, inNotes, drafts, "claude-desktop")
	writeTranscriptIn(t, dir, cli, tree, "cli")
	_, third := useDesktopDirs(t)
	// Each entry names the folder its session runs in, as the app writes it (writeDesktopEntry's own cwd is a
	// placeholder): the index's cwd comes before the transcript's when there is no originCwd (#7063).
	field := func(key, dir string) string {
		b, _ := json.Marshal(dir)
		return `,"` + key + `":` + string(b)
	}
	writeDesktopEntry(t, third, inTree, inTree, false, nil, field("cwd", tree)+field("originCwd", repo))
	writeDesktopEntry(t, third, noOrigin, noOrigin, false, nil, field("cwd", tree))
	writeDesktopEntry(t, third, inNotes, inNotes, false, nil, field("cwd", drafts)+field("originCwd", notes))

	awaitGitPlace(t, gitPlaces, tree) // read in the background: the first list after a start may not have it yet
	rows := map[string]proto.Session{}
	for _, s := range n.sessionList(context.Background(), false) {
		rows[s.SessionID] = s
	}
	root, folder := realPath(repo), realPath(notes)
	want := map[string][2]string{
		inTree:   {projectSpace + root, "app"},
		noOrigin: {projectSpace + root, "app"},
		inNotes:  {projectSpace + folder, "notes"},
		cli:      {"", ""},
	}
	for id, w := range want {
		s, ok := rows[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if s.SpaceID != w[0] || s.SpaceName != w[1] {
			t.Errorf("%s: space %q %q, want %q %q", id, s.SpaceID, s.SpaceName, w[0], w[1])
		}
		if kind := map[bool]string{true: proto.SourceClaudeDesktop}[w[0] != ""]; s.SpaceKind != kind {
			t.Errorf("%s: space_kind %q, want %q", id, s.SpaceKind, kind)
		}
	}

	// The space is the one a Codex App chat in that worktree is in (decorateCodexSession).
	if codex := projectSpace + codexProjectRoot(tree, map[string]string{}); rows[inTree].SpaceID != codex {
		t.Errorf("space %q, a Codex App chat's there %q", rows[inTree].SpaceID, codex)
	}

	// A live session of the app is placed the same way, by the folder the app started it in while its repository is not
	// read yet; one in the home folder is in no project, and a row some host placed keeps its space.
	list := []proto.Session{
		{SessionID: "home", Agent: "claude", SourceKind: proto.SourceClaudeDesktop, Cwd: home},
		{SessionID: "live", Agent: "claude", SourceKind: proto.SourceClaudeDesktop, Cwd: tree},
		{SessionID: "hosted", Agent: "claude", SourceKind: proto.SourceClaudeDesktop, Cwd: tree, SpaceKind: "herdr", SpaceID: "w1", SpaceName: "Release"},
	}
	placeClaudeDesktop(list, map[string]string{"home": home, "live": repo}, home)
	if s := list[1]; s.SpaceKind != proto.SourceClaudeDesktop || s.SpaceID != projectSpace+root || s.SpaceName != "app" {
		t.Errorf("live: %q %q %q", s.SpaceKind, s.SpaceID, s.SpaceName)
	}
	if s := list[0]; s.SpaceKind != "" || s.SpaceID != "" {
		t.Errorf("home: %q %q", s.SpaceKind, s.SpaceID)
	}
	if s := list[2]; s.SpaceKind != "herdr" || s.SpaceID != "w1" || s.SpaceName != "Release" {
		t.Errorf("hosted: %q %q %q", s.SpaceKind, s.SpaceID, s.SpaceName)
	}
}
