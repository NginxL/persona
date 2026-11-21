package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

type logicalConversationStore interface {
	LogicalConversation(context.Context, string, string) (*db.LogicalConversation, error)
	LogicalConversations(context.Context) ([]db.LogicalConversation, error)
	AdoptConversationAliases(context.Context, string, string, string, string, string, []string) error
	WithConversationEpoch(context.Context, string, string, int64, func(*db.LogicalConversation) error) error
	WithDrainedConversationEpoch(context.Context, string, string, int64, func(*db.LogicalConversation) error) error
	BeginConversationSwitch(context.Context, string, string, db.ConversationSwitch) error
	PrepareConversationSegment(context.Context, string, string, string, conversationlog.Segment, int) error
	ActivateConversationSegment(context.Context, string, string, string, int64) error
	ResolveConversationSwitch(context.Context, string, string, string, string, string) error
	ConversationSwitch(context.Context, string, string, string) (*db.ConversationSwitch, error)
	ObserveConversationSource(context.Context, string, string, string) error
	ObserveConversationRevision(context.Context, string, string, int64) error
	ConversationSegmentClosed(context.Context, string) error
	EnqueueConversationInput(context.Context, string, string, string, int64, json.RawMessage) error
	ConversationInputReceipt(context.Context, string, string, string, json.RawMessage) (string, error)
	ClaimConversationInput(context.Context, string, string, int64) (*db.ConversationInput, error)
	ResolveConversationInput(context.Context, string, string, string, string) error
}

type logicalSwitchStore interface {
	ResolveConversationDelivery(context.Context, string, string, string, int64, string, string) error
	RequeueConversationInput(context.Context, string, string, string, int64, string) error
	RecordConversationPreparedCandidate(context.Context, db.ConversationPreparedCandidate) error
	ConversationPreparedCandidates(context.Context) ([]db.ConversationPreparedCandidate, error)
	ConversationPreparedCandidateDone(context.Context, string, string) error
	logicalConversationStore
	PutConversationHandoff(context.Context, string, string, string, json.RawMessage) error
	ConversationPreparation(context.Context, string, string, string) (json.RawMessage, bool, error)
	ClaimConversationPreparation(context.Context, string, string, string) (bool, error)
	ConversationUnresolvedInput(context.Context, string, string) (*db.ConversationInput, error)
	ConversationInputPending(context.Context, string, string) (bool, error)
	ConversationClosures(context.Context) ([][2]string, error)
	ConversationClosureDone(context.Context, string, string) error
}

type logicalModesStore interface {
	SetConversationSegmentModes(context.Context, string, string, int64, string, *string, *string, *string) error
}
type logicalSourceKey struct{}
type logicalHistoryOnlyKey struct{}

type logicalDeviceState struct {
	mu      sync.Mutex
	at      time.Time
	rows    []db.LogicalConversation
	index   map[[2]string]*db.LogicalConversation
	running map[[2]string]bool
	watches map[[2]string]json.RawMessage
}

// Reads may use a short-lived projection index. Authority and execution never do:
// logicalCall and logicalCaller always consult Postgres and its epoch locks.
func (s *Service) logicalIndex() ([]db.LogicalConversation, map[[2]string]*db.LogicalConversation, error) {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return nil, nil, nil
	}
	b := &s.logical
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.index != nil && time.Since(b.at) < time.Second {
		return b.rows, b.index, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := st.LogicalConversations(ctx)
	if err != nil {
		return nil, nil, err
	}
	index := map[[2]string]*db.LogicalConversation{}
	for i := range rows {
		c := &rows[i]
		index[[2]string{c.MachineID, c.SessionID}] = c
		for _, seg := range c.Segments {
			index[[2]string{seg.MachineID, seg.SessionID}] = c
		}
	}
	b.rows, b.index, b.at = rows, index, time.Now()
	return rows, index, nil
}

func (s *Service) logicalChanged() {
	s.logical.mu.Lock()
	s.logical.at = time.Time{}
	s.logical.mu.Unlock()
}

type logicalDevices struct {
	Devices
	s *Service
}

func (s *Service) physicalDevices() Devices {
	if d, ok := s.devs.(*logicalDevices); ok {
		return d.Devices
	}
	return s.devs
}

