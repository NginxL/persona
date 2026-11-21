package hub

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

type segmentTestDevices struct {
	*mirrorDevices
	rows map[string][]proto.Session
}

func (d *segmentTestDevices) Has(machine string) bool { _, ok := d.rows[machine]; return ok }
func (d *segmentTestDevices) Sessions(machine string) ([]proto.Session, bool) {
	rows, ok := d.rows[machine]
	return slices.Clone(rows), ok
}
func (d *segmentTestDevices) AppendSessions(dst []proto.Session, machine string) ([]proto.Session, bool) {
	rows, ok := d.Sessions(machine)
	return append(dst, rows...), ok
}
func (d *segmentTestDevices) SessionsWhere(machine string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	rows, ok := d.Sessions(machine)
	out := rows[:0]
	for i := range rows {
		if keep(&rows[i]) {
			out = append(out, rows[i])
		}
	}
	return out, ok
}
func (d *segmentTestDevices) Session(machine, id string) (proto.Session, bool) {
	for _, row := range d.rows[machine] {
		if row.SessionID == id {
			return row, true
		}
	}
	return proto.Session{}, false
}
func (d *segmentTestDevices) Request(ctx context.Context, method string, raw json.RawMessage) (any, bool, error) {
	if method != devicewire.MethodCall {
		return d.mirrorDevices.Request(ctx, method, raw)
	}
	var p devicewire.NodeCall
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, true, err
	}
	// This is the raw physical transport. The embedded mirror routes through
	// the hub and would recursively re-enter the logical manager in this test.
	out, err := d.CallNode(ctx, p.MachineID, p.Method, p.Params)
	return out, true, err
}
func (d *segmentTestDevices) CallNode(_ context.Context, machine, method string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Session string `json:"session_id"`
		After   *int   `json:"after_seq"`
	}
	_ = json.Unmarshal(raw, &p)
	if method == "session.items" {
		from := 0
		if p.After != nil {
			from = *p.After
		}
		history, text := "source-history", "source"
		if machine == "target" {
			history, text = "target-history", "target"
		}
		items := []proto.Item{}
		if from < 1 {
			items = append(items, proto.Item{ID: "line1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: text})
		}
		return json.Marshal(map[string]any{"items": items, "history": history, "from_seq": from, "to_seq": 1, "has_more": false})
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestLogicalSegmentWebSocketSubscriptionLiveAndReconnect(t *testing.T) {
	h, srv, _ := newBusHub(t)
	database := managerDB(t, h)
	ctx := t.Context()
	h.cfg.Machines = []MachineConfig{{ID: "root", Name: "Root"}, {ID: "target", Name: "Target"}}
	for _, cfg := range h.cfg.Machines {
		m := newManagedMachineForSegmentTest(h, cfg)
		registerMachine(h, m)
	}
	raw := &segmentTestDevices{mirrorDevices: newMirrorDevices(h), rows: map[string][]proto.Session{
		"root":   {{SessionID: "chat", Cwd: "/home/example/.persona/assistant", Assistant: true, Agent: "claude", Status: proto.StatusIdle, Rev: 1}},
		"target": {{SessionID: "physical-target", Cwd: "/home/example/.persona/assistant", Assistant: true, Agent: "claude", Status: proto.StatusIdle, Rev: 1}},
	}}
	if err := database.AdoptConversation(ctx, "root", "chat", "first", "claude", "source-history"); err != nil {
		t.Fatal(err)
	}
	if err := database.BeginConversationSwitch(ctx, "root", "chat", db.ConversationSwitch{OperationID: "forced-same-engine", ExpectedEpoch: 1, SourceSegment: "first", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := database.PrepareConversationSegment(ctx, "root", "chat", "forced-same-engine", conversationlog.Segment{ID: "second", MachineID: "target", SessionID: "physical-target", Engine: "claude", SourceHistory: "target-history"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.ActivateConversationSegment(ctx, "root", "chat", "forced-same-engine", 1); err != nil {
		t.Fatal(err)
	}
	mgr := dialAssistantWithStore(t, h, srv.URL, false, database, func(o *assistant.BusOptions) { o.Devices = raw; o.Logf = func(string, ...any) {} })
	// A real Devices.BusUp reports this snapshot before calls are released. This
	// raw fake has no connection lifecycle, so explicitly complete that handshake.
	h.applyDeviceStatesFrom(devicesOwner, devicewire.States{Machines: []devicewire.State{
		{Machine: proto.Machine{ID: "root", Online: true}, Info: proto.NodeInfo{Home: "/home/example"}, Connected: true},
		{Machine: proto.Machine{ID: "target", Online: true}, Info: proto.NodeInfo{Home: "/home/example"}, Connected: true},
	}})
	dial := func() *websocket.Conn {
		conn, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/ws?token="+h.cfg.Token, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}
	read := func(stage string, conn *websocket.Conn, predicate func(map[string]any) bool) map[string]any {
		t.Helper()
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(wait)
			if err != nil {
				t.Fatalf("%s: %v", stage, err)
			}
			var event map[string]any
			if json.Unmarshal(data, &event) == nil {
				if event["type"] == "error" {
					t.Fatalf("%s: hub refused subscription: %s", stage, data)
				}
				if predicate(event) {
					return event
				}
			}
		}
	}
	conn := dial()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","machine_id":"root","session_id":"chat","since_seq":1,"history":"source-history"}`)); err != nil {
		t.Fatal(err)
	}
	page := read("initial backlog", conn, func(p map[string]any) bool {
		return p["type"] == "items" && strings.Contains(stringifySegmentEvent(p), "segment:second:line1#0")
	})
	if page["machine_id"] != "root" || page["session_id"] != "chat" || page["from_seq"] != float64(1) || page["to_seq"] != float64(2) {
		t.Fatalf("logical backlog: %+v", page)
	}
	mgr.svc.NodeEvent("target", "session.items", json.RawMessage(`{"session_id":"physical-target","history":"target-history","from_seq":1,"to_seq":2,"items":[{"id":"line2#0","seq":2,"kind":"text","role":"assistant","text":"live-after-switch"}]}`))
	live := read("live target update", conn, func(p map[string]any) bool {
		return p["type"] == "items" && strings.Contains(stringifySegmentEvent(p), "live-after-switch")
	})
	if live["machine_id"] != "root" || live["session_id"] != "chat" || live["from_seq"] != float64(2) || live["to_seq"] != float64(3) {
		t.Fatalf("logical live event: %+v", live)
	}
	// A new socket asks through the same public identity after the first segment.
	reconnect := dial()
	if err := reconnect.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","machine_id":"root","session_id":"chat","since_seq":1,"history":"source-history"}`)); err != nil {
		t.Fatal(err)
	}
	read("reconnect backlog", reconnect, func(p map[string]any) bool {
		return p["type"] == "items" && strings.Contains(stringifySegmentEvent(p), "segment:second:line1#0")
	})
}
func newManagedMachineForSegmentTest(h *Hub, cfg MachineConfig) *Machine {
	m := newMachine(h, cfg)
	m.managed = true
	m.owner = devicesOwner
	m.peer = &busNode{h: h, id: cfg.ID, done: make(chan struct{})}
	m.online = true
	m.info = proto.NodeInfo{Home: "/home/example"}
	return m
}
func stringifySegmentEvent(value any) string { data, _ := json.Marshal(value); return string(data) }

func TestLogicalTransportAliasesReleaseOwnershipWithTheSnapshot(t *testing.T) {
	h := newPerManagerHub(t, false)
	states := []devicewire.State{
		{Machine: proto.Machine{ID: "mb"}, Connected: true},
		{Machine: proto.Machine{ID: "retained-root"}, TransportFor: "mb", Derived: true, Connected: true},
		{Machine: proto.Machine{ID: "ma"}, TransportFor: "mb", Derived: true, Connected: true},
	}
	h.applyDeviceStatesFrom(perManagerOther, devicewire.States{Machines: states})
	if h.machineOwner("retained-root") != perManagerOther || h.localMachine("retained-root") == nil {
		t.Fatal("logical root lost its owning transport")
	}
	if h.machineOwner("ma") != devicesOwner {
		t.Fatal("alias took another manager's configured machine")
	}
	h.applyDeviceStatesFrom(perManagerOther, devicewire.States{Machines: states[:1]})
	h.devices.mu.Lock()
	retained := h.devices.transportOwners["retained-root"]
	h.devices.mu.Unlock()
	if retained != "" || h.localMachine("retained-root") != nil {
		t.Fatal("deleted logical root retained transport ownership")
	}
}
