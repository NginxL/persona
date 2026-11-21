package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A physical fleet whose former entry was absorbed into the connected computer.
// No model, CLI, or network listener is involved.
type logicalAbsorbedDevices struct{ *aliasDevs }

func (d *logicalAbsorbedDevices) PhysicalAliases(id string) []string {
	out := []string{id, d.Canonical(id)}
	for from, to := range d.alias {
		if to == d.Canonical(id) {
			out = append(out, from)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
func (d *logicalAbsorbedDevices) Has(id string) bool { return d.fleetDevs.Has(d.Canonical(id)) }
func (d *logicalAbsorbedDevices) Session(machine, session string) (proto.Session, bool) {
	return d.fleetDevs.Session(d.Canonical(machine), session)
}
func (d *logicalAbsorbedDevices) Sessions(machine string) ([]proto.Session, bool) {
	return d.fleetDevs.Sessions(d.Canonical(machine))
}
func (d *logicalAbsorbedDevices) CallNode(ctx context.Context, machine, method string, raw json.RawMessage) (json.RawMessage, error) {
	return d.fleetDevs.CallNode(ctx, d.Canonical(machine), method, raw)
}

func absorbedPair(t *testing.T) (*Service, *fakeHub, *db.DB, *logicalAbsorbedDevices) {
	t.Helper()
	s, f, st, _ := logicalPair(t, "codex")
	activatePair(t, st)
	raw := &logicalAbsorbedDevices{&aliasDevs{fleetDevs: newFleetDevs(), alias: map[string]string{"server": "connected"}}}
	raw.add("connected", "Computer")
	raw.setSessions("connected", []proto.Session{{SessionID: "side-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: "claude", Rev: 100}, {SessionID: "target-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: "codex", Rev: 100}})
	s.devs = &logicalDevices{Devices: raw, s: s}
	s.logicalChanged() // direct test DB activation precedes the installed physical fleet
	return s, f, st, raw
}

func TestLogicalAbsorptionPreservesAuthorityAndLiveProjection(t *testing.T) {
	s, f, st, _ := absorbedPair(t)
	ctx := t.Context()
	machine, session, _, err := s.logicalCaller(ctx, "connected", "target-chat")
	if err != nil || machine != "server" || session != "side-chat" {
		t.Fatalf("active alias: %s/%s %v", machine, session, err)
	}
	if _, _, _, err = s.logicalCaller(ctx, "connected", "side-chat"); err == nil {
		t.Fatal("retired source escaped fencing through its new machine ID")
	}
	if _, handled, err := s.logicalCall(ctx, "connected", DeviceSessionItems, json.RawMessage(`{"session_id":"target-chat"}`)); !handled || err == nil {
		t.Fatal("physical alias exposed private segment")
	}
	if list, ok := s.devs.Sessions("connected"); !ok || len(list) != 0 {
		t.Fatalf("physical list leaked segments: %+v %v", list, ok)
	}
	if list, ok := s.devs.Sessions("server"); !ok || len(list) != 1 || list[0].SessionID != "side-chat" {
		t.Fatalf("stable public list: %+v %v", list, ok)
	}
	summary, ok := s.devs.Summary("server")
	if !ok || summary.ID != "server" {
		t.Fatalf("public summary identity: %+v %v", summary, ok)
	}
	s.NodeEvent("connected", "session.items", json.RawMessage(`{"session_id":"target-chat","history":"h2","from_seq":0,"to_seq":1,"items":[{"id":"line1#0","seq":1,"role":"assistant","kind":"text","text":"live"}]}`))
	f.mu.Lock()
	projected := false
	for _, told := range f.told {
		if told.event != devicewire.EventNode {
			continue
		}
		var event devicewire.NodeEvent
		_ = json.Unmarshal(told.params, &event)
		projected = projected || event.MachineID == "server" && strings.Contains(string(event.Params), "segment:two:line1#0")
	}
	f.mu.Unlock()
	if !projected {
		t.Fatal("absorbed callback did not reach the public stream")
	}
	// Active approval routes recorded under the former physical identity remain
	// answerable, while source approvals cannot settle today's target turn.
	if err := st.PutConversationInteraction(ctx, "active", "server", "side-chat", "server", "target-chat", 2); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.logicalInteractionAnswer(ctx, "active", proto.Resolution{Dismiss: true}, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("active card: %v %v", called, err)
	}
	states := s.logicalProjectStates(devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "connected"}, Connected: true}}, Absorbed: map[string]string{"server": "connected"}})
	if len(states.Machines) != 2 || states.Machines[1].Machine.ID != "server" || states.Machines[1].TransportFor != "connected" || states.Absorbed["server"] != "connected" {
		t.Fatalf("retained root transport: %+v", states)
	}
}

