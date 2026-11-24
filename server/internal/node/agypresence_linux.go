package node

import (
	"os"
	"path/filepath"
	"strconv"
)

// agyPresenceLocks reads the process's open files from /proc.
func agyPresenceLocks(pid int) string {
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			files = append(files, target)
		}
	}
	return agyPresenceConversation(files)
}
