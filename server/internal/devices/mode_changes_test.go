package devices

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// modeChangesStore keeps session_mode_changes in memory.
type modeChangesStore struct {
	*memStore
	mu   sync.Mutex
	rows []db.SessionModeChange
}

func (s *modeChangesStore) AddSessionModeChange(_ context.Context, c db.SessionModeChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, c)
	return nil
}

func (s *modeChangesStore) SessionModeChanges(_ context.Context, machine, session string, limit int) ([]db.SessionModeChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.SessionModeChange
	for _, c := range s.rows {
		if c.MachineID == machine && c.SessionID == session {
			out = append(out, c)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *modeChangesStore) recorded() []db.SessionModeChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.SessionModeChange(nil), s.rows...)
}

// modesMachine is machine "mac" with its session s1 in acceptEdits, whose node answers session.modes with answer.
func modesMachine(t *testing.T, d *Devices, answer func() (any, error)) *Machine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	managerSide, nodeSide := rpc.New("m", h2nW), rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "session.modes" {
			return map[string]any{}, nil
		}
		return answer()
	}
	go func() { _ = managerSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(d, MachineConfig{ID: "mac", Name: "mac"})
	m.peer, m.online = managerSide, true
	m.info = proto.NodeInfo{OS: "darwin", Version: "test", Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}
	d.addEntry(m)
	m.setSessions([]proto.Session{{SessionID: "s1", MachineID: "mac", PermissionMode: "acceptEdits"}})
	return m
}

func modesCall(d *Devices, params string, actor *devicewire.Actor) error {
	_, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "mac", Method: "session.modes", Params: json.RawMessage(params), Actor: actor})
	return err
}

// A mode the hub's /modes route asks for is recorded once the node took it: who asked on which device, from the mode
// the session ran in, to the one picked, and how it took effect. Calls with no actor (not from /modes), effort-only
// changes and refused ones are not (#10297).
func TestModeChangesAreRecorded(t *testing.T) {
	d := newTestDevices(t)
	st := &modeChangesStore{memStore: newMemStore()}
	d.opts.Store = st
	var mu sync.Mutex
	answer := map[string]any{"ok": true, "applied": "restart"}
	var refuse error
	modesMachine(t, d, func() (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return answer, refuse
	})
	phone := &devicewire.Actor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}
	if err := modesCall(d, `{"session_id":"s1","permission_mode":"bypassPermissions"}`, phone); err != nil {
		t.Fatal(err)
	}
	rows := st.recorded()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	want := db.SessionModeChange{MachineID: "mac", SessionID: "s1", Kind: db.ModeChangeSet, From: "acceptEdits", To: "bypassPermissions",
		Applied: db.ModeAppliedRestart, ActorKind: "device", DeviceID: "d1", DeviceName: "Phone"}
	if rows[0] != want {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}

	_ = modesCall(d, `{"session_id":"s1","permission_mode":"plan"}`, nil) // not through /modes
	_ = modesCall(d, `{"session_id":"s1","effort":"high"}`, phone)        // no mode
	mu.Lock()
	refuse = &rpc.Error{Message: "no"}
	mu.Unlock()
	_ = modesCall(d, `{"session_id":"s1","permission_mode":"plan"}`, phone) // refused
	if got := st.recorded(); len(got) != 1 {
		t.Fatalf("recorded more: %+v", got[1:])
	}

	mu.Lock()
	refuse, answer = nil, map[string]any{"ok": true} // an older node: says neither
	mu.Unlock()
	if err := modesCall(d, `{"session_id":"s1","permission_mode":"plan"}`, &devicewire.Actor{Kind: "hub"}); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 2 || got[1].Applied != "" || got[1].ActorKind != "hub" || got[1].To != "plan" {
		t.Errorf("older node's row = %+v", got[len(got)-1])
	}
}

