package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

// A framed connection gives the heartbeat (devices.pingAnswered) the signals a connection of lines gives for the same
// traffic: these feed a manager-side peer the same lines both ways and compare PingActivity.

// framedHub is a manager-side peer that asked for lanes and read the node's answer: its input is framed from here. write
// hands it one raw read and returns once Serve has asked for the next. base is the activity the answer itself gave.
func framedHub(t *testing.T) (p *Peer, write func([]byte), base time.Time) {
	t.Helper()
	r, w := io.Pipe()
	p = New("h", io.Discard)
	p.EnableLanes(testControl, testBulk)
	served := make(chan error, 1)
	go func() { served <- p.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		p.Close(io.EOF)
		<-served
	})
	up := make(chan error, 1)
	go func() { up <- p.UpgradeLanes(context.Background()) }()
	eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.lanesCall != "" })
	write = func(b []byte) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := w.Write(b)
			if err == nil {
				_, err = w.Write(nil) // returns once Serve read again: what came before is taken
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not take the write")
		}
	}
	write([]byte(`{"id":"h1","result":{"version":1}}` + "\n"))
	if err := <-up; err != nil {
		t.Fatal(err)
	}
	base = p.PingActivity()
	for time.Since(base) < time.Millisecond { // later reads must be told apart from the answer's
		runtime.Gosched()
	}
	return p, write, base
}

// linesActivity is whether a peer of lines fed reads counts any activity.
func linesActivity(t *testing.T, reads ...string) bool {
	t.Helper()
	p := New("h", io.Discard)
	write := servePipe(t, p)
	for _, r := range reads {
		write(r)
	}
	return !p.PingActivity().IsZero()
}

func TestLanesActivityAsLines(t *testing.T) {
	small := []byte(`{"event":"session.screen","params":{"session_id":"s1"}}`)
	twoFrames := []byte(`{"event":"sessions.changed","params":{"list":"` + strings.Repeat("x", 40<<10) + `"}}`)
	port := []byte(`{"event":"stream.data","params":{"id":"p1","data":"` + strings.Repeat("A", 20<<10) + `"}}`)
	deadline := []byte(`{"id":"h9","error":{"message":"context deadline exceeded"}}`)
	reply := []byte(`{"id":"h7","result":{}}`)
	type part struct {
		lane  Lane
		piece int
		line  []byte
	}
	for _, c := range []struct {
		name  string
		parts []part
		moves bool
	}{
		// Lines the node starts itself never count, however they are cut and whatever follows in the same read.
		{"an event, then a whole event of two frames", []part{{LaneEvents, framePayloadMax, small}, {LaneEvents, framePayloadMax, twoFrames}}, false},
		{"two port lines cut small", []part{{LaneBulk, 8 << 10, port}, {LaneBulk, 8 << 10, port}}, false},
		{"a deadline, then a whole event of two frames", []part{{LaneInteractive, framePayloadMax, deadline}, {LaneEvents, framePayloadMax, twoFrames}}, false},
		{"small events", []part{{LaneEvents, framePayloadMax, small}, {LaneEvents, framePayloadMax, small}}, false},
		{"an event of one frame behind an event", []part{{LaneEvents, framePayloadMax, small}, {LaneEvents, framePayloadMax, port}}, false},
		// A reply does, ahead of the line before it.
		{"an event, then a reply", []part{{LaneEvents, framePayloadMax, small}, {LaneInteractive, framePayloadMax, reply}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var lines string
			for _, p := range c.parts {
				lines += string(p.line) + "\n"
			}
			if got := linesActivity(t, lines); got != c.moves {
				t.Fatalf("lines: activity %t", got)
			}
			p, write, base := framedHub(t)
			var wire []byte
			seq := uint64(0)
			for _, part := range c.parts {
				var w []byte
				w, seq = frames(seq, part.lane, part.piece, part.line)
				wire = append(wire, w...)
			}
			write(wire)
			if got := p.PingActivity().After(base); got != c.moves {
				t.Fatalf("framed: activity %t, lines %t", got, c.moves)
			}
		})
	}
}

