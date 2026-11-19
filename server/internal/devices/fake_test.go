package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Machines made by hand in tests have no run loop: a call to one never waits for its first try to connect, unless
// the test says so (TestACallWaitsForTheFirstConnection).
func init() { firstWait = 0 }

// fakeHost is the hub as the devices see it in tests: it keeps what they report and answers what they ask.
type fakeHost struct {
	revokedDevices     map[string]bool
	credentialCheckErr error
	mu                 sync.Mutex
	states             []devicewire.States
	desktops           []devicewire.Desktops
	notified           []string // every event's name, in order
	events             []devicewire.NodeEvent
	connected          []devicewire.Connected
	ended              []devicewire.RequestEnd
	creds              map[string]devicewire.ConnectorCred
	// reply is what devices.connected answers; request what a node's request the hub answers gets.
	reply   devicewire.ConnectedReply
	request func(ctx context.Context, p devicewire.NodeCall) (json.RawMessage, error)
	// credCall, when set, runs before devices.connector_cred is answered (a test acts while the fetch is out).
	credCall func()
	// theirs are the machines devices.connected refuses as another manager's (the hub's ownsMachine).
	theirs map[string]bool
	// rename answers devices.rename (nil: unknown_method, as an older hub).
	rename func(p devicewire.Rename) (devicewire.RenameReply, error)
	// broadcasts are the ws.broadcast messages sent to the apps.
	broadcasts []json.RawMessage

	// gates holds every event of a kind until its gate is closed (a hub slow to take it, hold); waiting counts the
	// ones held now.
	gates   map[string]chan struct{}
	waiting int
}

// passOne lets one event of this kind that hold keeps waiting through; the next ones are held still. The test fails
// when the kind is not held, or when no such event waits within 5 s.
func (f *fakeHost) passOne(t *testing.T, event string) {
	t.Helper()
	f.mu.Lock()
	gate := f.gates[event]
	f.mu.Unlock()
	if gate == nil {
		t.Fatalf("%s is not held", event)
	}
	select {
	case gate <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s waiting at the hub", event)
	}
}

// held: the hub holds an event now (hold).
func (f *fakeHost) held() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waiting > 0
}

// hold makes the hub take no event of this kind until release.
func (f *fakeHost) hold(event string) (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	if f.gates == nil {
		f.gates = map[string]chan struct{}{}
	}
	f.gates[event] = gate
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.gates, event)
			f.mu.Unlock()
			close(gate)
		})
	}
}

func (f *fakeHost) Call(ctx context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	var res any
	switch method {
	case bus.MethodCredentialRevoked:
		p := params.(bus.DeviceCredential)
		f.mu.Lock()
		res = bus.CredentialStatus{Revoked: f.revokedDevices[p.ID]}
		err := f.credentialCheckErr
		f.mu.Unlock()
		if err != nil {
			return err
		}
	case devicewire.MethodConnected:
		var p devicewire.Connected
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.connected = append(f.connected, p)
		res = f.reply
		theirs := f.theirs[p.MachineID]
		f.mu.Unlock()
		if theirs {
			return &rpc.Error{Code: devicewire.CodeNoDevice, Message: "another manager's"}
		}
	case devicewire.MethodRequest:
		var p devicewire.NodeCall
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		fn := f.request
		f.mu.Unlock()
		if fn == nil {
			return fmt.Errorf("no handler for %s", p.Method)
		}
		out2, err := fn(ctx, p)
		if err != nil {
			return err
		}
		res = out2
	case devicewire.MethodConnectorCred:
		var p devicewire.ConnectorCred
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		hook := f.credCall
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		f.mu.Lock()
		res = f.creds[p.MachineID]
		f.mu.Unlock()
	case devicewire.MethodConnectorPaired:
		var p devicewire.ConnectorCred
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		if f.creds == nil {
			f.creds = map[string]devicewire.ConnectorCred{}
		}
		f.creds[p.MachineID] = p
		f.mu.Unlock()
		res = map[string]any{}
	case devicewire.MethodRename:
		var p devicewire.Rename
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		fn := f.rename
		f.mu.Unlock()
		if fn == nil {
			return &rpc.Error{Code: "unknown_method", Message: method}
		}
		reply, err := fn(p)
		if err != nil {
			return err
		}
		res = reply
	default:
		return &rpc.Error{Code: "unknown_method", Message: method}
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func (f *fakeHost) Notify(event string, params any) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	gate := f.gates[event]
	if gate != nil {
		f.waiting++
	}
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	if gate != nil {
		f.waiting--
	}
	defer f.mu.Unlock()
	f.notified = append(f.notified, event)
	switch event {
	case devicewire.EventState:
		var st devicewire.States
		_ = json.Unmarshal(raw, &st)
		f.states = append(f.states, st)
	case devicewire.EventDesktops:
		var ds devicewire.Desktops
		_ = json.Unmarshal(raw, &ds)
		f.desktops = append(f.desktops, ds)
	case devicewire.EventNode:
		var ev devicewire.NodeEvent
		_ = json.Unmarshal(raw, &ev)
		f.events = append(f.events, ev)
	case devicewire.EventRequestEnd:
		var e devicewire.RequestEnd
		_ = json.Unmarshal(raw, &e)
		f.ended = append(f.ended, e)
	case assistantwire.EventBroadcast:
		var b assistantwire.Broadcast
		_ = json.Unmarshal(raw, &b)
		f.broadcasts = append(f.broadcasts, b.Message)
	}
	return nil
}

// broadcastsOf are the messages of type typ sent to the apps.
func (f *fakeHost) broadcastsOf(typ string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, m := range f.broadcasts {
		var v struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m, &v) == nil && v.Type == typ {
			out = append(out, m)
		}
	}
	return out
}

