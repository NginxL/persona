package node

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/node/clipshare"
)

// The clipboard.* methods (Node.handle, #8089); see handle_file.go. Neither reads the shared request fields:
// clipboard.set decodes its own request in clipshare.
const (
	methodClipboardGet = "clipboard.get"
	methodClipboardSet = "clipboard.set"
)

// Sharing the clipboard with the phone (clipshare/).
func init() {
	nodeRPC(methodClipboardGet, func(_ *Node, ctx context.Context, _ json.RawMessage) (any, error) { return clipshare.Get(ctx) })
	nodeRPC(methodClipboardSet, func(_ *Node, ctx context.Context, params json.RawMessage) (any, error) {
		return clipshare.Set(ctx, params)
	})
}
