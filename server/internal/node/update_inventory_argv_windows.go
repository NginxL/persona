package node

import "errors"

// updateProcessArgv reads the same authoritative command line as the process inventory's PEB scan.
func updateProcessArgv(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process id")
	}
	p, err := readWinProc(uint32(pid), nil)
	if err != nil {
		return nil, err
	}
	if len(p.argv) == 0 {
		return nil, errors.New("incomplete process arguments")
	}
	return p.argv, nil
}

func processExecPath(int) (string, error) {
	return "", errors.New("process executables unsupported on this platform")
}
