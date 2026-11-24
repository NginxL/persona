//go:build !windows

package node

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// peerAuthRequired: on macOS and Linux Claude Code's inbox takes a line without the auth line (the socket's directory
// is the user's own, 0700); the token is still sent when its key file is there, as Claude Code's own sender does.
const peerAuthRequired = false

// peerPathUsable: on macOS and Linux an inbox is a unix socket, an absolute path ending in .sock.
func peerPathUsable(socket string) bool {
	return filepath.IsAbs(socket) && filepath.Ext(socket) == ".sock" && !peerPipeRe.MatchString(socket)
}

// peerInboxExists waits for the socket created by a newly opened desktop session without connecting to it.
func peerInboxExists(socket string) bool {
	st, err := os.Stat(socket)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// dialPeer connects to the inbox's unix socket.
func dialPeer(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}

// peerFinish ends the sending side (the inbox reads the end of the line at once) and waits, at most wait, for the
// inbox to close its end, which it does once it read the line. A socket closed before that is the line's last chance
// gone: the transcript says whether it counted.
func peerFinish(c net.Conn, wait time.Duration) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = c.SetReadDeadline(time.Now().Add(wait))
	_, _ = io.Copy(io.Discard, io.LimitReader(c, 4<<10))
	return nil
}
