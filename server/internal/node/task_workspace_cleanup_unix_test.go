//go:build unix

package node

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func TestBackendWorkspacePartialCleanupRetryProtectsRemovedBranch(t *testing.T) {
	for _, boundary := range []string{"saved removal", "lost removal receipt", "lost branch deletion receipt"} {
		t.Run(boundary, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			req := taskworkspace.Request{WorkspaceID: "ws_cleanup_retry", OperationID: "op_cleanup_retry", Plan: workspacePlan(t, home, repository.LayoutContainer, workspaceRemote(t, "trunk"), workspaceRemote(t, "develop"))}
			ready := workspaceCall(t, n, "prepare", req)
			if ready.State != "ready" || len(ready.Repositories) != 2 {
				t.Fatal(ready)
			}
			first, second := ready.Repositories[0], ready.Repositories[1]
			workspaceGit(t, first.Path, "config", "user.name", "Example")
			workspaceGit(t, first.Path, "config", "user.email", "me@example.com")
			if err := os.WriteFile(filepath.Join(first.Path, "local.txt"), []byte("unmerged work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			workspaceGit(t, first.Path, "add", "local.txt")
			workspaceGit(t, first.Path, "commit", "-m", "Local work")
			head := strings.TrimSpace(workspaceGit(t, first.Path, "rev-parse", "HEAD"))
			// Every command still uses real Git. Only the second removal fails, after
			// the first worktree has really gone and its branch is the remaining copy.
			shim := workspaceTemp(t)
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(shim, "fail")
			if err = os.WriteFile(marker, nil, 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
previous=
removing=
last=
for argument do
  if [ "$previous" = worktree ] && [ "$argument" = remove ]; then removing=yes; fi
  previous=$argument
  last=$argument
done
if [ "$removing" = yes ] && [ "$last" = "$PERSONA_WORKSPACE_FAIL_PATH" ] && [ -f "$PERSONA_WORKSPACE_FAIL_MARKER" ]; then exit 1; fi
exec "$PERSONA_WORKSPACE_REAL_GIT" "$@"
`
			if err = os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PERSONA_WORKSPACE_REAL_GIT", realGit)
			t.Setenv("PERSONA_WORKSPACE_FAIL_PATH", second.Path)
			t.Setenv("PERSONA_WORKSPACE_FAIL_MARKER", marker)
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			partial := workspaceCall(t, n, "cleanup", req)
			if partial.Error == nil || partial.Error.Code != "cleanup_failed" || !partial.Repositories[0].Removed || partial.Repositories[1].Removed {
				t.Fatal("expected partial cleanup", partial)
			}
			if err = os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if boundary != "saved removal" {
				path := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID), "receipt.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var journal workspaceJournal
				if err = json.Unmarshal(raw, &journal); err != nil {
					t.Fatal(err)
				}
				if boundary == "lost removal receipt" {
					journal.Repositories[0].Removed = false
				} else {
					// An explicitly forced deletion may have completed before its receipt.
					workspaceGit(t, first.CommonDir, "branch", "-D", "--", first.TaskBranch)
				}
				if err = workspaceWrite(path, &journal); err != nil {
					t.Fatal(err)
				}
			}
			n = &Node{home: home, dir: n.dir}
			req.DeleteBranch = true
			retry := workspaceCall(t, n, "cleanup", req)
			if boundary != "lost branch deletion receipt" {
				if retry.Error == nil || retry.Error.Code != "worktree_unmerged" || retry.Error.RepoID != first.RepoID || retry.Error.OperationID != req.OperationID {
					t.Fatal("non-force retry must retain unmerged branch", retry.State, retry.Error)
				}
				if got := strings.TrimSpace(workspaceGit(t, first.CommonDir, "rev-parse", "refs/heads/"+first.TaskBranch)); got != head {
					t.Fatal("retry lost local commit", got, head)
				}
				if _, err = os.Stat(second.Path); err != nil {
					t.Fatal("preflight refusal removed another checkout", err)
				}
				req.Force = true
				retry = workspaceCall(t, n, "cleanup", req)
			}
			if retry.Error != nil || retry.Cleanup == nil || !retry.Cleanup.Removed {
				t.Fatal("explicit force or completed deletion must finish cleanup", retry)
			}
			for _, repo := range retry.Repositories {
				if !repo.Removed || !repo.BranchDeleted {
					t.Fatal(repo)
				}
				cmd := exec.Command("git", "-C", repo.CommonDir, "show-ref", "--verify", "--quiet", "refs/heads/"+repo.TaskBranch)
				cmd.Env = taskworkspace.GitEnv(os.Environ())
				if err = cmd.Run(); err == nil {
					t.Fatal("deleted task branch still exists", repo.RepoID)
				}
			}
		})
	}
}
