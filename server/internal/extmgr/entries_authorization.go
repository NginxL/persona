package extmgr

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"time"
)

// EntriesReadAccess is queried by the assistant for each request, including HTTP
// credentials issued before a grant was revoked. Nothing is cached per token.
func (h *Manager) EntriesReadAccess(id string) (allowed, personal bool) {
	if h.extensions == nil || h.extensions.reg == nil {
		return false, false
	}
	return h.extensions.reg.EntriesReadAccess(id)
}

func (ps *extensions) emitPersonalEntries(payload map[string]any) {
	if ps == nil || ps.reg == nil || ps.h == nil {
		return
	}
	local := false
	if ps.h.opts.AssistantMachine != nil {
		owner := ps.h.opts.AssistantMachine()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		raw, err := nodeclient.DialInterruptible(ctx, nodeclient.Request{Kind: "ping"})
		cancel()
		var p struct {
			Machine string `json:"machine_id"`
			OK      bool   `json:"ok"`
		}
		local = err == nil && json.Unmarshal(raw, &p) == nil && p.OK && owner != "" && p.Machine == owner
	}
	for _, id := range ps.reg.EventSubscribers(assistantwire.EventAssistantEntriesChanged, extension.ScopeAssistantEntriesRead) {
		allowed, personal := ps.h.EntriesReadAccess(id)
		if !allowed || !personal && !local {
			continue
		}
		if hp := ps.get(id); hp != nil && hp.proc != nil {
			_ = hp.proc.Notify("event."+assistantwire.EventAssistantEntriesChanged, payload)
		}
	}
}
