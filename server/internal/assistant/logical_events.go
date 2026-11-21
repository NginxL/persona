package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Physical notifications remain synchronization hints. Only an authoritative pull
// may change a source generation; a delayed notification cannot roll it back.
func (s *Service) NodeEvent(machine, event string, raw json.RawMessage) bool {
	if event == "session.echo" {
		var receipt struct {
			ClientMsgID string `json:"client_msg_id"`
		}
		if json.Unmarshal(raw, &receipt) == nil && strings.HasPrefix(receipt.ClientMsgID, proto.A2ADeliveryPrefix) {
			return true // The Hub hands this receipt back to the physical node.
		}
	}
	s.historySignal(machine, event, raw)
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return s.nodeEvent(machine, event, raw)
	}
	if event == "interaction.cancel" || event == "interaction.dismiss" {
		var p struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return false
		}
		if routes, ok := s.store().(logicalInteractionStore); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			route, err := routes.ConversationInteraction(ctx, p.ID)
			if err != nil {
				return false
			}
			if route != nil {
				if !s.physicalSameMachine(machine, route.PhysicalMachine) || p.SessionID != "" && p.SessionID != route.PhysicalSession {
					return false
				}
				_ = st.WithConversationEpoch(ctx, route.MachineID, route.SessionID, route.Epoch, func(c *db.LogicalConversation) error {
					a, ok := c.Active()
					if !ok || !s.physicalSameMachine(a.MachineID, route.PhysicalMachine) || a.SessionID != route.PhysicalSession {
						return db.ErrConversationEpoch
					}
					s.nodeEvent(route.MachineID, event, raw)
					return nil
				})
				return false
			}
		}
	}
	if event == "sessions.changed" {
		if s.devs == nil {
			return s.nodeEvent(machine, event, raw)
		}
		s.logicalChanged()
		rows, _, err := s.logicalIndex()
		if err != nil {
			return false
		}
		roots := map[string]bool{machine: true}
		for _, c := range rows {
			for _, seg := range c.Segments {
				if s.physicalSameMachine(seg.MachineID, machine) {
					roots[c.MachineID] = true
				}
			}
		}
		for root := range roots {
			if list, known := s.devs.Sessions(root); known {
				s.setSessions(root, list)
			}
		}
		s.historyWake()
		return false
	}
	if event == "sessions.revs" {
		var p struct {
			Revs map[string]int64 `json:"revs"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return false
		}
		// Do not block a node's reader on a write lock held by an in-flight RPC.
		s.inOrder(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			byMachine := map[string]map[string]int64{}
			for sid, rev := range p.Revs {
				c, err := s.physicalLogicalConversation(ctx, st, machine, sid)
				if err != nil {
					continue
				}
				root, id, value := machine, sid, rev
				if c != nil {
					a, found := c.Active()
					if !found || !s.physicalSameMachine(a.MachineID, machine) || a.SessionID != sid {
						continue
					}
					if st.ObserveConversationRevision(ctx, a.MachineID, a.SessionID, rev) != nil {
						continue
					}
					c, err = s.physicalLogicalConversation(ctx, st, machine, sid)
					if err != nil || c == nil {
						continue
					}
					root, id, value = c.MachineID, c.SessionID, c.Revision
				}
				if byMachine[root] == nil {
					byMachine[root] = map[string]int64{}
				}
				byMachine[root][id] = value
			}
			s.logicalChanged()
			for root, revs := range byMachine {
				s.logicalEmit(root, event, map[string]any{"revs": revs})
			}
		})
		return false
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return s.nodeEvent(machine, event, raw)
	}
	var sid string
	_ = json.Unmarshal(p["session_id"], &sid)
	if sid == "" {
		return s.nodeEvent(machine, event, raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := s.physicalLogicalConversation(ctx, st, machine, sid)
	if err != nil {
		return false
	}
	if c == nil {
		return s.nodeEvent(machine, event, raw)
	}
	a, found := c.Active()
	if c.Deleted || !found || !s.physicalSameMachine(a.MachineID, machine) || a.SessionID != sid {
		return false
	}
	if event == "session.items" {
		var page historyPage
		if json.Unmarshal(raw, &page) != nil {
			return false
		}
		if page.History != "" && a.SourceHistory != "" && page.History != a.SourceHistory {
			s.historyWake()
			return false
		}
		items := make([]proto.Item, 0, len(page.Items))
		for _, it := range page.Items {
			projected, visible, e := a.Project(it, c.MachineID, c.SessionID, c.History)
			if e != nil {
				return false
			}
			if visible {
				items = append(items, projected)
			}
		}
		p["items"], _ = json.Marshal(items)
		if page.FromSeq != nil && page.ToSeq != nil {
			from, to, e := a.Span(*page.FromSeq, *page.ToSeq)
			if e != nil {
				return false
			}
			p["from_seq"], _ = json.Marshal(from)
			p["to_seq"], _ = json.Marshal(to)
		}
		p["history"], _ = json.Marshal(c.History)
	}
	p["session_id"], _ = json.Marshal(c.SessionID)
	if _, ok := p["machine_id"]; ok {
		p["machine_id"], _ = json.Marshal(c.MachineID)
	}
	projected, err := json.Marshal(p)
	if err != nil {
		return false
	}
	if s.nodeEvent(c.MachineID, event, projected) {
		s.hub.Tell(devicewire.EventNode, devicewire.NodeEvent{MachineID: c.MachineID, Event: event, Params: projected})
	}
	return false
}

func (s *Service) logicalEmit(machine, event string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if s.nodeEvent(machine, event, raw) {
		s.hub.Tell(devicewire.EventNode, devicewire.NodeEvent{MachineID: machine, Event: event, Params: raw})
	}
}

// Rebind subscriptions after the committed epoch, including channels' follows.
// A zero-width bridge preserves the shared integer boundary for an idle target.
func (s *Service) logicalRebind(ctx context.Context, c *db.LogicalConversation) {
	if c == nil {
		return
	}
	ctx = context.WithValue(ctx, logicalTransportKey{}, nil)
	s.logicalChanged()
	if publisher, ok := s.physicalDevices().(interface{ ReportState() }); ok {
		publisher.ReportState()
	}
	a, ok := c.Active()
	if !ok {
		return
	}
	for _, retired := range c.Segments {
		if retired.State == "retired" || retired.State == "abandoned" {
			_, _ = s.logicalPhysical(ctx, retired.MachineID, "session.unwatch", map[string]any{"session_id": retired.SessionID})
			if physical := s.physicalDevices(); physical != nil {
				_ = physical.Follow(ctx, retired.MachineID, retired.SessionID, false)
			}
		}
	}
	if c.Epoch > 1 {
		_, _ = s.logicalPhysical(ctx, a.MachineID, "assistant.segment.restore", map[string]any{"session_id": a.SessionID})
	}
	s.logical.mu.Lock()
	watch := append(json.RawMessage(nil), s.logical.watches[[2]string{c.MachineID, c.SessionID}]...)
	s.logical.mu.Unlock()
	if len(watch) > 0 {
		var p map[string]any
		_ = json.Unmarshal(watch, &p)
		p["session_id"] = a.SessionID
		p["since_seq"] = 0
		p["history"] = a.SourceHistory
		_, _ = s.logicalPhysical(ctx, a.MachineID, "session.watch", p)
	}
	if s.followed(c.MachineID, c.SessionID) {
		_ = s.physicalDevices().Follow(ctx, a.MachineID, a.SessionID, true)
	}
	s.logicalEmit(c.MachineID, "session.items", map[string]any{"session_id": c.SessionID, "items": []proto.Item{}, "history": c.History, "from_seq": a.Offset, "to_seq": a.Offset})
	if s.devs != nil {
		if list, known := s.devs.Sessions(c.MachineID); known {
			s.setSessions(c.MachineID, list)
		}
	}
}
