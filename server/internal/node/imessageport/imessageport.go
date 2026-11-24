// Package imessageport is how the iMessage connector module reaches the node (docs/architecture/core-and-modules.md
// "Provider seams"): the node's composition root (internal/modules/all/node) provides the module's handler of the hub's
// connector.* RPCs, and the node answers them with it. A build without the module (persona_without_imessage,
// persona_core_only) provides none, and the node answers as on a computer with no Messages: an empty connector.list,
// and no connector to read. It is its own package so that the composition root need not import the node.
package imessageport

import "github.com/nwverse/persona/server/internal/imessagewire"

var serve imessagewire.LocalConnector

// Provide sets the module's handler; the node's composition root calls it from init.
func Provide(f imessagewire.LocalConnector) { serve = f }

// Serve is the module's handler, nil when this build has no iMessage module.
func Serve() imessagewire.LocalConnector { return serve }
