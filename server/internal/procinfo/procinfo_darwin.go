//go:build darwin

package procinfo

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// Parent is pid's parent process (kern.proc.pid).
func Parent(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(kp.Eproc.Ppid), nil
}

// szomb is macOS's P_stat value for a zombie process (sys/proc.h SZOMB).
const szomb = 5

// StartMark is when pid started as "darwin:<sec>.<usec>" from kern.proc.pid's p_starttime.
// Returns "" when the process is gone or is a zombie (not yet reaped).
func StartMark(pid int) string {
	if pid <= 0 {
		return ""
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_stat == szomb {
		return ""
	}
	st := kp.Proc.P_starttime
	if st.Sec == 0 && st.Usec == 0 {
		return ""
	}
	return "darwin:" + strconv.FormatInt(st.Sec, 10) + "." + strconv.FormatInt(int64(st.Usec), 10)
}
