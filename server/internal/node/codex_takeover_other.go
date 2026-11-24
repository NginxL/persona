//go:build !linux && !darwin && !windows

package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func (*Node) stopCodexWriter(context.Context, codexWriter) error {
	return takeoverRefusal("unavailable")
}

func (*Node) prepareCodexWriterQuit(context.Context, codexWriter) (func() error, error) {
	return nil, takeoverRefusal("unavailable")
}

// codexWriterLockHeldOS fails closed when a thread writer lock file exists on a platform without OS lock probing.
func codexWriterLockHeldOS(src codexSource) bool {
	if src.home == "" || src.thread == "" {
		return false
	}
	lockPath := filepath.Join(src.home, "thread-writer-locks", src.thread+".lock")
	_, err := os.Stat(lockPath)
	return !errors.Is(err, os.ErrNotExist)
}
