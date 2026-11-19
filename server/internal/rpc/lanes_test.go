package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

// gate is a transport whose writes wait for the test: each write is recorded, then held until release lets it return.
type gate struct {
	mu     sync.Mutex
	writes [][]byte
	wrote  chan struct{}
	ok     chan struct{}
}

func newGate() *gate { return &gate{wrote: make(chan struct{}, 1000), ok: make(chan struct{}, 1000)} }

func (g *gate) Write(b []byte) (int, error) {
	g.mu.Lock()
	g.writes = append(g.writes, bytes.Clone(b))
	g.mu.Unlock()
	g.wrote <- struct{}{}
	<-g.ok
	return len(b), nil
}

func (g *gate) release(n int) {
	for range n {
		g.ok <- struct{}{}
	}
}

func (g *gate) waitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-g.wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("no write")
	}
}

// heads is the frames in b, by header, and their payloads.
func heads(t *testing.T, b []byte) ([]frameHead, [][]byte) {
	t.Helper()
	var hs []frameHead
	var ps [][]byte
	for len(b) > 0 {
		h, err := parseHead(b)
		if err != nil {
			t.Fatal(err)
		}
		hs = append(hs, h)
		ps = append(ps, b[frameHeader:frameHeader+h.size])
		b = b[frameHeader+h.size:]
	}
	return hs, ps
}

func queue(t *testing.T, lw *laneWriter, lane Lane, line string) *outLine {
	t.Helper()
	l := &outLine{b: []byte(line), lane: lane, credit: creditLane(lane), queued: time.Now(), done: make(chan error, 1)} // as a request's
	if _, err := lw.enqueue(l); err != nil {
		t.Fatal(err)
	}
	return l
}

// While a write is under way, lines queue; the next write takes control first, then interactive, events and bulk,
// and a line longer than a frame gives way to the others after each of its frames.
func TestLaneWriterPriority(t *testing.T) {
	g := newGate()
	closed := make(chan struct{})
	defer close(closed)
	lw := newLaneWriter(g, closed, func(error) {}, nil)
	lw.start()
	first := queue(t, lw, LaneEvents, `{"event":"first"}`)
	g.waitWrite(t)                                             // the writer holds "first" in its write now
	bulk := queue(t, lw, LaneBulk, strings.Repeat("b", 20000)) // within the first window: no acknowledgement needed
	ev := queue(t, lw, LaneEvents, `{"event":"e"}`)
	in := queue(t, lw, LaneInteractive, `{"id":"h1","method":"m"}`)
	ctl := queue(t, lw, LaneControl, `{"id":"h2","method":"ping"}`)
	g.release(1)
	if err := <-first.done; err != nil {
		t.Fatal(err)
	}
	g.waitWrite(t)
	g.mu.Lock()
	hs, ps := heads(t, g.writes[1])
	g.mu.Unlock()
	var order []string
	for i, h := range hs {
		order = append(order, fmt.Sprintf("%s:%d", h.lane, len(ps[i])))
	}
	// One write, in priority order; the bulk frames after the others, cut to a quarter of the window, as many as the
	// write has room for.
	bulkFrame := fmt.Sprintf("bulk:%d", windowInit/4)
	want := []string{"control:27", "interactive:24", "events:13", bulkFrame, bulkFrame, fmt.Sprintf("bulk:%d", 20000-windowInit/2)}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("write 2: %v, want %v", order, want)
	}
	g.release(100)
	for _, l := range []*outLine{ctl, in, ev, bulk} {
		select {
		case err := <-l.done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s line not written", l.lane)
		}
	}
}