// The middle piece of a line whose first piece is in the same read is never read as a line of its own: params with a
// nested id and result look like a reply's envelope.
func TestLanesActivityMiddlePieceIsNotALine(t *testing.T) {
	inner := strings.Repeat(`{"id":"s1","result":"ok","x":"`+strings.Repeat("y", 200)+`"},`, 200)
	big := []byte(`{"event":"tool.results","params":{"items":[` + inner + `{}]}}`)
	p, write, base := framedHub(t)
	wire, seq := frames(0, LaneEvents, framePayloadMax, []byte(`{"event":"session.screen","params":{}}`))
	for i := 0; i < 3; i++ { // the first three pieces of big, not its last
		seq++
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: LaneBulk, slot: 1, size: 4 << 10, seq: seq})
		wire = append(append(wire, h[:]...), big[i*4<<10:(i+1)*4<<10]...)
	}
	write(wire)
	if p.PingActivity().After(base) {
		t.Fatal("a middle piece of a node event counted")
	}
}

// The middle of a long line counts read for read, as with lines: a line of two frames read 4 KiB at a time.
func TestLanesActivityMiddleReadForRead(t *testing.T) {
	line := `{"event":"sessions.changed","params":"` + strings.Repeat("e", 50<<10) + `"}`
	var reads []string
	for i := 0; i < len(line)-1000; i += 4 << 10 {
		reads = append(reads, line[i:min(i+4<<10, len(line)-1000)])
	}
	if !linesActivity(t, reads...) {
		t.Fatal("lines: the middle of a 50 KiB line did not count")
	}
	p, write, base := framedHub(t)
	wire, _ := frames(0, LaneEvents, framePayloadMax, []byte(line))
	for i := 0; i < len(wire)-1000; i += 4 << 10 {
		write(wire[i:min(i+4<<10, len(wire)-1000)])
	}
	if !p.PingActivity().After(base) {
		t.Fatal("framed: the middle of a 50 KiB line did not count")
	}
}

// A short line finishing while a long one still arrives leaves the long one's middle counting until it is whole; with
// lines nothing finishes in between.
func TestLanesActivityMiddleStandsUntilTheLineIsWhole(t *testing.T) {
	p, write, base := framedHub(t)
	long := []byte(`{"event":"sessions.changed","params":"` + strings.Repeat("e", 4*framePayloadMax) + `"}`)
	wire, _ := frames(0, LaneEvents, framePayloadMax, long) // frames 1..5
	piece := frameHeader + framePayloadMax
	write(wire[:piece]) // a line begun in a read does not count in it, as with lines
	write(wire[piece : 2*piece])
	mid := p.PingActivity()
	if !mid.After(base) {
		t.Fatal("the middle did not count")
	}
	short := []byte(`{"event":"session.screen","params":{}}`)
	var h [frameHeader]byte
	putHead(h[:], frameHead{kind: frameData, lane: LaneEvents, fin: true, slot: 1, size: len(short), seq: 3})
	write(append(h[:], short...))
	if p.PingActivity().Before(mid) {
		t.Fatal("a short line took the activity back while the long one still arrived")
	}
}

