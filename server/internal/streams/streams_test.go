package streams

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// pair wires two Muxes back to back over rpc.Peers, the way the hub and a node are.
func pair(t *testing.T) (hub, node *Mux, hubPeer *rpc.Peer) {
	t.Helper()
	return pairWith(t, nodeHooks{})
}

// nodeHooks watch the node's side of a pair: beforeOpen runs in a dial once it has connected, dialed once it has
// answered, onEvent after the node's Mux took an event.
type nodeHooks struct {
	beforeOpen, dialed func()
	onEvent            func(event string, params json.RawMessage)
	halfClose          bool
}

func pairWith(t *testing.T, hooks nodeHooks) (hub, node *Mux, hubPeer *rpc.Peer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a2bR, a2bW := io.Pipe()
	b2aR, b2aW := io.Pipe()
	hp, np := rpc.New("h", a2bW), rpc.New("n", b2aW)
	hub, node = New(hp.Notify), New(np.Notify)
	hp.OnEvent = func(e string, p json.RawMessage) { hub.Event(e, p) }
	np.OnEvent = func(e string, p json.RawMessage) {
		node.Event(e, p)
		if hooks.onEvent != nil {
			hooks.onEvent(e, p)
		}
	}
	// The node's side of a dial: open and start the stream on a connection to addr, then answer.
	np.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p struct{ ID, Addr string }
		_ = json.Unmarshal(params, &p)
		c, err := net.Dial("tcp", p.Addr)
		if err != nil {
			return nil, err
		}
		if hooks.dialed != nil {
			defer hooks.dialed()
		}
		if hooks.beforeOpen != nil {
			hooks.beforeOpen()
		}
		if err := node.Open(p.ID); err != nil {
			c.Close()
			return nil, err
		}
		if hooks.halfClose {
			node.StartHalfClose(p.ID, c.(*net.TCPConn), true)
		} else {
			node.Start(p.ID, c)
		}
		return map[string]any{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hp.Serve(ctx, b2aR) }()
	go func() { defer wg.Done(); _ = np.Serve(ctx, a2bR) }()
	t.Cleanup(func() {
		cancel()
		a2bW.Close()
		b2aW.Close()
		wg.Wait()
		hub.CloseAll()
		node.CloseAll()
	})
	return hub, node, hp
}

// dial is the hub's side: a stream to addr on the node, as a net.Conn the test reads and writes.
func dial(t *testing.T, hub *Mux, hp *rpc.Peer, id, addr string) net.Conn {
	t.Helper()
	if err := hub.Open(id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hp.Call(ctx, "port.dial", map[string]any{"id": id, "addr": addr}, nil); err != nil {
		hub.Abort(id, err)
		t.Fatal(err)
	}
	mine, theirs := net.Pipe()
	hub.Start(id, theirs)
	return mine
}

func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return ln.Addr().String()
}

// Megabytes both ways, more than a window, arrive whole and in order.
func TestStreamCarriesBytesBothWays(t *testing.T) {
	hub, node, hp := pair(t)
	addr := listen(t, func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }) // echo
	conn := dial(t, hub, hp, "s1", addr)
	payload := make([]byte, 3<<20)
	_, _ = rand.Read(payload)
	go func() { _, _ = conn.Write(payload) }()
	got := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the echo differs from what was sent")
	}
	conn.Close()
	waitFor(t, func() bool { return hub.Len() == 0 && node.Len() == 0 }, "both ends forget a closed stream")
}

// The server closing after its answer: everything it wrote reaches the hub's side before that ends.
func TestStreamFlushesBeforeClosing(t *testing.T) {
	hub, _, hp := pair(t)
	answer := bytes.Repeat([]byte("x"), 900<<10)
	addr := listen(t, func(c net.Conn) { _, _ = c.Write(answer); c.Close() })
	conn := dial(t, hub, hp, "s1", addr)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(answer) {
		t.Fatalf("got %d bytes, want %d", len(got), len(answer))
	}
}

// A reader that stops reading holds the sender at the window: the node stops reading from its connection.
func TestStreamWindowHoldsTheSender(t *testing.T) {
	hub, node, hp := pair(t)
	wrote := make(chan int, 1)
	addr := listen(t, func(c net.Conn) {
		defer c.Close()
		n := 0
		buf := bytes.Repeat([]byte("y"), chunk)
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		for {
			k, err := c.Write(buf)
			n += k
			if err != nil {
				break
			}
		}
		wrote <- n
	})
	conn := dial(t, hub, hp, "s1", addr)
	defer conn.Close()
	// Nobody reads conn: the hub's side takes one write into the pipe and acknowledges nothing more, so the node
	// sends a window's worth and stops reading (the server's writes then time out in the kernel's buffers).
	<-wrote
	node.mu.Lock()
	s := node.streams["s1"]
	node.mu.Unlock()
	if s == nil {
		t.Fatal("the stream ended")
	}
	s.mu.Lock()
	inflight := s.inflight
	s.mu.Unlock()
	if inflight > window || inflight < window-chunk {
		t.Fatalf("in flight %d, want about the window %d", inflight, window)
	}
}

// Asking for a stream that cannot be dialed ends it on both sides.
func TestStreamDialFailure(t *testing.T) {
	hub, node, hp := pair(t)
	if err := hub.Open("s1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hp.Call(ctx, "port.dial", map[string]any{"id": "s1", "addr": "127.0.0.1:1"}, nil); err == nil {
		t.Fatal("dialing a closed port should fail")
	}
	hub.Abort("s1", errors.New("dial failed"))
	if hub.Len() != 0 || node.Len() != 0 {
		t.Fatalf("streams left: hub %d node %d", hub.Len(), node.Len())
	}
}

// CloseAll (the connection to the node went away) ends the streams on that side.
func TestCloseAllEndsStreams(t *testing.T) {
	hub, _, hp := pair(t)
	addr := listen(t, func(c net.Conn) { defer c.Close(); _, _ = io.Copy(io.Discard, c) })
	conn := dial(t, hub, hp, "s1", addr)
	hub.CloseAll()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the hub's side should be closed")
	}
	if err := hub.Open("s2"); err == nil {
		t.Fatal("a closed Mux should not open streams")
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
