package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// A consumed start file proves the command reached the shell. Allow time for
// the shell to hand the terminal to the child and for wrappers to launch it.
type paneStartExit struct {
	file  string
	since time.Time
}

func (p *paneStartExit) check(ctx context.Context, n *Node, pane, engine string) error {
	if p == nil || p.file == "" {
		return nil
	}
	_, err := os.Stat(p.file)
	if !errors.Is(err, fs.ErrNotExist) || !n.shellInFront(ctx, pane) {
		p.since = time.Time{}
		return nil
	}
	if p.since.IsZero() {
		p.since = time.Now()
	}
	if time.Since(p.since) < claudeGoneWait {
		return nil
	}
	shown, _ := n.readPane(ctx, pane, "recent-unwrapped", 20)
	return fmt.Errorf("%s exited during startup\n%s", engine, strings.TrimSpace(lastLines(shown, 8)))
}

func startupErrorLine(text string, prefixes ...string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "│┃╎•✖×⎿"))
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimRight(line, "│┃╎ "))
			}
		}
	}
	return ""
}

func (n *Node) codexStartupFailure(ctx context.Context, pane, text string, exit []*paneStartExit) error {
	if i := strings.LastIndex(text, codexStartError); i >= 0 {
		reason, _, _ := strings.Cut(text[i:], "\n")
		return errors.New(strings.TrimSpace(reason))
	}
	if reason := startupErrorLine(text, "Error: Failed to resume session", "Failed to resume session"); reason != "" {
		return errors.New(reason)
	}
	if len(exit) > 0 {
		return exit[0].check(ctx, n, pane, "Codex")
	}
	return nil
}
