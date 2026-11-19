package node

import (
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// linkHerdrChildren draws each session running in a herdr terminal that another terminal started under the session of
// the nearest listed ancestor terminal (herdr-core's terminal.parent). They are side sessions (ParentAgentID): each
// keeps its own state, unread and sends, and the manager folds it under that session's row. A chain with no listed
// agent session above it (another machine's terminal, or only shells) links nothing, and a loop links nothing.
func linkHerdrChildren(list []proto.Session, agents map[string]host.Agent) {
	if len(list) == 0 || len(agents) == 0 {
		return
	}
	parent := make(map[string]string, len(agents))
	for _, a := range agents {
		if a.PaneID != "" && a.ParentPaneID != "" {
			parent[a.PaneID] = a.ParentPaneID
		}
	}
	if len(parent) == 0 {
		return
	}
	byPane := make(map[string]int, len(list))
	for i := range list {
		if list[i].PaneID == "" {
			continue
		}
		if _, ok := byPane[list[i].PaneID]; !ok {
			byPane[list[i].PaneID] = i
		}
	}
	for i := range list {
		s := &list[i]
		if s.PaneID == "" || s.ParentAgentID() != "" || loopsBack(s.PaneID, parent) {
			continue
		}
		seen := map[string]bool{s.PaneID: true}
		for p := parent[s.PaneID]; p != "" && !seen[p]; p = parent[p] {
			seen[p] = true
			if j, ok := byPane[p]; ok && j != i {
				s.SetParentAgentID(list[j].SessionID)
				break
			}
		}
	}
}
