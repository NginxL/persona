package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// extensionMachinesCall handles machines.pair_link and machines.status from extension id (#12798).
// machines.pair_link requires machines.pair (explicitly granted by name, never covered by legacy all-scope tokens).
// machines.status requires machines.read or machines.pair.
func (ps *extensions) extensionMachinesCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	var hubMethod string
	switch method {
	case "machines.pair_link":
		if ps.reg == nil || !ps.reg.HubScopeAllowed(id, extension.ScopeMachinesPair) {
			data, _ := json.Marshal(map[string]string{"scope": extension.ScopeMachinesPair})
			return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + extension.ScopeMachinesPair, Data: data}
		}
		hubMethod = extwire.HubMachinesPairLink
	case "machines.status":
		allowed := ps.reg != nil && (ps.reg.HubScopeAllowed(id, extension.ScopeMachinesRead) || ps.reg.HubScopeAllowed(id, extension.ScopeMachinesPair))
		if !allowed {
			data, _ := json.Marshal(map[string]string{"scope": extension.ScopeMachinesRead})
			return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + extension.ScopeMachinesRead, Data: data}
		}
		hubMethod = extwire.HubMachinesStatus
	default:
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	req := struct {
		Extension string          `json:"extension"`
		Request   json.RawMessage `json:"request,omitempty"`
	}{Extension: id, Request: params}
	var out json.RawMessage
	if err := ps.h.callHub(ctx, hubMethod, req, &out); err != nil {
		return nil, err
	}
	return out, nil
}
