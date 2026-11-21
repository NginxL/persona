package node

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// A local runtime can give its provider process a narrowly scoped MCP ticket.
// Providers discover tools during startup. Calls require a session owned by
// the runtime that issued the ticket.
func (rh *RuntimeHost) a2aMCPToken(h *runtimeConn, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if h.isDaemon() || h.id() == "" || json.Unmarshal(params, &p) != nil || !runtimeIDRe.MatchString(p.SessionID) {
		return nil, errors.New("A2A MCP requires a local runtime and a session ID")
	}
	token, err := a2aMCPDigest(h.id(), p.SessionID)
	if err != nil {
		return nil, errors.New("A2A MCP authorization is unavailable")
	}
	return map[string]string{"token": h.id() + ":" + token}, nil
}

func a2aMCPDigest(runtimeID, sessionID string) (string, error) {
	key, err := readLaunchKey(nodeclient.LaunchKeyPath())
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("persona-runtime-a2a-v1\x00" + runtimeID + "\x00" + sessionID))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (n *Node) scopedA2AMCP(ctx context.Context, req sockRequest) any {
	list := req.Kind == "mcp.tools"
	unavailable := func() any {
		if list {
			return map[string]any{"tools": []proto.Tool{}}
		}
		return proto.TextResult("A2A requires the runtime to own this session", true)
	}
	runtimeID, token, ok := strings.Cut(req.A2AToken, ":")
	if !ok || !runtimeIDRe.MatchString(runtimeID) || !runtimeIDRe.MatchString(req.SessionID) || n.runtimes == nil {
		return unavailable()
	}
	expected, err := a2aMCPDigest(runtimeID, req.SessionID)
	if err != nil || !hmac.Equal([]byte(expected), []byte(token)) {
		return unavailable()
	}
	n.runtimes.mu.Lock()
	h := n.runtimes.byRuntime(runtimeID)
	n.runtimes.mu.Unlock()
	owner, _, known := n.runtimes.belongs(req.SessionID)
	if list && (h == nil || n.peer == nil) {
		return map[string]any{"error": "A2A is temporarily unavailable"}
	}
	if h == nil || n.peer == nil || h.isDaemon() || owner != h && (known || !list) {
		return unavailable()
	}
	guest := req.Guest || n.guestOf(req.SessionID) != nil
	if list {
		// During startup, the Manager can list declarations from machine enrollment.
		session := req.SessionID
		if owner == nil {
			session = ""
		}
		var res struct {
			Tools     []proto.Tool `json:"tools"`
			Error     string       `json:"error"`
			Revision  string       `json:"revision"`
			Unchanged bool         `json:"unchanged"`
		}
		if err := n.peer.Call(ctx, "mcp.tools", mcpToolsParams(req.Input, session, false, guest), &res); err != nil || res.Error != "" {
			return map[string]any{"error": "A2A is temporarily unavailable"}
		}
		tools := slices.DeleteFunc(res.Tools, func(t proto.Tool) bool { return !assistantwire.IsA2ATool(t.Name) })
		if tools == nil {
			tools = []proto.Tool{}
		}
		return map[string]any{"tools": tools, "session": session, "revision": res.Revision, "unchanged": res.Unchanged}
	}
	if !assistantwire.IsA2ATool(req.Name) {
		return proto.TextResult("This MCP server exposes only A2A tools", true)
	}
	meta := map[string]any{"session_id": req.SessionID, "guest": guest, "name": req.Name, "args": req.Args}
	var res proto.ToolResult
	if err := n.peer.Call(ctx, "mcp.call", meta, &res); err != nil {
		return proto.TextResult("A2A is temporarily unavailable", true)
	}
	return res
}
