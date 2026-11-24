//go:build darwin

package node

import (
	"context"
	"os/exec"
)

// readSystemProxy queries macOS SystemConfiguration proxy settings via `/usr/sbin/scutil --proxy`.
// Errors and timeouts fail open so bridge startup is never blocked.
func readSystemProxy(ctx context.Context) systemProxy {
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, systemProxyTimeout)
	defer cancel()
	out, err := exec.CommandContext(pctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxy{}
	}
	return parseScutilProxy(string(out))
}
