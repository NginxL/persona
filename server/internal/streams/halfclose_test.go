package streams

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := ln.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	outer, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { outer.Close() })
	inner, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inner.Close() })
	for _, c := range []*net.TCPConn{outer, inner} {
		if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return outer, inner
}

func dialForHalfClose(t *testing.T, hub *Mux, hp *rpc.Peer, addr string) {
	t.Helper()
	if err := hub.Open("s1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := hp.Call(ctx, "port.dial", map[string]any{"id": "s1", "addr": addr}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTCPStreamHalfCloseBothDirections(t *testing.T) {
	for _, nodeFirst := range []bool{false, true} {
		name := "hub sends EOF first"
		if nodeFirst {
			name = "node sends EOF before hub starts"
		}
		t.Run(name, func(t *testing.T) {
			hub, node, hp := pairWith(t, nodeHooks{halfClose: true})
			accepted := make(chan *net.TCPConn, 1)
			addr := listen(t, func(c net.Conn) { accepted <- c.(*net.TCPConn) })
			dialForHalfClose(t, hub, hp, addr)
			server := <-accepted
			defer server.Close()
			if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			client, bridge := tcpPair(t)
			sender, receiver := client, server
			payload := bytes.Repeat([]byte("request"), window/2) // multiple windows
			if nodeFirst {
				sender, receiver = server, client
				payload = payload[:window/2] // fits in the hub's queue before Start
			} else {
				hub.StartHalfClose("s1", bridge, false)
			}
			sent := make(chan error, 1)
			go func() {
				_, err := sender.Write(payload)
				if err == nil {
					err = sender.CloseWrite()
				}
				sent <- err
			}()
			if nodeFirst {
				hub.mu.Lock()
				s := hub.streams["s1"]
				hub.mu.Unlock()
				if s == nil {
					t.Fatal("hub stream closed before Start")
				}
				waitFor(t, func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					return s.peerEOF && s.queued == len(payload)
				}, "data and EOF did not queue before Start")
				hub.StartHalfClose("s1", bridge, false)
			}
			if got, err := io.ReadAll(receiver); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("read through EOF: got %d/%d bytes, err %v", len(got), len(payload), err)
			}
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			if hub.Len() != 1 || node.Len() != 1 {
				t.Fatal("one EOF ended the stream")
			}
			if _, err := io.WriteString(receiver, "response"); err != nil {
				t.Fatal(err)
			}
			if err := receiver.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(sender); err != nil || string(got) != "response" {
				t.Fatalf("reverse read through EOF: %q, %v", got, err)
			}
			waitFor(t, func() bool { return hub.Len() == 0 && node.Len() == 0 }, "both sides forget a completed stream")
		})
	}
}

func TestTCPStreamAbortAfterHalfClose(t *testing.T) {
	hub, node, hp := pairWith(t, nodeHooks{halfClose: true})
	accepted := make(chan net.Conn, 1)
	addr := listen(t, func(c net.Conn) { accepted <- c })
	dialForHalfClose(t, hub, hp, addr)
	server := <-accepted
	defer server.Close()
	if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, bridge := tcpPair(t)
	blocked := &blockedTCPWrite{TCPConn: bridge, writing: make(chan struct{}), closed: make(chan struct{})}
	hub.StartHalfClose("s1", blocked, false)
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(server); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(server, "response"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub did not start writing the response")
	}
	node.Abort("s1", context.Canceled)
	if got, err := io.ReadAll(client); err != nil || len(got) != 0 {
		t.Fatalf("read after abort: %q, %v", got, err)
	}
	waitFor(t, func() bool { return hub.Len() == 0 && node.Len() == 0 }, "aborted stream remains open")
}

// blockedTCPWrite stands in for a TCP receiver with a full buffer until the socket is closed.
type blockedTCPWrite struct {
	*net.TCPConn
	writing, closed chan struct{}
	once            sync.Once
}

func (c *blockedTCPWrite) Write([]byte) (int, error) {
	close(c.writing)
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockedTCPWrite) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.TCPConn.Close()
}
