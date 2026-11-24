package extmgr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// callerEcho is an extension behind a hub.json socket that answers with the X-Persona-* headers it was given: as the
// body of a request, as the first message of a WebSocket (which then echoes). Each WebSocket's end on its side is sent
// on ended, with its device. /bytes?n= answers n bytes of relayBody, /sse?n= streams n events.
type callerEcho struct {
	ended chan string
}

func personaHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-persona-") {
			out[k] = strings.Join(v, ",")
		}
	}
	return out
}

func serveCallerEcho(t *testing.T, sock string) *callerEcho {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	e := &callerEcho{ended: make(chan string, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(personaHeaders(r)) })
	mux.HandleFunc("GET /bytes", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		_, _ = w.Write(relayBody(n))
	})
	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range n {
			fmt.Fprintf(w, "data: %d %s\n\n", i, relayBody(512))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		device := r.Header.Get(extwire.HeaderDevice)
		defer func() { e.ended <- device }()
		b, _ := json.Marshal(personaHeaders(r))
		if c.Write(r.Context(), websocket.MessageText, b) != nil {
			return
		}
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), typ, b) != nil {
				return
			}
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	return e
}

// relayBody is n bytes that are not all alike, so a lost or shifted piece shows.
func relayBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%23)
	}
	return b
}

// callerTestManager is a manager with the extension "callee" (callerEcho) behind its socket.
func callerTestManager(t *testing.T) (*Manager, *callerEcho) {
	t.Helper()
	h := newTestManager(t)
	h.store = newExtensionStore()
	sock := filepath.Join(h.cfg.dir, "callee.sock")
	echo := serveCallerEcho(t, sock)
	startTestExtensions(t, h, ExtensionConfig{ID: "callee", Kind: "voice", Socket: sock})
	waitLong(t, "the extension running", func() bool {
		for _, v := range h.extensions.list() {
			if v.ID == "callee" && v.Status == "running" {
				return true
			}
		}
		return false
	})
	return h, echo
}

// An extension is told who is calling — the credential, its role and language as the hub found them — and nothing
// else of what the hub passed on (scopes, address, owner), let alone what a client sent.
func TestExtensionCallerHeaders(t *testing.T) {
	h, _ := callerTestManager(t)
	mux := h.routes()
	phone := deviceToken("dev_phone", roleFull)
	watch := deviceToken("dev_watch", roleAnswer)
	ext := h.extTokens.issue("caller", "Caller", []string{extension.ScopeCallPrefix + "callee"})
	other := h.extTokens.issue("other", "Other", []string{extension.ScopeCallPrefix + "elsewhere"})
	get := func(token, lang string) (int, map[string]string) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/extensions/callee/echo", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Persona-Device", "someone-else")
		req.Header.Set("X-Persona-Forged", "1")
		if lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		out := map[string]string{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	want := func(device, role, lang string) map[string]string {
		return map[string]string{extwire.HeaderDevice: device, extwire.HeaderRole: role, extwire.HeaderLang: lang}
	}
	for _, c := range []struct {
		name, token, lang string
		want              map[string]string
	}{
		{"a full device", phone, "en-US", want("dev_phone", roleFull, "en")},
		{"another extension's token", ext, "ja", want("extension:caller", roleExtension, "ja")},
		// The master token is a full caller with no device, as on the api / messaging points.
		{"the master token", h.cfg.Token, "zh-CN", want("", roleFull, "zh-Hans")},
	} {
		code, got := get(c.token, c.lang)
		if code != 200 || len(got) != len(c.want) {
			t.Fatalf("%s: %d %v", c.name, code, got)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: %s = %q, want %q (%v)", c.name, k, got[k], v, got)
			}
		}
	}
	for name, token := range map[string]string{"an answer-only device": watch, "an extension without extensions.call:callee": other} {
		if code, got := get(token, ""); code != http.StatusForbidden {
			t.Fatalf("%s: %d %v", name, code, got)
		}
	}

	// Whatever else reached the manager under X-Persona-* stays with it.
	req := httptest.NewRequest("GET", "/api/v1/extensions/callee/echo", nil)
	for k, v := range map[string]string{extwire.HeaderDevice: "dev_mac", extwire.HeaderDeviceName: "Mac", extwire.HeaderRole: roleFull,
		extwire.HeaderLang: "ja", extwire.HeaderRemote: "192.0.2.1", extwire.HeaderOwner: "1", extwire.HeaderPaired: "1",
		extwire.HeaderScopes: "kb.read", "X-Persona-Forged": "1"} {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	got := map[string]string{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got) != 3 || got[extwire.HeaderDevice] != "dev_mac" || got[extwire.HeaderRole] != roleFull ||
		got[extwire.HeaderLang] != "ja" {
		t.Fatalf("relayed with the hub's other headers: %d %v", w.Code, got)
	}
}

