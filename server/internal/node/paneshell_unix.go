//go:build !windows

package node

import (
	"context"

	"github.com/nwverse/persona/server/internal/host"
)

// adoptedPaneShells keeps an adopted pane's programs as they are on Unix: a pwsh there gets the command typed as
// before, and no Git Bash handoff. A var for tests, which put Windows' windowsAdoptedPaneShells in its place.
var adoptedPaneShells = func(names []string) []string { return names }

// freshPaneShells preserves the pre-existing Unix choice: the host's report, else herdrPaneShell. It performs no
// foreground request or config read, so Unix starts keep their exact inputs and typed bytes.
func (n *Node) freshPaneShells(_ context.Context, _ host.Launcher, created *host.Created) []string {
	if created.Shell != "" {
		return []string{created.Shell}
	}
	return []string{herdrPaneShell()}
}
