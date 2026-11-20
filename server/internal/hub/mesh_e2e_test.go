package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// The mesh end to end: hubs in this process on 127.0.0.1 over TLS, in-process nodes, apps on WebSockets.

// meshNode is a computer's node: it answers the calls a hub makes and can push events and ask questions. A resident
// one takes one connection at a time (a new one drops the one before), like `personad node --attach`.
type meshNode struct {
	uid, host     string
	resident      bool
	portHalfClose bool

	mu       sync.Mutex
	sessions []proto.Session
	items    map[string][]proto.Item
	conns    []*meshNodeConn
	sent     []string
	watched  map[string]int
	connects int
	refuse   map[*Hub]bool // hubs whose connections fail (the network between them broke)
	refusal  error         // what such a connection fails with (io.ErrUnexpectedEOF when nil)
	// onEvent, when set, also sees each one-way event from the hub (after it is logged), with the connection it came on.
	onEvent func(c *meshNodeConn, event string, params json.RawMessage)
	// hold: a method whose answer waits until its channel is closed (a node still typing a message); held counts the
	// calls that waited.
	hold   map[string]chan struct{}
	held   map[string]int
	events []string // one-way events from the hub, "name text"
	who    []string // the hubs that connected, in order
	whoAt  []whoAt
	// pushWatchedOnly: addItem pushes only to the connections that watch the session, as a real node does (#6372);
	// failWatch: the next session.watch calls of a session that fail (a node still starting).
	pushWatchedOnly bool
	failWatch       map[string]int
}

type whoAt struct {
	hub string
	at  time.Time
}

type meshNodeConn struct {
	p   *rpc.Peer
	c   net.Conn
	hub *Hub
	mux *streams.Mux // forwarded ports' streams
	// watching: the sessions this connection was told to watch (meshNode.mu), as a real node's watch follows the
	// connection that asked for it.
	watching map[string]bool
}

func newMeshNode(uid, host string, sessions ...string) *meshNode {
	f := &meshNode{uid: uid, host: host, items: map[string][]proto.Item{}, watched: map[string]int{}, refuse: map[*Hub]bool{}}
	for _, s := range sessions {
		f.sessions = append(f.sessions, proto.Session{SessionID: s, Title: "task " + s, Status: proto.StatusIdle, Cwd: "/home/user/" + s})
		f.items[s] = []proto.Item{{Seq: 1, Kind: "user", Text: "hello " + s}, {Seq: 2, Kind: "assistant", Text: "hi from " + s}}
	}
	return f
}