// relayHub is the hub's end of the relay streams to a manager (Accept), as relayToManager uses them: each request on a
// stream of its own (no keep-alives), asked for by id.
type relayHub struct {
	h       *Manager
	n       atomic.Int64
	waiting sync.Map // relay id → chan *websocket.Conn
}

func newRelayHub(t *testing.T, h *Manager) *relayHub {
	t.Helper()
	rh := &relayHub{h: h}
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch, ok := rh.waiting.Load(r.URL.Query().Get("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(64 << 20)
		ch.(chan *websocket.Conn) <- c
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	h.opts.DialRelay = func(ctx context.Context, query string) (*websocket.Conn, error) {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/relay?"+query, nil)
		return c, err
	}
	return rh
}

// dial asks the manager to take a relay stream and gives the hub's side of it, as the stream and as a net.Conn.
func (rh *relayHub) dial(ctx context.Context) (*websocket.Conn, net.Conn, error) {
	id := fmt.Sprintf("r%d", rh.n.Add(1))
	ch := make(chan *websocket.Conn, 1)
	rh.waiting.Store(id, ch)
	defer rh.waiting.Delete(id)
	if err := rh.h.Accept(ctx, id); err != nil {
		return nil, nil, err
	}
	select {
	case c := <-ch:
		return c, websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("the manager never dialed the relay: %w", ctx.Err())
	}
}

// client is the hub's relayToManager transport: a new relay stream for every request.
func (rh *relayHub) client() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			_, nc, err := rh.dial(ctx)
			return nc, err
		}}}
}

// relayedFromPhone is a request as the hub relays it for a full device: its headers say who asked.
func relayedFromPhone(ctx context.Context, url string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set(extwire.HeaderDevice, "dev_phone")
	req.Header.Set(extwire.HeaderRole, roleFull)
	req.Header.Set(extwire.HeaderLang, "en")
	return req
}

// Every answer the manager gives over a relay reaches the hub whole, to its last byte: small ones (the whole answer in
// one write), large ones to a slow reader, a stream. The relay's context once ended before the splice had written the
// answer's tail to it, so many answers lost their body or their last frame.
func TestRelayedAnswersArriveWhole(t *testing.T) {
	h, _ := callerTestManager(t)
	h.routes() // the manager's routes on h.apiMux
	rh := newRelayHub(t, h)
	client := rh.client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	get := func(path string, read func(io.Reader) ([]byte, error)) ([]byte, error) {
		res, err := client.Do(relayedFromPhone(ctx, "http://manager/api/v1/extensions/callee"+path))
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d", res.StatusCode)
		}
		return read(res.Body)
	}

	const workers, rounds, small = 4, 25, 2048
	errs := make(chan error, workers)
	for range workers {
		go func() {
			for i := range rounds {
				b, err := get(fmt.Sprintf("/bytes?n=%d", small), io.ReadAll)
				if err == nil && !bytes.Equal(b, relayBody(small)) {
					err = fmt.Errorf("%d of %d bytes, not the answer", len(b), small)
				}
				if err != nil {
					errs <- fmt.Errorf("small answer %d: %w", i, err)
					return
				}
			}
			errs <- nil
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	const large = 4 << 20
	slowly := func(r io.Reader) ([]byte, error) {
		var out bytes.Buffer
		buf := make([]byte, 64<<10)
		for {
			n, err := r.Read(buf)
			out.Write(buf[:n])
			if err == io.EOF {
				return out.Bytes(), nil
			}
			if err != nil {
				return out.Bytes(), err
			}
			time.Sleep(time.Millisecond)
		}
	}
	for i := range 5 {
		b, err := get(fmt.Sprintf("/bytes?n=%d", large), slowly)
		if err != nil || !bytes.Equal(b, relayBody(large)) {
			t.Fatalf("large answer %d: %d of %d bytes, %v", i, len(b), large, err)
		}
	}

	const events = 40
	for i := range 20 {
		var got int
		_, err := get(fmt.Sprintf("/sse?n=%d", events), func(r io.Reader) ([]byte, error) {
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				if line := sc.Text(); strings.HasPrefix(line, "data: ") {
					if want := fmt.Sprintf("data: %d ", got); !strings.HasPrefix(line, want) {
						return nil, fmt.Errorf("event %d: %.20q", got, line)
					}
					got++
				}
			}
			return nil, sc.Err()
		})
		if err != nil || got != events {
			t.Fatalf("stream %d: %d of %d events, %v", i, got, events, err)
		}
	}
}

