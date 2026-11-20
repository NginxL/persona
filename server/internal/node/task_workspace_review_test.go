package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestBackendWorkspaceCorrectionSkipsDeletedRemoteRef(t *testing.T) {
	for _, kind := range []string{"branch", "tag"} {
		t.Run(kind, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			p := workspacePlan(t, home, repository.LayoutContainer, workspaceRemote(t, "main"), workspaceRemote(t, "trunk"))
			second := p.Source.Repositories.Repositories[1].SourceSnapshot.CloneURL
			workspaceGit(t, second, kind, "formerly-valid")
			p.Source.Repositories.Repositories[1].RequestedRef = repository.RequestedRef{Kind: kind, Value: "missing"}
			req := taskworkspace.Request{WorkspaceID: "ws_source", OperationID: "op_first", Plan: *workspacenaming.Resolve(p.Source, p.Node).Plan}
			failed := workspaceCall(t, n, "prepare", req)
			if failed.Error == nil || failed.Error.Code != "ref_not_found" || failed.Repositories[0].State != "ready" {
				t.Fatal(failed)
			}
			kept := filepath.Join(failed.Repositories[0].Path, "keep.txt")
			if err := os.WriteFile(kept, []byte("uncommitted work"), 0600); err != nil {
				t.Fatal(err)
			}
			if r := workspaceCall(t, n, "cancel", req); r.State != "cancelled" {
				t.Fatal(r)
			}
			workspaceGit(t, second, kind, "-d", "formerly-valid")
			p.Source.Repositories.Repositories[1].RequestedRef.Value = "formerly-valid"
			req.Plan = *workspacenaming.Resolve(p.Source, p.Node).Plan
			req.PreviousOperationID, req.OperationID = req.OperationID, "op_corrected"
			r := workspaceCall(t, n, "prepare", req)
			if r.Error == nil || r.Error.Code != "ref_not_found" || r.Repositories[0].State != "ready" || r.Repositories[1].ResolvedCommit != "" {
				t.Fatal(r)
			}
			if data, err := os.ReadFile(kept); err != nil || string(data) != "uncommitted work" {
				t.Fatal(string(data), err)
			}
		})
	}
}

