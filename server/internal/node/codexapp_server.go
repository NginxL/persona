package node

import (
	"context"
	"fmt"
)

// codexAppCall performs one metadata request on an isolated app-server. Callers never resume or run a turn in a
// desktop-owned thread: its live owner handles that through the desktop bus.
type codexAppCall func(method string, params any, result any) error

type codexAppServerError struct {
	Method  string
	Code    int
	Message string
}

func (e *codexAppServerError) Error() string { return fmt.Sprintf("%s: %s", e.Method, e.Message) }

// withCodexAppServer starts a short-lived metadata client in the desktop's Codex home. Calls are sequential, bounded
// by the context, and use JSON on standard input rather than a shell command line.
func withCodexAppServer(ctx context.Context, cli, home, cwd string, use func(codexAppCall) error) error {
	s, err := startCodexStdio(ctx, cli, home, cwd)
	if err != nil {
		return err
	}
	defer s.close()
	return use(func(method string, params, result any) error {
		return s.call(ctx, method, params, result)
	})
}
