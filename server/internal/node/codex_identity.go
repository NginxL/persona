package node

import (
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"slices"
	"strings"
)

func (n *Node) ownedAssistantSource(id string) bool {
	n.mu.Lock()
	_, own := n.procs[id]
	path := n.paths[id]
	n.mu.Unlock()
	if own || compat.IsAssistantTranscript(path) {
		return true
	}
	if n.state != nil {
		if st, ok := n.state.get(id); ok && identity.IsAssistantDir(st.Cwd) {
			return true
		}
	}
	return false
}

// A legacy id shared with another product must not redirect old outbox entries to that product.
func (n *Node) codexBlocked(id string) bool {
	r := n.codices
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	blocked := slices.Contains(r.config.BlockedIDs, id)
	src, known := r.threads[id]
	sameHome := src.home == r.config.LegacyHome
	otherFile := r.claudeIDs[id]
	r.mu.Unlock()
	if blocked {
		return true
	}
	if !known || !sameHome || src.thread != id || n.ownedAssistantSource(id) {
		return false
	}
	n.mu.Lock()
	a, live := n.agents[id]
	path := n.paths[id]
	n.mu.Unlock()
	if !otherFile && !(live && a.Agent != "codex") && !strings.Contains(path, "/projects/") {
		return false
	}
	r.mu.Lock()
	if !slices.Contains(r.config.BlockedIDs, id) {
		r.config.BlockedIDs = append(r.config.BlockedIDs, id)
	}
	r.mu.Unlock()
	n.rememberCodex()
	return true
}

func markCodexConflict(s *proto.Session) {
	s.SourceKind = "identity_conflict"
	s.Capabilities = &proto.SessionCapabilities{}
	s.Status = proto.StatusUnknown
	s.ReadOnlyReason = "会话 ID 与另一来源重复，不能判断目标，原记录没有合并或删除"
	s.LastText = ""
}

func (n *Node) codexPaneHome(pane string) string {
	if r := n.codices; r != nil {
		r.mu.Lock()
		home := r.byPane[pane]
		r.mu.Unlock()
		if home != "" {
			return home
		}
	}
	return n.codexHome()
}

func (n *Node) codexPublicID(home, raw string) string {
	if n.codices == nil || strings.HasPrefix(raw, "c0de-") {
		return raw
	}
	return n.registerCodex(home, raw, "")
}
