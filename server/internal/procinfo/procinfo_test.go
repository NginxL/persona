package procinfo

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestOwnProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no parent lookup here")
	}
	if p, err := Parent(os.Getpid()); err != nil || p != os.Getppid() {
		t.Fatalf("parent %d %v, want %d", p, err, os.Getppid())
	}
	if runtime.GOOS == "linux" && !strings.HasPrefix(StartMark(os.Getpid()), "linux:") {
		t.Fatalf("start mark %q", StartMark(os.Getpid()))
	}
	if runtime.GOOS == "darwin" {
		m := StartMark(os.Getpid())
		if !strings.HasPrefix(m, "darwin:") || StartMark(os.Getpid()) != m {
			t.Fatalf("darwin start mark %q", m)
		}
		if got := StartMark(999999); got != "" {
			t.Fatalf("missing pid start mark %q", got)
		}
	}
}

// On Windows the start mark is the process's creation time, as Claude Code writes procStart there; a pid that is not
// running has none.
func TestStartMarkWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	m := StartMark(os.Getpid())
	if !strings.HasPrefix(m, "windows:") || strings.Trim(strings.TrimPrefix(m, "windows:"), "0123456789") != "" {
		t.Fatalf("start mark %q", m)
	}
	if StartMark(os.Getpid()) != m {
		t.Fatal("the start mark changed")
	}
	if got := StartMark(999999); got != "" {
		t.Fatalf("a pid that does not run: %q", got)
	}
}
