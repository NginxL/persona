package node

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// herdr's server is not started on Windows: the node keeps reporting it missing, as before (herdrstart.go).
func launchHerdrServer(context.Context, string, string) (<-chan error, error) {
	return nil, errors.New("starting herdr is not supported on Windows")
}

func lockHerdrDir(string) (func(), error) { return func() {}, nil }

// herdrPaneShell: herdr on Windows does not read HERDR_AGENT, so nothing types it (hostherdr.go). A var for tests.
var herdrPaneShell = func() string { return "" }

// adoptedPaneShells: an adopted pane whose innermost program is PowerShell or pwsh takes a new PowerShell pane's
// handoff; any other stack is used as it is (windowsAdoptedPaneShells). A var for tests.
var adoptedPaneShells = windowsAdoptedPaneShells

// freshPaneShells resolves a new pane. Only classic herdr gets foreground/config detection and a PowerShell default.
// Every unnamed nonclassic pane returns main's empty fallback; a host-named non-PowerShell shell stays as reported.
func (n *Node) freshPaneShells(ctx context.Context, launch host.Launcher, created *host.Created) []string {
	classic := created.Pane.Kind == host.Herdr
	if created.Shell != "" || !classic {
		return windowsPaneShellDecision(classic, created.Shell, nil, "")
	}
	var foreground []string
	if f, ok := launch.(host.ForegroundNamer); ok {
		if names, err := f.PaneForeground(ctx, created.Pane); err == nil {
			foreground = names
		}
	}
	if windowsForegroundUsable(foreground) {
		return windowsPaneShellDecision(true, "", foreground, "")
	}
	return windowsPaneShellDecision(true, "", foreground, herdr.TerminalDefaultShell(n.home))
}
