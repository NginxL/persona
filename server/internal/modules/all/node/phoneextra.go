//go:build !persona_without_phone_extra && !persona_core_only

package node

import (
	"context"
	"encoding/json"
	"io"

	"github.com/nwverse/persona/server/internal/modules/phone-extra/iphone"
	"github.com/nwverse/persona/server/internal/modules/phone-extra/nodeiphone"
	"github.com/nwverse/persona/server/internal/node/iphoneport"
	"github.com/nwverse/persona/server/internal/nodecmd/commandport"
	"github.com/nwverse/persona/server/internal/proto"
)

// The phone-extra module on the node: the built-in program iphone (this computer's paired iPhones for phone control,
// docs/extensions/iphone.md) and `personad iphone …`, which pairs them (docs/extensions/iphone-pairing.md).
func init() {
	iphoneport.Provide(iphoneBridge{})
	commandport.AddCommand("iphone", iphone.Usage, func(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return iphone.Command(ctx, args, stdout, stderr)
	})
}

// iphoneBridge is the module's iphone program as the node's port has it.
type iphoneBridge struct{}

func (iphoneBridge) Status(ctx context.Context) proto.IPhone { return nodeiphone.Status(ctx) }

func (iphoneBridge) Call(ctx context.Context, params json.RawMessage) (*proto.IPhoneResult, error) {
	return nodeiphone.Call(ctx, params)
}

func (iphoneBridge) Pair(ctx context.Context, method string, params json.RawMessage) (proto.IPhone, error) {
	return nodeiphone.Pair(ctx, method, params)
}

func (iphoneBridge) Install(ctx context.Context, params json.RawMessage) (proto.IPhone, error) {
	return nodeiphone.Install(ctx, params)
}

func (iphoneBridge) Cancel() { nodeiphone.Cancel() }
