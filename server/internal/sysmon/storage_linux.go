//go:build linux

package sysmon

import (
	"os"
	"syscall"

	"github.com/nwverse/persona/server/internal/proto"
)

// StorageMounts reports per-mount disk capacity and inode usage on Linux using statfs (sub-millisecond, no subprocess).
func StorageMounts() ([]proto.MountStat, bool) {
	var entries []storageMountEntry
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		entries = storageMountinfo(b)
	} else if b, err := os.ReadFile("/proc/self/mounts"); err == nil {
		entries = storageMountsFallback(b)
	}
	if len(entries) == 0 {
		entries = []storageMountEntry{{Mount: "/"}}
	}
	out := make([]proto.MountStat, 0, len(entries))
	for _, entry := range entries {
		var st syscall.Statfs_t
		if err := syscall.Statfs(entry.Mount, &st); err != nil {
			continue
		}
		if st.Blocks == 0 || st.Bsize <= 0 {
			continue
		}
		out = append(out, buildMountStat(
			entry,
			st.Blocks,
			st.Bfree,
			st.Bavail,
			uint64(st.Bsize),
			st.Files,
			st.Ffree,
		))
	}
	sortMountStats(out)
	return out, true
}

// AvailableBytes returns the unprivileged available bytes on the filesystem containing path.
func AvailableBytes(path string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Bsize <= 0 {
		return 0, false
	}
	return int64(st.Bavail * uint64(st.Bsize)), true
}
