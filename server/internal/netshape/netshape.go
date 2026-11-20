// Package netshape is an in-process connection shaped like a slow link, for tests and debug tools: what a real
// network does to a protocol (head-of-line blocking, round trips, back-pressure) without root or the system's traffic
// control.
//
// Each direction is a bottleneck: writes go into a queue of at most Config.Queue bytes and wait while it is full, the
// way a TCP sender waits on its buffer; bytes leave the queue one after another at Config.Rate; each can be read
// Config.Delay after it left. The receiving end holds at most Config.Queue bytes that have arrived and not been read:
// past that the bottleneck stops sending, so a reader that stops reading stops the writer too. Order is kept and
// nothing is lost. A design like cmd/vncbench's shaped transport, around no real connection.
package netshape

import (
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Config shapes one direction of a Pipe.
type Config struct {
	Rate  float64       // bytes a second leaving the bottleneck; 0 sends them at once
	Delay time.Duration // one way: a byte can be read this long after it left the bottleneck
	Queue int           // bytes the bottleneck holds before Write blocks; 0 is DefaultQueue
}

// DefaultQueue is the queue of a Config that names none.
const DefaultQueue = 64 << 10

// tick is about how long a piece the bottleneck sends at a time takes at its rate: timers in pieces, not per byte.
const tick = 4 * time.Millisecond

// maxFlight bounds the bytes on the way at Rate 0, where nothing else does while Delay holds them back.
const maxFlight = 16 << 20

// Pipe is a connection whose end a's writes reach end b shaped by ab, and b's reach a shaped by ba. Each direction
// runs one goroutine: closing both ends stops both, and each Close returns once its incoming direction has stopped.
// Deadlines work as net.Conn's do; the addresses are placeholders.
func Pipe(ab, ba Config) (a, b net.Conn) {
	toB, toA := newLink(ab), newLink(ba)
	return &conn{in: toA, out: toB, local: "a", remote: "b"}, &conn{in: toB, out: toA, local: "b", remote: "a"}
}

// link is one direction: its writing end's queue at the bottleneck, and what left it on the way to the reading end.
type link struct {
	cfg Config
	wmu sync.Mutex // one Write at a time, so that concurrent writes do not interleave

	mu      sync.Mutex
	changed chan struct{} // closed and replaced whenever what follows changes (signal)
	queue   []byte        // waiting at the bottleneck
	transit []segment     // left it, in order, each readable from its time
	flight  int           // bytes in transit
	free    time.Time     // when the bottleneck has sent the last piece it took
	wclosed bool          // the writing end closed: the reader gets io.EOF once everything has arrived
	rclosed bool          // the reading end closed: what was on its way is dropped, and writes fail
	rdl     time.Time     // the reading end's read deadline
	wdl     time.Time     // the writing end's write deadline
	pumped  chan struct{} // closed when pump returns
}

type segment struct {
	b  []byte
	at time.Time // when it has arrived
}

func newLink(cfg Config) *link {
	if cfg.Queue <= 0 {
		cfg.Queue = DefaultQueue
	}
	l := &link{cfg: cfg, changed: make(chan struct{}), pumped: make(chan struct{})}
	go l.pump()
	return l
}

// signal wakes whoever waits on l. l.mu is held.
func (l *link) signal() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// wait lets go of l.mu until l changes or until passes (zero: no limit), then takes it again.
func (l *link) wait(until time.Time) {
	ch := l.changed
	l.mu.Unlock()
	defer l.mu.Lock()
	if until.IsZero() {
		<-ch
		return
	}
	d := time.Until(until)
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

// earliest is the earlier of two times, a zero one being none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// arrived is how many bytes have arrived by now and are not read yet. l.mu is held.
func (l *link) arrived(now time.Time) int {
	n := 0
	for _, s := range l.transit {
		if s.at.After(now) {
			break
		}
		n += len(s.b)
	}
	return n
}

// full: the reader holds all it may, or at Rate 0 maxFlight bytes are on the way. l.mu is held.
func (l *link) full(now time.Time) bool {
	return l.arrived(now) >= l.cfg.Queue || l.cfg.Rate <= 0 && l.flight >= maxFlight
}

// pump moves the queue through the bottleneck, a piece at a time, until the writing end has closed and the queue is
// empty or the reading end has closed.
func (l *link) pump() {
	defer close(l.pumped)
	l.mu.Lock()
	defer l.mu.Unlock()
	idle := true // nothing sent yet
	for {
		for !l.rclosed && (len(l.queue) == 0 && !l.wclosed || len(l.queue) > 0 && l.full(time.Now())) {
			l.wait(time.Time{}) // nothing to send, or the reader holds all it may
			idle = true
		}
		if l.rclosed || len(l.queue) == 0 {
			return
		}
		now := time.Now()
		if l.cfg.Rate <= 0 || idle && l.free.Before(now) {
			l.free = now // nothing to wait for, or the link was idle: the next piece starts now
		}
		idle = false
		if l.cfg.Rate > 0 && now.Before(l.free) {
			l.wait(l.free) // still sending the last piece
			continue
		}
		// Busy since the last piece: this one starts when that one ended, even if the timer woke late, so lateness
		// never adds up to a lower rate.
		k := len(l.queue)
		if l.cfg.Rate > 0 {
			k = min(k, max(1, int(l.cfg.Rate*tick.Seconds())))
		}
		piece := append([]byte(nil), l.queue[:k]...)
		if k == len(l.queue) {
			l.queue = l.queue[:0]
		} else {
			l.queue = l.queue[k:]
		}
		if l.cfg.Rate > 0 {
			l.free = l.free.Add(time.Duration(float64(k) / l.cfg.Rate * float64(time.Second)))
		}
		l.transit = append(l.transit, segment{b: piece, at: l.free.Add(l.cfg.Delay)})
		l.flight += k
		l.signal()
	}
}

func (l *link) write(b []byte) (int, error) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for {
		switch {
		case l.wclosed:
			return n, net.ErrClosed
		case l.rclosed:
			return n, io.ErrClosedPipe
		case !l.wdl.IsZero() && !time.Now().Before(l.wdl):
			return n, os.ErrDeadlineExceeded
		case n == len(b):
			return n, nil
		}
		if room := l.cfg.Queue - len(l.queue); room > 0 {
			k := min(room, len(b)-n)
			l.queue = append(l.queue, b[n:n+k]...)
			n += k
			l.signal()
			if n == len(b) {
				return n, nil
			}
			continue
		}
		l.wait(l.wdl)
	}
}

func (l *link) read(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		now := time.Now()
		switch {
		case l.rclosed:
			return 0, net.ErrClosed
		case !l.rdl.IsZero() && !now.Before(l.rdl):
			return 0, os.ErrDeadlineExceeded
		case len(b) == 0:
			return 0, nil
		}
		n := 0
		for n < len(b) && len(l.transit) > 0 && !l.transit[0].at.After(now) {
			s := &l.transit[0]
			c := copy(b[n:], s.b)
			n += c
			l.flight -= c
			if s.b = s.b[c:]; len(s.b) == 0 {
				l.transit[0] = segment{}
				l.transit = l.transit[1:]
			}
		}
		if n > 0 {
			l.signal() // room at the receiver
			return n, nil
		}
		if len(l.transit) == 0 && len(l.queue) == 0 && l.wclosed {
			return 0, io.EOF
		}
		var next time.Time
		if len(l.transit) > 0 {
			next = l.transit[0].at
		}
		l.wait(earliest(next, l.rdl))
	}
}

