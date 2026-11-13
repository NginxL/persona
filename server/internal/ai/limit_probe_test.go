package ai

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// heldGate holds c's connection with one busy answer (its upstream's first) and moves the clock past the pause: the
// next request finds out.
func heldGate(t *testing.T, clk *clock, c *Client) {
	t.Helper()
	if err := call(c); !IsPaused(err) {
		t.Fatalf("the busy answer: %v", err)
	}
	clk.add(busyFirst + time.Second)
}

// gateUntil waits until ok holds of g (read under its lock), woken by the gate's changes.
func gateUntil(t *testing.T, g *gate, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		g.mu.Lock()
		done, wake := ok(), g.wake
		g.mu.Unlock()
		if done {
			return
		}
		select {
		case <-wake:
		case <-deadline:
			t.Fatalf("%s: not within 10s", what)
		}
	}
}

// The gate opens when the upstream says 200 to the request finding out, not when that request's answer has been read
// to the end: every call streams, so a long answer (a knowledge base call takes minutes) kept every other request on
// the connection (Inbox triage, a draft, a title) put off as "busy" for as long as it lasted, though the upstream
// had taken it at once (#6698).
func TestTheGateOpensAtTheProbesAnswer(t *testing.T) {
	clk := fakeClock(t)
	var n atomic.Int32
	streaming := make(chan struct{}) // the probe's 200 and first events are on the wire
	finish := make(chan struct{})    // lets the probe's stream end
	var once sync.Once
	end := func() { once.Do(func() { close(finish) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // a port probe from elsewhere on the machine, not this test's client
			http.NotFound(w, r)
			return
		}
		switch n.Add(1) {
		case 1: // one busy answer
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, busyBody)
		case 2: // the probe: answered 200 at once, then a long generation
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, sse(evStart, evBlock(0, "text"), evText(0, `{"ok":`)))
			w.(http.Flusher).Flush()
			close(streaming)
			<-finish
			_, _ = io.WriteString(w, sse(evText(0, `true}`), evDelta("end_turn", 5), evStop))
		default: // anything after it is answered at once
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, sse(evStart, evBlock(0, "text"), evText(0, `{"ok":true}`), evDelta("end_turn", 5), evStop))
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(end) // before the server's Close, so a failed test does not hang it
	c := New(Config{BaseURL: srv.URL, APIKey: "probe-stream", Model: "m"})
	heldGate(t, clk, c)
	g := gateFor(srv.URL, "probe-stream")
	g.mu.Lock()
	g.cap = 2 // room for one more beside the probe, which keeps its slot under the cap for as long as it streams
	g.mu.Unlock()

	probe := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // as the knowledge base's calls
		defer cancel()
		var out struct{ OK bool }
		probe <- c.JSON(ctx, "sys", "a long job", &out)
	}()
	select {
	case <-streaming:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe never reached the upstream")
	}

	// The upstream answered the probe 200 and is generating. Requests someone waits for come now.
	gateUntil(t, g, "the gate let go at the probe's 200", func() bool { return !g.held })
	for i := range 5 {
		clk.add(30 * time.Second)
		if err := call(c); err != nil {
			t.Fatalf("request %d while the probe streams: %v (Held = %+v)", i, err, Held(c.Config()))
		}
	}
	if p := Held(c.Config()); p != nil {
		t.Fatalf("still held while the probe streams: %+v", p)
	}
	if logs := clk.lines(); len(logs) != 2 || !strings.Contains(logs[1], "answers again after 6s; 0 request(s) were put off") {
		t.Fatalf("logs = %q", logs)
	}
	end()
	if err := <-probe; err != nil {
		t.Fatalf("the probe: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || g.probing || g.inflight != 0 || g.strikes != 0 {
		t.Fatalf("after the probe: held %v, probing %v, %d in flight, %d strike(s)", g.held, g.probing, g.inflight, g.strikes)
	}
}

// probeUpstream answers the first request busy and holds each later one until the test says how it ends
// (answers <- a reply): the requests after a pause, one at a time or several at once.
type probeUpstream struct {
	srv     *httptest.Server
	sent    atomic.Int32
	arrived chan struct{}                    // one per request after the first
	answers chan func(w http.ResponseWriter) // how the next held request ends
}

