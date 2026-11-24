package sysmon

import (
	"cmp"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// storageMountEntry describes a mounted filesystem candidate discovered before calling statfs.
type storageMountEntry struct {
	Mount    string
	Device   string
	FSType   string
	ReadOnly bool
}

// storageMountinfo parses /proc/self/mountinfo into mounted filesystems suitable for per-mount storage monitoring.
// It excludes virtual pseudo-filesystems, loop devices, and subdirectory bind mounts, while retaining:
//   - real local block devices (/, /data00, etc.)
//   - container overlay root (/)
//   - explicit temporary mounts (/tmp, /var/tmp) even when backed by tmpfs
func storageMountinfo(b []byte) []storageMountEntry {
	seenDev := map[string]bool{}
	seenMount := map[string]bool{}
	var out []storageMountEntry
	for _, line := range strings.Split(string(b), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		f, g := strings.Fields(pre), strings.Fields(post)
		if !ok || len(f) < 6 || len(g) < 2 {
			continue
		}
		dev, root, point, opts := f[2], unescapeMount(f[3]), unescapeMount(f[4]), f[5]
		fstype, source := g[0], unescapeMount(g[1])
		superOpts := ""
		if len(g) >= 3 {
			superOpts = g[2]
		}
		if !includeLinuxStorageMount(point, source, fstype, root) {
			continue
		}
		if seenMount[point] {
			continue
		}
		if isBlockStorageDevice(source, fstype) {
			if seenDev[dev] {
				continue
			}
			seenDev[dev] = true
		}
		seenMount[point] = true
		out = append(out, storageMountEntry{
			Mount:    point,
			Device:   source,
			FSType:   fstype,
			ReadOnly: hasReadOnlyOption(opts) || hasReadOnlyOption(superOpts),
		})
	}
	sortStorageMountEntries(out)
	return out
}

// storageMountsFallback parses /proc/self/mounts when /proc/self/mountinfo is unavailable.
func storageMountsFallback(b []byte) []storageMountEntry {
	seenSource := map[string]bool{}
	seenMount := map[string]bool{}
	var out []storageMountEntry
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		source, point, fstype, opts := unescapeMount(f[0]), unescapeMount(f[1]), f[2], f[3]
		if !includeLinuxStorageMount(point, source, fstype, "/") {
			continue
		}
		if seenMount[point] {
			continue
		}
		if isBlockStorageDevice(source, fstype) {
			if seenSource[source] {
				continue
			}
			seenSource[source] = true
		}
		seenMount[point] = true
		out = append(out, storageMountEntry{
			Mount:    point,
			Device:   source,
			FSType:   fstype,
			ReadOnly: hasReadOnlyOption(opts),
		})
	}
	sortStorageMountEntries(out)
	return out
}

// includeLinuxStorageMount decides whether a Linux mount entry represents real storage or an important temp mount.
func includeLinuxStorageMount(point, source, fstype, root string) bool {
	if point == "" || isVirtualPseudoFS(fstype) {
		return false
	}
	// Explicit temp mounts (/tmp, /var/tmp) on tmpfs are high-signal because exhausting them breaks builds/agents.
	if fstype == "tmpfs" {
		return point == "/tmp" || point == "/var/tmp"
	}
	// Container root filesystems are commonly overlay on /.
	if fstype == "overlay" {
		return point == "/"
	}
	// Skip non-root subdirectory bind mounts (except btrfs subvolumes).
	if root != "/" && point != "/" && fstype != "btrfs" {
		return false
	}
	// Real local block devices (excluding loop devices and read-only image filesystems).
	if isBlockStorageDevice(source, fstype) {
		return true
	}
	// Local virtualized/ZFS filesystems mounted outside system pseudo-trees.
	switch fstype {
	case "zfs", "virtiofs", "9p":
		return !isSystemPseudoPath(point)
	}
	return false
}

func isBlockStorageDevice(source, fstype string) bool {
	return diskDevice(source, fstype)
}