func (f *meshNode) dialer(h *Hub) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		f.mu.Lock()
		if f.refuse[h] {
			why := f.refusal
			f.mu.Unlock()
			if why == nil {
				why = io.ErrUnexpectedEOF
			}
			return nil, why
		}
		hubSide, nodeSide := net.Pipe()
		fc := &meshNodeConn{p: rpc.New("n", nodeSide), c: nodeSide, hub: h}
		f.connects++
		if h.cfg != nil {
			f.who = append(f.who, h.cfg.Name+"@"+time.Now().Format("15:04:05.000"))
			f.whoAt = append(f.whoAt, whoAt{h.cfg.Name, time.Now()})
		}
		var kicked []*meshNodeConn
		if f.resident {
			kicked, f.conns = f.conns, nil
		}
		f.conns = append(f.conns, fc)
		f.mu.Unlock()
		for _, k := range kicked {
			_ = k.c.Close()
		}
		fc.mux = streams.New(fc.p.Notify)
		fc.p.OnEvent = func(event string, params json.RawMessage) {
			if fc.mux.Event(event, params) {
				return
			}
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(params, &p)
			f.mu.Lock()
			f.events = append(f.events, event+" "+p.Text)
			hook := f.onEvent
			f.mu.Unlock()
			if hook != nil {
				hook(fc, event, params)
			}
		}
		fc.p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			if method == devicewire.MethodPortDial { // a forwarded port: a stream to 127.0.0.1:port
				var p devicewire.PortDial
				_ = json.Unmarshal(params, &p)
				c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(p.Port)))
				if err != nil {
					return nil, err
				}
				if err := fc.mux.Open(p.ID); err != nil {
					c.Close()
					return nil, err
				}
				if f.portHalfClose && p.StreamVersion == devicewire.PortStreamHalfCloseV1 {
					fc.mux.StartHalfClose(p.ID, c.(*net.TCPConn), true)
					return devicewire.PortDialReply{StreamVersion: p.StreamVersion}, nil
				}
				fc.mux.Start(p.ID, c)
				return devicewire.PortDialReply{}, nil
			}
			f.mu.Lock()
			held := f.hold[method]
			if held != nil {
				if f.held == nil {
					f.held = map[string]int{}
				}
				f.held[method]++
			}
			f.mu.Unlock()
			if held != nil {
				<-held
			}
			if method == "session.watch" || method == "session.unwatch" {
				var w struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(params, &w)
				f.mu.Lock()
				if method == "session.watch" && f.failWatch[w.SessionID] > 0 {
					f.failWatch[w.SessionID]--
					f.mu.Unlock()
					return nil, errors.New("transcript for session " + w.SessionID + " not found")
				}
				if fc.watching == nil {
					fc.watching = map[string]bool{}
				}
				if method == "session.watch" {
					fc.watching[w.SessionID] = true
				} else {
					delete(fc.watching, w.SessionID)
				}
				f.mu.Unlock()
			}
			return f.handle(method, params)
		}
		go func() {
			_ = fc.p.Serve(context.Background(), nodeSide)
			fc.mux.CloseAll()
			f.mu.Lock()
			for i, c := range f.conns {
				if c == fc {
					f.conns = append(f.conns[:i], f.conns[i+1:]...)
					break
				}
			}
			f.mu.Unlock()
		}()
		return hubSide, nil
	}
}

func (f *meshNode) handle(method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		AfterSeq  *int   `json:"after_seq"`
		Text      string `json:"text"`
	}
	_ = json.Unmarshal(params, &p)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "node.info":
		return proto.NodeInfo{Hostname: f.host, OS: "linux", Arch: "arm64", Home: "/home/user", Version: "test", MachineUID: f.uid}, nil
	case "sessions.list":
		return map[string]any{"sessions": f.sessions}, nil
	case "session.items":
		out := []proto.Item{}
		for _, it := range f.items[p.SessionID] {
			if p.AfterSeq == nil || it.Seq > *p.AfterSeq {
				out = append(out, it)
			}
		}
		return map[string]any{"items": out, "has_more": false}, nil
	case "session.watch":
		f.watched[p.SessionID]++
		return map[string]any{}, nil
	case "session.send":
		f.sent = append(f.sent, p.Text)
		return map[string]any{"delivered": "yes"}, nil
	case "terminal.watch":
		var c struct {
			Control *struct{} `json:"control"`
		}
		_ = json.Unmarshal(params, &c)
		return map[string]any{"control": c.Control != nil}, nil
	case "fs.list":
		return map[string]any{"path": "/home/user", "entries": []any{map[string]any{"name": "notes.txt"}}}, nil
	case "port.probe": // a port the user opens (ports.go): something listens there
		return map[string]any{"listening": true}, nil
	case "claude.dirs.list":
		return map[string]any{"dirs": []proto.ClaudeDir{{Path: "/home/user/.automation/claude-a", Sessions: 1}}}, nil
	}
	return map[string]any{}, nil
}

// current is the connection a hub holds now (nil: none).
func (f *meshNode) current() *meshNodeConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conns) == 0 {
		return nil
	}
	return f.conns[len(f.conns)-1]
}

