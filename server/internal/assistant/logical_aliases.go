package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/db"
)

// Physical equivalence comes from authenticated device configuration, never
// node-supplied logical identities or the public transport projection.
func (s *Service) physicalSameMachine(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if d := s.physicalDevices(); d != nil {
		return d.Canonical(a) == d.Canonical(b)
	}
	return a == b
}

func (s *Service) physicalAliases(machine string) []string {
	out := []string{machine}
	if d := s.physicalDevices(); d != nil {
		out = append(out, d.Canonical(machine))
		if aliases, ok := d.(interface{ PhysicalAliases(string) []string }); ok {
			out = append(out, aliases.PhysicalAliases(machine)...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Read every physical alias authoritatively. Even an exact match must not hide
// a conflicting root after devices were absorbed. Ambiguity fails closed.
func (s *Service) physicalLogicalConversation(ctx context.Context, st logicalConversationStore, machine, session string) (*db.LogicalConversation, error) {
	var found *db.LogicalConversation
	for _, alias := range s.physicalAliases(machine) {
		c, err := st.LogicalConversation(ctx, alias, session)
		if err != nil {
			return nil, err
		}
		if c == nil {
			continue
		}
		if c.Deleted {
			return nil, db.ErrConversationDeleted
		}
		if found != nil && (found.MachineID != c.MachineID || found.SessionID != c.SessionID) {
			return nil, db.ErrConversationEpoch
		}
		found = c
	}
	return found, nil
}

func (s *Service) physicalLogicalIndex(index map[[2]string]*db.LogicalConversation, machine, session string) (*db.LogicalConversation, error) {
	var found *db.LogicalConversation
	for _, alias := range s.physicalAliases(machine) {
		c := index[[2]string{alias, session}]
		if c == nil {
			continue
		}
		if found != nil && (found.MachineID != c.MachineID || found.SessionID != c.SessionID) {
			return nil, db.ErrConversationEpoch
		}
		found = c
	}
	return found, nil
}
