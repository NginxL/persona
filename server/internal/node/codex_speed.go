package node

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// A missing serviceTier is unknown. Native null means Standard, and older
// Codex versions used "fast" for the tier now advertised as "priority".
func codexServiceTier(raw json.RawMessage) string {
	var tier string
	if len(raw) == 0 || json.Unmarshal(raw, &tier) != nil {
		return ""
	}
	switch tier {
	case "":
		return "default"
	case "fast":
		return "priority"
	default:
		return tier
	}
}

func (n *Node) codexSpeedRow(s *proto.Session) {
	c, thread, ok := n.codexEffortThread(s.SessionID)
	if !ok {
		return
	}
	c.mu.Lock()
	tiers := c.models[n.codexModelNow(s.SessionID, c, thread, s.Model)].tiers
	c.mu.Unlock()
	s.ServiceTier = thread.serviceTier
	if len(tiers) > 0 {
		s.ServiceTiers = append([]proto.ServiceTier{{ID: "default", Name: "Standard"}}, tiers...)
	}
}

// Speed is a separate RPC so an older node cannot silently ignore it in modes.
// There is no optimistic or next-message fallback for a billable speed choice.
func (n *Node) setCodexSpeed(ctx context.Context, id, tier string) error {
	n.mu.Lock()
	proc := n.procs[id]
	n.mu.Unlock()
	if proc != nil && proc.agent == "codex" && proc.cx != nil {
		return proc.cx.setSpeed(ctx, tier)
	}
	c, thread, ok := n.codexEffortThread(id)
	if !ok {
		return fmt.Errorf("this session does not support Codex speed changes")
	}
	model := n.codexModelNow(id, c, thread, n.metaOf(id).model)
	c.mu.Lock()
	tiers := c.models[model].tiers
	c.mu.Unlock()
	if len(tiers) == 0 || tier != "default" && !slices.ContainsFunc(tiers, func(t proto.ServiceTier) bool { return t.ID == tier }) {
		return fmt.Errorf("this model does not advertise the requested Codex speed")
	}
	call, cancel := context.WithTimeout(ctx, codexEffortTimeout)
	defer cancel()
	if err := c.wire.call(call, "thread/settings/update", map[string]any{"threadId": thread.id, "serviceTier": tier}, nil); err != nil {
		return fmt.Errorf("couldn't change Codex speed: %w", err)
	}
	c.mu.Lock()
	if current, exists := c.threads[thread.id]; exists {
		// A native settings notification received during the RPC is newer.
		if current.serviceTierRevision == thread.serviceTierRevision {
			current.serviceTier = tier
			current.serviceTierRevision++
		}
		current.revision++
		c.threads[thread.id] = current
	}
	c.mu.Unlock()
	n.notifyCodex()
	return nil
}
