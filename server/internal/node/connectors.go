package node

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/node/imessageport"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Connectors whose data is only on this computer (docs/plans/plan-connectors.md "Third form: node as a local `connector`"): the hub
// asks, this node reads. For now that is Messages on a Mac, read-only: the iMessage module answers through its port
// (imessageport). A build without it answers as a computer without Messages does.
func localConnector(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if serve := imessageport.Serve(); serve != nil {
		return serve(ctx, method, params)
	}
	if method == "connector.list" {
		return map[string]any{"connectors": []any{}}, nil
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(params, &p)
	return nil, &rpc.Error{Message: "no connector " + p.ID + " on this machine"}
}
