package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitFailure keeps the line that names the cause with git's fatal line: a worktree of an LFS repository whose git-lfs
// the node cannot run said only "the remote end hung up unexpectedly" (#10127).
func TestGitFailureNamesTheCause(t *testing.T) {
	for _, c := range []struct{ name, err, want string }{
		{"lfs missing, as the issue saw it", "git worktree: Preparing worktree (new branch 'agent/x')\n" +
			"git-lfs filter-process: git-lfs: command not found\nfatal: the remote end hung up unexpectedly",
			"git-lfs filter-process: git-lfs: command not found; the remote end hung up unexpectedly"},
		{"lfs missing, through herdr", "worktree_create_failed: git worktree add failed: Preparing worktree (new branch 'agent/x')\n" +
			"git-lfs filter-process: git-lfs: command not found\nfatal: the remote end hung up unexpectedly",
			"git-lfs filter-process: git-lfs: command not found; the remote end hung up unexpectedly"},
		{"lfs missing, newer git", "git worktree: Preparing worktree (new branch 'agent/x')\n" +
			"git-lfs filter-process: git-lfs: command not found\n" +
			"error: could not read greeting from subprocess 'git-lfs filter-process'\n" +
			"error: initialization for subprocess 'git-lfs filter-process' failed\nfatal: a.bin: smudge filter lfs failed",
			"git-lfs filter-process: git-lfs: command not found; a.bin: smudge filter lfs failed"},
		{"dash", "git worktree: git-lfs filter-process: 1: git-lfs: not found\nfatal: the remote end hung up unexpectedly",
			"git worktree: git-lfs filter-process: 1: git-lfs: not found; the remote end hung up unexpectedly"},
		{"error line", "git worktree: Preparing worktree (checking out 'x')\nerror: could not lock config file .git/config: File exists\n" +
			"hint: remove the lock\nfatal: could not set up the new worktree",
			"could not lock config file .git/config: File exists; could not set up the new worktree"},
		{"fatal alone", "git worktree: Preparing worktree (new branch 'x')\nfatal: invalid reference: main",
			"invalid reference: main"},
		{"fatal says it already", "error: pathspec 'x' did not match\nfatal: pathspec 'x' did not match", "pathspec 'x' did not match"},
		{"no fatal line", "git worktree: error: something", "git worktree: error: something"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := gitFailure(errors.New(c.err)); got != c.want {
				t.Fatalf("gitFailure\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// The same with a real git: a repository whose LFS filter program is not on PATH (a made-up name, so an installed
// git-lfs does not matter) refuses the checkout, and the failure names the program.
func TestGitFailureNamesMissingFilterProgram(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	repo := mkdir(t, filepath.Join(t.TempDir(), "proj"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "a.bin"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.bin filter=lfs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, repo, "add", ".")
	runPullGit(t, repo, "commit", "-m", "first")
	runPullGit(t, repo, "config", "filter.lfs.process", "persona-missing-lfs filter-process")
	runPullGit(t, repo, "config", "filter.lfs.required", "true")
	_, err := gitTimed(context.Background(), repo, time.Minute, "worktree", "add", "-b", "agent/x", "--", filepath.Join(filepath.Dir(repo), "wt"))
	if err == nil {
		t.Fatal("the checkout ran a filter that is not there")
	}
	if got := gitFailure(err); !strings.Contains(got, "persona-missing-lfs") {
		t.Fatalf("gitFailure does not name the filter program: %q (git said %q)", got, err)
	}
}
