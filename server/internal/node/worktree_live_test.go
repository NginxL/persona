//go:build herdrlive

package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// Against a real herdr: a throwaway repository in a temp directory, a throwaway herdr workspace in it, and a worktree
// of that repository made through worktree.prepare / worktree.create and removed again (`herdr worktree remove`, then
// the workspace closed). It adds and removes two workspaces on the machine, so it needs a say-so:
// PERSONA_LIVE_WORKTREE=1 go test -tags herdrlive -run TestLiveWorktree ./internal/node/
func TestLiveWorktree(t *testing.T) {
	if os.Getenv("PERSONA_LIVE_WORKTREE") != "1" {
		t.Skip("PERSONA_LIVE_WORKTREE=1 opens and removes a throwaway worktree workspace in herdr")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	ctx := context.Background()
	tmp := t.TempDir()
	repo := mkdir(t, filepath.Join(tmp, "live-repo"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, repo, "add", "app.txt")
	runPullGit(t, repo, "commit", "-m", "first")

	client := herdr.New()
	base, err := client.WorkspaceCreate(ctx, repo, "persona-live-worktree")
	if err != nil || base.Workspace == nil {
		t.Fatalf("workspace create: %+v %v", base, err)
	}
	wsID := base.Workspace.ID
	home, _ := os.UserHomeDir()
	n := &Node{hosts: herdrHosts(client), agents: map[string]host.Agent{}, home: home, state: newState()}
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}
	var made string
	t.Cleanup(func() {
		if made != "" { // git removes the checkout (never the branch); this also closes its workspace
			if out, err := exec.Command("herdr", "worktree", "remove", "--workspace", made).CombinedOutput(); err != nil {
				t.Errorf("worktree remove: %v %s", err, out)
			}
		}
		if out, err := exec.Command("herdr", "workspace", "close", wsID).CombinedOutput(); err != nil {
			t.Errorf("workspace close: %v %s", err, out)
		}
	})

	list, err := n.listPanes(ctx)
	if err != nil || len(list.Hosts) != 1 || !list.Hosts[0].Worktree {
		t.Fatalf("the host does not advertise worktrees: %+v %v", list.Hosts, err)
	}
	prep, err := n.worktreePrepare(ctx, host.Herdr, wsID, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prepare: %+v", prep)
	if prep.Root != repo || prep.RepoName != "live-repo" || prep.Branch != "main" || filepath.Base(prep.Path) != "live-repo" {
		t.Errorf("prepare = %+v", prep)
	}

	dest := filepath.Join(tmp, "checkouts", "live-checkout")
	res, err := n.worktreeCreate(ctx, host.Herdr, wsID, repo, "persona-live/worktree", dest, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	made = res.WorkspaceID
	t.Logf("create: %+v", res)
	if res.WorkspaceID == "" || res.WorkspaceID == wsID || res.Path != dest || !strings.HasPrefix(res.Terminal, "pane:") {
		t.Errorf("create = %+v", res)
	}
	if got := runPullGit(t, dest, "branch", "--show-current"); got != "persona-live/worktree" {
		t.Errorf("the checkout is on %q", got)
	}
	if head, want := runPullGit(t, dest, "rev-parse", "HEAD"), runPullGit(t, repo, "rev-parse", "HEAD"); head != want {
		t.Error("the new branch did not start at the workspace's HEAD")
	}
	out, err := exec.Command("herdr", "worktree", "list", "--workspace", wsID).CombinedOutput()
	if err != nil || !strings.Contains(string(out), res.WorkspaceID) || !strings.Contains(string(out), "persona-live/worktree") {
		t.Errorf("herdr does not list the worktree as open: %v %s", err, out)
	}
	if _, err := n.worktreeCreate(ctx, host.Herdr, wsID, repo, "persona-live/other", dest, "", nil); err == nil {
		t.Error("created a second worktree over the first one's directory")
	}
}

func TestLiveHerdrWorkspaceActions(t *testing.T) {
	if os.Getenv("PERSONA_LIVE_WORKTREE") != "1" {
		t.Skip("PERSONA_LIVE_WORKTREE=1 enables temporary native workspace actions")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	ctx := t.Context()
	directory := t.TempDir()
	repo := mkdir(t, filepath.Join(directory, "repo"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, repo, "add", "app.txt")
	runPullGit(t, repo, "commit", "-m", "initial")
	client := herdr.New()
	base, err := client.WorkspaceCreate(ctx, repo, "persona-check-workspace-actions")
	if err != nil || base.Workspace == nil {
		t.Fatalf("workspace create: %+v %v", base, err)
	}
	baseID := base.Workspace.ID
	childID := ""
	t.Cleanup(func() {
		if childID != "" {
			_ = client.WorktreeRemove(context.Background(), childID, true)
		}
		if baseID != "" {
			_ = client.WorkspaceClose(context.Background(), baseID)
		}
	})
	path := filepath.Join(directory, "checkout")
	child, err := client.WorktreeCreate(ctx, baseID, "persona-check/actions", path)
	if err != nil || child.Workspace == nil {
		t.Fatalf("native worktree create: %+v %v", child, err)
	}
	childID = child.Workspace.ID
	home, _ := os.UserHomeDir()
	n := &Node{hosts: herdrHosts(client), agents: map[string]host.Agent{}, home: home, state: newState()}
	w := worktreeFixture{n: n}
	token := herdrDeletionPreview(t, w, childID, false)
	res, err := request(t, n, "worktree.delete", map[string]any{"kind": "herdr", "workspace_id": childID, "preview_token": token})
	if err != nil || res["removed"] != true {
		t.Fatalf("native checkout removal: %v %v", res, err)
	}
	childID = ""
	if _, err := os.Stat(path); !os.IsNotExist(err) || !branchExists(ctx, repo, "persona-check/actions") {
		t.Fatalf("checkout/branch after native removal: %v", err)
	}
	token = herdrDeletionPreview(t, w, baseID, true)
	res, err = request(t, n, "worktree.delete", map[string]any{"kind": "herdr", "workspace_id": baseID, "preview_token": token, "close_only": true})
	if err != nil || res["removed"] != true {
		t.Fatalf("native workspace close: %v %v", res, err)
	}
	baseID = ""
	if data, err := os.ReadFile(filepath.Join(repo, "app.txt")); err != nil || string(data) != "kept\n" {
		t.Fatalf("files after close: %q %v", data, err)
	}
}
