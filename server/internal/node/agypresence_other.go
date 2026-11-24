//go:build !linux

package node

// agyPresenceLocks: only Linux shows a process's open files without another program; elsewhere a herdr-core terminal's
// agy conversation stays unknown, and agyPresenceHeld still keeps the companion out of it.
func agyPresenceLocks(pid int) string { return "" }
