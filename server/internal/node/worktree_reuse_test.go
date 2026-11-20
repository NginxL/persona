package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestWorktreePrepareFindsCheckedOutBranch(t *testing.T) {
	w := worktreeNode(t)
	// Git says where the checkout is with the home's links resolved (#9114); the node answers the home's own
	// spelling, the one a terminal opens (#10365). The same on a home that is no link.
	at := filepath.Join(w.home, "retained")
	runPullGit(t, w.repo, "worktree", "add", "-b", "fix/reuse", "--", at)
	params := map[string]any{"cwd": w.repo, "branch": "fix/reuse", "lang": "en"}
	got, err := request(t, w.n, "worktree.prepare", params)
	if err != nil {
		t.Fatal(err)
	}
	existing, _ := got["existing"].(map[string]any)
	if str(existing["path"]) != at || str(existing["branch"]) != "fix/reuse" {
		t.Fatalf("actual checkout was not found: %v", got)
	}
	if err := os.WriteFile(filepath.Join(at, "untracked.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, w.n, "worktree.prepare", params); err == nil || !strings.Contains(err.Error(), at) {
		t.Fatalf("dirty checkout must refuse with its path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(at, "untracked.txt")); err != nil {
		t.Fatal("inspection changed the existing checkout:", err)
	}
}

func TestWorktreePrepareCheckedOutBranchRefusesAgentOwner(t *testing.T) {
	w := worktreeNode(t)
	// Git says where the checkout is with the home's links resolved (#9114); the node answers the home's own
	// spelling, the one a terminal opens (#10365). The same on a home that is no link.
	at := filepath.Join(w.home, "retained")
	runPullGit(t, w.repo, "worktree", "add", "-b", "fix/reuse", "--", at)
	sub := mkdir(t, filepath.Join(at, "sub"))
	w.f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "branch": "main"}},
		nil,
		[]any{claudeAgent("writer-session", proto.StatusIdle, "w1:p2", "w1:t2", "w1", sub, "Other writer")},
		[]any{map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "cwd": w.repo}},
	))
	_, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": "fix/reuse", "lang": "en"})
	if err == nil || !strings.Contains(err.Error(), at) || !strings.Contains(err.Error(), "writer-session") {
		t.Fatalf("idle Agent in a subdirectory must refuse with path and owner: %v", err)
	}
}

func TestWorktreePrepareCheckedOutBranchRequiresCompleteInventory(t *testing.T) {
	w := worktreeNode(t)
	// Git says where the checkout is with the home's links resolved (#9114); the node answers the home's own
	// spelling, the one a terminal opens (#10365). The same on a home that is no link.
	at := filepath.Join(w.home, "retained")
	runPullGit(t, w.repo, "worktree", "add", "-b", "fix/reuse", "--", at)
	h := &inventoryHost{bareHost: bareHost{kind: host.Orca}, snapshotErr: errors.New("source unavailable"), listErr: errors.New("source unavailable")}
	w.n.hosts = host.NewRegistry(w.n.hosts.Get(host.Herdr), h)
	_, err := request(t, w.n, "worktree.prepare", map[string]any{"kind": host.Herdr, "cwd": w.repo, "branch": "fix/reuse", "lang": "en"})
	if err == nil || !strings.Contains(err.Error(), at) {
		t.Fatalf("partial inventory must not certify reuse: %v", err)
	}
}

func TestWorktreePrepareBranchLookupDoesNotChangeCreation(t *testing.T) {
	w := worktreeNode(t)
	at := filepath.Join(w.home, "retained")
	runPullGit(t, w.repo, "worktree", "add", "-b", "fix/reuse", "--", at)
	for _, branch := range []string{"", "fix/new", "spare"} {
		got, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo, "branch": branch})
		if err != nil || got["existing"] != nil {
			t.Fatalf("unused branch %q should preserve normal preparation: %v %v", branch, got, err)
		}
	}
	if _, err := w.n.worktreeCreate(t.Context(), "", "", w.repo, "fix/reuse", filepath.Join(w.home, "duplicate"), "", nil); err == nil {
		t.Fatal("general worktree creation must still refuse a branch checked out elsewhere")
	}
}
