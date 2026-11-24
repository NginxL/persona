//go:build !linux && !darwin

package sysmon

import "github.com/nwverse/persona/server/internal/proto"

// StorageMounts returns a safe empty fallback on platforms without POSIX statfs per-mount inode stats.
func StorageMounts() ([]proto.MountStat, bool) {
	return []proto.MountStat{}, false
}

// AvailableBytes returns false on platforms without POSIX statfs support.
func AvailableBytes(string) (int64, bool) {
	return 0, false
}
