//go:build !darwin

package node

// claudeAppAttendedNow: only macOS's desktop app index is read (claudeDesktopDirs), so nothing is unread elsewhere.
func claudeAppAttendedNow() bool { return true }
