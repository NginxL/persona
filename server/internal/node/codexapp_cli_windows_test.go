//go:build windows

package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsBundledCodexCLIChoosesNewestBuild(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root := filepath.Join(local, "OpenAI", "Codex", "bin")
	write := func(build string, at time.Time) string {
		t.Helper()
		path := filepath.Join(root, build, "codex.exe")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test executable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := time.Unix(1700000000, 0)
	newer := old.Add(time.Hour)
	write("1111111111111111", old)
	want := write("2222222222222222", newer)
	write("3333333333333333", newer) // Equal times resolve by path, not enumeration order.
	write("not-a-build", newer.Add(time.Hour))
	write("44444444444444444", newer.Add(time.Hour))
	write("gggggggggggggggg", newer.Add(time.Hour))
	if err := os.MkdirAll(filepath.Join(root, "5555555555555555", "codex.exe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := bundledCodexAppCLI(); got != want {
		t.Fatalf("bundled CLI = %q, want %q", got, want)
	}
}

func TestWindowsBundledCodexCLIHasNoUnverifiedFallback(t *testing.T) {
	for _, local := range []string{"", "relative", t.TempDir()} {
		t.Run(local, func(t *testing.T) {
			t.Setenv("LOCALAPPDATA", local)
			if got := bundledCodexAppCLI(); got != "" {
				t.Fatalf("unexpected CLI: %q", got)
			}
		})
	}
}
