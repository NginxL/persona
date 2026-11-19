package node

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexProjectRootDoesNotStartGitOutsideRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fake git")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "git-called")
	fake := "#!/bin/sh\nprintf called >> \"$CODEX_TEST_GIT_CALLS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_TEST_GIT_CALLS", marker)

	if got := codexProjectRoot(cwd, map[string]string{}); got != realPath(cwd) {
		t.Fatalf("root = %q, want the non-repository directory", got)
	}
	codexProjectRoots.Lock()
	entry := codexProjectRoots.entries[realPath(cwd)]
	entry.at = entry.at.Add(-codexProjectCacheTTL)
	codexProjectRoots.entries[realPath(cwd)] = entry
	codexProjectRoots.Unlock()
	if got := codexProjectRoot(cwd, map[string]string{}); got != realPath(cwd) {
		t.Fatalf("root after cache expiry = %q, want the non-repository directory", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a directory with no repository marker still started Git")
	}
}