// Long lines of one lane go one after the other, as on the one writer: taking turns, each would end only when all end.
// A short line queued behind them goes between the frames of the one going out.
func TestLaneWriterLongLinesInTurn(t *testing.T) {
	g := newGate()
	closed := make(chan struct{})
	defer close(closed)
	lw := newLaneWriter(g, closed, func(error) {}, nil)
	block := queue(t, lw, LaneEvents, `{"event":"block"}`)
	lw.start()
	g.waitWrite(t)
	a := queue(t, lw, LaneEvents, strings.Repeat("a", 3*framePayloadMax))
	b := queue(t, lw, LaneEvents, strings.Repeat("b", 3*framePayloadMax))
	g.release(1)
	<-block.done
	g.waitWrite(t) // a's first frame
	short := queue(t, lw, LaneEvents, `{"event":"short"}`)
	g.release(100)
	for _, l := range []*outLine{a, b, short} {
		if err := <-l.done; err != nil {
			t.Fatal(err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var seen []byte
	for _, w := range g.writes[1:] {
		hs, ps := heads(t, w)
		for i := range hs {
			c := ps[i][0]
			if c == '{' {
				c = 's'
			}
			seen = append(seen, c)
		}
	}
	if got := string(seen); got != "asaabbb" {
		t.Fatalf("frames in order %q, want asaabbb", got)
	}
}

// buffered is a transport that takes everything at once, as a large socket buffer would: only the window keeps the
// bulk writer from handing it all of a transfer.
type buffered struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffered) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffered) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Len()
}

// A peer that stops reading holds the writer of interactive and bulk lines back once the window is full; what it
// acknowledges lets more go; control and events go on meanwhile.
func TestLaneWriterCreditWindow(t *testing.T) {
	for _, lane := range []Lane{LaneInteractive, LaneBulk} {
		t.Run(lane.String(), func(t *testing.T) { testCreditWindow(t, lane) })
	}
}

func testCreditWindow(t *testing.T, lane Lane) {
	var sink buffered
	closed := make(chan struct{})
	defer close(closed)
	lw := newLaneWriter(&sink, closed, func(error) {}, nil)
	lw.start()
	line := &outLine{b: bytes.Repeat([]byte("z"), 1<<20), lane: lane, credit: true, queued: time.Now(), done: make(chan error, 1)}
	if _, err := lw.enqueue(line); err != nil {
		t.Fatal(err)
	}
	// Held back by the window: the writer idle, with more bulk to send than credit for it. It writes nothing more until
	// an acknowledgement wakes it.
	stalled := func() int {
		eventually(t, func() bool {
			lw.mu.Lock()
			defer lw.mu.Unlock()
			return !lw.busy && lw.win.inFlight > 0 && lw.win.inFlight+lw.win.frameSize() > lw.win.cwnd
		})
		return sink.Len()
	}
	n := stalled()
	if max := windowInit + 4*frameHeader; n > max {
		t.Fatalf("%d bytes written to a peer that read none, window %d", n, windowInit)
	}
	select {
	case <-line.done:
		t.Fatal("the transfer finished without acknowledgements")
	default:
	}
	// Control and events still go while the others wait for credit.
	for _, l := range []*outLine{queue(t, lw, LaneControl, `{"id":"h1","method":"ping"}`), queue(t, lw, LaneEvents, `{"event":"e"}`)} {
		select {
		case <-l.done:
		case <-time.After(5 * time.Second):
			t.Fatalf("a %s line waited for the window", l.lane)
		}
	}
	lw.mu.Lock()
	sent := lw.seq
	lw.mu.Unlock()
	if err := lw.acked(sent, clockMS(time.Now())); err != nil {
		t.Fatal(err)
	}
	if m := stalled(); m <= n {
		t.Fatalf("an acknowledgement let nothing more go (%d, then %d)", n, m)
	}
	if err := lw.acked(sent+1000, clockMS(time.Now())); !errors.Is(err, ErrFrame) {
		t.Fatalf("an acknowledgement of frames never sent: %v", err)
	}
}

