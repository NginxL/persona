//go:build !windows

package node

// longPath: only Windows gives names a second, short spelling (longpath_windows.go).
func longPath(p string) string { return p }
