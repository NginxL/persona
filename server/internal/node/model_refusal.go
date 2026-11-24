package node

import "github.com/nwverse/persona/server/internal/proto"

// mergeModelFallback uses the existing incremental scan: a session-scoped fallback can be far before the quick
// tail, including on the first read after a node restart. A pending scan leaves the tail's evidence alone.
func (n *Node) mergeModelFallback(path string, m *quickMeta) {
	n.mu.Lock()
	sc := n.bgScans[path]
	n.mu.Unlock()
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.pending || sc.off > m.size {
		// The file may grow between the tail read and the scan; a later turn cannot explain this snapshot's error.
		return
	}
	m.modelFallback = nil
	if r := sc.meta.ModelFallback; r != nil {
		copy := *r
		m.modelFallback = &copy
		m.model = r.FallbackModel
	}
	// Only the latest failed turn has a code. Never revive an error cleared by a new prompt or a successful reply.
	if m.turnErr.text != "" {
		// Preserve a rewound tail's upstream-unavailable code (sc.meta does not apply terminal rewinds), while still
		// promoting from sc.meta when no rewind happened (for example when a large request blob pushed the previous
		// failed turn before quickTail) and clearing any stale refusal-fallback code.
		keepUpstream := m.turnErr.code == proto.TurnErrorUpstreamUnavailable
		m.turnErr.code = ""
		if m.turnErr.text == sc.meta.TurnError {
			if sc.meta.TurnErrorCode == proto.TurnErrorModelFallbackUnavailable || len(m.rewound) == 0 {
				m.turnErr.code = sc.meta.TurnErrorCode
			}
		}
		if m.turnErr.code == "" && keepUpstream {
			m.turnErr.code = proto.TurnErrorUpstreamUnavailable
		}
		if m.turnErr.code == proto.TurnErrorModelFallbackUnavailable {
			m.turnErr.kind = proto.TurnErrorUser
		}
	}
}
