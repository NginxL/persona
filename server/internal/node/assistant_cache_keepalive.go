package node

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/cachewarm"
	"github.com/nwverse/persona/server/internal/proto"
)

// promptCaching1hEnv makes Claude Code mark its cache breakpoints {"type":"ephemeral","ttl":"1h"}.
const promptCaching1hEnv = "ENABLE_PROMPT_CACHING_1H"

type assistantCacheEntry struct {
	proxy     *cachewarm.Proxy
	launcher  string
	execution modelExecution
	stamp     string
	checking  bool
}
type assistantCachePool struct {
	mu      sync.Mutex
	entries map[string]*assistantCacheEntry
}

func (n *Node) cacheWarmPool() *assistantCachePool {
	if k := n.keeper; k != nil {
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.cacheWarm == nil {
			k.cacheWarm = &assistantCachePool{entries: map[string]*assistantCacheEntry{}}
		}
		return k.cacheWarm
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cacheWarm == nil {
		n.cacheWarm = &assistantCachePool{entries: map[string]*assistantCacheEntry{}}
	}
	return n.cacheWarm
}
func (b *assistantCachePool) put(id string, p *cachewarm.Proxy, launcher string, e modelExecution, stamp string) {
	b.mu.Lock()
	old := b.entries[id]
	b.entries[id] = &assistantCacheEntry{proxy: p, launcher: launcher, execution: e, stamp: stamp}
	b.mu.Unlock()
	if old != nil {
		old.proxy.Close()
	}
}
func (b *assistantCachePool) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, e := range b.entries {
		e.proxy.Close()
		delete(b.entries, id)
	}
}
func (n *Node) forgetCacheWarm(id string) {
	if id == "" {
		return
	}
	b := n.cacheWarmPool()
	b.mu.Lock()
	e := b.entries[id]
	b.mu.Unlock()
	if e != nil {
		e.proxy.Forget()
	}
}
func (n *Node) inputCacheWarm(id string) {
	if id == "" {
		return
	}
	b := n.cacheWarmPool()
	b.mu.Lock()
	e := b.entries[id]
	b.mu.Unlock()
	if e != nil {
		e.proxy.Input()
	}
}

func (n *Node) startCacheWarm(id, launcher string, c assistantCompaction, e modelExecution, excluded bool) *cachewarm.Proxy {
	if excluded || e.Strict || e.CacheKeepalive == nil || !e.CacheKeepalive.Enabled || !e.CacheKeepalive.Valid() || proto.IsExternalLauncher(launcher) || !c.endpointKnown {
		return nil
	}
	// A disconnected managed runtime can fall back to an unknown shell probe in
	// assistantAgent. Its catalog identity still rules it out for Claude replay.
	if managed, ok := n.managedCatalog(launcher); ok && managed.AgentType() != "claude" {
		return nil
	}
	agent, err := n.assistantAgent(context.Background(), launcher)
	if err != nil || agent != "claude" {
		return nil
	}
	upstream := c.endpoint
	if upstream == "" {
		upstream = "https://api.anthropic.com"
	}
	p, err := cachewarm.New(id, upstream, n.logger.Printf)
	if err != nil {
		return nil
	}
	return p
}

// Refresh checks are bounded, once per entry at a time. The reaper itself never
// waits on a model service. A missing role/policy or changed execution discards it.
func (n *Node) sweepCacheWarm(ctx context.Context, now time.Time) {
	b := n.cacheWarmPool()
	b.mu.Lock()
	for id, e := range b.entries {
		if e.checking {
			continue
		}
		e.checking = true
		go func(id string, e *assistantCacheEntry) {
			defer func() {
				b.mu.Lock()
				e.checking = false
				if b.entries[id] == e && e.proxy.Discarded() {
					delete(b.entries, id)
					e.proxy.Close()
				}
				b.mu.Unlock()
			}()
			check, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			launcher, current, known := n.assistantModelDecision(check, id)
			if n.executionFingerprint(e.launcher, modelExecution{}) != e.stamp || !known || launcher != e.launcher || current.Model != e.execution.Model || current.Effort != e.execution.Effort || current.Fingerprint != e.execution.Fingerprint || current.IsMain != e.execution.IsMain || current.Interactive != e.execution.Interactive || current.SupervisionInstructions != e.execution.SupervisionInstructions || assistantOriginToolPolicy(current.Origin) != assistantOriginToolPolicy(e.execution.Origin) {
				e.proxy.Forget()
				return
			}
			n.mu.Lock()
			p := n.procs[id]
			n.mu.Unlock()
			idle := true
			if p != nil {
				p.mu.Lock()
				idle = !p.busy && !p.compacting
				p.mu.Unlock()
			}
			e.proxy.Tick(ctx, now, current.CacheKeepalive, idle)
		}(id, e)
	}
	b.mu.Unlock()
}
