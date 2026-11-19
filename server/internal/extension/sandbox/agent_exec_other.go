//go:build !linux

package sandbox

import "errors"

func runAgentExec([]string) error {
	return errors.New("agent sandbox exec is only available on Linux")
}
