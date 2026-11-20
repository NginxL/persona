package hub

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/streams"
)

// legacyPortManager decodes devices.call with the pre-9418 envelope and forwards its params unchanged.
type legacyPortManager struct{ assistant.Devices }

func (l legacyPortManager) Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if method == devicewire.MethodCall {
		var call struct {
			MachineID string          `json:"machine_id"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
			TimeoutMS int64           `json:"timeout_ms"`
		}
		if json.Unmarshal(params, &call) == nil && call.Method == devicewire.MethodPortDial {
			params, _ = json.Marshal(call)
		}
	}
	return l.Devices.Request(ctx, method, params)
}

func portTestListener(t *testing.T) *net.TCPListener {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if err := ln.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return ln
}

func portTestAccept(t *testing.T, ln *net.TCPListener) *net.TCPConn {
	t.Helper()
	c, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPortStreamHalfCloseRollingUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		newHub, newManager, newNode bool
	}{
		{"all new", true, true, true},
		{"old hub", false, true, true},
		{"old manager", true, false, true},
		{"old node", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = newMeshStore()
			h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}
			h.startDevices(t.Context())
			srv := newHubServer(t, h)
			box := newMeshNode("uid-box", "box")
			box.portHalfClose = tc.newNode
			wrap := func(d assistant.Devices) assistant.Devices {
				if !tc.newManager {
					return legacyPortManager{d}
				}
				return d
			}
			startTestManagerDialing(t, h, srv, func(string) func(context.Context) (net.Conn, error) {
				return box.dialer(h)
			}, "", wrap)
			m := mirror(t, h, "box")
			eventually(t, 5*time.Second, "box is ready to forward ports", func() bool { return m.portStreams() != nil })
			box.mu.Lock()
			nodeMux := box.conns[len(box.conns)-1].mux
			box.mu.Unlock()
			hubMux := m.portStreams()

			open := func(mux *streams.Mux) (client, server *net.TCPConn) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				ln, front := portTestListener(t), portTestListener(t)
				client, err := net.DialTCP("tcp", nil, front.Addr().(*net.TCPAddr))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { client.Close() })
				if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				bridge := portTestAccept(t, front)
				dial := devicewire.PortDial{ID: hubStreamIDs.next("test"), Port: ln.Addr().(*net.TCPAddr).Port}
				if err := mux.Open(dial.ID); err != nil {
					t.Fatal(err)
				}
				var res devicewire.PortDialReply
				if tc.newHub {
					res, err = m.dialPortStream(ctx, mux, dial, bridge)
				} else {
					err = m.call(ctx, devicewire.MethodPortDial, map[string]any{"id": dial.ID, "port": dial.Port}, &res)
					mux.Start(dial.ID, bridge)
				}
				if err != nil {
					t.Fatal(err)
				}
				want := tc.newHub && tc.newManager && tc.newNode
				if (res.StreamVersion == devicewire.PortStreamHalfCloseV1) != want {
					t.Fatalf("negotiated version %d, want half-close %v", res.StreamVersion, want)
				}
				return client, portTestAccept(t, ln)
			}

			client, server := open(hubMux)
			if _, err := io.WriteString(client, "request"); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(server); err != nil || string(got) != "request" {
				t.Fatalf("node read through EOF: %q, %v", got, err)
			}
			if tc.newHub && tc.newManager && tc.newNode {
				if _, err := io.WriteString(server, "response"); err != nil {
					t.Fatal(err)
				}
				if err := server.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				if got, err := io.ReadAll(client); err != nil || string(got) != "response" {
					t.Fatalf("hub read through EOF: %q, %v", got, err)
				}
			} else if got, err := io.ReadAll(client); err != nil || len(got) != 0 {
				t.Fatalf("legacy full close: %q, %v", got, err)
			}
			eventually(t, 5*time.Second, "both muxes retire completed stream", func() bool { return hubMux.Len() == 0 && nodeMux.Len() == 0 })

			if tc.name != "all new" {
				return
			}
			client, server = open(hubMux)
			if err := server.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(client); err != nil {
				t.Fatal(err)
			}
			if hubMux.Len() != 1 || nodeMux.Len() != 1 {
				t.Fatal("one EOF ended the stream before reconnect")
			}
			conn := h.busConnOf(devicesOwner)
			if conn == nil {
				t.Fatal("manager bus disconnected before test reconnect")
			}
			if err := conn.ws.CloseNow(); err != nil {
				t.Fatal(err)
			}
			eventually(t, 10*time.Second, "a new bus cleans up both halves", func() bool {
				current := h.busConnOf(devicesOwner)
				return current != nil && current != conn && hubMux.Len() == 0 && nodeMux.Len() == 0
			})
			box.mu.Lock()
			sameNode := len(box.conns) == 1 && box.conns[0].mux == nodeMux
			box.mu.Unlock()
			if !sameNode {
				t.Fatal("the node itself reconnected, bypassing bus stream cleanup")
			}
			if got, err := io.ReadAll(server); err != nil || len(got) != 0 {
				t.Fatalf("node socket after reconnect: %q, %v", got, err)
			}
		})
	}
}