// addItem appends an item to a session and pushes it to the hub, as a node watching the transcript does.
func (f *meshNode) addItem(sid, text string) {
	f.mu.Lock()
	it := proto.Item{Seq: len(f.items[sid]) + 1, Kind: "assistant", Text: text}
	f.items[sid] = append(f.items[sid], it)
	var conns []*meshNodeConn
	for _, c := range f.conns {
		if !f.pushWatchedOnly || c.watching[sid] {
			conns = append(conns, c)
		}
	}
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.p.Notify("session.items", map[string]any{"session_id": sid, "items": []proto.Item{it}})
	}
}

func (f *meshNode) heldCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held[method]
}

func (f *meshNode) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.events...)
}

func (f *meshNode) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.sent...)
}

// testHub is one hub of a test mesh.
type testHub struct {
	h      *Hub
	srv    *httptest.Server
	store  *meshStore
	cancel context.CancelFunc
	nodes  map[string]*meshNode
	mgr    *testManager // carries its machines
}

var meshLogs = os.Getenv("PERSONA_MESH_LOG") == "1"

func startTestHub(t *testing.T, id, name string, machines []MachineConfig, nodes []*meshNode) *testHub {
	t.Helper()
	return startTestHubWith(t, id, name, newMeshStore(), "", machines, nodes)
}

// startTestHubWith starts a hub on a store and data directory of its own (a restart: the ones it had).
func startTestHubWith(t *testing.T, id, name string, st *meshStore, dir string, machines []MachineConfig, nodes []*meshNode) *testHub {
	t.Helper()
	return startTestHubOpts(t, id, name, st, dir, machines, nodes, testHubOpts{})
}

// testHubOpts: plain serves the hub without TLS (no key, http:// addresses only); plainAllow is its
// mesh.plaintext.allow.
type testHubOpts struct {
	plain      bool
	plainAllow []string
}

func startTestHubOpts(t *testing.T, id, name string, st *meshStore, dir string, machines []MachineConfig, nodes []*meshNode, o testHubOpts) *testHub {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	cfg := defaultConfig()
	cfg.Machines, cfg.dir, cfg.Name = machines, dir, name
	off := false
	cfg.Mesh.Discovery, cfg.Mesh.PathSwitchSeconds = &off, 1
	cfg.Mesh.Plaintext.Allow = o.plainAllow
	logger := log.New(io.Discard, "", 0)
	if meshLogs {
		logger = log.New(os.Stderr, "["+name+"] ", log.Lmicroseconds)
	}
	h := &Hub{cfg: cfg, version: "test", logger: logger, byID: map[string]*Machine{}, clients: map[*client]struct{}{},
		calls: map[string]chan proto.ToolResult{}, push: newPusher(cfg.dir, nil, func(string, ...any) {})}
	h.live = newLiveActivities(h)
	h.push.hubID = h.hubID
	h.store = st
	h.public.id = id
	mh := &testHub{h: h, store: st, nodes: map[string]*meshNode{}}
	dials := map[string]func(context.Context) (net.Conn, error){} // the manager connects them (devices_harness_test.go)
	for i, mc := range machines {
		if i < len(nodes) && nodes[i] != nil {
			dials[mc.ID] = nodes[i].dialer(h)
			mh.nodes[mc.ID] = nodes[i]
		} else if mc.Kind != kindService && mc.Kind != kindLink {
			dials[mc.ID] = func(context.Context) (net.Conn, error) { return nil, io.ErrUnexpectedEOF } // no ssh in tests
		}
	}
	srv := httptest.NewUnstartedServer(h.routes())
	if o.plain {
		srv.Start()
	} else {
		srv.StartTLS()
	}
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	h.public.mu.Lock()
	if o.plain {
		h.public.endpoints = []Endpoint{{URL: srv.URL, Kind: "lan", Port: port, Scheme: "http"}}
	} else {
		h.public.port, h.public.fp = port, link.Pin(srv.Certificate())
		h.public.endpoints = []Endpoint{{URL: srv.URL, Kind: "lan", Port: port, Scheme: "https"}}
	}
	h.public.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	h.linkPing = [2]time.Duration{300 * time.Millisecond, 2 * time.Second}
	h.initMesh(ctx)
	h.mesh.mu.Lock()
	h.mesh.ping = [2]time.Duration{300 * time.Millisecond, 2 * time.Second}
	h.mesh.mu.Unlock()
	h.startMesh(ctx)
	mh.srv, mh.cancel = srv, cancel
	mh.mgr = startTestManager(t, h, srv, dials)
	t.Cleanup(mh.stop)
	return mh
}

