//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallCuaCancellationKillsInstallerTree(t *testing.T) {
	home := t.TempDir()
	childPath := filepath.Join(home, "child-pid")
	fakeInstaller(t, home, map[string]string{
		"install.sh": "#!/bin/bash\nsleep 10 &\necho $! > " + strconv.Quote(childPath) + "\nwait\n",
	})
	var child int
	killChild := func() {
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	}
	t.Cleanup(killChild)
	if err := startJob("install", installCua); err != nil {
		t.Fatal(err)
	}
	waitWithin(t, 3*time.Second, "the installer to start its child", func() bool {
		data, _ := os.ReadFile(childPath)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	})
	if !cancelJob() {
		t.Fatal("running install was not cancellable")
	}
	if !jobCancelledWithin(750 * time.Millisecond) {
		// Clean up the descendant that the production cancellation left behind.
		killChild()
		waitWithin(t, 2*time.Second, "the install job to end after its child was killed", func() bool {
			j := currentJob()
			return j != nil && j.State == "cancelled"
		})
		t.Fatal("install job remained running until the installer shell's child was killed separately")
	}
}

// jobCancelledWithin reports whether the install job reaches the cancelled state within d. Unlike waitWithin it does
// not fail the test: a cancellation that misses its soft deadline runs its own cleanup and names the failure itself.
// The deadline is checked before every state read, like the baseline for-condition: once d has passed no state is
// read, so a cancellation first observed past the deadline cannot be accepted.
func jobCancelledWithin(d time.Duration) bool {
	deadline := time.Now().Add(d)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !time.Now().Before(deadline) {
			return false
		}
		if j := currentJob(); j != nil && j.State == "cancelled" {
			return true
		}
		<-ticker.C
	}
}
