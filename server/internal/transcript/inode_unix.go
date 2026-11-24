//go:build !windows

package transcript

import (
	"os"
	"syscall"
)

// inode is the file's inode number (0 where the platform does not say).
func inode(st os.FileInfo) uint64 {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(sys.Ino)
	}
	return 0
}

func statTranscript(path string) (os.FileInfo, error)      { return os.Stat(path) }
func statOpenedTranscript(f *os.File) (os.FileInfo, error) { return f.Stat() }
func openTranscript(path string) (*os.File, error)         { return os.Open(path) }
