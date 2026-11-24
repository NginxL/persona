package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The node's side (docs/plans/plan-connectors.md "Third form: node as a local `connector`"): the hub asks, the node of
// this Mac reads its Messages, read-only. Only the hub asks — these are hub → node RPCs; the node socket of hooks and
// MCP does not route here — and nothing is logged: no number, no message. The node reaches ServeLocal through its port
// (internal/node/imessageport), which the node's composition root fills.

var messagesReader = sync.OnceValue(func() *Reader {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	// A resident node is started by launchd and has no ssh in its environment: Full Disk Access is then its own.
	via := "launchd"
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" {
		via = "ssh"
	}
	return &Reader{Home: home, Via: via, Executable: exe}
})

// localConnectorStatus is one entry of connector.list.
type localConnectorStatus struct {
	ID string `json:"id"`
	Status
}

// ServeLocal answers the hub's connector.* RPCs on this computer (imessagewire.LocalConnector).
func ServeLocal(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return serveLocalConnector(ctx, messagesReader(), runtime.GOOS == "darwin", method, params)
}

// serveLocalConnector answers connector.list, .chats, .older, .changes and .search with r (mac: this is a Mac).
func serveLocalConnector(ctx context.Context, r *Reader, mac bool, method string, params json.RawMessage) (any, error) {
	var p struct {
		ID     string `json:"id"`
		Chat   string `json:"chat"`
		Before string `json:"before"`
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
		Query  string `json:"query"`
		Days   int    `json:"days"` // connector.changes: where the first page starts; connector.search: how far back
		Keep   string `json:"keep"` // contacts (default) | all
	}
	_ = json.Unmarshal(params, &p)
	if method == "connector.list" {
		list := []localConnectorStatus{}
		if mac {
			list = append(list, localConnectorStatus{ID: ID, Status: r.Status(ctx)})
		}
		return map[string]any{"connectors": list}, nil
	}
	if p.ID != ID || !mac {
		return nil, &rpc.Error{Message: "no connector " + p.ID + " on this machine"}
	}
	if p.Days <= 0 {
		p.Days = 30 // the first page of changes: a month back
		if method == "connector.search" {
			p.Days = 365
		}
	}
	since := time.Now().AddDate(0, 0, -p.Days)
	var res any
	var err error
	switch method {
	case "connector.chats": // keep is empty for the app's own view: every conversation
		res, err = r.Chats(ctx, p.Limit, p.Before, p.Keep)
	case "connector.older":
		res, err = r.Messages(ctx, p.Chat, p.Limit, p.Before, p.Keep)
	case "connector.changes":
		res, err = r.Changes(ctx, p.Cursor, p.Limit, since, p.Keep)
	case "connector.search":
		var hits []Hit
		hits, err = r.Search(ctx, p.Query, p.Limit, since, p.Keep)
		res = map[string]any{"hits": hits}
	}
	switch {
	case errors.Is(err, ErrNoPermission):
		return nil, &rpc.Error{Code: proto.CodeNoPermission, Message: r.Status(ctx).Error}
	case errors.Is(err, ErrNoChat):
		return nil, &rpc.Error{Code: proto.CodeNoChat, Message: "no such conversation"}
	case err != nil:
		return nil, err
	}
	return res, nil
}