func newProbeUpstream(t *testing.T) *probeUpstream {
	u := &probeUpstream{arrived: make(chan struct{}, 64), answers: make(chan func(w http.ResponseWriter), 64)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // a port probe from elsewhere on the machine, not this test's client
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body) // read, so the request's context ends only when its caller gives up
		if u.sent.Add(1) == 1 {
			answer(503, busyBody)(w)
			return
		}
		u.arrived <- struct{}{}
		select {
		case reply := <-u.answers:
			reply(w)
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *probeUpstream) wait(t *testing.T) {
	t.Helper()
	select {
	case <-u.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the upstream")
	}
}

// Once a pause is over one request finds out, and the others wait for its answer instead of being put off at once:
// a round of Inbox triage right after a pause used to sort one item and report all the others as "the model is at
// its concurrency limit", with nothing in flight, until a later round asked again (#6698). Answered, they all go.
func TestRequestsWaitForTheProbe(t *testing.T) {
	clk := fakeClock(t)
	u := newProbeUpstream(t)
	c := New(Config{BaseURL: u.srv.URL, APIKey: "probe-wait", Model: "m"})
	heldGate(t, clk, c)
	g := gateFor(u.srv.URL, "probe-wait")
	g.mu.Lock()
	g.cap = 0 // the cap is another test's: here every request may go once the gate does
	g.mu.Unlock()

	results := make(chan error, 7)
	for range 7 { // a round's items, four at a time in the Inbox; all at once here
		go func() { results <- call(c) }()
	}
	u.wait(t) // the one finding out
	select {
	case err := <-results:
		t.Fatalf("a request ended while the probe was out: %v", err)
	case <-u.arrived:
		t.Fatal("a second request went out while the probe was out")
	case <-time.After(200 * time.Millisecond):
	}
	for range 7 {
		u.answers <- ok
	}
	for i := range 7 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the requests never ended")
		}
	}
	if n := u.sent.Load(); n != 8 {
		t.Fatalf("sent %d requests, want 8 (the busy one and the seven)", n)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || g.probing || g.inflight != 0 || g.putOff != 0 {
		t.Fatalf("after: held %v, probing %v, %d in flight, %d put off", g.held, g.probing, g.inflight, g.putOff)
	}
}