// A line not started yet can be withdrawn; one begun is written to its end.
func TestLaneWriterWithdraw(t *testing.T) {
	g := newGate()
	closed := make(chan struct{})
	defer close(closed)
	lw := newLaneWriter(g, closed, func(error) {}, nil)
	lw.start()
	busy := queue(t, lw, LaneEvents, strings.Repeat("x", 2*framePayloadMax)) // a lane that waits for no credit
	g.waitWrite(t)
	waiting := queue(t, lw, LaneBulk, `{"event":"stream.data"}`)
	if !lw.withdraw(waiting) {
		t.Fatal("a line nothing of which went out was not withdrawn")
	}
	if lw.withdraw(busy) {
		t.Fatal("a line begun was withdrawn")
	}
	g.release(100)
	if err := <-busy.done; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, w := range g.writes {
		if bytes.Contains(w, []byte("stream.data")) {
			t.Fatal("the withdrawn line went out")
		}
	}
}

// laneConn is two peers on a pipe, manager ("h") and node ("n").
type laneConn struct {
	h, n         *Peer
	hConn, nConn net.Conn
	served       sync.WaitGroup
}

var testControl, testBulk = []string{"ping", "stream.ack"}, []string{"file.chunk", "stream.data"}

func newLaneConn(t *testing.T, hLanes, nLanes bool, nHandler Handler) *laneConn {
	t.Helper()
	return newLaneConnWith(t, hLanes, nLanes, nHandler, nil)
}

// newLaneConnWith also lets setup configure the node's peer before it serves.
func newLaneConnWith(t *testing.T, hLanes, nLanes bool, nHandler Handler, setup func(n *Peer)) *laneConn {
	t.Helper()
	hc, nc := net.Pipe()
	c := &laneConn{h: New("h", hc), n: New("n", nc), hConn: hc, nConn: nc}
	if hLanes {
		c.h.EnableLanes(testControl, testBulk)
	}
	if nLanes {
		c.n.EnableLanes(testControl, testBulk)
	}
	c.n.OnRequest = nHandler
	if setup != nil {
		setup(c.n)
	}
	c.h.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]string{"from": "h", "method": method}, nil
	}
	c.served.Add(2)
	go func() { defer c.served.Done(); _ = c.h.Serve(context.Background(), hc) }()
	go func() { defer c.served.Done(); _ = c.n.Serve(context.Background(), nc) }()
	t.Cleanup(func() {
		hc.Close()
		nc.Close()
		c.served.Wait()
	})
	return c
}

func echo(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == lanesMethod {
		return nil, errors.New("unknown method " + method) // what a node from before lanes answers
	}
	return map[string]any{"method": method, "params": params}, nil
}

