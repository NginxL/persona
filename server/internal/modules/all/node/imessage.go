//go:build !persona_without_imessage && !persona_core_only

package node

import (
	"github.com/nwverse/persona/server/internal/modules/imessage"
	"github.com/nwverse/persona/server/internal/node/imessageport"
)

// The iMessage module on a Mac's node: it answers the hub's connector.* RPCs from this Mac's Messages.
func init() { imessageport.Provide(imessage.ServeLocal) }
