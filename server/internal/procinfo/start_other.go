//go:build !linux && !windows && !darwin

package procinfo

// StartMark: Linux (/proc), macOS (kern.proc.pid), and Windows tell when a process started; elsewhere a pid is trusted while it is alive.
func StartMark(int) string { return "" }