func call(t *testing.T, p *Peer, method string, params any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out map[string]any
	if err := p.Call(ctx, method, params, &out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func TestLanesNegotiation(t *testing.T) {
	ctx := context.Background()
	t.Run("both ends new: both directions framed", func(t *testing.T) {
		c := newLaneConn(t, true, true, echo)
		if err := c.h.UpgradeLanes(ctx); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return c.h.Framed() && c.n.Framed() })
		if got := call(t, c.h, "file.chunk", map[string]any{"data": strings.Repeat("d", 200_000)}); got["method"] != "file.chunk" {
			t.Fatalf("%v", got)
		}
		if got := call(t, c.n, "state.load", nil); got["from"] != "h" {
			t.Fatalf("%v", got)
		}
		if err := c.h.UpgradeLanes(ctx); err == nil {
			t.Fatal("upgraded twice")
		}
	})
	t.Run("new manager, old node: refused, lines go on", func(t *testing.T) {
		c := newLaneConn(t, true, false, echo)
		if err := c.h.UpgradeLanes(ctx); err == nil || !strings.Contains(err.Error(), "unknown method") {
			t.Fatalf("%v", err)
		}
		if c.h.Framed() || c.n.Framed() {
			t.Fatal("framed after a refusal")
		}
		call(t, c.h, "file.chunk", map[string]any{"data": "x"})
		call(t, c.n, "state.load", nil)
	})
	t.Run("old manager, new node: the node writes lines byte for byte", func(t *testing.T) {
		hc, nc := net.Pipe()
		defer hc.Close()
		n := New("n", nc)
		n.EnableLanes(testControl, testBulk)
		n.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return map[string]bool{"ok": true}, nil
		}
		go func() { _ = n.Serve(ctx, nc) }()
		if _, err := hc.Write([]byte(`{"id":"h1","method":"ping","params":{}}` + "\n")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 64)
		k, err := io.ReadAtLeast(hc, got, len(`{"id":"h1","result":{"ok":true}}`+"\n"))
		if err != nil || string(got[:k]) != `{"id":"h1","result":{"ok":true}}`+"\n" {
			t.Fatalf("%q, %v", got[:k], err)
		}
	})
	t.Run("node asked by a peer with lanes that it refuses", func(t *testing.T) {
		c := newLaneConn(t, true, true, echo)
		// Both asking at once: neither switches.
		c.n.mu.Lock()
		c.n.lanesCall = "n0"
		c.n.mu.Unlock()
		var re *Error
		if err := c.h.UpgradeLanes(ctx); !errors.As(err, &re) || re.Code != CodeLanesRefused {
			t.Fatalf("%v", err)
		}
		call(t, c.h, "x", nil)
	})
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("not in time")
		}
		time.Sleep(time.Millisecond) // sleep-ok: the polling interval of a wait for a condition, not a fixed wait
	}
}

// Calls and events going both ways while the connection switches all arrive, once each, and each sender's events in
// the order it sent them.
func TestLanesUpgradeWithTrafficInFlight(t *testing.T) {
	for round := 0; round < 20; round++ {
		var got sync.Map // "side/goroutine" → the last index seen
		var bad atomic.Value
		hc, nc := net.Pipe()
		h, n := New("h", hc), New("n", nc)
		h.EnableLanes(testControl, testBulk)
		n.EnableLanes(testControl, testBulk)
		onEvent := func(side string) EventHandler {
			return func(event string, params json.RawMessage) {
				var p struct {
					G, I int
				}
				_ = json.Unmarshal(params, &p)
				key := fmt.Sprintf("%s/%d", side, p.G)
				prev, _ := got.LoadOrStore(key, -1)
				if prev.(int) != p.I-1 { // one goroutine's events, bulk or not, in the order they were sent
					bad.Store(fmt.Sprintf("%s: %s %d after %d", key, event, p.I, prev))
				}
				got.Store(key, p.I)
			}
		}
		h.OnEvent, n.OnEvent = onEvent("h"), onEvent("n")
		h.OnRequest, n.OnRequest = echo, echo
		go func() { _ = h.Serve(context.Background(), hc) }()
		go func() { _ = n.Serve(context.Background(), nc) }()
		var wg sync.WaitGroup
		const per = 30
		for g := 0; g < 4; g++ {
			for _, p := range []*Peer{h, n} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < per; i++ {
						event := "evt"
						if i%3 == 0 {
							event = "stream.data" // bulk
						}
						if err := p.Notify(event, map[string]any{"g": g, "i": i, "pad": strings.Repeat("p", (i%5)*20000)}); err != nil {
							bad.Store(err.Error())
						}
						var out map[string]any
						if err := p.Call(context.Background(), "file.chunk", map[string]int{"g": g, "i": i}, &out); err != nil {
							bad.Store(err.Error())
						}
					}
				}()
			}
		}
		if err := h.UpgradeLanes(context.Background()); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if v := bad.Load(); v != nil {
			t.Fatalf("round %d: %v", round, v)
		}
		// Events are delivered synchronously before the calls behind them are answered, so all have arrived.
		for _, side := range []string{"h", "n"} {
			for g := 0; g < 4; g++ {
				if v, ok := got.Load(fmt.Sprintf("%s/%d", side, g)); !ok || v.(int) != per-1 {
					t.Fatalf("round %d: %s/%d last %v, want %d", round, side, g, v, per-1)
				}
			}
		}
		if !h.Framed() || !n.Framed() {
			t.Fatal("not framed")
		}
		hc.Close()
		nc.Close()
	}
}