// eventsOf are the node events forwarded for machine named event.
func (f *fakeHost) eventsOf(machine, event string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, e := range f.events {
		if e.MachineID == machine && e.Event == event {
			out = append(out, e.Params)
		}
	}
	return out
}

// memStore is the settings in memory.
type memStore struct {
	mu   sync.Mutex
	kv   map[string]json.RawMessage
	fail error // every call fails with it, when set
}

func newMemStore() *memStore { return &memStore{kv: map[string]json.RawMessage{}} }

func (s *memStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	b, ok := s.kv[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}

func (s *memStore) PutSetting(_ context.Context, key string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.kv[key] = b
	return nil
}

func (s *memStore) TakeSetting(ctx context.Context, key string, out any) (bool, error) {
	ok, err := s.Setting(ctx, key, out)
	s.mu.Lock()
	delete(s.kv, key)
	s.mu.Unlock()
	return ok, err
}

// newTestDevices is a manager's devices with no machines, a fake hub and the settings in memory; its context ends
// with the test.
func newTestDevices(t *testing.T) *Devices {
	t.Helper()
	logf := func(string, ...any) {}
	if os.Getenv("PERSONA_DEVICES_LOG") == "1" {
		logf = log.New(os.Stderr, "", log.Lmicroseconds).Printf
	} else {
		_ = io.Discard
	}
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Logf: logf})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.mu.Lock()
	d.ctx, d.got = ctx, true
	d.cfg = devicewire.Config{HubID: "hub-test", HubName: "test", HubVersion: "test"}
	d.mu.Unlock()
	close(d.gotCh)
	d.readyOnce.Do(func() { close(d.ready) })
	return d
}

// host is the fake hub of d.
func (d *Devices) host() *fakeHost { return d.opts.Hub.(*fakeHost) }

// addEntry makes m one of d's hub.json entries.
func (d *Devices) addEntry(m *Machine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, m)
	d.byID[m.cfg.ID] = m
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: not within %s", what, within)
}

// machineList is the machines as devices.state reports them.
func (d *Devices) machineList() []proto.Machine {
	var out []proto.Machine
	for _, s := range d.states().Machines {
		out = append(out, s.Machine)
	}
	return out
}

// withMachine serves fn for the machine the path names (Handler's routes).
func (d *Devices) withMachine(fn func(http.ResponseWriter, *http.Request, *Machine)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := d.machineByID(r.PathValue("mid"))
		if m == nil {
			writeErr(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		fn(w, r, m)
	}
}
