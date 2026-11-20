//go:build !darwin

package node

// Linux and Windows already expose their kernel generation through procinfo.
// Other platforms return no evidence rather than substituting PID liveness.
func sourceGeneration(pid int) string { return procStartMark(pid) }
