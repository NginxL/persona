//go:build !persona_without_imessage && !persona_core_only

package all

import (
	"github.com/nwverse/persona/server/internal/extmgr/imessageport"
	"github.com/nwverse/persona/server/internal/modules/imessage"
)

// The iMessage connector module: the connector reaches the extension manager through its port
// (docs/architecture/core-and-modules.md "Provider seams"); its node side is internal/modules/all/node's.
func init() {
	modules = append(modules, imessage.Module{})
	imessageport.Provide(imessage.Provider)
}