// closeWriter ends the writing side: a Write under way returns, what was written still goes through.
func (l *link) closeWriter() {
	l.mu.Lock()
	l.wclosed = true
	l.signal()
	l.mu.Unlock()
}

// closeReader ends the reading side: what was on its way is dropped, and the pump stops.
func (l *link) closeReader() {
	l.mu.Lock()
	l.rclosed = true
	l.queue, l.transit, l.flight = nil, nil, 0
	l.signal()
	l.mu.Unlock()
	<-l.pumped
}

type conn struct {
	in, out       *link
	local, remote addr
	closed        atomic.Bool
}

func (c *conn) Read(b []byte) (int, error)  { return c.in.read(b) }
func (c *conn) Write(b []byte) (int, error) { return c.out.write(b) }

// Close ends both directions at this end: the other end reads what was written here before io.EOF, and its writes
// fail. A second Close answers net.ErrClosed.
func (c *conn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return net.ErrClosed
	}
	c.out.closeWriter()
	c.in.closeReader()
	return nil
}

func (c *conn) LocalAddr() net.Addr  { return c.local }
func (c *conn) RemoteAddr() net.Addr { return c.remote }

func (c *conn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

func (c *conn) SetReadDeadline(t time.Time) error {
	c.in.mu.Lock()
	c.in.rdl = t
	c.in.signal()
	c.in.mu.Unlock()
	return nil
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	c.out.mu.Lock()
	c.out.wdl = t
	c.out.signal()
	c.out.mu.Unlock()
	return nil
}

type addr string

func (addr) Network() string  { return "netshape" }
func (a addr) String() string { return string(a) }
