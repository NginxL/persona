//go:build !windows

package node

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The Codex App's bus on macOS: a socket in its Codex home (codex_desktop.go).

func codexDesktopSocket(home string) string { return filepath.Join(home, "ipc", "ipc.sock") }

// codexDesktopAvailable requires a listener, not just the socket file an App update or exit can leave behind.
// A list pass probes once per home. Connecting and closing sends no bus request and changes no conversation.
func codexDesktopAvailable(home string) bool {
	if home == "" {
		return false
	}
	st, err := os.Stat(codexDesktopSocket(home))
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	conn, err := dialCodexDesktopBus(ctx, home)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func dialCodexDesktopBus(ctx context.Context, home string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", codexDesktopSocket(home))
}

// openCodexThread asks the App to open a thread (it comes to the front, showing it). A variable for tests.
var openCodexThread = func(thread string) error {
	return exec.Command("open", "codex://threads/"+thread).Run()
}
