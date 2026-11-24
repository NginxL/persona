package hub

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type codexSaveEntry struct {
	mu      sync.Mutex // serializes PutSetting writes for this machine
	pending bool
	cfg     proto.CodexConfig
	gen     uint64
}

func codexInventoryKey(machine string) string { return "codex_inventory/" + machine }

func (m *Machine) codexConfig(ctx context.Context) proto.CodexConfig {
	var cfg proto.CodexConfig
	if m.hub.store != nil {
		_, _ = m.hub.store.Setting(ctx, codexInventoryKey(m.cfg.ID), &cfg)
	}
	if m.cfg.CodexDiscovery != nil {
		cfg.Discovery = m.cfg.CodexDiscovery
	}
	for _, h := range m.cfg.CodexHomes {
		if !slices.Contains(cfg.Homes, h) {
			cfg.Homes = append(cfg.Homes, h)
		}
	}
	for _, s := range m.cfg.CodexServers {
		i := slices.IndexFunc(cfg.Servers, func(v proto.CodexServer) bool { return v.ID == s.ID })
		if i >= 0 {
			cfg.Servers[i] = s
		} else {
			cfg.Servers = append(cfg.Servers, s)
		}
	}
	return cfg
}

// codexInventoryHanded: the node's current connection, as its manager reports it, is the one the hub handed the kept
// inventory to (devices.connected, machineConnected's codex). A node's registry starts empty on every connection and
// gets the kept inventory only then; a report from before (a node before this check waited for it, node
// codex_registry.go rememberCodex) would store that empty registry over what the hub kept: the threads the user joined,
// the other homes and the app-servers. A machine the hub connects itself (no manager reports it) is taken as handed.
func (m *Machine) codexInventoryHanded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.managed || m.connected && !m.hookedAt.IsZero() && m.hookedAt.Equal(m.connectedAt)
}

func (m *Machine) rememberCodexInventory(raw json.RawMessage) {
	if m.hub.store == nil {
		return
	}
	var cfg proto.CodexConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	h := m.hub
	h.codexPendingMu.Lock()
	if h.codexPending == nil {
		h.codexPending = map[string]*codexSaveEntry{}
	}
	e := h.codexPending[m.cfg.ID]
	if e == nil {
		e = &codexSaveEntry{}
		h.codexPending[m.cfg.ID] = e
	}
	e.cfg = cfg
	e.pending = true
	e.gen++
	h.codexPendingMu.Unlock()
	h.flushCodexInventory(m.cfg.ID, e)
}

func (h *Hub) flushCodexInventory(mid string, e *codexSaveEntry) {
	if h.store == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for {
		h.codexPendingMu.Lock()
		if !e.pending {
			h.codexPendingMu.Unlock()
			return
		}
		cfg := e.cfg
		gen := e.gen
		e.pending = false
		h.codexPendingMu.Unlock()
		if !h.quiesce.beginOrDefer(func() {
			h.codexPendingMu.Lock()
			if e.gen == gen {
				e.pending = true
			}
			h.codexPendingMu.Unlock()
		}) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// Updates come from this authenticated node, not public endpoint URLs. The node never reports credentials.
		if err := h.store.PutSetting(ctx, codexInventoryKey(mid), cfg); err != nil {
			h.logf("[%s] remember Codex inventory: %v", mid, err)
		}
		cancel()
		h.quiesce.end()
	}
}

func (h *Hub) resumeCodexInventory() {
	if h.store == nil {
		return
	}
	type item struct {
		mid string
		e   *codexSaveEntry
	}
	h.codexPendingMu.Lock()
	var todo []item
	for mid, e := range h.codexPending {
		if e.pending {
			todo = append(todo, item{mid: mid, e: e})
		}
	}
	h.codexPendingMu.Unlock()
	if len(todo) == 0 {
		return
	}
	h.nodeWorkers.Add(1)
	go func() {
		defer h.nodeWorkers.Done()
		for _, it := range todo {
			h.flushCodexInventory(it.mid, it.e)
		}
	}()
}
