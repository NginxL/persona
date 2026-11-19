//go:build !windows

package herdrcore

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
)

func socketPath() string { return unixSocketPath() }

// socketStat fails when herdr-core's socket file is not there.
func socketStat(path string) error {
	_, err := os.Stat(path)
	return err
}

// dialSocket connects to herdr-core's unix socket.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

// socketGone: err says nobody listens at the socket (refused, or the file went away).
func socketGone(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