// Over a framed connection a short call is answered while a long answer of the bulk lane is still on its way.
func TestLanesShortCallPassesBulk(t *testing.T) {
	link := netshape.Config{Rate: 64 << 10, Delay: 20 * time.Millisecond} // 64 KiB a second each way
	a, b := netshape.Pipe(link, link)
	h, n := New("h", a), New("n", b)
	h.EnableLanes(testControl, testBulk)
	n.EnableLanes(testControl, testBulk)
	big := strings.Repeat("c", 250_000)
	n.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "file.chunk" {
			return map[string]string{"data": big}, nil
		}
		return map[string]bool{"ok": true}, nil
	}
	go func() { _ = h.Serve(context.Background(), a) }()
	go func() { _ = n.Serve(context.Background(), b) }()
	defer func() { a.Close(); b.Close() }()
	if err := h.UpgradeLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	chunk := make(chan error, 1)
	go func() {
		var out map[string]string
		chunk <- h.Call(context.Background(), "file.chunk", map[string]any{}, &out)
	}()
	eventually(t, func() bool { return n.LaneStats().Lanes[LaneBulk].SentFrames > 0 }) // the answer is under way
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := h.Call(ctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("ping behind a bulk answer: %v after %s", err, time.Since(start))
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("ping took %s behind a bulk answer", took)
	}
	select {
	case err := <-chunk:
		t.Fatalf("the bulk answer finished first (%v): the test proves nothing", err)
	default:
	}
	if err := <-chunk; err != nil {
		t.Fatal(err)
	}
}

// LineFilter rewrites what goes out and LineWritten sees what was meant, framed or not.
func TestLanesLineHooks(t *testing.T) {
	for _, framed := range []bool{false, true} {
		var mu sync.Mutex
		var written []string
		c := newLaneConnWith(t, true, true, echo, func(n *Peer) {
			n.LineFilter = func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("secret"), []byte("******")) }
			n.LineWritten = func(b []byte) {
				mu.Lock()
				written = append(written, string(b))
				mu.Unlock()
			}
		})
		if framed {
			if err := c.h.UpgradeLanes(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		got := call(t, c.h, "file.chunk", map[string]string{"v": "a secret"})
		if s := fmt.Sprint(got["params"]); !strings.Contains(s, "a ******") {
			t.Fatalf("framed=%t: %s", framed, s)
		}
		// The caller can have its answer before the writer returns: LineWritten follows the write.
		eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, w := range written {
				if strings.Contains(w, "a secret") && strings.HasSuffix(w, "\n") {
					return true
				}
			}
			return false
		})
	}
}