// A large request on a slow link with a deep transport buffer does not hold a ping behind it: interactive lines wait
// for credit too, so the buffer holds about a window of them, not all of it (with lines the ping would wait to be
// written, which pingAnswered counts neither way).
func TestLanesPingPassesALargeRequest(t *testing.T) {
	slow := netshape.Config{Rate: 64 << 10, Queue: 256 << 10} // 4 s of buffer one way
	a, b := netshape.Pipe(slow, netshape.Config{})
	h, n := New("h", a), New("n", b)
	h.EnableLanes(testControl, testBulk)
	n.EnableLanes(testControl, testBulk)
	n.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil }
	go func() { _ = h.Serve(context.Background(), a) }()
	go func() { _ = n.Serve(context.Background(), b) }()
	defer func() { a.Close(); b.Close() }()
	if err := h.UpgradeLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = h.Call(context.Background(), "model.http", map[string]string{"body": strings.Repeat("b", 1<<20)}, nil)
	}()
	eventually(t, func() bool { return h.LaneStats().Lanes[LaneInteractive].SentFrames > 4 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := h.Call(ctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("ping behind a 1 MiB request: %v after %s (%s)", err, time.Since(start), h.LaneStats())
	}
}

// Lines in progress hold at most twice the line limit together: a peer opening many never finishes none of them.
func TestFrameReaderBoundsLinesInProgress(t *testing.T) {
	var wire []byte
	seq := uint64(0)
	for slot := uint16(0); slot < 8; slot++ {
		for range 4 { // 4 KiB a line so far
			seq++
			var h [frameHeader]byte
			putHead(h[:], frameHead{kind: frameData, lane: LaneEvents, slot: slot, size: 1 << 10, seq: seq})
			wire = append(append(wire, h[:]...), bytes.Repeat([]byte("x"), 1<<10)...)
		}
	}
	fr := newFrameReader(bytes.NewReader(wire), 10<<10) // eight lines of 4 KiB: over twice the limit together
	for fr.Scan() {
	}
	if !errors.Is(fr.Err(), bufio.ErrTooLong) {
		t.Fatalf("%v", fr.Err())
	}
}

// Lines that never started are NotSent when the writer stops, however it stops.
func TestLaneWriterStopIsNotSentForLinesNotStarted(t *testing.T) {
	g := newGate()
	closed := make(chan struct{})
	lw := newLaneWriter(g, closed, func(error) {}, nil)
	lw.start()
	busy := queue(t, lw, LaneEvents, strings.Repeat("x", 2*framePayloadMax))
	g.waitWrite(t)
	waiting := queue(t, lw, LaneEvents, `{"event":"e"}`)
	lw.stop(ErrClosed)
	if err := <-waiting.done; !errors.Is(err, ErrNotSent) || !errors.Is(err, ErrClosed) {
		t.Fatalf("not started: %v", err)
	}
	if err := <-busy.done; errors.Is(err, ErrNotSent) || !errors.Is(err, ErrClosed) {
		t.Fatalf("begun: %v", err)
	}
	close(closed)
	g.release(10)
}

// The reader asks for an acknowledgement after frames with credit (interactive, bulk), never after events or control
// alone.
func TestFrameReaderAsksAcksForCreditOnly(t *testing.T) {
	for lane, wants := range map[Lane]bool{LaneControl: false, LaneEvents: false, LaneInteractive: true, LaneBulk: true} {
		wire, _ := frames(0, lane, framePayloadMax, []byte(`{"event":"x"}`))
		fr := newFrameReader(bytes.NewReader(wire), 1<<20)
		asked := false
		fr.ack = func(uint64, uint32) { asked = true }
		for fr.Scan() {
		}
		if asked != wants {
			t.Errorf("%s: asked %t", lane, asked)
		}
	}
}

// A line whose write is under way when the peer closes is told how the write went, as a line on the one writer is: an
// accepted update's answer that went out whole is still seen by LineWritten.
func TestLanesLineWrittenAcrossClose(t *testing.T) {
	g := newGate()
	n := New("n", g)
	n.EnableLanes(testControl, testBulk)
	written := make(chan []byte, 1)
	n.LineWritten = func(b []byte) { written <- b }
	lw := n.frameWriter()
	lw.start()
	n.out.Store(lw)
	errc := make(chan error, 1)
	go func() { errc <- n.Notify("evt", map[string]string{"x": "y"}) }()
	g.waitWrite(t) // its only frame is being written
	n.Close(errors.New("gone"))
	g.release(10)
	if err := <-errc; err != nil {
		t.Fatalf("written whole across the close: %v", err)
	}
	if b := <-written; !bytes.Contains(b, []byte(`"evt"`)) {
		t.Fatalf("LineWritten saw %q", b)
	}
}

func oneFrame(lane Lane, slot uint16, fin bool, seq uint64, payload []byte) []byte {
	var h [frameHeader]byte
	putHead(h[:], frameHead{kind: frameData, lane: lane, fin: fin, slot: slot, size: len(payload), seq: seq})
	return append(h[:], payload...)
}

// tick waits until the clock is past t, so that a later activity is told apart from t.
func tick(t time.Time) {
	for time.Since(t) < time.Millisecond {
		runtime.Gosched()
	}
}

// A middle stands for its own line only: once that line is whole, another long line in progress (one that never
// counted, or one held back by credit) does not keep it.
func TestLanesActivityMiddleStandsForItsLineOnly(t *testing.T) {
	p, write, base := framedHub(t)
	x := []byte(`{"event":"sessions.changed","params":"` + strings.Repeat("e", 2*framePayloadMax) + `"}`)
	z := []byte(`{"id":"n4","method":"state.save","params":"` + strings.Repeat("z", 2*framePayloadMax) + `"}`)
	m := framePayloadMax
	write(oneFrame(LaneEvents, 0, false, 1, x[:m]))
	write(oneFrame(LaneEvents, 0, false, 2, x[m:2*m])) // the middle of x
	if !p.PingActivity().After(base) {
		t.Fatal("no middle")
	}
	write(oneFrame(LaneInteractive, 0, false, 3, z[:m])) // z begins: no middle of its own
	write(oneFrame(LaneEvents, 0, true, 4, x[2*m:]))     // x whole
	if p.PingActivity().After(base) {
		t.Fatal("x's middle still stands after x is whole, held by z")
	}
}

// The #8771 shape framed: a node's answer stuck for credit (the manager's acknowledgements do not get through), its
// long events going on: no event's middle stands once that event is whole.
func TestLanesActivityStuckLineKeepsNoEventAlive(t *testing.T) {
	ev := []byte(`{"event":"sessions.changed","params":"` + strings.Repeat("s", 90<<10) + `"}`)
	p, write, base := framedHub(t)
	z := []byte(`{"id":"n4","result":"` + strings.Repeat("z", 64<<10) + `"}`)
	seq := uint64(0)
	for i := 0; i < 4; i++ { // a window of z in 8 KiB frames, then nothing more of it
		seq++
		write(oneFrame(LaneInteractive, 0, false, seq, z[i*8<<10:(i+1)*8<<10]))
	}
	prev := p.PingActivity()
	for round := 0; round < 3; round++ {
		tick(prev)
		wire, last := frames(seq, LaneEvents, framePayloadMax, ev)
		seq = last
		k := len(wire) / 3
		write(wire[:k])
		write(wire[k : 2*k])
		write(wire[2*k:])
		got := p.PingActivity()
		if got.After(prev) {
			t.Fatalf("round %d: a whole node event left activity %s after the lanes answer", round, got.Sub(base))
		}
		prev = got
	}
}

// The read-ahead behind a deadline counts a whole answer of several frames, an error one too, as lines do; and the
// start of an answer taken in from the same read before the deadline.
func TestLanesActivityAnswersAroundADeadline(t *testing.T) {
	d := []byte(`{"id":"h9","error":{"message":"context deadline exceeded"}}`)
	t.Run("a whole error answer of two frames behind it", func(t *testing.T) {
		e := []byte(`{"id":"h5","error":{"message":"` + strings.Repeat("o", 12<<10) + `"}}`)
		if !linesActivity(t, string(d)+"\n"+string(e)+"\n") {
			t.Fatal("lines: no activity")
		}
		p, write, base := framedHub(t)
		held := make(chan message) // the deadline's caller: Serve waits there, after it counted what it read ahead
		released := false
		t.Cleanup(func() { // before Serve's own cleanup waits for it (cleanups run last first)
			if !released {
				<-held
			}
		})
		p.mu.Lock()
		p.pending["h9"], p.methods["h9"] = held, "ping"
		p.mu.Unlock()
		w1, seq := frames(0, LaneInteractive, 8<<10, d)
		w2, _ := frames(seq, LaneInteractive, 8<<10, e)
		wrote := make(chan struct{})
		go func() { write(append(w1, w2...)); close(wrote) }()
		eventually(t, func() bool { return p.PingActivity().After(base) })
		<-held
		released = true
		<-wrote
	})
	t.Run("an answer begun before it in the same read", func(t *testing.T) {
		p, write, base := framedHub(t)
		r := []byte(`{"id":"h5","result":"` + strings.Repeat("r", 2*framePayloadMax) + `"}`)
		write(append(oneFrame(LaneInteractive, 0, false, 1, r[:framePayloadMax]), oneFrame(LaneInteractive, 1, true, 2, d)...))
		if !p.PingActivity().After(base) {
			t.Fatal("the start of an answer read with the deadline did not count")
		}
	})
}

// A callback in the read loop may wait for an answer of this side's to be written (AfterReply) while bulk has the
// window full: interactive answers wait for no credit, which only that loop's reading would bring back.
func TestLanesReadLoopWaitingForItsOwnAnswer(t *testing.T) {
	link := netshape.Config{Rate: 1 << 20}
	a, b := netshape.Pipe(link, link)
	h, n := New("h", a), New("n", b)
	h.EnableLanes(testControl, testBulk)
	n.EnableLanes(testControl, testBulk)
	h.TrackReplies = true
	asked, waiting, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "x.q" {
			close(asked)
			<-waiting // answered once the read loop waits for the answer
			AfterReply(ctx, func() { close(written) })
			return map[string]string{"answer": strings.Repeat("a", 40<<10)}, nil // several frames
		}
		return map[string]bool{"ok": true}, nil
	}
	h.OnEvent = func(event string, _ json.RawMessage) {
		if event == "x.block" {
			close(waiting)
			select { // the read loop waits for its side's answer
			case <-written:
			case <-time.After(10 * time.Second):
			}
		}
	}
	n.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil }
	go func() { _ = h.Serve(context.Background(), a) }()
	go func() { _ = n.Serve(context.Background(), b) }()
	defer func() { a.Close(); b.Close() }()
	if err := h.UpgradeLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	for range 2 { // uploads that keep h's window full
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if h.Call(context.Background(), "file.chunk", map[string]string{"data": strings.Repeat("u", 64<<10)}, nil) != nil {
					return
				}
			}
		}()
	}
	eventually(t, func() bool { s := h.LaneStats(); return s.InFlight > 0 && s.Lanes[LaneBulk].SentFrames > 8 })
	answered := make(chan error, 1)
	go func() { answered <- n.Call(context.Background(), "x.q", map[string]any{}, nil) }()
	<-asked // the request is read before the event that blocks the read loop
	if err := n.Notify("x.block", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("the answer waited for credit the blocked read loop would bring (%s)", h.LaneStats())
	}
}