func isVirtualPseudoFS(fstype string) bool {
	switch fstype {
	case "proc", "sysfs", "devtmpfs", "devpts", "cgroup", "cgroup2", "pstore", "securityfs", "debugfs", "tracefs",
		"configfs", "fusectl", "hugetlbfs", "mqueue", "autofs", "rpc_pipefs", "nfsd", "binfmt_misc", "efivarfs",
		"bpf", "nsfs", "ramfs", "selinuxfs", "squashfs", "iso9660", "erofs", "udf", "lxcfs", "fuse.lxcfs",
		"fuse.portal", "fuse.gvfsd-fuse", "sockfs", "pipefs", "anon_inodefs", "devfs", "fdesc", "volfs", "nullfs",
		"vmhgfs", "procfs":
		return true
	}
	return false
}

func isSystemPseudoPath(point string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/dev", "/run"} {
		if point == prefix || strings.HasPrefix(point, prefix+"/") {
			return true
		}
	}
	return false
}

func hasReadOnlyOption(opts string) bool {
	for _, tok := range strings.Split(opts, ",") {
		if tok == "ro" {
			return true
		}
	}
	return false
}

// darwinIncludeMount decides whether a macOS getfsstat entry should be reported.
// Keeps /, /System/Volumes/Data, and mounted data volumes (/Volumes/*), while excluding pseudo-filesystems and
// internal Apple sealed/preboot/VM system volumes.
func darwinIncludeMount(mount, device, fstype string, readOnly, dontBrowse bool) bool {
	if mount == "" || isVirtualPseudoFS(fstype) {
		return false
	}
	if mount == "/System/Volumes/Data" {
		return true
	}
	if strings.HasPrefix(mount, "/System/Volumes/") {
		return false
	}
	if mount == "/" {
		return true
	}
	if dontBrowse {
		return false
	}
	if mount == "/dev" || strings.HasPrefix(mount, "/dev/") || mount == "/private/var/vm" || strings.HasPrefix(mount, "/private/var/vm/") {
		return false
	}
	switch fstype {
	case "apfs", "hfs", "exfat", "msdos", "ufs", "zfs", "ntfs", "tmpfs":
		return device != "" && !readOnly || strings.HasPrefix(mount, "/Volumes/")
	}
	return false
}

// buildMountStat turns raw statfs block and inode counters into a proto.MountStat.
func buildMountStat(info storageMountEntry, blocks, bfree, bavail, bsize, files, ffree uint64) proto.MountStat {
	used, total := diskUsage(blocks, bfree, bavail, bsize)
	avail := int64(bavail * bsize)
	var usedPct float64
	if total > 0 {
		usedPct = clampPercent(100 * float64(used) / float64(total))
	}
	st := proto.MountStat{
		Mount:       info.Mount,
		Device:      info.Device,
		FSType:      info.FSType,
		UsedBytes:   used,
		AvailBytes:  avail,
		TotalBytes:  total,
		UsedPercent: usedPct,
		ReadOnly:    info.ReadOnly,
	}
	if files > 0 {
		inodesUsed := files - min(ffree, files)
		st.InodesTotal = files
		st.InodesUsed = inodesUsed
		st.InodesFree = min(ffree, files)
		p := clampPercent(100 * float64(inodesUsed) / float64(files))
		st.InodesPercent = &p
	}
	return st
}

func sortStorageMountEntries(entries []storageMountEntry) {
	slices.SortFunc(entries, func(a, b storageMountEntry) int {
		if a.Mount == "/" && b.Mount != "/" {
			return -1
		}
		if b.Mount == "/" && a.Mount != "/" {
			return 1
		}
		return cmp.Compare(a.Mount, b.Mount)
	})
}

func sortMountStats(stats []proto.MountStat) {
	slices.SortFunc(stats, func(a, b proto.MountStat) int {
		if a.Mount == "/" && b.Mount != "/" {
			return -1
		}
		if b.Mount == "/" && a.Mount != "/" {
			return 1
		}
		return cmp.Compare(a.Mount, b.Mount)
	})
}
