//go:build !windows

package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// Git before 2.36 has no `worktree list -z` and exits 129 on it (Debian 11 ships 2.30): the listing still names
// every worktree and branch, read newline-separated.
func TestWorkspaceWorktreeListWithoutZ(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	repo, linked := filepath.Join(dir, "repo"), filepath.Join(dir, "linked")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "one"},
		{"-C", repo, "worktree", "add", "-q", "-b", "task", linked},
	} {
		if out, err := exec.Command(real, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stand := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = -z ] && { echo \"error: unknown switch \\`z'\" >&2; exit 129; }; done\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stand), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fields, err := workspaceWorktreeList(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(linked)
	if !slices.Contains(fields, "branch refs/heads/task") || !slices.Contains(fields, "worktree "+linked) && !slices.Contains(fields, "worktree "+resolved) {
		t.Fatalf("fields %q", fields)
	}
}
