package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// linkHome moves w onto a home reached through a link (as a container's /home/me is the host's /data/home/me): the
// node's home and every path the test passes it are spelled through the link, while git writes the links between a
// worktree and its repository with it resolved. It returns the resolved home.
func linkHome(t *testing.T, w *worktreeFixture) string {
	t.Helper()
	real := resolved(t, w.home)
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	w.n.home, w.n.dir = link, filepath.Join(link, ".persona")
	w.home, w.repo = link, filepath.Join(link, "proj")
	return real
}

// gitLinks are the two links of the linked worktree at dest: its .git file's gitdir, and the one back from the
// repository's admin folder.
func gitLinks(t *testing.T, dest string) (forward, back string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dest, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	forward = strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir: "))
	admin := forward
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(dest, admin)
	}
	b, err = os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}
	return forward, strings.TrimSpace(string(b))
}

// assertHomeLinks: both links of dest are spelled through home, none through the resolved home, and git works there
// and still lists the worktree as a live one.
func assertHomeLinks(t *testing.T, home, real, repo, dest string) {
	t.Helper()
	forward, back := gitLinks(t, dest)
	for _, l := range []string{forward, back} {
		if !strings.HasPrefix(l, home+string(filepath.Separator)) || strings.HasPrefix(l, real+string(filepath.Separator)) {
			t.Errorf("link %q of %s is not spelled through the home %s (resolved %s)", l, dest, home, real)
		}
	}
	if back != filepath.Join(dest, ".git") {
		t.Errorf("repository's link back = %q, want %q", back, filepath.Join(dest, ".git"))
	}
	runPullGit(t, dest, "status", "--porcelain")
	if list := runPullGit(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, "prunable") {
		t.Errorf("the worktree reads as prunable:\n%s", list)
	}
}

// A task checkout made by git (herdr-core, or a host that makes none) on a linked home: git resolved the paths it wrote,
// and the node rewrites both links to the home's spelling a container's terminal has (#10365).
func TestWorktreeByGitWritesHomeSpelledLinks(t *testing.T) {
	w, _ := herdrCoreWorktreeNode(t)
	real := linkHome(t, &w)
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(str(prep["path"]), "task-x")
	if !strings.HasPrefix(dest, w.home+string(filepath.Separator)) {
		t.Fatalf("checkouts folder %q is not under the home's spelling", dest)
	}
	if _, err := request(t, w.n, "worktree.create", map[string]any{"cwd": w.repo, "branch": "task/x", "path": dest}); err != nil {
		t.Fatal(err)
	}
	assertHomeLinks(t, w.home, real, w.repo, dest)
}

