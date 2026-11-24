//go:build !windows

package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The Lark gate asks the user's shell for its PATH and for where lark-cli is, inside a tool call's hook. A background
// job the shell's startup leaves running with stdout inherited (an update check, a daemon started from ~/.bashrc) keeps
// the pipe open after the shell exits; the ask returns what the shell printed instead of waiting for that job.
func TestLarkLoginShellAsksDoNotWaitForABackgroundJob(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	home := t.TempDir()
	tools := filepath.Join(home, "vm", "bin") // a version manager's directory only the shell's startup adds
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "lark-cli"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	jobs := filepath.Join(home, "jobs")
	rc := `export PATH="$HOME/vm/bin:$PATH"
sleep 30 & echo $! >> "$HOME/jobs"
`
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, _ := os.ReadFile(jobs)
		for _, f := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	// bash with no ~/.zshrc, whether or not zsh is installed (loginShellProgram)
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "/usr/bin:/bin")

	n := &Node{}
	start := time.Now()
	if dirs := n.larkLoginPATH(); !slices.Contains(dirs, tools) {
		t.Fatalf("login PATH %q has no %s", dirs, tools)
	}
	if got, want := n.larkCLIBin(&proto.LarkCLI{}), filepath.Join(tools, "lark-cli"); got != want {
		t.Fatalf("lark-cli at %q, want %q", got, want)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the two asks took %s: they waited for the startup's background job", took.Round(time.Second))
	}
	if b, _ := os.ReadFile(jobs); len(strings.Fields(string(b))) != 2 {
		t.Fatalf("the shell's startup ran %d times, want 2 (one per ask): %q", len(strings.Fields(string(b))), b)
	}
}
