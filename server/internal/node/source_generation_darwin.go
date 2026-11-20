//go:build darwin

package node

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Source evidence needs a kernel generation even where legacy hook tracking uses
// only liveness. Keep this stricter requirement local to the new evidence path.
func sourceGeneration(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	const zombie = 5 // SZOMB, Darwin sys/proc.h
	if err != nil || kp.Proc.P_stat == zombie {
		return ""
	}
	return fmt.Sprintf("darwin:%d:%d", kp.Proc.P_starttime.Sec, kp.Proc.P_starttime.Usec)
}
