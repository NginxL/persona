package iphone

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastTiming shortens the session's delays for a test.
func fastTiming(t *testing.T) {
	saved := []time.Duration{backoffMin, backoffMax, stableAfter, parkAfter, minGap, browseWait, answerMargin, healthEvery, healthTimeout, usbFresh, pairRetry}
	backoffMin, backoffMax, stableAfter, parkAfter, minGap, browseWait, answerMargin = 5*time.Millisecond, 40*time.Millisecond, time.Hour, time.Hour, time.Millisecond, 20*time.Millisecond, 0
	healthEvery, healthTimeout, usbFresh, pairRetry = time.Hour, 50*time.Millisecond, 0, 5*time.Millisecond
	t.Cleanup(func() {
		backoffMin, backoffMax, stableAfter, parkAfter, minGap, browseWait, answerMargin = saved[0], saved[1], saved[2], saved[3], saved[4], saved[5], saved[6]
		healthEvery, healthTimeout, usbFresh, pairRetry = saved[7], saved[8], saved[9], saved[10]
	})
}

type request struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	Args      json.RawMessage `json:"args"`
	TimeoutMS int64           `json:"timeout_ms"`
}

// fakeLink is one session with a fake phone; the phone's handler decides what each request gets.
type fakeLink struct {
	ep       Endpoint
	lines    chan []byte
	mu       sync.Mutex
	closed   bool
	err      error
	sendFail bool
	sendHang bool // the bridge stopped reading stdin
	sendLate bool // the write finishes just as the caller's deadline passes
	phone    *fakePhone
}

func (l *fakeLink) Send(ctx context.Context, line []byte) error {
	l.mu.Lock()
	if l.closed || l.sendFail {
		l.mu.Unlock()
		return errors.New("broken pipe")
	}
	if l.sendHang {
		l.mu.Unlock()
		<-ctx.Done()
		l.Close()
		return ctx.Err()
	}
	late := l.sendLate
	l.mu.Unlock()
	if late { // the whole line reaches the bridge, and the deadline passes at that moment (sendLine returns nil)
		var r request
		_ = json.Unmarshal(line, &r)
		l.phone.mu.Lock()
		l.phone.got = append(l.phone.got, r)
		l.phone.mu.Unlock()
		<-ctx.Done()
		l.Close()
		return nil
	}
	var r request
	if err := json.Unmarshal(line, &r); err != nil {
		return err
	}
	l.phone.received(l, r)
	return nil
}

func (l *fakeLink) Lines() <-chan []byte { return l.lines }
func (l *fakeLink) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}
func (l *fakeLink) Close() { l.drop("closed") }

// drop ends the link as the bridge does when the phone goes away.
func (l *fakeLink) drop(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.err = &Error{Code: bridgeDisconnected, Message: reason}
	close(l.lines)
}

func (l *fakeLink) answer(r request, result string) {
	b, _ := json.Marshal(map[string]any{"v": 1, "id": r.ID, "ok": true, "op": r.Op, "result": json.RawMessage(result)})
	l.push(b)
}

func (l *fakeLink) fail(r request, code string) {
	b, _ := json.Marshal(map[string]any{"v": 1, "id": r.ID, "ok": false, "op": r.Op, "error": map[string]string{"code": code, "message": "fake"}})
	l.push(b)
}

func (l *fakeLink) push(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.lines <- b
	}
}

// fakePhone counts what reached it and answers by handler (default: {"done":op}).
type fakePhone struct {
	mu      sync.Mutex
	dials   []Endpoint
	links   []*fakeLink
	got     []request
	dialErr error
	handler func(l *fakeLink, r request)
}

func (p *fakePhone) dial(ctx context.Context, rec Record, path string, ep Endpoint) (Link, json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dials = append(p.dials, ep)
	if p.dialErr != nil {
		return nil, nil, p.dialErr
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil, err
	}
	l := &fakeLink{ep: ep, lines: make(chan []byte, 8), phone: p}
	p.links = append(p.links, l)
	return l, json.RawMessage(`{"name":"Test Phone"}`), nil
}

func (p *fakePhone) received(l *fakeLink, r request) {
	p.mu.Lock()
	p.got = append(p.got, r)
	h := p.handler
	p.mu.Unlock()
	if h != nil {
		h(l, r)
		return
	}
	l.answer(r, `{"done":"`+r.Op+`"}`)
}