func (d *logicalDevices) Has(machine string) bool {
	if d.Devices.Has(machine) {
		return true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			return true
		}
	}
	return false
}

func (d *logicalDevices) Canonical(machine string) string {
	rows, _, err := d.s.logicalIndex()
	if err == nil {
		for _, c := range rows {
			if c.MachineID == machine {
				return machine
			}
		}
	}
	return d.Devices.Canonical(machine)
}

func (d *logicalDevices) List() []devices.Summary {
	out := d.Devices.List()
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for i := range out {
		seen[out[i].ID] = true
		if !out[i].Online {
			if _, ok := d.liveTarget(out[i].ID); ok {
				out[i].Online = true
			}
		}
	}
	for _, c := range rows {
		if seen[c.MachineID] {
			continue
		}
		if active, ok := c.Active(); ok {
			if m, ok := d.Devices.Summary(active.MachineID); ok {
				m.ID = c.MachineID
				out = append(out, m)
				seen[c.MachineID] = true
			}
		}
	}
	return out
}

func (d *logicalDevices) Summary(machine string) (devices.Summary, bool) {
	if m, ok := d.Devices.Summary(machine); ok {
		m.ID = d.Canonical(machine)
		if !m.Online {
			if _, live := d.liveTarget(machine); live {
				m.Online = true
			}
		}
		return m, true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return devices.Summary{}, false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if a, ok := c.Active(); ok {
				m, found := d.Devices.Summary(a.MachineID)
				m.ID = machine
				return m, found
			}
		}
	}
	return devices.Summary{}, false
}

func (d *logicalDevices) Shown(machine string) (devices.Shown, bool) {
	if target, ok := d.liveTarget(machine); ok {
		return d.Devices.Shown(target)
	}
	if shown, ok := d.Devices.Shown(machine); ok {
		return shown, true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return devices.Shown{}, false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if a, ok := c.Active(); ok {
				return d.Devices.Shown(a.MachineID)
			}
		}
	}
	return devices.Shown{}, false
}

func (d *logicalDevices) ConnectedAt(machine string) time.Time {
	if target, ok := d.liveTarget(machine); ok {
		return d.Devices.ConnectedAt(target)
	}
	if d.Devices.Has(machine) {
		return d.Devices.ConnectedAt(machine)
	}
	rows, _, err := d.s.logicalIndex()
	if err == nil {
		for _, c := range rows {
			if c.MachineID == machine {
				if a, ok := c.Active(); ok {
					return d.Devices.ConnectedAt(a.MachineID)
				}
			}
		}
	}
	return time.Time{}
}

func (d *logicalDevices) Session(machine, session string) (proto.Session, bool) {
	_, index, err := d.s.logicalIndex()
	if err != nil {
		return proto.Session{}, false
	}
	c, err := d.s.physicalLogicalIndex(index, machine, session)
	if err != nil {
		return proto.Session{}, false
	}
	if c != nil {
		if machine != c.MachineID || session != c.SessionID {
			return proto.Session{}, false
		}
		if a, ok := c.Active(); ok {
			row, found := d.Devices.Session(a.MachineID, a.SessionID)
			if !found {
				row = proto.Session{Status: proto.StatusOffline, Assistant: true}
			}
			row.MachineID, row.SessionID = c.MachineID, c.SessionID
			if c.Epoch > 1 {
				row.Rev = c.Revision
			}
			return row, true
		}
		return proto.Session{}, false
	}
	return d.Devices.Session(machine, session)
}

func (d *logicalDevices) Sessions(machine string) ([]proto.Session, bool) {
	rows, index, err := d.s.logicalIndex()
	if err != nil {
		return nil, false
	}
	physical, known := d.Devices.Sessions(machine)
	out := make([]proto.Session, 0, len(physical))
	for _, row := range physical {
		c, err := d.s.physicalLogicalIndex(index, machine, row.SessionID)
		if err == nil && c == nil {
			out = append(out, row)
		}
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if row, ok := d.Session(machine, c.SessionID); ok {
				out = append(out, row)
				known = true
			}
		}
	}
	return out, known
}

func (d *logicalDevices) AppendSessions(dst []proto.Session, machine string) ([]proto.Session, bool) {
	list, ok := d.Sessions(machine)
	return append(dst, list...), ok
}

