//go:build darwin

package sysmon

import (
	"golang.org/x/sys/unix"

	"github.com/nwverse/persona/server/internal/proto"
)

// StorageMounts reports per-mount disk capacity and inode usage on macOS using getfsstat (sub-millisecond, no subprocess).
func StorageMounts() ([]proto.MountStat, bool) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return []proto.MountStat{}, true
	}
	buf := make([]unix.Statfs_t, n+4)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return []proto.MountStat{}, true
	}
	seen := map[string]bool{}
	out := make([]proto.MountStat, 0, n)
	for _, st := range buf[:n] {
		mount := unix.ByteSliceToString(st.Mntonname[:])
		device := unix.ByteSliceToString(st.Mntfromname[:])
		fstype := unix.ByteSliceToString(st.Fstypename[:])
		readOnly := st.Flags&unix.MNT_RDONLY != 0
		dontBrowse := st.Flags&unix.MNT_DONTBROWSE != 0
		if seen[mount] || !darwinIncludeMount(mount, device, fstype, readOnly, dontBrowse) {
			continue
		}
		if st.Blocks == 0 || st.Bsize == 0 {
			continue
		}
		seen[mount] = true
		entry := storageMountEntry{
			Mount:    mount,
			Device:   device,
			FSType:   fstype,
			ReadOnly: readOnly,
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
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Bsize == 0 {
		return 0, false
	}
	return int64(st.Bavail * uint64(st.Bsize)), true
}
