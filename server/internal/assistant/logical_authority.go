package assistant

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type logicalAuthenticatedKey struct{}
type logicalAuthenticated struct {
	Machine, Session, PhysicalMachine, PhysicalSession string
	Epoch                                              int64
	Lease                                              *atomic.Bool
}

func (s *Service) Tool(ctx context.Context, call ToolCall) proto.ToolResult {
	ctx = asToolCall(ctx) // a model waits on it: its node calls are bounded (tool_node_wait.go)
	if caller, ok := ctx.Value(logicalAuthenticatedKey{}).(logicalAuthenticated); ok && caller.Machine == call.MachineID && caller.Session == call.SessionID {
		if caller.Lease != nil && caller.Lease.Load() {
			return s.tool(ctx, call)
		}
		call.MachineID, call.SessionID = caller.PhysicalMachine, caller.PhysicalSession
	}
	machine, session, c, err := s.logicalCaller(ctx, call.MachineID, call.SessionID)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	if c == nil {
		return s.tool(ctx, call)
	}
	st := s.store().(logicalConversationStore)
	var result proto.ToolResult
	err = st.WithConversationEpoch(ctx, machine, session, c.Epoch, func(current *db.LogicalConversation) error {
		active, ok := current.Active()
		if !ok || !s.physicalSameMachine(active.MachineID, call.MachineID) || active.SessionID != call.SessionID {
			return logicalRefusal(ctx, "retired")
		}
		lease := &atomic.Bool{}
		lease.Store(true)
		defer lease.Store(false)
		verified := logicalAuthenticated{machine, session, call.MachineID, call.SessionID, current.Epoch, lease}
		call.MachineID, call.SessionID = machine, session
		result = s.tool(context.WithValue(ctx, logicalAuthenticatedKey{}, verified), call)
		return nil
	})
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return result
}

type logicalNodeHandler func(context.Context, string, string, json.RawMessage) (any, bool, error)

type logicalInteractionStore interface {
	ReserveConversationInteraction(context.Context, proto.Interaction, string, string, string, string, int64) error
	ConversationInteraction(context.Context, string) (*db.ConversationInteractionRoute, error)
	FinishConversationInteraction(context.Context, string) error
}

func (s *Service) logicalNodeRequest(ctx context.Context, machine, method string, raw json.RawMessage, next logicalNodeHandler) (any, bool, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return next(ctx, machine, method, raw)
	}
	var session string
	_ = json.Unmarshal(envelope["session_id"], &session)
	var ix proto.Interaction
	if method == "interaction.create" {
		_ = json.Unmarshal(envelope["interaction"], &ix)
		session = ix.SessionID
	}
	if session == "" {
		return next(ctx, machine, method, raw)
	}
	rootMachine, rootSession, c, err := s.logicalCaller(ctx, machine, session)
	if err != nil {
		return nil, true, err
	}
	if c == nil {
		return next(ctx, machine, method, raw)
	}
	var out any
	var handled bool
	waits := method == "interaction.create" || method == "mcp.tools"
	var invoke func() error
	st := s.store().(logicalConversationStore)
	err = st.WithConversationEpoch(ctx, rootMachine, rootSession, c.Epoch, func(current *db.LogicalConversation) error {
		active, ok := current.Active()
		if !ok || !s.physicalSameMachine(active.MachineID, machine) || active.SessionID != session {
			return logicalRefusal(ctx, "retired")
		}
		lease := &atomic.Bool{}
		lease.Store(true)
		defer lease.Store(false)
		verified := logicalAuthenticated{rootMachine, rootSession, machine, session, current.Epoch, lease}
		if method == "interaction.create" {
			if reservedCard(ix) {
				return errReservedCard
			}
			if current.Switch != nil && current.Switch.Phase != "active" {
				return logicalRefusal(ctx, "busy")
			}
			// Keep the authenticated physical destination apart from public identity.
			if routes, ok := s.store().(logicalInteractionStore); ok {
				if err := routes.ReserveConversationInteraction(ctx, ix, rootMachine, rootSession, machine, session, current.Epoch); err != nil {
					return err
				}
			}
			ix.MachineID, ix.SessionID = rootMachine, rootSession
			envelope["interaction"], _ = json.Marshal(ix)
		} else {
			// Verify permission against the authenticated physical call before
			// translating its session identity for logical tool dispatch.
			if method == "mcp.call" {
				var permission proto.SourcePermission
				_ = json.Unmarshal(envelope["source_permission"], &permission)
				var name string
				_ = json.Unmarshal(envelope["name"], &name)
				if permission.Matches(session, name, envelope["args"]) {
					permission.Session = rootSession
					envelope["source_permission"], _ = json.Marshal(permission)
				} else {
					// A proof already naming the public root was never bound to
					// this physical caller; it must not become valid after remap.
					delete(envelope, "source_permission")
				}
			}
			envelope["session_id"], _ = json.Marshal(rootSession)
		}
		projected, e := json.Marshal(envelope)
		if e != nil {
			return e
		}
		invoke = func() error {
			var err error
			out, handled, err = next(context.WithValue(ctx, logicalAuthenticatedKey{}, verified), rootMachine, method, projected)
			return err
		}
		if waits {
			return nil
		}
		return invoke()
	})
	if err == nil && waits && invoke != nil {
		err = invoke()
		if err == nil && method == "interaction.create" {
			// Automatic grants need the same final authority check as UI answers.
			// A switch may have started while the request prepared its card.
			err = st.WithConversationEpoch(ctx, rootMachine, rootSession, c.Epoch, func(current *db.LogicalConversation) error {
				active, ok := current.Active()
				if !ok || !s.physicalSameMachine(active.MachineID, machine) || active.SessionID != session {
					return logicalRefusal(ctx, "retired")
				}
				if current.Switch != nil && current.Switch.Phase != "active" {
					return logicalRefusal(ctx, "busy")
				}
				return nil
			})
			if err != nil {
				out = nil
			}
		}
	}
	if method == "interaction.create" {
		if routes, ok := s.store().(logicalInteractionStore); ok {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = routes.FinishConversationInteraction(cleanup, ix.ID)
		}
	}
	return out, handled || err != nil, err
}