func TestLogicalAbsorptionKeepsDurableHistorySourceKeys(t *testing.T) {
	s, hub, st, raw := absorbedPair(t)
	rt := &historyRuntime{history: "h2", items: []proto.Item{{ID: "line1#0", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "persisted after reconnect"}}}
	raw.machines["connected"].call = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case DeviceSessionItems:
			var p historyParams
			_ = json.Unmarshal(params, &p)
			if p.SessionID != "target-chat" {
				return nil, errors.New("retired fake source is offline")
			}
			return rt.page("connected", params)
		case DeviceAssistantHandoff:
			return map[string]any{"summary": "source summary"}, nil
		}
		return map[string]any{}, nil
	}
	// Production BusHub.DeviceCall uses the configured physical devices. This
	// fixture's Hub is separate; wire it to the same fake physical transport.
	for _, method := range []string{DeviceSessionItems, DeviceAssistantHandoff} {
		hub.answerNode(method, func(machine string, params json.RawMessage) (any, error) {
			return raw.CallNode(t.Context(), machine, method, params)
		})
	}
	s.NodeEvent("connected", "session.items", json.RawMessage(`{"session_id":"target-chat","items":[]}`))
	if !s.historyDirty(searchKey{"server", "target-chat"}) {
		t.Fatal("source-key history did not receive the synchronization hint")
	}
	s.historySweep(t.Context(), st)
	h, err := st.ConversationHistory(t.Context(), "server", "target-chat")
	if err != nil || h == nil || h.History != "h2" {
		t.Fatalf("lost original source coverage: %+v %v", h, err)
	}
	if h, err := st.ConversationHistory(t.Context(), "connected", "target-chat"); err != nil || h != nil {
		t.Fatalf("duplicated durable history under absorbed identity: %+v %v", h, err)
	}
	if !s.sameMachine("server", "connected") {
		t.Fatal("logical transport root overrode physical provider scope equivalence")
	}
}

func TestLogicalAbsorptionFailsClosedOnConflictingRootsAndDeletion(t *testing.T) {
	s, _, st, _ := absorbedPair(t)
	ctx := t.Context()
	if err := st.AdoptConversation(ctx, "connected", "target-chat", "conflicting-root", "codex", "h3"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.logicalCaller(ctx, "connected", "target-chat"); !errors.Is(err, db.ErrConversationEpoch) {
		t.Fatalf("conflicting roots granted authority: %v", err)
	}
	s.logicalChanged()
	if _, ok := s.devs.Session("connected", "target-chat"); ok {
		t.Fatal("conflicting physical session became public")
	}
	for _, row := range []deletedConversation{{Machine: "server", ID: "deleted-chat"}, {Machine: "server", ID: "hidden-segment", Hidden: true}} {
		raw, _ := json.Marshal(row)
		if err := st.PutConversationDeletion(ctx, row.Machine, row.ID, raw); err != nil {
			t.Fatal(err)
		}
		if err := s.conversationAvailable(ctx, "connected", row.ID); err == nil {
			t.Fatal("absorbed callback resurrected deleted conversation")
		}
	}
	rows := s.deletionRows(ctx, "connected", []proto.Session{{SessionID: "deleted-chat", Assistant: true}, {SessionID: "hidden-segment", Assistant: true}}, true)
	if len(rows) != 1 || !rows[0].IsDeleted() || rows[0].MachineID != "connected" {
		t.Fatalf("deletion projection after absorption: %+v", rows)
	}
}

func TestLogicalTransportProjectionDoesNotInventPhysicalReachability(t *testing.T) {
	s, _, st, _ := historyFixture(t)
	ctx := t.Context()
	for _, p := range []struct{ root, source, target, session string }{{"a-public", "a-source", "connected", "first-target"}, {"z-public", "z-source", "a-public", "second-target"}} {
		if err := st.AdoptConversation(ctx, p.root, p.source, p.source, "claude", "history"); err != nil {
			t.Fatal(err)
		}
		if err := st.BeginConversationSwitch(ctx, p.root, p.source, db.ConversationSwitch{OperationID: "switch", ExpectedEpoch: 1, SourceSegment: p.source, Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := st.PrepareConversationSegment(ctx, p.root, p.source, "switch", conversationlog.Segment{ID: p.session, MachineID: p.target, SessionID: p.session, Engine: "codex"}, 1); err != nil {
			t.Fatal(err)
		}
		if err := st.ActivateConversationSegment(ctx, p.root, p.source, "switch", 1); err != nil {
			t.Fatal(err)
		}
	}
	s.logicalChanged()
	states := s.logicalProjectStates(devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "connected"}, Connected: true}, {Machine: proto.Machine{ID: "a-public"}, Connected: false}}})
	for _, state := range states.Machines {
		if state.Machine.ID == "a-public" && !state.Connected {
			t.Fatal("retained public root did not reach its own connected target")
		}
		if state.Machine.ID == "z-public" && state.Connected {
			t.Fatal("another logical root's projected connection impersonated an offline physical target")
		}
	}
}
