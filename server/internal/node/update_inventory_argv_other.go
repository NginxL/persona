//go:build !linux && !darwin && !windows

package node

import "errors"

func updateProcessArgv(int) ([]string, error) {
	return nil, errors.New("authoritative process arguments unsupported on this platform")
}

func processExecPath(int) (string, error) {
	return "", errors.New("process executables unsupported on this platform")
}