// A line is filtered as it goes on the wire, as on the one writer: a value handed over while it waited is hidden too.
func TestLanesFilterAsTheLineGoesOut(t *testing.T) {
	g := newGate()
	n := New("n", g)
	n.EnableLanes(testControl, testBulk)
	var hide atomic.Bool
	n.LineFilter = func(b []byte) []byte {
		if !hide.Load() {
			return b
		}
		return bytes.ReplaceAll(b, []byte("secret"), []byte("******"))
	}
	lw := n.frameWriter()
	lw.start()
	n.out.Store(lw)
	defer n.Close(errors.New("done"))
	go func() { _ = n.Notify("evt", map[string]string{"x": strings.Repeat("x", 2*framePayloadMax)}) }()
	g.waitWrite(t) // the writer is busy: the next line waits
	sent := make(chan error, 1)
	go func() { sent <- n.Notify("evt2", map[string]string{"v": "a secret"}) }()
	eventually(t, func() bool { lw.mu.Lock(); defer lw.mu.Unlock(); return len(lw.lanes[LaneEvents].lines) == 2 })
	hide.Store(true) // handed over while the line waited
	g.release(100)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, w := range g.writes {
		if bytes.Contains(w, []byte("a secret")) {
			t.Fatal("a value handed over while its line waited went out")
		}
	}
}