func (mh *testHub) stop() {
	if mh.mgr != nil {
		mh.mgr.stop()
	}
	mh.h.stopPeerLinks()
	mh.cancel()
	mh.srv.CloseClientConnections()
	mh.srv.Close()
}

func (mh *testHub) id() string { return mh.h.hubID() }

// pair pairs a with b: b makes the code, a uses it.
func pairHubs(t *testing.T, a, b *testHub) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := b.h.newPeerCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.h.joinPeer(ctx, code.Code); err != nil {
		t.Fatalf("join: %v", err)
	}
	eventually(t, 20*time.Second, "the link between "+a.h.cfg.Name+" and "+b.h.cfg.Name, func() bool {
		return a.h.peerLinkTo(b.id()) != nil && b.h.peerLinkTo(a.id()) != nil
	})
}

// deviceToken issues an app's credential on a hub.
func (mh *testHub) deviceToken(t *testing.T, role string) (tok, id string) {
	t.Helper()
	tok, id = "jd_"+randomHex(24), "dev_"+randomHex(6)
	if err := mh.store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: id, Name: "phone", Kind: "phone", Role: role}, hashToken(tok)); err != nil {
		t.Fatal(err)
	}
	return tok, id
}

var insecureClient = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

func (mh *testHub) do(t *testing.T, method, path, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, mh.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := insecureClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (mh *testHub) machines(t *testing.T, token string) map[string]proto.Machine {
	t.Helper()
	code, b := mh.do(t, "GET", "/api/v1/machines", token, "")
	if code != 200 {
		t.Fatalf("machines: %d %s", code, b)
	}
	var out struct {
		Machines []proto.Machine `json:"machines"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]proto.Machine{}
	for _, x := range out.Machines {
		if _, dup := m[x.ID]; dup {
			t.Fatalf("machine id %q twice: %s", x.ID, b)
		}
		m[x.ID] = x
	}
	return m
}

// app is an app's WebSocket.
type app struct {
	conn *websocket.Conn
	mu   sync.Mutex
	msgs []map[string]any
	raw  []string
	done chan struct{}
	err  error
}

func dialApp(t *testing.T, mh *testHub, token string) *app {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(mh.srv.URL, "https")+"/api/v1/ws",
		&websocket.DialOptions{HTTPClient: insecureClient, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatalf("app dial: %v", err)
	}
	conn.SetReadLimit(64 << 20)
	a := &app{conn: conn, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				a.mu.Lock()
				a.err = err
				a.mu.Unlock()
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				a.mu.Lock()
				a.msgs = append(a.msgs, m)
				a.raw = append(a.raw, string(b))
				a.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { _ = conn.CloseNow() })
	return a
}

func (a *app) send(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("app write: %v", err)
	}
}

// wait returns the first message (from index from on) that ok takes, and its index.
func (a *app) wait(t *testing.T, what string, from int, ok func(map[string]any) bool) (map[string]any, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.mu.Lock()
		for i := from; i < len(a.msgs); i++ {
			if ok(a.msgs[i]) {
				m := a.msgs[i]
				a.mu.Unlock()
				return m, i
			}
		}
		n := len(a.msgs)
		a.mu.Unlock()
		if time.Now().After(deadline) {
			a.mu.Lock()
			last := a.raw[max(0, len(a.raw)-8):]
			a.mu.Unlock()
			t.Fatalf("the app never got %s (%d messages; last: %s)", what, n, strings.Join(last, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func typeIs(typ string, kv ...string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		if m["type"] != typ {
			return false
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if m[kv[i]] != kv[i+1] {
				return false
			}
		}
		return true
	}
}

func itemTexts(m map[string]any) []string {
	var out []string
	items, _ := m["items"].([]any)
	for _, it := range items {
		if mm, ok := it.(map[string]any); ok {
			s, _ := mm["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

func hasItem(text string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		if m["type"] != "items" {
			return false
		}
		for _, s := range itemTexts(m) {
			if s == text {
				return true
			}
		}
		return false
	}
}

const (
	hubA = "aaaaaaaa-0000-4000-8000-000000000001"
	hubB = "bbbbbbbb-0000-4000-8000-000000000002"
	hubC = "cccccccc-0000-4000-8000-000000000003"
)

func TestMeshDuplicateLink(t *testing.T) {
	a := startTestHub(t, hubA, "家里", nil, nil)
	b := startTestHub(t, hubB, "Office", nil, nil)
	pairHubs(t, a, b)
	kept := a.h.peerLinkTo(hubB)
	if kept.dialer != hubA {
		t.Fatalf("the link was dialed by %s", kept.dialer)
	}
	b.h.mesh.mu.Lock()
	row := b.h.mesh.peers[hubA]
	b.h.mesh.mu.Unlock()
	token, err := b.h.openKey(row.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, addr, err := b.h.dialPeer(ctx, row.FP, token, b.h.peerEndpoints(hubA))
	if err != nil {
		t.Fatal(err)
	}
	// Refused by either side: B itself (its link is A's), or A closing it (4011) before or after the hellos.
	if err = b.h.runPeerLink(ctx, conn, hubA, hubB, addr, ""); err == nil {
		t.Fatal("the second link was served")
	}
	time.Sleep(200 * time.Millisecond)
	if a.h.peerLinkTo(hubB) != kept || b.h.peerLinkTo(hubA) == nil || b.h.peerLinkTo(hubA).dialer != hubA {
		t.Fatal("the link the smaller hub_id dialed did not stay")
	}
}

// A pairing code works once, and only within its time.
func TestMeshPairingCode(t *testing.T) {
	a := startTestHub(t, hubA, "家里", nil, nil)
	b := startTestHub(t, hubB, "Office", nil, nil)
	c := startTestHub(t, hubC, "Lab", nil, nil)
	ctx := context.Background()
	code, err := b.h.newPeerCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code.Code, "persona://peer?") || time.Until(code.ExpiresAt) > peerCodeLife || time.Until(code.ExpiresAt) < peerCodeLife-time.Minute {
		t.Fatalf("code %+v", code)
	}
	if _, err := a.h.joinPeer(ctx, code.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.joinPeer(ctx, code.Code); err == nil {
		t.Fatal("a used code paired a second hub")
	}
	if _, err := b.h.joinPeer(ctx, code.Code); err == nil {
		t.Fatal("a hub paired with itself")
	}
	// The credential C issued for B before trying is revoked again.
	toks, _ := c.store.DeviceTokens(ctx)
	for _, tk := range toks {
		if tk.Kind == "peer" && !c.store.revoked[tk.ID] {
			t.Fatalf("a failed pairing left a live credential: %+v", tk)
		}
	}
	expired, _ := b.h.newPeerCode(ctx)
	b.store.mu.Lock()
	for k := range b.store.codes {
		if !b.store.usedCodes[k] {
			b.store.codes[k] = time.Now().Add(-time.Second)
		}
	}
	b.store.mu.Unlock()
	if _, err := c.h.joinPeer(ctx, expired.Code); err == nil {
		t.Fatal("an expired code paired")
	}
	// A peer's credential opens the link and nothing else (on the center, which answers the API).
	a.h.mesh.mu.Lock()
	row := a.h.mesh.peers[hubB]
	a.h.mesh.mu.Unlock()
	peerTok, _ := a.h.openKey(row.Token) // the credential B issued to A
	if code, _ := b.do(t, "GET", "/api/v1/machines", peerTok, ""); code != 403 {
		t.Fatalf("a peer credential on the API: %d", code)
	}
}
