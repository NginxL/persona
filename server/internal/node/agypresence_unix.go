//go:build unix

package node

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// agyPresenceHeld: some process holds the conversation's presence lock. A shared, non-blocking try: refused while an
// agy holds its exclusive lock; when free it is taken and let go at once (an agy starting on the conversation in that
// instant waits for it, as for any other reader).
func agyPresenceHeld(dir, conversation string) bool {
	f, err := os.Open(filepath.Join(dir, "presence", conversation+".lock"))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