// The read-ahead is looked at once a read, not once a line: thousands of small lines a read cost about what they cost
// read one by one (it was quadratic: 0.3 MB/s where lines read 2.5).
func TestFrameReaderReadAheadOnceARead(t *testing.T) {
	var wire []byte
	const lines = 20000
	for i := range lines {
		p := []byte(fmt.Sprintf(`{"event":"session.activity","params":{"n":%d,"s":"abcdefghijklmnopqrstuvwxyz0123456789"}}`, i))
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: LaneEvents, fin: true, size: len(p), seq: uint64(i + 1)})
		wire = append(append(wire, h[:]...), p...)
	}
	read := func(ahead bool) (int, time.Duration) {
		fr := newFrameReader(bytes.NewReader(wire), 64<<20)
		fr.middle = func() {}
		start := time.Now()
		n := 0
		for fr.Scan() {
			if ahead {
				_ = fr.aheadActivity()
			}
			n++
		}
		return n, time.Since(start)
	}
	_, plain := read(false)
	n, took := read(true)
	if n != lines || took > 5*plain+50*time.Millisecond {
		t.Fatalf("%d lines in %s with the read-ahead looked at for each, %s without", n, took, plain)
	}
}

// lastRead hands over all of b in one read, which also ends the input when endsWith; or the next one does.
type lastRead struct {
	b        []byte
	endsWith bool
	done     bool
}

