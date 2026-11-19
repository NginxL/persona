//go:build !windows

package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A ~/.zshrc that leaves something running in the background, holding the shell's output open (#10952): the launcher
// list comes back once the shell is done, not when that child is (the hub gives up on the computer after 30 s, and
// every ask after it waited behind this one), and it is cached for the next ask within the minute.
func TestLaunchersDoNotWaitForWhatTheRcFileLeavesRunning(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	pidFile := filepath.Join(home, "pid")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("sleep 300 &\necho $! > "+pidFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/zsh")
	killPidFileAtCleanup(t, pidFile)

	launcherCache.Lock()
	at, list := launcherCache.at, launcherCache.list
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = at, list
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})

	type answer struct {
		list []proto.Launcher
		err  error
	}
	ask := func() (answer, time.Duration) {
		t.Helper()
		began := time.Now()
		done := make(chan answer, 1)
		go func() {
			ls, err := (&Node{}).launchers(context.Background())
			done <- answer{ls, err}
		}()
		select {
		case a := <-done:
			return a, time.Since(began)
		case <-time.After(15 * time.Second):
			t.Fatal("the launcher list still waits for the rc file's background child after 15 s")
			return answer{}, 0
		}
	}
	a, took := ask()
	t.Logf("first ask: %d launchers in %v (error %v)", len(a.list), took, a.err)
	if a.err != nil || len(a.list) == 0 {
		t.Fatalf("launchers %v, error %v; want the list (nothing at all: plain claude)", a.list, a.err)
	}
	if took > 10*time.Second {
		t.Errorf("first ask took %v, want within ~3 s of the shell finishing", took)
	}
	if again, took := ask(); took > time.Second || len(again.list) != len(a.list) {
		t.Errorf("second ask: %d launchers in %v, want the cached %d at once", len(again.list), took, len(a.list))
	}
}
