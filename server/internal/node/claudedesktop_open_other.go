//go:build !windows

package node

import (
	"context"
	"os/exec"
)

// openDesktopLink opens a desktop session in the background (a variable: tests record the link).
var openDesktopLink = func(ctx context.Context, link string) error {
	return exec.CommandContext(ctx, "/usr/bin/open", "-g", link).Run()
}
