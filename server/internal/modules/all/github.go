//go:build !persona_without_github && !persona_core_only

package all

import (
	"github.com/nwverse/persona/server/internal/extmgr/githubport"
	"github.com/nwverse/persona/server/internal/modules/github"
)

// The GitHub connector module: the built-in GitHub connector reaches the extension manager through its port
// (docs/architecture/core-and-modules.md "Provider seams"); it registers nothing with the kernel yet.
func init() {
	modules = append(modules, github.Module{})
	githubport.Provide(github.Provider)
}
