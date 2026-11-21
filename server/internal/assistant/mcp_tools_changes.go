package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A cursor is process-local: rebuilding it after a manager restart invalidates old
// watches. Take it before listing tools so a concurrent link cannot be missed.
type mcpToolChanges struct {
	mu      sync.Mutex
	cursor  string
	changed chan struct{}
}

func (c *mcpToolChanges) snapshot() (string, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.cursor, c.changed = randomHex(16), make(chan struct{})
	}
	return c.cursor, c.changed
}

func (c *mcpToolChanges) notify() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed != nil {
		close(c.changed)
	}
	c.cursor, c.changed = randomHex(16), make(chan struct{})
}

// mcpToolsHeartbeat is a discovery heartbeat, not a legacy startup wait. Revision-aware calls never use leaderListWait.
var mcpToolsHeartbeat = 30 * time.Second

// mcpToolsWatchEvent is a test-only observation hook. Production leaves it nil.
var mcpToolsWatchEvent func(string)

func noteMCPWatchEvent(event string) {
	if mcpToolsWatchEvent != nil {
		mcpToolsWatchEvent(event)
	}
}

type revisionDiscoveryKey struct{}

func (s *Service) sessionMCPToolsSince(ctx context.Context, machine, session string, assistant, guest bool, since string) map[string]any {
	if since != "" {
		ctx = context.WithValue(ctx, revisionDiscoveryKey{}, true)
	}
	ctx = context.WithValue(ctx, guardDecisionKey{}, &guardDecisionCapture{})
	base, changed := s.mcpChanges.snapshot()
	cursor := s.guardDiscoveryCursor(ctx, machine, session, assistant, base)
	if since == cursor {
		noteMCPWatchEvent("wait")
		timer := time.NewTimer(mcpToolsHeartbeat)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return map[string]any{"revision": cursor, "unchanged": true}
		case <-timer.C:
			noteMCPWatchEvent("heartbeat")
			// A heartbeat also rechecks filter state: expiry and read recovery do not call notify themselves.
			base, _ = s.mcpChanges.snapshot()
			ctx = context.WithValue(ctx, guardDecisionKey{}, &guardDecisionCapture{})
			next := s.guardDiscoveryCursor(ctx, machine, session, assistant, base)
			if next == cursor {
				return map[string]any{"revision": cursor, "unchanged": true}
			}
			cursor = next
		case <-changed:
			base, _ = s.mcpChanges.snapshot()
			ctx = context.WithValue(ctx, guardDecisionKey{}, &guardDecisionCapture{})
			cursor = s.guardDiscoveryCursor(ctx, machine, session, assistant, base)
		}
	}
	// Filter with a fresh capture and return its decision as the revision too. An admission read and a list read may
	// disagree transiently, but the list and revision a client caches always come from the same effective decision.
	ctx = context.WithValue(ctx, guardDecisionKey{}, &guardDecisionCapture{})
	tools, err := s.sessionMCPToolsChecked(ctx, machine, session, assistant, guest)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if tools == nil {
		tools = []proto.Tool{}
	}
	return map[string]any{"tools": tools, "revision": s.guardDiscoveryCursor(ctx, machine, session, assistant, base)}
}