// A pick of the mode the session runs in already changed nothing: no set row, from a node that says how it took effect
// or an older one. When it called off a restart that waited, the node's event is the one row, with its code (#11089).
func TestPickOfTheModeItRunsInIsNotAChange(t *testing.T) {
	d := newTestDevices(t)
	st := &modeChangesStore{memStore: newMemStore()}
	d.opts.Store = st
	var mu sync.Mutex
	answer := map[string]any{"ok": true, "applied": "now"}
	m := modesMachine(t, d, func() (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return answer, nil
	})
	phone := &devicewire.Actor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}
	if err := modesCall(d, `{"session_id":"s1","permission_mode":"acceptEdits"}`, phone); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	answer = map[string]any{"ok": true} // an older node
	mu.Unlock()
	if err := modesCall(d, `{"session_id":"s1","permission_mode":"acceptEdits"}`, phone); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 0 {
		t.Fatalf("a pick of the mode it runs in was recorded: %+v", got)
	}
	b, _ := json.Marshal(proto.ModeRestartEvent{SessionID: "s1", Mode: "bypassPermissions", Outcome: proto.ModeRestartDropped,
		Reason: proto.ModeRestartReasonCancelled})
	m.onEvent(proto.EventModeRestart, b)
	if got := st.recorded(); len(got) != 1 || got[0].Kind != db.ModeChangeRestartDropped || got[0].Reason != proto.ModeRestartReasonCancelled {
		t.Errorf("rows = %+v", got)
	}
}

// What became of a queued restart is kept with the session's changes and goes no further than the manager.
func TestModeRestartOutcomeIsRecordedAndKept(t *testing.T) {
	d := newTestDevices(t)
	st := &modeChangesStore{memStore: newMemStore()}
	d.opts.Store = st
	m := modesMachine(t, d, func() (any, error) { return map[string]any{"ok": true}, nil })
	host := d.opts.Hub.(*fakeHost)
	host.mu.Lock()
	before := len(host.events)
	host.mu.Unlock()
	b, _ := json.Marshal(proto.ModeRestartEvent{SessionID: "s1", Mode: "bypassPermissions", Outcome: proto.ModeRestartDropped,
		Reason: "claude 4101, the process it was picked for, has exited"})
	m.onEvent(proto.EventModeRestart, b)
	m.onEvent(proto.EventModeRestart, json.RawMessage(`{"session_id":"s1","outcome":"made up"}`))
	rows := st.recorded()
	if len(rows) != 1 || rows[0].Kind != db.ModeChangeRestartDropped || rows[0].To != "bypassPermissions" || rows[0].Reason == "" {
		t.Fatalf("rows = %+v", rows)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, e := range host.events[before:] {
		if e.Event == proto.EventModeRestart {
			t.Error("the restart's outcome went on to the hub")
		}
	}
}

// The owner's devices read a session's changes; a watch's or an extension's token does not.
func TestModeChangesRoute(t *testing.T) {
	d := newTestDevices(t)
	st := &modeChangesStore{memStore: newMemStore()}
	d.opts.Store = st
	modesMachine(t, d, func() (any, error) { return map[string]any{"ok": true, "applied": "now"}, nil })
	if err := modesCall(d, `{"session_id":"s1","permission_mode":"plan"}`, &devicewire.Actor{Kind: "device", DeviceName: "Phone"}); err != nil {
		t.Fatal(err)
	}
	mux := d.Handler(http.NotFoundHandler())
	for _, role := range []string{"", "answer", "extension"} {
		if code, _ := clipCall(t, mux, "GET", "/api/v1/sessions/mac/s1/mode-changes", "", role); code != http.StatusForbidden {
			t.Errorf("role %q: %d", role, code)
		}
	}
	code, out := clipCall(t, mux, "GET", "/api/v1/sessions/mac/s1/mode-changes", "", "full")
	changes, _ := out["changes"].([]any)
	if code != http.StatusOK || len(changes) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	c, _ := changes[0].(map[string]any)
	if c["kind"] != "set" || c["from"] != "acceptEdits" || c["to"] != "plan" || c["applied"] != "now" || c["device_name"] != "Phone" || c["at"] == nil {
		t.Errorf("change = %v", c)
	}
	if code, out := clipCall(t, mux, "GET", "/api/v1/sessions/mac/other/mode-changes", "", "full"); code != http.StatusOK || len(out["changes"].([]any)) != 0 {
		t.Errorf("another session: %d %v", code, out)
	}
}