// An extension's WebSocket through the hub's relay (Accept): it opens and stays open, the extension is told who is
// calling, and the hub ending its side of the relay (the device was revoked) ends the extension's side.
func TestRelayedExtensionWebSocket(t *testing.T) {
	h, echo := callerTestManager(t)
	h.routes() // the manager's routes on h.apiMux
	rh := newRelayHub(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	relay, hubSide, err := rh.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hubSide.Close()
	// The hub's relayed request, as relayToManager sends it: its headers say who asked.
	client := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return hubSide, nil
	}}}
	hd := http.Header{}
	hd.Set(extwire.HeaderDevice, "dev_phone")
	hd.Set(extwire.HeaderRole, roleFull)
	hd.Set(extwire.HeaderLang, "en")
	hd.Set(extwire.HeaderRemote, "192.0.2.1")
	c, _, err := websocket.Dial(ctx, "ws://manager/api/v1/extensions/callee/ws", &websocket.DialOptions{HTTPClient: client, HTTPHeader: hd})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("the WebSocket ended right after it opened: %v", err)
	}
	got := map[string]string{}
	_ = json.Unmarshal(b, &got)
	if len(got) != 3 || got[extwire.HeaderDevice] != "dev_phone" || got[extwire.HeaderRole] != roleFull || got[extwire.HeaderLang] != "en" {
		t.Fatalf("who is calling: %v", got)
	}
	for i := 0; i < 3; i++ {
		if err := c.Write(ctx, websocket.MessageBinary, []byte{0, 1, byte(i)}); err != nil {
			t.Fatal(err)
		}
		if _, b, err := c.Read(ctx); err != nil || len(b) != 3 || b[2] != byte(i) {
			t.Fatalf("echo %d: %v %v", i, b, err)
		}
	}
	// The hub cuts the relay (the device was revoked): the extension's side ends with it.
	_ = relay.CloseNow()
	select {
	case device := <-echo.ended:
		if device != "dev_phone" {
			t.Fatalf("ended: %q", device)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the extension's side stayed open after the hub cut the relay")
	}
}

// relayServer must keep idle relay streams open longer than the hub's relayIdleFor (90 s) so the hub retires them
// first instead of racing a peer-side idle close on a non-idempotent POST.
func TestRelayServerIdleTimeoutExceedsHubPool(t *testing.T) {
	h := New(Options{Dir: t.TempDir()})
	srv := h.relayServer()
	if srv.IdleTimeout <= 90*time.Second {
		t.Fatalf("relayServer IdleTimeout = %s, want > 90s", srv.IdleTimeout)
	}
	if srv.ReadHeaderTimeout != 30*time.Second {
		t.Fatalf("relayServer ReadHeaderTimeout = %s, want 30s", srv.ReadHeaderTimeout)
	}
}

// Accept serves sequential GET and POST requests over one keep-alive relay stream without closing between them.
func TestRelayAcceptKeepAlive(t *testing.T) {
	h, _ := callerTestManager(t)
	h.routes()
	rh := newRelayHub(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			_, nc, err := rh.dial(dctx)
			return nc, err
		},
	}}
	defer client.CloseIdleConnections()

	for i := range 3 {
		method, body := "GET", io.Reader(nil)
		if i > 0 {
			method, body = "POST", bytes.NewBufferString(`{"action":"ping"}`)
		}
		req, _ := http.NewRequestWithContext(ctx, method, "http://manager/api/v1/extensions/callee/echo", body)
		req.Header.Set(extwire.HeaderDevice, "dev_phone")
		req.Header.Set(extwire.HeaderRole, roleFull)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s #%d: %v", method, i, err)
		}
		_, _ = io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s #%d: status %d", method, i, res.StatusCode)
		}
	}
	if got := rh.n.Load(); got != 1 {
		t.Fatalf("dialed %d relay streams for sequential GET+POST, want 1", got)
	}
}
