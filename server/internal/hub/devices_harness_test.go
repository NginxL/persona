package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
)

// The machines end to end: the manager's device layer (internal/devices, as `personad assistant` runs it) on the hub's
// bus over a test server, connecting the machines the hub hands it, with in-process nodes where a test gives them.

// testManager is a manager carrying the machines of a test hub.
type testManager struct {
	d    *devices.Devices
	a    *testAssistant
	stop func()
	done chan struct{}
}

// busHost is the manager's bus connection once there is one.
type busHost struct {
	bh atomic.Pointer[assistant.BusHub]
}

func (b *busHost) client() *bus.Client {
	if bh := b.bh.Load(); bh != nil {
		return bh.Client()
	}
	return nil
}

func (b *busHost) Call(ctx context.Context, method string, params, out any) error {
	c := b.client()
	if c == nil {
		return bus.ErrOffline
	}
	return c.Call(ctx, method, params, out)
}

func (b *busHost) Notify(event string, params any) error {
	c := b.client()
	if c == nil {
		return bus.ErrOffline
	}
	return c.Notify(event, params)
}

// startTestManager starts the manager for h, served by srv; nodes dials a machine's node in process (an id it does
// not have: that machine's own ways). The hub's store must hold bus identities (newBusHub's, or memIdents).
func startTestManager(t *testing.T, h *Hub, srv *httptest.Server, nodes map[string]func(context.Context) (net.Conn, error)) *testManager {
	t.Helper()
	return startTestManagerWith(t, h, srv, nodes, "")
}

// startTestManagerWith is startTestManager with configFile as the manager's manager.json ("": none).
func startTestManagerWith(t *testing.T, h *Hub, srv *httptest.Server, nodes map[string]func(context.Context) (net.Conn, error),
	configFile string) *testManager {
	t.Helper()
	return startTestManagerDialing(t, h, srv, func(id string) func(context.Context) (net.Conn, error) { return nodes[id] }, configFile)
}

// startTestManagerDialing is startTestManagerWith with dial asked for each machine as the manager makes it (nil: the
// machine's own way, a link's).
func startTestManagerDialing(t *testing.T, h *Hub, srv *httptest.Server, dial func(id string) func(context.Context) (net.Conn, error),
	configFile string) *testManager {
	t.Helper()
	if h.busStore() == nil {
		h.bus.idents = &memIdents{}
	}
	var mu sync.Mutex
	over := false
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !over {
			t.Logf("manager: "+format, args...)
		}
	}
	t.Cleanup(func() { mu.Lock(); over = true; mu.Unlock() })
	host := &busHost{}
	var tok string
	var store devices.Store // the same Postgres in production; here the mesh tests' store, which has the settings
	if s, ok := h.store.(*meshStore); ok {
		store = s
	}
	var svc atomic.Pointer[assistant.Service] // the manager's own business with what comes from the nodes (as main.go wires it)
	d := devices.New(devices.Options{Hub: host, Logf: logf, Store: store, ConfigFile: configFile,
		Answer: func(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
			s := svc.Load()
			if s == nil {
				return nil, false, nil
			}
			if out, ok, err := s.NodeAnswer(ctx, machine, method, params); ok {
				return out, ok, err
			}
			return s.NodeRequest(ctx, machine, method, params)
		},
		Event: func(machine, event string, params json.RawMessage) bool {
			if s := svc.Load(); s != nil {
				return s.NodeEvent(machine, event, params)
			}
			return true
		},
		Detached: func(machine string) {
			if s := svc.Load(); s != nil {
				s.NodeDetached(machine)
			}
		},
		DialRelay: func(ctx context.Context, query string) (devices.Relay, error) {
			return bus.DialAt(ctx, srv.URL, tok, srv.Client(), devicewire.RelayPath, query)
		},
		Connected: func() bool { c := host.client(); return c != nil && c.Peer() != nil },
		DialNode:  dial})
	a := dialAssistant(t, h, srv.URL, false, func(o *assistant.BusOptions) {
		o.Devices, o.HTTPClient, o.Logf = d, srv.Client(), logf
	})
	tok = a.token
	svc.Store(a.svc)
	host.bh.Store(a.bh)
	ctx, cancel := context.WithCancel(context.Background())
	m := &testManager{d: d, a: a, done: make(chan struct{})}
	go func() {
		defer close(m.done)
		_ = d.Run(ctx)
	}()
	var once sync.Once
	m.stop = func() {
		once.Do(func() {
			a.stop()
			<-a.done
			cancel()
			select {
			case <-m.done:
			case <-time.After(10 * time.Second):
				t.Error("the manager did not stop")
			}
		})
	}
	t.Cleanup(m.stop)
	return m
}

// mirror waits for the hub to have machine id as the manager reported it.
func mirror(t *testing.T, h *Hub, id string) *Machine {
	t.Helper()
	var m *Machine
	eventually(t, 10*time.Second, "the manager reports "+id, func() bool {
		m = h.machineByID(id)
		if m == nil {
			return false
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.managed && m.snap != nil
	})
	return m
}

// newHubServer serves h's routes for a test.
func newHubServer(t *testing.T, h *Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	return srv
}