// An Agent's own worktree (worktree.add) and a review copy get the same links.
func TestWorktreeAddAndReviewSnapshotWriteHomeSpelledLinks(t *testing.T) {
	w := worktreeNode(t)
	real := linkHome(t, &w)
	ctx := t.Context()
	added, err := w.n.worktreeAdd(ctx, "", w.repo, "", "Fix the login")
	if err != nil {
		t.Fatal(err)
	}
	assertHomeLinks(t, w.home, real, w.repo, added.Path)
	if err := os.WriteFile(filepath.Join(w.repo, "app.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := w.n.worktreeSnapshot(ctx, "", w.repo, "Review the login")
	if err != nil {
		t.Fatal(err)
	}
	assertHomeLinks(t, w.home, real, w.repo, snap.Path)
}

// On a home that is no link the links stay byte for byte as git wrote them.
func TestPortableWorktreeLinksNoopWithoutLinkedHome(t *testing.T) {
	w := worktreeNode(t)
	if realHome(w.home) != "" {
		t.Skip("the temporary directory is itself reached through a link here")
	}
	dest := filepath.Join(w.home, "trees", "plain")
	runPullGit(t, w.repo, "worktree", "add", "-b", "plain", "--", dest)
	forward, back := gitLinks(t, dest)
	w.n.portableWorktreeLinks(dest)
	if f, b := gitLinks(t, dest); f != forward || b != back {
		t.Fatalf("links changed on a plain home: %q %q, were %q %q", f, b, forward, back)
	}
}

// Relative links (git 2.48's worktree.useRelativePaths, written here by hand so any git runs the test) are left alone.
func TestPortableWorktreeLinksLeavesRelativeLinks(t *testing.T) {
	w := worktreeNode(t)
	linkHome(t, &w)
	dest := filepath.Join(w.home, "trees", "rel")
	runPullGit(t, w.repo, "worktree", "add", "-b", "rel", "--", dest)
	forward, _ := gitLinks(t, dest)
	admin := filepath.Join(w.repo, ".git", "worktrees", filepath.Base(forward))
	relForward, err := filepath.Rel(dest, admin)
	if err != nil {
		t.Fatal(err)
	}
	relBack, err := filepath.Rel(admin, filepath.Join(dest, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".git"), []byte("gitdir: "+relForward+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(relBack+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.n.portableWorktreeLinks(dest)
	if f, b := gitLinks(t, dest); f != relForward || b != relBack {
		t.Fatalf("relative links changed: %q %q", f, b)
	}
}

// A repository outside the home keeps git's links: the home's spelling names nothing there.
func TestPortableWorktreeLinksIgnoresPathsOutsideHome(t *testing.T) {
	w := worktreeNode(t)
	outside := w.repo // the fixture's repository, while the node's home becomes another, linked one
	home, _ := linkedHome(t)
	w.n.home = home
	dest := filepath.Join(t.TempDir(), "outside-tree")
	runPullGit(t, outside, "worktree", "add", "-b", "outside", "--", dest)
	forward, back := gitLinks(t, dest)
	w.n.portableWorktreeLinks(dest)
	if f, b := gitLinks(t, dest); f != forward || b != back {
		t.Fatalf("links outside the home changed: %q %q, were %q %q", f, b, forward, back)
	}
}

// worktree.prepare's branch lookup on a linked home: git lists a checkout made through the resolved spelling, and
// the node answers the checkouts folder's own spelling, says it is in that folder and makes its links portable (the
// repair of a leftover made before this change). The branch of the main checkout is not in that folder.
func TestWorktreePrepareBranchReportsCheckoutsSpelling(t *testing.T) {
	w := worktreeNode(t)
	real := linkHome(t, &w)
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil {
		t.Fatal(err)
	}
	checkouts := str(prep["path"])
	want := filepath.Join(checkouts, "task-todo-x")
	rest, _ := filepath.Rel(w.home, want)
	viaReal := filepath.Join(real, rest)
	if err := os.MkdirAll(filepath.Dir(viaReal), 0o755); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, filepath.Join(real, "proj"), "worktree", "add", "-b", "task/todo_x", "--", viaReal)

	got, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": "task/todo_x"})
	if err != nil {
		t.Fatal(err)
	}
	existing, _ := got["existing"].(map[string]any)
	if str(existing["path"]) != want || existing["in_checkouts"] != true || str(existing["branch"]) != "task/todo_x" {
		t.Fatalf("existing = %v, want path %s in the checkouts folder", existing, want)
	}
	assertHomeLinks(t, w.home, real, w.repo, want)

	got, err = request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": "main"})
	if err != nil {
		t.Fatal(err)
	}
	existing, _ = got["existing"].(map[string]any)
	if str(existing["path"]) != w.repo || existing["in_checkouts"] == true {
		t.Fatalf("the main checkout = %v, want %s outside the checkouts folder", existing, w.repo)
	}
}

// worktree.prepare's refusals carry their code and the checkout's path, so the hub can tell them apart.
func TestWorktreePrepareBranchRefusalsAreCoded(t *testing.T) {
	w := worktreeNode(t)
	at := filepath.Join(w.home, "retained")
	runPullGit(t, w.repo, "worktree", "add", "-b", "fix/reuse", "--", at)
	if err := os.WriteFile(filepath.Join(at, "untracked.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": "fix/reuse", "locale": "en"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeCheckoutDirty || !strings.Contains(re.Message, "uncommitted changes") {
		t.Fatalf("dirty checkout refusal = %v", err)
	}
	var data map[string]string
	if json.Unmarshal(re.Data, &data) != nil || data["path"] != at || data["branch"] != "fix/reuse" {
		t.Fatalf("refusal data = %s", re.Data)
	}
}

// A checkout path that holds something is refused with code worktree_path_taken and its path, in the request's
// language; the node still never adopts or overwrites it.
func TestCheckoutPathTakenIsCoded(t *testing.T) {
	w := worktreeNode(t)
	dest := mkdir(t, filepath.Join(w.home, "taken"))
	if err := os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ locale, words string }{{"", "不是空目录"}, {"en", "already exists and is not empty"}} {
		_, err := request(t, w.n, "worktree.create", map[string]any{"cwd": w.repo, "branch": "task/taken", "path": dest, "locale": c.locale})
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != proto.CodeWorktreePathTaken || !strings.Contains(re.Message, c.words) ||
			!strings.Contains(re.Message, dest) || !strings.Contains(string(re.Data), dest) {
			t.Fatalf("locale %q: refusal = %v", c.locale, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dest, "keep.txt")); err != nil || string(b) != "mine\n" {
		t.Fatalf("the folder changed: %q %v", b, err)
	}
}

// workspace.resolve with terminal: the cwd is the home's own spelling of the directory, while its id stays the
// physical path; without terminal it is the physical path as before. (The hub folds the home's two spellings when it
// compares task folders.)
func TestWorkspaceResolveTerminalSpelling(t *testing.T) {
	home, real := linkedHome(t)
	if err := os.MkdirAll(filepath.Join(real, "src", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, state: newState()}
	for _, asked := range []string{"~/src/app", filepath.Join(real, "src", "app")} {
		w, err := n.resolveWorkspace(asked, true)
		if err != nil || w.Cwd != filepath.Join(home, "src", "app") || w.ID != filepath.Join(real, "src", "app") {
			t.Fatalf("terminal resolve of %s = %+v, %v", asked, w, err)
		}
		if w, err = n.resolveWorkspace(asked, false); err != nil || w.Cwd != filepath.Join(real, "src", "app") {
			t.Fatalf("plain resolve of %s = %+v, %v", asked, w, err)
		}
	}
	raw, err := n.handle(t.Context(), "workspace.resolve", []byte(`{"path":"~/src/app","terminal":true}`))
	if err != nil || raw.(proto.Workspace).Cwd != filepath.Join(home, "src", "app") {
		t.Fatalf("resolve RPC = %+v, %v", raw, err)
	}
}

// realSpelledLinks: both links of dest name the resolved home, as git wrote them before the node made them portable.
func realSpelledLinks(t *testing.T, real, dest string) bool {
	t.Helper()
	forward, back := gitLinks(t, dest)
	return strings.HasPrefix(forward, real+string(filepath.Separator)) && strings.HasPrefix(back, real+string(filepath.Separator))
}

// A leftover of the to-do's branch that git made before (links under the resolved home) and that the node refuses as
// dirty: the refusal names it for a workspace {existing}, so its links are made portable before the refusal, or an
// Agent's terminal there would find no repository.
func TestWorktreePrepareBranchRefusalMakesLinksPortable(t *testing.T) {
	w := worktreeNode(t)
	real := linkHome(t, &w)
	at := filepath.Join(real, "left", "task-todo-x")
	runPullGit(t, filepath.Join(real, "proj"), "worktree", "add", "-b", "task/todo_x", "--", at)
	if !realSpelledLinks(t, real, at) {
		t.Fatal("fixture: git should have written the resolved home's spelling")
	}
	if err := os.WriteFile(filepath.Join(at, "untracked.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": "task/todo_x"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeCheckoutDirty {
		t.Fatalf("dirty leftover = %v, want %s", err, proto.CodeCheckoutDirty)
	}
	assertHomeLinks(t, w.home, real, w.repo, filepath.Join(w.home, "left", "task-todo-x"))
}

// workspace.resolve for a terminal in a checkout git made before (or a folder inside it) makes its links portable; a
// plain resolve leaves the files alone.
func TestWorkspaceResolveTerminalMakesWorktreeLinksPortable(t *testing.T) {
	w := worktreeNode(t)
	real := linkHome(t, &w)
	at := filepath.Join(real, "trees", "old")
	runPullGit(t, filepath.Join(real, "proj"), "worktree", "add", "-b", "old", "--", at)
	mkdir(t, filepath.Join(at, "sub"))
	if _, err := w.n.resolveWorkspace("~/trees/old/sub", false); err != nil || !realSpelledLinks(t, real, at) {
		t.Fatalf("a plain resolve changed the links (or failed: %v)", err)
	}
	got, err := w.n.resolveWorkspace("~/trees/old/sub", true)
	if err != nil || got.Cwd != filepath.Join(w.home, "trees", "old", "sub") {
		t.Fatalf("terminal resolve = %+v, %v", got, err)
	}
	assertHomeLinks(t, w.home, real, w.repo, filepath.Join(w.home, "trees", "old"))
	// The main checkout's .git is a folder: nothing to rewrite, and the resolve still answers.
	if got, err := w.n.resolveWorkspace("~/proj", true); err != nil || got.Cwd != w.repo {
		t.Fatalf("terminal resolve of the main checkout = %+v, %v", got, err)
	}
}

// On Windows the links stay as Git for Windows wrote them, forward slashes and all.
func TestPortableWorktreeLinksLeavesWindowsAlone(t *testing.T) {
	w := worktreeNode(t)
	real := linkHome(t, &w)
	old := worktreeLinksGOOS
	worktreeLinksGOOS = "windows"
	t.Cleanup(func() { worktreeLinksGOOS = old })
	dest := filepath.Join(real, "trees", "win")
	runPullGit(t, filepath.Join(real, "proj"), "worktree", "add", "-b", "win", "--", dest)
	forward, back := gitLinks(t, dest)
	w.n.portableWorktreeLinks(dest)
	if f, b := gitLinks(t, dest); f != forward || b != back {
		t.Fatalf("links rewritten on Windows: %q %q, were %q %q", f, b, forward, back)
	}
}