func (r *lastRead) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	if r.endsWith {
		return copy(p, r.b), io.EOF
	}
	return copy(p, r.b), nil
}

// A read that ends the input brings frames all the same: the reply among them is ahead of the line before it.
func TestFrameReaderReadAheadOnTheLastRead(t *testing.T) {
	wire, _ := frames(0, LaneEvents, framePayloadMax, []byte(`{"event":"x","params":{}}`), []byte(`{"id":"h1","result":{}}`))
	fr := newFrameReader(&lastRead{b: wire, endsWith: true}, 64<<20)
	if !fr.Scan() {
		t.Fatalf("no line: %v", fr.Err())
	}
	if !fr.aheadActivity() {
		t.Fatal("the reply behind the event, brought by the read that ended the input, not seen ahead")
	}
}

// What the read-ahead marked moves with the bytes when the buffer is compacted: a reply already read is not taken for
// one ahead once the buffer starts over.
func TestFrameReaderReadAheadAfterCompaction(t *testing.T) {
	reply := []byte(`{"id":"h1","result":{}}`)
	pad := []byte(`{"event":"x","params":{"s":"` + strings.Repeat("a", 16<<10) + `"}}`)
	wire, seq := frames(0, LaneEvents, framePayloadMax, reply, pad, pad)
	next, _ := frames(seq, LaneEvents, framePayloadMax, pad)
	wire = append(wire, next[:frameHeader+10]...) // a frame begun: reading on needs more, from the start of the buffer
	fr := newFrameReader(&lastRead{b: wire}, 64<<20)
	for range 3 {
		if !fr.Scan() {
			t.Fatalf("no line: %v", fr.Err())
		}
	}
	if fr.Scan() {
		t.Fatal("a line out of half a frame")
	}
	if fr.aheadActivity() {
		t.Fatal("the reply read first taken for one ahead after the buffer was compacted")
	}
}
