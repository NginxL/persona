package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/db"
)

type logicalPickStore interface {
	logicalConversationStore
	SetConversationPick(context.Context, string, string, int64, string, string, string) error
}

func (s *Service) logicalExecutionMachine(ctx context.Context, machine, session string) string {
	if st, ok := s.store().(logicalConversationStore); ok {
		c, err := st.LogicalConversation(ctx, machine, session)
		if err == nil && c != nil {
			if a, ok := c.Active(); ok {
				return a.MachineID
			}
		}
	}
	return machine
}
func (s *Service) logicalPickConversation(ctx context.Context, machine, session string) (*db.LogicalConversation, error) {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return nil, nil
	}
	c, err := st.LogicalConversation(ctx, machine, session)
	if err != nil || c == nil {
		return nil, err
	}
	a, ok := c.Active()
	if !ok || c.Deleted || a.Ordinal == 1 {
		return nil, nil
	}
	return c, nil
}
func (s *Service) logicalUpdatePick(ctx context.Context, session string, change func(modelPick, bool) (*modelPick, bool)) (bool, error) {
	st, ok := s.store().(logicalPickStore)
	if !ok {
		return false, nil
	}
	rows, err := st.LogicalConversations(ctx)
	if err != nil {
		return true, err
	}
	for _, c := range rows {
		if c.SessionID != session {
			continue
		}
		a, ok := c.Active()
		if !ok || a.Ordinal == 1 {
			return false, nil
		}
		old := modelPick{MachineID: c.MachineID, Launcher: a.Launcher, Model: a.Model, Effort: a.Effort}
		next, changed := change(old, true)
		if !changed {
			return true, nil
		}
		if next == nil {
			next = &modelPick{MachineID: c.MachineID, Launcher: a.Launcher}
		}
		if next.MachineID != c.MachineID {
			return true, db.ErrConversationEpoch
		}
		target, err := s.logicalTarget(ctx, a.MachineID, next.Launcher)
		if err != nil {
			return true, err
		}
		engine := target.Engine
		if engine != a.Engine {
			return true, logicalRefusal(ctx, "explicit_switch_required")
		}
		if err = st.SetConversationPick(ctx, c.MachineID, session, c.Epoch, next.Launcher, next.Model, next.Effort); err != nil {
			return true, err
		}
		s.logicalChanged()
		s.publish(ctx)
		return true, nil
	}
	return false, nil
}
