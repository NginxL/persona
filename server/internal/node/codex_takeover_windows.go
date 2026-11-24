//go:build windows

package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func (*Node) stopCodexWriter(context.Context, codexWriter) error {
	return takeoverRefusal("unavailable")
}

func (*Node) prepareCodexWriterQuit(context.Context, codexWriter) (func() error, error) {
	return nil, takeoverRefusal("unavailable")
}

// codexWriterLockHeldOS performs a non-destructive LockFileEx probe on Codex's per-thread writer lock.
// It never creates, writes, or deletes the lock file.
func codexWriterLockHeldOS(src codexSource) bool {
	if src.home == "" || src.thread == "" {
		return false
	}
	lockPath := filepath.Join(src.home, "thread-writer-locks", src.thread+".lock")
	f, err := os.Open(lockPath)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		return true
	}
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	return false
}