func (d *logicalDevices) SessionsWhere(machine string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	list, ok := d.Sessions(machine)
	out := list[:0]
	for i := range list {
		if keep(&list[i]) {
			out = append(out, list[i])
		}
	}
	return out, ok
}

func (d *logicalDevices) Watched(machine, session string) bool {
	d.s.logical.mu.Lock()
	_, ok := d.s.logical.watches[[2]string{machine, session}]
	d.s.logical.mu.Unlock()
	return ok || d.Devices.Watched(machine, session)
}

func (d *logicalDevices) CallNode(ctx context.Context, machine, method string, raw json.RawMessage) (json.RawMessage, error) {
	if ctx.Value(logicalSourceKey{}) == nil {
		if out, handled, err := d.s.logicalCall(ctx, machine, method, raw); handled {
			return out, err
		}
	}
	return d.Devices.CallNode(ctx, machine, method, raw)
}

func (d *logicalDevices) Request(ctx context.Context, method string, raw json.RawMessage) (any, bool, error) {
	if method == devicewire.MethodCall && ctx.Value(logicalSourceKey{}) == nil {
		var p devicewire.NodeCall
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, true, err
		}
		if out, handled, err := d.s.logicalCall(context.WithValue(ctx, logicalTransportKey{}, p), p.MachineID, p.Method, p.Params); handled {
			return out, true, err
		}
	}
	return d.Devices.Request(ctx, method, raw)
}

func (d *logicalDevices) Follow(ctx context.Context, machine, session string, on bool) error {
	st, ok := d.s.store().(logicalConversationStore)
	if !ok {
		return d.Devices.Follow(ctx, machine, session, on)
	}
	c, err := d.s.physicalLogicalConversation(ctx, st, machine, session)
	if err != nil {
		return err
	}
	if c == nil {
		return d.Devices.Follow(ctx, machine, session, on)
	}
	if c.Deleted {
		return db.ErrConversationDeleted
	}
	if c.MachineID != machine || c.SessionID != session {
		return logicalRefusal(ctx, "private_segment")
	}
	a, ok := c.Active()
	if !ok {
		return errors.New("conversation has no active segment")
	}
	return d.Devices.Follow(ctx, a.MachineID, a.SessionID, on)
}

// The manager retains the root bus destination even if its original computer
// disappeared. It advertises no physical session mapping to the transport hub.
func (s *Service) logicalProjectStates(st devicewire.States) devicewire.States {
	rows, _, err := s.logicalIndex()
	if err != nil {
		return st
	}
	index := map[string]int{}
	physicalStates := map[string]devicewire.State{}
	for i, state := range st.Machines {
		index[state.Machine.ID] = i
		physicalStates[state.Machine.ID] = state
	}
	// Work on copies: the devices' snapshots may be concurrently read elsewhere.
	st.Machines = append([]devicewire.State(nil), st.Machines...)
	for _, c := range rows {
		a, ok := c.Active()
		if !ok {
			continue
		}
		target, known := physicalStates[a.MachineID]
		if !known {
			if physical := s.physicalDevices(); physical != nil {
				target, known = physicalStates[physical.Canonical(a.MachineID)]
			}
		}
		if !known {
			continue
		}
		if source, exists := index[c.MachineID]; exists {
			if !st.Machines[source].Connected && target.Connected {
				st.Machines[source].Connected = true
				st.Machines[source].ConnectedAt = target.ConnectedAt
				st.Machines[source].Machine.Online = true
			}
			continue
		}
		state := target
		state.Machine.ID = c.MachineID
		state.TransportFor = target.Machine.ID
		state.Config = nil
		state.Derived = true
		st.Machines = append(st.Machines, state)
		index[c.MachineID] = len(st.Machines) - 1
	}
	return st
}

// A retained but disconnected source must not make the public conversation look
// unreachable while its active segment has a live transport on another machine.
// Physical resource/accounting fields remain the source machine's own fields.
func (d *logicalDevices) liveTarget(machine string) (string, bool) {
	if source, ok := d.Devices.Summary(machine); ok && source.Online {
		return "", false
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return "", false
	}
	for _, c := range rows {
		if c.MachineID != machine {
			continue
		}
		if a, ok := c.Active(); ok && a.MachineID != machine {
			if target, found := d.Devices.Summary(a.MachineID); found && target.Online {
				return a.MachineID, true
			}
		}
	}
	return "", false
}
