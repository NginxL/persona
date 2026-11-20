package bus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A WebSocket carries the rpc protocol both ways, one line per message, large lines included.
func TestConnCarriesRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(params, &in)
			return map[string]any{"echo": in.Text, "method": method}, nil
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Dial(ctx, srv.URL, "bad", nil); !errors.Is(err, ErrRejected) {
		t.Fatalf("a wrong token: %v", err)
	}
	ws, err := Dial(ctx, srv.URL, "good", nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := NewConn(ctx, ws)
	defer conn.Close()
	p := rpc.New("n", conn)
	go func() { _ = p.Serve(ctx, conn) }()
	big := strings.Repeat("长", 1<<20) // 3 MB in one line
	for _, text := range []string{"hi", "two\nlines", big} {
		var out struct {
			Echo, Method string
		}
		if err := p.Call(ctx, "echo", map[string]any{"text": text}, &out); err != nil {
			t.Fatal(err)
		}
		if out.Echo != text || out.Method != "echo" {
			t.Fatalf("echo %d bytes: got %d bytes", len(text), len(out.Echo))
		}
	}
	// Keepalive answers while the connection is read.
	kctx, kcancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer kcancel()
	if err := Keepalive(kctx, ws, 50*time.Millisecond, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("keepalive ended with %v", err)
	}
}

// Keepalive gives up on a peer that does not read (so never answers a ping).
func TestKeepaliveDropsADeafPeer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-r.Context().Done() // never reads
		_ = ws.CloseNow()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, err := Dial(ctx, srv.URL, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	go func() { _, _, _ = ws.Read(ctx) }() // our side reads, so only the server's silence counts
	if err := Keepalive(ctx, ws, 30*time.Millisecond, 100*time.Millisecond); err == nil || ctx.Err() != nil {
		t.Fatalf("keepalive ended with %v", err)
	}
}

func TestEndpoint(t *testing.T) {
	for _, c := range []struct{ hub, want string }{
		{"http://hub.example:18660", "http://hub.example:18660/api/v1/bus"},
		{"https://hub.example/base/", "https://hub.example/base/api/v1/bus"},
	} {
		if got, err := Endpoint(c.hub); err != nil || got != c.want {
			t.Errorf("Endpoint(%q) = %q, %v", c.hub, got, err)
		}
	}
	for _, hub := range []string{"hub.example", "ftp://hub.example", ""} {
		if got, err := Endpoint(hub); err == nil {
			t.Errorf("Endpoint(%q) = %q, want an error", hub, got)
		}
	}
}

