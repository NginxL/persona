// Package iphoneport is how the phone-extra module reaches the node (docs/architecture/core-and-modules.md "The node's
// modules"): the built-in program iphone (docs/extensions/iphone-bridge.md), this computer's paired iPhones over the
// bridge, which the hub asks over plugin.node.call. The node's composition root (internal/modules/all/node) provides
// the module's bridge; a build without the module (persona_without_phone_extra, persona_core_only) provides none, and
// the node lists no iphone program in node.info, so the hub offers no phone control on this computer. It is its own
// package so that the composition root need not import internal/node.
package iphoneport

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/proto"
)

// Bridge is the iphone program's methods, as the node routes them (internal/node/builtin.go).
type Bridge interface {
	// Status is the iPhones this computer reaches and the bridge's state.
	Status(ctx context.Context) proto.IPhone
	// Call runs one phone-control call.
	Call(ctx context.Context, params json.RawMessage) (*proto.IPhoneResult, error)
	// Pair starts a background pair, forget or prepare job; its state comes back with Status.
	Pair(ctx context.Context, method string, params json.RawMessage) (proto.IPhone, error)
	// Install installs the bridge from the release's build the manager found (#8199).
	Install(ctx context.Context, params json.RawMessage) (proto.IPhone, error)
	// Cancel stops the running job.
	Cancel()
}

var bridge Bridge

// Provide sets the phone-extra module's bridge; the node's composition root calls it from init.
func Provide(b Bridge) { bridge = b }

// Get is the module's bridge, nil when this build has no phone-extra module.
func Get() Bridge { return bridge }
