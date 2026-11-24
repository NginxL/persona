// Package imessageport is how the iMessage connector module reaches the extension manager
// (docs/architecture/core-and-modules.md "Provider seams"): the composition root (internal/modules/all) provides the
// module's factory, and extmgr makes the connector with it for the Mac the user picked. A build without the module
// (persona_without_imessage, persona_core_only) provides none: the manager registers no Messages connector and no
// Messages extension, and its routes answer that it is not configured. It is its own package so that the composition
// root need not import extmgr.
package imessageport

import "github.com/nwverse/persona/server/internal/imessagewire"

var factory imessagewire.Factory

// Provide sets the iMessage module's factory; the composition root calls it from init.
func Provide(f imessagewire.Factory) { factory = f }

// Factory is the iMessage module's factory, nil when this build has no iMessage module.
func Factory() imessagewire.Factory { return factory }