// fakeHub answers the handshake (or refuses the token), counts the dials and lets a test drop the connection.
type fakeHub struct {
	refuse atomic.Bool
	plain  atomic.Bool // refuse with a 401 that says nothing about the token (not CodeTokenRevoked)
	dials  atomic.Int32
	// sick answers /api/v1/health with a 503; healths counts the health requests of a Go client (a probe).
	sick    atomic.Bool
	healths atomic.Int32
	// compress accepts as the hub does (internal/hub/bus.go: permessage-deflate when the client offers it).
	compress bool
	mu       sync.Mutex
	conns    []*websocket.Conn
	exts     []string // each connection's agreed extensions (Sec-WebSocket-Extensions)
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == healthPath { // as the hub's: no credential, {"ok": true}
		if strings.HasPrefix(r.UserAgent(), "Go-http-client/") { // the client's probe, not another program's port scan
			f.healths.Add(1)
		}
		if f.sick.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"hub_id":"h"}`))
		return
	}
	// Only the client's dials count: other programs on this machine probe local ports too (a node's port scan in
	// another package's tests, user agent persona-ports), and one could land on this server's.
	if r.URL.Path != Path || r.Header.Get("Authorization") == "" {
		http.NotFound(w, r)
		return
	}
	f.dials.Add(1)
	if f.refuse.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		code := CodeTokenRevoked
		if f.plain.Load() {
			code = ""
		}
		_, _ = w.Write([]byte(`{"error":"invalid token","code":"` + code + `"}`))
		return
	}
	opts := &websocket.AcceptOptions{}
	if f.compress {
		opts.CompressionMode = websocket.CompressionContextTakeover
	}
	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns = append(f.conns, ws)
	f.exts = append(f.exts, w.Header().Get("Sec-WebSocket-Extensions"))
	f.mu.Unlock()
	conn := NewConn(r.Context(), ws)
	p := rpc.New("h", conn)
	p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "manager.hello" {
			return map[string]any{"name": "n"}, nil
		}
		return map[string]any{"method": method}, nil
	}
	_ = p.Serve(r.Context(), conn)
}

func (f *fakeHub) closeAll(code websocket.StatusCode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ws := range f.conns {
		_ = ws.Close(code, "test")
	}
	f.conns = nil
}

// The client says its handshake, is reachable for calls, and dials again when the connection drops; a token the hub
// refuses (401, or closed as revoked) waits the longest backoff each time.
func TestClientReconnectsAndBacksOffOnRejection(t *testing.T) {
	f := &fakeHub{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var hellos atomic.Int32
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", HelloParams: func() any { return map[string]any{"id": "a"} },
		OnHello: func(res json.RawMessage) error {
			if !strings.Contains(string(res), `"name":"n"`) {
				t.Errorf("hello answer %s", res)
			}
			hellos.Add(1)
			return nil
		},
		Backoff: 10 * time.Millisecond, MaxBackoff: 400 * time.Millisecond}
	if err := c.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("a call before connecting: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- c.Run(ctx) }()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var out struct{ Method string }
	if err := c.Call(ctx, "ping", nil, &out); err != nil || out.Method != "ping" {
		t.Fatalf("ping: %+v %v", out, err)
	}

	// Dropped: back within the short backoff.
	f.closeAll(websocket.StatusGoingAway)
	deadline := time.Now().Add(5 * time.Second)
	for hellos.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Refused: the dials come no faster than MaxBackoff.
	f.refuse.Store(true)
	before := f.dials.Load()
	f.closeAll(CloseRevoked)
	time.Sleep(1 * time.Second)
	if n := f.dials.Load() - before; n < 1 || n > 3 {
		t.Fatalf("%d dials in a second with a rejected token, want 1–3 (every %s)", n, c.MaxBackoff)
	}
	if c.Peer() != nil {
		t.Fatal("still connected")
	}
	cancel()
	if err := <-ran; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}

// An Uncompressed client (the hub's own manager) offers no permessage-deflate, so a hub that compresses when offered
// does not; any other client still offers it.
func TestClientUncompressed(t *testing.T) {
	for uncompressed, want := range map[bool]string{false: "permessage-deflate", true: ""} {
		f := &fakeHub{compress: true}
		srv := httptest.NewServer(f)
		c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", HelloParams: func() any { return nil },
			OnHello: func(json.RawMessage) error { return nil }, Uncompressed: uncompressed}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ran := make(chan error, 1)
		go func() { ran <- c.Run(ctx) }()
		if err := c.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		var out struct{ Method string }
		if err := c.Call(ctx, "ping", nil, &out); err != nil || out.Method != "ping" {
			t.Fatalf("ping: %+v %v", out, err)
		}
		f.mu.Lock()
		got := f.exts[0]
		f.mu.Unlock()
		if !strings.HasPrefix(got, want) || want == "" && got != "" {
			t.Errorf("Uncompressed %v: the hub agreed %q, want %q", uncompressed, got, want)
		}
		cancel()
		<-ran
		srv.Close()
	}
}

// A close the hub gives a reason for maps to what the client does next.
func TestCloseReason(t *testing.T) {
	for code, rejected := range map[websocket.StatusCode]bool{CloseRevoked: true, CloseRefused: true, CloseReplaced: false,
		websocket.StatusGoingAway: false} {
		err := closeReason(websocket.CloseError{Code: code, Reason: "r"})
		if errors.Is(err, ErrRejected) != rejected {
			t.Errorf("close %d: %v", code, err)
		}
	}
}

// With StopWhenRevoked a client whose token the hub revokes (closed 4002, then 401) returns ErrRevoked from Run
// and dials no more; a refused handshake (4003) is not a revoked token.
func TestClientStopsWhenRevoked(t *testing.T) {
	f := &fakeHub{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", StopWhenRevoked: true,
		Backoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- c.Run(ctx) }()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	f.closeAll(CloseRevoked)
	select {
	case err := <-ran:
		if !errors.Is(err, ErrRevoked) || !errors.Is(err, ErrRejected) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run went on after the token was revoked")
	}
	dials := f.dials.Load()
	time.Sleep(200 * time.Millisecond)
	if f.dials.Load() != dials {
		t.Fatal("dialed again after stopping")
	}

	// A token the hub never knew (401 at the dial) stops it too.
	f.refuse.Store(true)
	c2 := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", StopWhenRevoked: true, Backoff: 10 * time.Millisecond}
	if err := c2.Run(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("401: %v", err)
	}
	if err := closeReason(websocket.CloseError{Code: CloseRefused}); errors.Is(err, ErrRevoked) || !errors.Is(err, ErrRejected) {
		t.Fatalf("4003: %v", err)
	}

	// A 401 that does not say token_revoked is rejected, not revoked: the client keeps trying.
	f.plain.Store(true)
	if _, err := Dial(ctx, srv.URL, "t", nil); !errors.Is(err, ErrRejected) || errors.Is(err, ErrRevoked) {
		t.Fatalf("plain 401: %v", err)
	}
	c3 := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", StopWhenRevoked: true, Backoff: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond}
	short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	if err := c3.Run(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plain 401 stopped the client: %v", err)
	}
	// With StopWhenRejected (the built-in manager's own service) a plain 401 stops it too, as revoked.
	c4 := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", StopWhenRevoked: true, StopWhenRejected: true,
		Backoff: 10 * time.Millisecond}
	if err := c4.Run(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("plain 401 with StopWhenRejected: %v", err)
	}
}