// The request finding out is turned away again: the ones waiting for it are put off without being sent, and the
// pause is the longer one.
func TestRequestsWaitingForTheProbeArePutOffWhenItIsTurnedAway(t *testing.T) {
	clk := fakeClock(t)
	u := newProbeUpstream(t)
	c := New(Config{BaseURL: u.srv.URL, APIKey: "probe-busy", Model: "m"})
	heldGate(t, clk, c)
	results := make(chan error, 4)
	for range 4 {
		go func() { results <- call(c) }()
	}
	u.wait(t)
	select { // the other three wait for it
	case err := <-results:
		t.Fatalf("a request ended while the probe was out: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	u.answers <- answer(503, busyBody)
	for i := range 4 {
		select {
		case err := <-results:
			if !IsPaused(err) {
				t.Fatalf("request %d: %v, want put off", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the requests never ended")
		}
	}
	if n := u.sent.Load(); n != 2 {
		t.Fatalf("sent %d requests, want 2 (the busy one and the probe)", n)
	}
	if p := Held(c.Config()); p == nil || !p.Until.Equal(now().Add(2*busyFirst)) {
		t.Fatalf("held = %+v, want %s from now", p, 2*busyFirst)
	}
}

// The request finding out fails some other way (a 404, a 5xx no limit stands behind, a dropped connection): the
// connection stays held, and the next one waiting finds out in its place; each is sent, none waits forever.
func TestTheNextRequestFindsOutWhenTheProbeFails(t *testing.T) {
	clk := fakeClock(t)
	u := newProbeUpstream(t)
	c := New(Config{BaseURL: u.srv.URL, APIKey: "probe-fails", Model: "m"})
	heldGate(t, clk, c)
	results := make(chan error, 3)
	for range 3 {
		go func() { results <- call(c) }()
	}
	u.wait(t)
	u.answers <- answer(404, `{"error":{"type":"not_found_error","message":"model: m"}}`)
	u.wait(t)
	u.answers <- answer(502, "bad gateway")
	u.wait(t)
	u.answers <- ok
	var failed, fine int
	for range 3 {
		select {
		case err := <-results:
			switch {
			case err == nil:
				fine++
			case IsPaused(err):
				t.Fatalf("put off: %v", err)
			default:
				failed++
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the requests never ended")
		}
	}
	if failed != 2 || fine != 1 || Held(c.Config()) != nil {
		t.Fatalf("%d failed, %d answered, held = %+v; want 2, 1 and open", failed, fine, Held(c.Config()))
	}
}

// A request waiting for the one finding out gives up at its own deadline (a background one after its slot wait) and
// is put off, as one waiting for a slot.
func TestWaitingForTheProbeIsBounded(t *testing.T) {
	clk := fakeClock(t)
	u := newProbeUpstream(t)
	c := New(Config{BaseURL: u.srv.URL, APIKey: "probe-bounded", Model: "m"})
	heldGate(t, clk, c)
	probe := make(chan error, 1)
	go func() { probe <- call(c) }()
	u.wait(t)
	var out struct{ OK bool }
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	err := c.JSON(withSlotWait(ctx, 100*time.Millisecond), "sys", "user", &out)
	if waited := time.Since(start); !IsPaused(err) || waited > 5*time.Second {
		t.Fatalf("background call: %v after %v; want put off after about 100ms", err, waited)
	}
	fg, cancelFG := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelFG()
	start = time.Now()
	err = c.JSON(fg, "sys", "user", &out)
	if waited := time.Since(start); !IsPaused(err) || waited < 250*time.Millisecond {
		t.Fatalf("other call: %v after %v; want put off at its own deadline (300ms)", err, waited)
	}
	if n := u.sent.Load(); n != 2 {
		t.Fatalf("sent %d requests, want 2", n)
	}
	u.answers <- ok
	if err := <-probe; err != nil {
		t.Fatalf("the probe: %v", err)
	}
}

// A limit the stream reports after the 200 that let the connection go holds it again, for longer than the last
// time: the busy answers in a row are counted on, not started over at the 200.
func TestALimitInTheProbesStreamHoldsAgain(t *testing.T) {
	clk := fakeClock(t)
	u := newProbeUpstream(t)
	c := New(Config{BaseURL: u.srv.URL, APIKey: "probe-stream-limit", Model: "m"})
	heldGate(t, clk, c)
	probe := make(chan error, 1)
	go func() { probe <- call(c) }()
	u.wait(t)
	u.answers <- func(w http.ResponseWriter) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, sse(evStart, `error {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}
	if err := <-probe; !IsPaused(err) {
		t.Fatalf("the probe: %v, want put off", err)
	}
	if p := Held(c.Config()); p == nil || p.Reason != ReasonBusy || !p.Until.Equal(now().Add(2*busyFirst)) {
		t.Fatalf("held = %+v, want busy for %s", p, 2*busyFirst)
	}
}

// Every way a request ends gives its slot back (#6698 suspected one leaked on the error paths): while the upstream
// answers 5xx, 404, busy, nothing at all, a dropped connection or half a stream, and callers give up or cancel,
// nothing stays counted as in flight and no request stays "finding out"; healthy again, the connection opens with
// the first request after the pause.
func TestEveryExitGivesItsSlotBack(t *testing.T) {
	clk := fakeClock(t)
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // a port probe from elsewhere on the machine, not this test's client
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if healthy.Load() {
			ok(w)
			return
		}
		switch rand.Intn(8) {
		case 0:
			answer(500, `{"error":{"type":"api_error","message":"example failure"}}`)(w)
		case 1:
			answer(502, "bad gateway")(w)
		case 2:
			answer(503, busyBody)(w)
		case 3:
			answer(404, `{"error":{"type":"not_found_error","message":"model: m"}}`)(w)
		case 4: // no answer until the caller gives up
			<-r.Context().Done()
		case 5: // the connection drops before any answer
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		case 6: // a 200, part of a stream, then the connection drops
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, sse(evStart, evBlock(0, "text"), evText(0, `{"ok":`)))
			w.(http.Flusher).Flush()
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		default:
			answer(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)(w)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "every-exit", Model: "m"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(20+rand.Intn(60))*time.Millisecond)
				if rand.Intn(5) == 0 { // the caller cancels partway
					time.AfterFunc(time.Duration(rand.Intn(10))*time.Millisecond, cancel)
				}
				var out struct{ OK bool }
				_ = c.JSON(ctx, "sys", "user", &out)
				cancel()
				if rand.Intn(3) == 0 {
					clk.add(time.Duration(rand.Intn(20)) * time.Second) // past some pauses: requests find out
				}
			}
		}()
	}
	wg.Wait()
	g := gateFor(srv.URL, "every-exit")
	g.mu.Lock()
	inflight, probing, until := g.inflight, g.probing, g.pause.Until
	g.mu.Unlock()
	if inflight != 0 || probing {
		t.Fatalf("after the outage: %d counted in flight, finding out %v; want none", inflight, probing)
	}
	healthy.Store(true)
	if d := until.Sub(now()); d > 0 {
		clk.add(d)
	}
	for i := range 3 {
		if err := call(c); err != nil {
			t.Fatalf("healthy again, request %d: %v", i, err)
		}
	}
	if p := Held(c.Config()); p != nil {
		t.Fatalf("still held: %+v", p)
	}
}