func (p *fakePhone) ops() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.got {
		out = append(out, r.Op)
	}
	return out
}

func (p *fakePhone) dialCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.dials)
}

func (p *fakePhone) lastLink() *fakeLink {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.links[len(p.links)-1]
}

// testManager is a Manager over a temporary store with a paired phone, the fake phone and a fake Bonjour.
func testManager(t *testing.T, rec Record) (*Manager, *fakePhone) {
	t.Helper()
	fastTiming(t)
	m := New(t.TempDir())
	t.Cleanup(m.Close)
	p := &fakePhone{}
	m.dial = p.dial
	m.findBridge = func() (string, error) { return "/nonexistent/bridge", nil }
	m.match = func(context.Context, string, Service) (bool, error) { return false, nil }
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) { return nil, nil }
	m.logf = t.Logf
	if rec.UDID == "" {
		rec.UDID = testUDID
	}
	if err := m.store.Save(rec); err != nil {
		t.Fatal(err)
	}
	path, _ := m.store.RecordPath(rec.UDID)
	if err := os.WriteFile(path, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, p
}

func call(t *testing.T, m *Manager, op string, d time.Duration) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return m.Call(ctx, testUDID, op, json.RawMessage(`{"x":1}`))
}

func TestCallConnectsAndStaysConnectedWhileIdle(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5:49152"})
	res, err := call(t, m, "tap", time.Second)
	if err != nil || string(res) != `{"done":"tap"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	if p.dials[0] != (Endpoint{Host: "192.0.2.5", Port: 49152, Source: "manual"}) {
		t.Fatalf("dialed %+v", p.dials[0])
	}
	timer := time.NewTimer(50 * time.Millisecond) // idle: nothing closes the link
	defer timer.Stop()
	<-timer.C
	d := m.Devices(context.Background())
	if len(d) != 1 || d[0].State != StateConnected || d[0].Address != "192.0.2.5" || d[0].Port != 49152 || string(d[0].Info) != `{"name":"Test Phone"}` {
		t.Fatalf("Devices = %+v", d)
	}
	if _, err := call(t, m, "screenshot", time.Second); err != nil || p.dialCount() != 1 {
		t.Fatalf("second call: %v, dials %d (the link should be reused)", err, p.dialCount())
	}
}

func TestInputNotReplayedAfterLinkDrops(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	p.handler = func(l *fakeLink, r request) {
		if r.Op == "tap" {
			go l.drop("wifi lost") // sent, never answered
			return
		}
		l.answer(r, `{}`)
	}
	_, err := call(t, m, "tap", time.Second)
	if Code(err) != CodeUncertain || Sent(err) != "unknown" {
		t.Fatalf("want uncertain, got %v", err)
	}
	if Sent(&Error{Code: "app_not_found"}) != "answered" {
		t.Fatal("a bridge's answer is answered")
	}
	p.handler = nil
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	if ops := p.ops(); len(ops) != 2 || ops[0] != "tap" || ops[1] != "device_info" {
		t.Fatalf("phone got %v: the tap must reach it exactly once", ops)
	}
	if p.dialCount() != 2 {
		t.Fatalf("dials = %d, want a reconnect", p.dialCount())
	}
}

func TestBridgeDisconnectAnswerIsUncertainForInput(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	p.handler = func(l *fakeLink, r request) { l.fail(r, bridgeDisconnected); go l.drop("gone") }
	if _, err := call(t, m, "type_text", time.Second); Code(err) != CodeUncertain {
		t.Fatalf("want uncertain, got %v", err)
	}
	p.handler = func(l *fakeLink, r request) { l.fail(r, bridgeTimeout) }
	if _, err := call(t, m, "swipe", time.Second); Code(err) != CodeUncertain {
		t.Fatalf("bridge timeout on input: want uncertain, got %v", err)
	}
	if n := len(p.ops()); n != 2 {
		t.Fatalf("phone got %d requests, want 2", n)
	}
}

func TestReadOnlyRetriedOnceAfterDrop(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	first := true
	p.handler = func(l *fakeLink, r request) {
		if first {
			first = false
			go l.drop("wifi lost")
			return
		}
		l.answer(r, `{"png":"…"}`)
	}
	res, err := call(t, m, "screenshot", time.Second)
	if err != nil || string(res) != `{"png":"…"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	if ops := p.ops(); len(ops) != 2 {
		t.Fatalf("phone got %v, want the screenshot twice", ops)
	}
	// It drops every time: retried once, then an error (not uncertain: nothing changes on the phone).
	p.handler = func(l *fakeLink, r request) { go l.drop("again") }
	if _, err := call(t, m, "ax_tree", time.Second); Code(err) != bridgeDisconnected {
		t.Fatalf("want disconnected, got %v", err)
	}
}

func TestDeadlineAfterSendIsUncertainForInputOnly(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	// The link first: a call's 100 ms below is for its answer, not for bringing a link up, which under a full parallel
	// run took longer and left the call unsent (unreachable, not uncertain).
	if _, err := call(t, m, "device_info", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var timeouts []int64
	p.handler = func(l *fakeLink, r request) { timeouts = append(timeouts, r.TimeoutMS) } // never answers
	if _, err := call(t, m, "launch_app", 100*time.Millisecond); Code(err) != CodeUncertain {
		t.Fatalf("input: want uncertain, got %v", err)
	}
	if _, err := call(t, m, "list_apps", 100*time.Millisecond); Code(err) != bridgeTimeout {
		t.Fatalf("read-only: want timeout, got %v", err)
	}
	if len(timeouts) != 2 || timeouts[0] <= 0 || timeouts[0] > 100 {
		t.Fatalf("timeout_ms = %v, want the caller's deadline", timeouts)
	}
	// A late answer to the first call doesn't answer the next one.
	l := p.lastLink()
	p.handler = func(l *fakeLink, r request) { l.answer(r, `{"n":2}`) }
	l.answer(request{ID: "stale"}, `{"n":1}`)
	if res, err := call(t, m, "tap", time.Second); err != nil || string(res) != `{"n":2}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
}

func TestFailedSendIsResentOnNextLink(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	l := p.lastLink()
	l.mu.Lock()
	l.sendFail = true // the helper's stdin is gone: nothing reaches the phone
	l.mu.Unlock()
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatalf("a send that never left should go on the next link: %v", err)
	}
	if ops := p.ops(); len(ops) != 2 || ops[1] != "tap" {
		t.Fatalf("phone got %v", ops)
	}
}

func TestBonjourFollowsAddressChanges(t *testing.T) {
	m, p := testManager(t, Record{})
	m.match = func(_ context.Context, _ string, svc Service) (bool, error) { return svc.AuthTag() == "TAG-1", nil }
	addr := netip.MustParseAddr("192.0.2.20")
	var mu sync.Mutex
	m.browse = func(ctx context.Context, wait time.Duration, done func([]Service) bool) ([]Service, error) {
		mu.Lock()
		defer mu.Unlock()
		return []Service{
			{Instance: "other", Port: 1, TXT: map[string]string{"identifier": "ID-2", "authtag": "TAG-2"}, Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.99")}},
			// The identifier rotates; the tag (checked with the record's key) is what says it is this phone.
			{Instance: "mine", Port: 49152, TXT: map[string]string{"identifier": "ID-" + addr.String(), "authtag": "TAG-1"}, Addrs: []netip.Addr{addr}},
		}, nil
	}
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	// DHCP gave the phone a new address; the link drops; the next call finds it there.
	mu.Lock()
	addr = netip.MustParseAddr("192.0.2.21")
	mu.Unlock()
	p.lastLink().drop("wifi changed")
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := p.dials[len(p.dials)-1]; got.Host != "192.0.2.21" || got.Source != "bonjour" {
		t.Fatalf("last dial %+v", got)
	}
	rec, _ := m.store.Load(testUDID)
	if rec.LastHost != "192.0.2.21" || rec.LastPort != 49152 {
		t.Fatalf("remembered %s:%d", rec.LastHost, rec.LastPort)
	}
	// Bonjour finds nothing (another network): the last address is still tried.
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) { return nil, nil }
	p.lastLink().drop("gone")
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := p.dials[len(p.dials)-1]; got.Source != "last" || got.Host != "192.0.2.21" {
		t.Fatalf("last dial %+v", got)
	}
}

func TestUnreachableBacksOffAndReportsNothingSent(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	p.dialErr = &Error{Code: "tunnel_failed", Message: "no route"}
	_, err := call(t, m, "tap", 150*time.Millisecond)
	if Code(err) != CodeUnreachable || Sent(err) != "no" {
		t.Fatalf("want unreachable, got %v", err)
	}
	if len(p.ops()) != 0 {
		t.Fatal("nothing should reach the phone")
	}
	// The session keeps retrying after the call gave up; how many attempts fit in the call's 150ms depends on the
	// machine's load, so wait for the retry itself rather than count what one window held.
	deadline := time.Now().Add(10 * time.Second)
	for p.dialCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("dials = %d, want retries", p.dialCount())
		}
		<-time.After(5 * time.Millisecond)
	}
	d := m.Devices(context.Background())
	if d[0].LastErrorCode != "tunnel_failed" || (d[0].State != StateBackoff && d[0].State != StateConnecting) {
		t.Fatalf("Devices = %+v", d[0])
	}
	// It comes back: the waiting call gets it without waiting out the backoff. The call returns as soon as the link is
	// up; its deadline only bounds a failure, and must cover a whole reconnect on a loaded machine.
	p.mu.Lock()
	p.dialErr = nil
	p.mu.Unlock()
	if _, err := call(t, m, "tap", 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestNotPairedAndForget(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := m.Call(context.Background(), "00000000-OTHERPHONE", "tap", nil); Code(err) != CodeNotPaired || Sent(err) != "no" {
		t.Fatalf("want not_paired, got %v", err)
	}
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget(testUDID); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, m, "tap", time.Second); Code(err) != CodeNotPaired || Sent(err) != "no" {
		t.Fatalf("after Forget: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		l := p.lastLink()
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Forget left the link open")
		}
		<-time.After(5 * time.Millisecond)
	}
}

func TestCloseEndsSessions(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	m.Close()
	l := p.lastLink()
	deadline := time.Now().Add(time.Second)
	for {
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close left the link open")
		}
		<-time.After(5 * time.Millisecond)
	}
	if _, err := call(t, m, "tap", time.Second); Code(err) != CodeUnreachable {
		t.Fatalf("after Close: %v", err)
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]Endpoint{
		"192.0.2.1":           {Host: "192.0.2.1", Source: "manual"},
		"192.0.2.1:49152":     {Host: "192.0.2.1", Port: 49152, Source: "manual"},
		"phone.example.com":   {Host: "phone.example.com", Source: "manual"},
		"[2001:db8::1]:49152": {Host: "2001:db8::1", Port: 49152, Source: "manual"},
		"2001:db8::1":         {Host: "2001:db8::1", Source: "manual"},
	} {
		if got, err := ParseAddress(in); err != nil || got != want {
			t.Errorf("ParseAddress(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "host:0", "host:x", "a b", "-x", "-x:1", "[-x]"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("ParseAddress(%q) accepted", bad)
		}
	}
}

func TestRecordChangedByAnotherProcessRestartsLink(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	// The command line runs with a Manager of its own over the same directory.
	cli := New(m.store.Dir)
	defer cli.Close()
	if err := cli.SetAddress(testUDID, "192.0.2.6"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := p.dials[len(p.dials)-1]; got.Host != "192.0.2.6" {
		t.Fatalf("dialed %+v, want the new address", got)
	}
	if d := m.Devices(context.Background()); d[0].LastError != "" || d[0].State != StateConnected {
		t.Fatalf("a restart on purpose isn't a failure: %+v", d[0])
	}
	// Forgotten by the command line: the next call says so, and nothing reaches the phone.
	if err := cli.Forget(testUDID); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, m, "tap", time.Second); Code(err) != CodeNotPaired {
		t.Fatalf("want not_paired, got %v", err)
	}
	if ops := p.ops(); len(ops) != 2 {
		t.Fatalf("phone got %v", ops)
	}
}

func TestHealthCheckFindsDeadLinkAndForget(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	healthEvery = 20 * time.Millisecond
	locked := true
	p.handler = func(l *fakeLink, r request) {
		if r.Op == "device_info" {
			b, _ := json.Marshal(map[string]any{"name": "Test Phone", "os_version": "26.2", "locked": locked})
			l.answer(r, string(b))
			return
		}
		l.answer(r, `{}`)
	}
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { d := m.Devices(context.Background()); return d[0].Locked != nil && *d[0].Locked })
	if d := m.Devices(context.Background()); d[0].OSVersion != "26.2" {
		t.Fatalf("Devices = %+v", d[0])
	}
	// The phone goes silent (a half-open link): the probe times out and the link starts again.
	p.mu.Lock()
	p.handler = func(*fakeLink, request) {}
	p.mu.Unlock()
	waitFor(t, func() bool { return p.dialCount() >= 2 })
	// Forgotten from another process while idle: the link is closed without any call.
	p.mu.Lock()
	p.handler = nil
	p.mu.Unlock()
	cli := New(m.store.Dir)
	defer cli.Close()
	_ = cli.Forget(testUDID)
	waitFor(t, func() bool { d := m.Devices(context.Background()); return len(d) == 0 })
	waitFor(t, func() bool {
		l := p.lastLink()
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.closed
	})
}

func TestPairAgainAfterForgetWorksAtOnce(t *testing.T) {
	m, _ := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	rec, _ := m.store.Load(testUDID)
	_ = m.Forget(testUDID)
	if _, err := call(t, m, "tap", time.Second); Code(err) != CodeNotPaired {
		t.Fatalf("want not_paired, got %v", err)
	}
	waitFor(t, func() bool { d := m.Devices(context.Background()); return len(d) == 0 })
	_ = m.store.Save(rec)
	path, _ := m.store.RecordPath(testUDID)
	_ = os.WriteFile(path, []byte("opaque"), 0o600)
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatalf("paired again: %v", err)
	}
}

func TestSendThatNeverLeavesIsNotUncertain(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	l := p.lastLink()
	l.mu.Lock()
	l.sendHang = true
	l.mu.Unlock()
	_, err := call(t, m, "tap", 100*time.Millisecond)
	if Code(err) != CodeUnreachable || Sent(err) != "no" {
		t.Fatalf("want unreachable, nothing sent; got %v", err)
	}
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatalf("the next call gets a new link: %v", err)
	}
	if ops := p.ops(); len(ops) != 2 || ops[1] != "tap" {
		t.Fatalf("phone got %v: the tap that never left must not count", ops)
	}
}

func TestCallRejectsBadIDs(t *testing.T) {
	m, _ := testManager(t, Record{})
	_, err := m.Call(context.Background(), "../x", "tap", nil)
	if Code(err) != "bad_request" || Sent(err) != "no" {
		t.Fatalf("got %v", err)
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "bad_request" {
		t.Fatalf("ErrorCode through an interface: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		<-time.After(5 * time.Millisecond)
	}
}

func TestWriteFinishingAtTheDeadlineIsUncertain(t *testing.T) {
	m, p := testManager(t, Record{Address: "192.0.2.5"})
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	l := p.lastLink()
	l.mu.Lock()
	l.sendLate = true
	l.mu.Unlock()
	_, err := call(t, m, "tap", 100*time.Millisecond)
	if Code(err) != CodeUncertain || Sent(err) != "unknown" {
		t.Fatalf("the tap reached the bridge: want uncertain, never \"no\"; got %v (sent %s)", err, Sent(err))
	}
	if _, err := call(t, m, "device_info", time.Second); err != nil {
		t.Fatal(err)
	}
	if ops := p.ops(); strings.Join(ops, ",") != "device_info,tap,device_info" {
		t.Fatalf("phone got %v: the tap must not be sent again", ops)
	}
}

func TestSimulatorSessionHealthAndReconnect(t *testing.T) {
	m, p := testManager(t, Record{Transport: TransportSimulator})
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) {
		t.Error("a Simulator device was looked for on the network")
		return nil, nil
	}
	healthEvery = 20 * time.Millisecond
	if _, err := call(t, m, "tap", time.Second); err != nil {
		t.Fatal(err)
	}
	if got := p.dials[0]; got.Host != "" || got.Source != TransportSimulator {
		t.Fatalf("dialed %+v", got)
	}
	waitFor(t, func() bool {
		for _, op := range p.ops() {
			if op == "device_info" {
				return true
			}
		}
		return false
	}) // the health probe runs as for a phone
	p.lastLink().drop("simulator shut down")
	if _, err := call(t, m, "screenshot", time.Second); err != nil || p.dialCount() < 2 {
		t.Fatalf("after a drop: %v, dials %d", err, p.dialCount())
	}
	// Unreachable (simulator not booted): the bridge's reason, not "not found on the network".
	p.mu.Lock()
	p.dialErr = &Error{Code: "service_unavailable", Message: "simulator not booted"}
	p.mu.Unlock()
	p.lastLink().drop("gone")
	_, err := call(t, m, "tap", 150*time.Millisecond)
	if Code(err) != CodeUnreachable || !strings.Contains(err.Error(), "simulator not booted") || Sent(err) != "no" {
		t.Fatalf("got %v", err)
	}
}
