package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func cacheKeepaliveOf(v Settings) assistantwire.CacheKeepalive {
	p := assistantwire.CacheKeepalive{Enabled: v.CacheKeepalive == nil || *v.CacheKeepalive, IntervalMinutes: 55, MaxRequests: 24}
	if v.CacheKeepaliveMinutes != nil {
		p.IntervalMinutes = *v.CacheKeepaliveMinutes
	}
	if v.CacheKeepaliveMax != nil {
		p.MaxRequests = *v.CacheKeepaliveMax
	}
	if !p.Valid() {
		p.IntervalMinutes, p.MaxRequests = 55, 24
	}
	return p
}

// putCacheKeepalive attests the exact registry role, never the broader supervision
// or model-following flags. The node additionally requires a native Claude launcher.
func (s *Service) putCacheKeepalive(ctx context.Context, machine, id string, main bool, p map[string]any) {
	delete(p, "cache_keepalive")
	v := s.settings(ctx)
	if !main && (id == "" || !slices.Contains(v.Leaders, id)) {
		return
	}
	if id != "" {
		if id == v.BackgroundSupervisor {
			return
		}
		if archived, known := s.archivedSet(); !known || archived[id] != "" {
			return
		}
		// Eligibility must not add an RPC to every send or background read. The
		// registry attests the role; retained inventory can additionally revoke it.
		if c, ok := s.nodeSession(machine, id); ok && (!s.inventoryAssistant(machine, c) || c.IsSubagent || c.ParentSessionID != "" || c.ParentThreadID != "" || c.Archived || c.IsDeleted()) {
			return
		}
	}
	p["cache_keepalive"] = cacheKeepaliveOf(v)
}

// inventoryAssistant reads the assistant mark the way the apps get it. A node never sets Session.Assistant in its
// own list (only the hub's decorated rows carry it), so the raw inventory row is judged by its directory as
// tryAgentName does. Reading the bare flag revoked every main and lead, and no start mounted the refresh proxy.
func (s *Service) inventoryAssistant(machine string, c proto.Session) bool {
	if c.Assistant {
		return true
	}
	m, ok := s.fleetMachine(machine)
	return ok && m.isAssistant(c)
}

func (s *Service) cacheKeepaliveRefs(ctx context.Context, v Settings) []SessionRef {
	refs := s.leadSessions(ctx, v)
	if v.Main != "" {
		refs = append(refs, SessionRef{MachineID: v.mainRouteMachine(), SessionID: v.Main})
	}
	return refs
}