func TestBackendWorkspaceCleanupProtectsConcurrentGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell timing fixture")
	}
	for _, change := range []string{"advance at deletion", "check out elsewhere", "bisect elsewhere", "inspection failure", "replace removed root"} {
		t.Run(change, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			first := workspaceRemote(t, "main")
			req := taskworkspace.Request{WorkspaceID: "ws_cleanup", OperationID: "op_cleanup", Plan: workspacePlan(t, home, repository.LayoutContainer, first, workspaceRemote(t, "develop"))}
			if change == "replace removed root" {
				req.Plan = workspacePlan(t, home, repository.LayoutRoot, first)
			}
			if change == "bisect elsewhere" {
				source := req.Plan.Source.Repositories.Repositories[0].SourceSnapshot.CloneURL
				workspaceGit(t, source, "commit", "--allow-empty", "-m", "second")
				workspaceGit(t, source, "commit", "--allow-empty", "-m", "third")
			}
			ready := workspaceCall(t, n, "prepare", req)
			if ready.State != "ready" {
				t.Fatal(ready)
			}
			r := ready.Repositories[0]
			// The actual removal completes before the external Git user changes
			// the branch. All real commands still run against the test repository.
			shim := workspaceTemp(t)
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("WORKSPACE_TEST_GIT", git)
			t.Setenv("WORKSPACE_TEST_COMMON", r.CommonDir)
			t.Setenv("WORKSPACE_TEST_BRANCH", r.TaskBranch)
			t.Setenv("WORKSPACE_TEST_PATH", r.Path)
			t.Setenv("WORKSPACE_TEST_OTHER", filepath.Join(home, "other-checkout"))
			t.Setenv("WORKSPACE_TEST_MODE", change)
			t.Setenv("WORKSPACE_TEST_MARK", filepath.Join(shim, "removed"))
			script := `#!/bin/sh
set -eu
previous=
remove=
lookup=
deleting=
for arg do
	if [ "$previous" = worktree ] && [ "$arg" = remove ]; then remove=yes; fi
	if [ "$arg" = -d ] || [ "$arg" = -D ]; then deleting=yes; fi
	if [ "$arg" = rev-parse ] || [ "$arg" = show-ref ]; then lookup=yes; fi
	previous=$arg
done
if [ "$remove" = yes ] && [ "$previous" = "$WORKSPACE_TEST_PATH" ]; then
	"$WORKSPACE_TEST_GIT" "$@"
	touch "$WORKSPACE_TEST_MARK"
	case "$WORKSPACE_TEST_MODE" in
		"check out elsewhere"|"bisect elsewhere")
			"$WORKSPACE_TEST_GIT" -C "$WORKSPACE_TEST_COMMON" worktree add "$WORKSPACE_TEST_OTHER" "$WORKSPACE_TEST_BRANCH"
			if [ "$WORKSPACE_TEST_MODE" = "bisect elsewhere" ]; then
				"$WORKSPACE_TEST_GIT" -C "$WORKSPACE_TEST_OTHER" bisect start HEAD HEAD~2
				if "$WORKSPACE_TEST_GIT" -C "$WORKSPACE_TEST_OTHER" symbolic-ref --quiet HEAD; then exit 1; fi
			fi
			;;
	esac
	exit 0
fi
if [ "$WORKSPACE_TEST_MODE" = "advance at deletion" ] && [ "$deleting" = yes ] && [ -f "$WORKSPACE_TEST_MARK" ]; then
	new=$(echo concurrent | "$WORKSPACE_TEST_GIT" -C "$WORKSPACE_TEST_COMMON" -c user.name=Example -c user.email=me@example.com commit-tree "$WORKSPACE_TEST_BRANCH^{tree}" -p "$WORKSPACE_TEST_BRANCH")
	"$WORKSPACE_TEST_GIT" -C "$WORKSPACE_TEST_COMMON" update-ref "refs/heads/$WORKSPACE_TEST_BRANCH" "$new"
	echo "$new" >"$WORKSPACE_TEST_MARK"
fi
if [ "$WORKSPACE_TEST_MODE" = "replace removed root" ] && [ -f "$WORKSPACE_TEST_MARK" ] && [ "$lookup" = yes ]; then
	printf 'user file' >"$WORKSPACE_TEST_PATH"
fi
if [ "$WORKSPACE_TEST_MODE" = "inspection failure" ] && [ -f "$WORKSPACE_TEST_MARK" ] && [ "$lookup" = yes ]; then exit 2; fi
exec "$WORKSPACE_TEST_GIT" "$@"
`
			if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			req.DeleteBranch = true
			got := workspaceCall(t, n, "cleanup", req)
			if change == "replace removed root" {
				if got.Error == nil || got.Error.Code != "cleanup_retained" {
					t.Fatal(got)
				}
				if data, err := os.ReadFile(r.Path); err != nil || string(data) != "user file" {
					t.Fatal("replaced root was lost", string(data), err)
				}
				return
			}
			if got.Error == nil || got.Repositories[0].BranchDeleted || !got.Repositories[0].Removed || got.Repositories[1].Removed {
				t.Fatal(got)
			}
			expected := r.CurrentHead
			if change == "advance at deletion" {
				tip, err := os.ReadFile(os.Getenv("WORKSPACE_TEST_MARK"))
				if err != nil {
					t.Fatal(err)
				}
				expected = strings.TrimSpace(string(tip))
			}
			out, err := exec.Command(git, "-C", r.CommonDir, "rev-parse", "refs/heads/"+r.TaskBranch).Output()
			if err != nil || strings.TrimSpace(string(out)) != expected {
				t.Fatal("user's branch was changed", string(out), expected, err)
			}
		})
	}
}