func (s *Service) logicalInteractionAnswer(ctx context.Context, id string, r proto.Resolution, next func() error) error {
	routes, ok := s.store().(logicalInteractionStore)
	if !ok {
		return next()
	}
	lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	route, err := routes.ConversationInteraction(lookup, id)
	cancel()
	if err != nil {
		return err
	}
	if route == nil {
		return next()
	}
	st := s.store().(logicalConversationStore)
	return st.WithConversationEpoch(ctx, route.MachineID, route.SessionID, route.Epoch, func(c *db.LogicalConversation) error {
		a, ok := c.Active()
		if !ok || !s.physicalSameMachine(a.MachineID, route.PhysicalMachine) || a.SessionID != route.PhysicalSession {
			return logicalRefusal(ctx, "retired")
		}
		if c.Switch != nil && c.Switch.Phase != "active" {
			return logicalRefusal(ctx, "busy")
		}
		return next()
	})
}

func (s *Service) NodeAnswer(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	return s.logicalNodeRequest(ctx, machine, method, params, s.nodeAnswer)
}

func (s *Service) NodeRequest(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	// A direction request's session_id names the outside coding Agent it reports for (#8236), never the calling
	// conversation, so it is not remapped or refused as a logical caller.
	if method == "direction.create" || method == "direction.progress" {
		return s.nodeRequest(ctx, machine, method, params)
	}
	return s.logicalNodeRequest(ctx, machine, method, params, s.nodeRequest)
}

// Publication occurs after the ordinary card pipeline has validated and cleaned
// node input, expiry, Always policy and privacy. A route-only reservation is not
// an approval: if a switch wins before publication, no actionable card appears.
func (s *Service) logicalPublishInteraction(ctx context.Context, ix proto.Interaction) (bool, error) {
	caller, authenticated := ctx.Value(logicalAuthenticatedKey{}).(logicalAuthenticated)
	if !authenticated || caller.Machine != ix.MachineID || caller.Session != ix.SessionID {
		// Ordinary and manager-owned cards keep their existing lifetime,
		// including owner proposals whose caller has already disconnected.
		return false, nil
	}
	routes, ok := s.store().(logicalInteractionStore)
	if !ok {
		return false, nil
	}
	route, err := routes.ConversationInteraction(ctx, ix.ID)
	if err != nil || route == nil {
		return false, err
	}
	if !s.physicalSameMachine(caller.PhysicalMachine, route.PhysicalMachine) || caller.PhysicalSession != route.PhysicalSession || caller.Epoch != route.Epoch {
		return false, logicalRefusal(ctx, "retired")
	}
	st := s.store().(logicalConversationStore)
	cards, ok := s.store().(interface {
		InteractionOutcome(context.Context, string) (string, *proto.Resolution, error)
		InsertInteraction(context.Context, proto.Interaction) error
	})
	if !ok {
		return false, logicalRefusal(ctx, "unavailable")
	}
	fresh := false
	err = st.WithConversationEpoch(ctx, route.MachineID, route.SessionID, route.Epoch, func(c *db.LogicalConversation) error {
		a, ok := c.Active()
		if !ok || !s.physicalSameMachine(a.MachineID, route.PhysicalMachine) || a.SessionID != route.PhysicalSession || ix.MachineID != route.MachineID || ix.SessionID != route.SessionID {
			return logicalRefusal(ctx, "retired")
		}
		if c.Switch != nil && c.Switch.Phase != "active" {
			return logicalRefusal(ctx, "busy")
		}
		status, _, err := cards.InteractionOutcome(ctx, ix.ID)
		if err != nil {
			return err
		}
		fresh = status == ""
		return cards.InsertInteraction(ctx, ix)
	})
	return fresh, err
}