// A ping cancelled before it got on the wire is NotSent, as with lines, and one written late says so.
func TestLanesWriteErrorsAsLines(t *testing.T) {
	g := newGate()
	n := New("n", g)
	n.EnableLanes(testControl, testBulk)
	lw := n.frameWriter()
	lw.start()
	n.out.Store(lw)
	defer n.Close(errors.New("done"))
	go func() { _ = n.Notify("evt", map[string]string{"x": strings.Repeat("x", 3*framePayloadMax)}) }()
	g.waitWrite(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- n.Notify("stream.data", map[string]int{}) }()
	go func() {
		_, err := n.writeContextObserved(ctx, message{Event: "e2"}, route{lane: LaneEvents})
		errc <- err
	}()
	eventually(t, func() bool { // both queued behind the long line
		lw.mu.Lock()
		defer lw.mu.Unlock()
		return len(lw.lanes[LaneBulk].lines) == 1 && len(lw.lanes[LaneEvents].lines) == 2
	})
	cancel()
	// The cancelled one never went out, while the writer still holds the long line's first frame; the other waits its
	// turn behind it, then goes.
	if err := <-errc; !errors.Is(err, ErrNotSent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before it went out: %v", err)
	}
	g.release(1000)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestLaneTable(t *testing.T) {
	tab := newLaneTable([]string{"ping", "stream.ack"}, []string{"file.chunk", "stream.data"})
	for _, c := range []struct {
		got, want Lane
	}{
		{tab.method("ping"), LaneControl},
		{tab.method("file.chunk"), LaneBulk},
		{tab.method("session.send"), LaneInteractive},
		{tab.event("rpc.cancel"), LaneControl},
		{tab.event("stream.ack"), LaneControl},
		{tab.event("stream.data"), LaneBulk},
		{tab.event("session.items"), LaneEvents},
		{laneTable(nil).method("ping"), LaneInteractive},
		{laneTable(nil).event("rpc.cancel"), LaneControl},
		{newLaneTable([]string{"x"}, []string{"x"}).method("x"), LaneControl},
	} {
		if c.got != c.want {
			t.Errorf("%s, want %s", c.got, c.want)
		}
	}
}

// Framed, the middle of a long line still counts as ping activity, as it does with lines; its first frame does not.
func TestLanesPingActivityInTheMiddleOfALine(t *testing.T) {
	big := bytes.Repeat([]byte("e"), 3*framePayloadMax)
	wire, _ := frames(0, LaneEvents, framePayloadMax, append([]byte(`{"event":"x","params":"`), append(big, `"}`...)...))
	marker := []byte(`{"event":"rpc.lanes","params":{"version":1}}` + "\n")
	pr, pw := io.Pipe()
	p := New("h", io.Discard)
	p.EnableLanes(testControl, testBulk)
	p.mu.Lock()
	p.lanesAsked = true // as if it had answered the other side's rpc.lanes
	p.mu.Unlock()
	done := make(chan struct{})
	go func() { _ = p.Serve(context.Background(), pr); close(done) }()
	defer func() { pw.Close(); <-done }()
	if _, err := pw.Write(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(wire[:frameHeader+framePayloadMax]); err != nil {
		t.Fatal(err)
	}
	// An acknowledgement of nothing is read only once the reader took in the frame before it.
	var ack [frameHeader + ackPayload]byte
	putHead(ack[:], frameHead{kind: frameAck, size: ackPayload})
	if _, err := pw.Write(ack[:]); err != nil {
		t.Fatal(err)
	}
	if !p.PingActivity().IsZero() {
		t.Fatal("the first frame of a line counted")
	}
	if _, err := pw.Write(wire[frameHeader+framePayloadMax : 2*(frameHeader+framePayloadMax)]); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !p.PingActivity().IsZero() })
}

// A long request waiting for credit does not hold a long answer of its lane behind it: answers never wait on credit,
// through another line neither.
func TestLaneWriterAnswerPassesARequestWaitingForCredit(t *testing.T) {
	var sink buffered // takes everything; nothing is ever acknowledged
	closed := make(chan struct{})
	defer close(closed)
	lw := newLaneWriter(&sink, closed, func(error) {}, nil)
	lw.start()
	request := queue(t, lw, LaneInteractive, strings.Repeat("q", 4*framePayloadMax)) // credit, as a request's
	eventually(t, func() bool {
		lw.mu.Lock()
		defer lw.mu.Unlock()
		return request.started && !lw.busy && lw.win.inFlight > 0
	})
	answer := &outLine{b: []byte(strings.Repeat("a", 3*framePayloadMax)), lane: LaneInteractive, queued: time.Now(), done: make(chan error, 1)}
	if _, err := lw.enqueue(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answer.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a long answer waited behind a request held back by the window")
	}
	select {
	case <-request.done:
		t.Fatal("the request went out without credit")
	default:
	}
}
