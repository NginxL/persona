package rpc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// laneWriter writes a framed connection (lanes.go). The peer's writers queue their lines on it and wait; one goroutine
// (run) cuts them into frames and writes them, each write a batch of up to frameMax bytes: the acknowledgement due
// first, then frames by lane in strict priority, a frame for each line of a lane in turn. A line once started is
// written to its end, as a line on the one writer was; one not started yet can still be withdrawn (NotSent).
type laneWriter struct {
	w      io.Writer
	closed <-chan struct{} // the peer's
	fail   func(error)     // closes the peer: a write failed, or the other side broke the protocol
	filter func([]byte) []byte
	wrote  *atomic.Int64 // when a write last went through (Peer.LastWrite); nil: not kept

	mu      sync.Mutex
	wake    chan struct{} // 1: lines queued, credit or an acknowledgement due
	busy    bool          // a write is under way
	err     error         // why the writer stopped; nil while it goes on
	lanes   [laneCount]laneQueue
	seq     uint64 // the last data frame numbered
	ackDue  uint64 // the reader has read every frame through this one, and wants it acknowledged
	ackAt   uint32 // when it read it (clockMS)
	ackSent uint64
	win     creditWindow
	totals  [laneCount]laneTotals
}

// laneSlots is how many lines of one lane may be in progress at once (laneQueue.slots); a frame's slot says which.
const laneSlots = 16

var newline = []byte("\n")

type laneQueue struct {
	lines []*outLine // waiting or in progress, in turn order
	next  int        // whose turn it is
	slots uint16     // slots in use, one bit each
}

type laneTotals struct {
	frames  int64
	maxWait time.Duration
}

// outLine is a line queued on a laneWriter.
type outLine struct {
	b       []byte // the line; once it starts, as it goes out: filtered, its newline dropped
	off     int    // written so far
	lane    Lane
	credit  bool // it waits for credit (route)
	slot    uint16
	started bool
	queued  time.Time
	done    chan error // told once: nil when the last frame is written, or why it never will be
}

func newLaneWriter(w io.Writer, closed <-chan struct{}, fail func(error), filter func([]byte) []byte) *laneWriter {
	return &laneWriter{w: w, closed: closed, fail: fail, filter: filter, wake: make(chan struct{}, 1), win: newCreditWindow()}
}

func (lw *laneWriter) poke() {
	select {
	case lw.wake <- struct{}{}:
	default:
	}
}

// start begins writing: from now on everything this side sends is frames.
func (lw *laneWriter) start() { go lw.run() }

// enqueue queues l; delayed: something was being written or waiting already.
func (lw *laneWriter) enqueue(l *outLine) (delayed bool, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.err != nil {
		return false, lw.err
	}
	delayed = lw.busy
	for i := range lw.lanes {
		delayed = delayed || len(lw.lanes[i].lines) > 0
	}
	q := &lw.lanes[l.lane]
	q.lines = append(q.lines, l)
	lw.poke()
	return delayed, nil
}

// withdraw takes l back if none of it went out yet.
func (lw *laneWriter) withdraw(l *outLine) bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if !l.started {
		lw.lanes[l.lane].drop(l) // not found: the writer stopped and let it go
	}
	return !l.started
}

// drop takes l out of the turn order, which goes on with the line after it.
func (q *laneQueue) drop(l *outLine) {
	for i, x := range q.lines {
		if x == l {
			q.lines = append(q.lines[:i], q.lines[i+1:]...)
			if i < q.next {
				q.next--
			}
			if q.next >= len(q.lines) {
				q.next = 0
			}
			return
		}
	}
}

// ack asks for an acknowledgement of every frame read through seq, read at at (clockMS).
func (lw *laneWriter) ack(seq uint64, at uint32) {
	lw.mu.Lock()
	if seq > lw.ackDue {
		lw.ackDue, lw.ackAt = seq, at
		lw.poke()
	}
	lw.mu.Unlock()
}

// acked takes the other side's acknowledgement of every frame through seq, the last read at at by its clock.
func (lw *laneWriter) acked(seq uint64, at uint32) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if seq > lw.seq {
		return fmt.Errorf("%w: acknowledges frame %d, %d sent", ErrFrame, seq, lw.seq)
	}
	lw.win.acked(seq, at, time.Now())
	if len(lw.lanes[LaneInteractive].lines)+len(lw.lanes[LaneBulk].lines) > 0 { // what may wait for credit
		lw.poke()
	}
	return nil
}

func (lw *laneWriter) run() {
	buf := make([]byte, 0, frameMax)
	var finished []*outLine
	for {
		select {
		case <-lw.closed: // nothing more goes out on a closed peer, however much is queued
			lw.stop(ErrClosed)
			return
		default:
		}
		lw.mu.Lock()
		var batch []byte
		batch, finished = lw.fillLocked(buf[:0], finished[:0], time.Now())
		if len(batch) == 0 {
			lw.mu.Unlock()
			select {
			case <-lw.wake:
			case <-lw.closed:
				lw.stop(ErrClosed)
				return
			}
			continue
		}
		lw.busy = true
		lw.mu.Unlock()
		n, err := lw.w.Write(batch)
		if err == nil && n != len(batch) {
			err = io.ErrShortWrite
		}
		if err == nil && lw.wrote != nil {
			lw.wrote.Store(time.Now().UnixNano())
		}
		lw.mu.Lock()
		lw.busy = false
		for _, l := range finished {
			l.done <- err
		}
		lw.mu.Unlock()
		if err != nil {
			lw.stop(err)
			lw.fail(err)
			return
		}
	}
}

// stop ends the writer: every line still queued is told err.
func (lw *laneWriter) stop(err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.err == nil {
		lw.err = err
	}
	for i := range lw.lanes {
		q := &lw.lanes[i]
		for _, l := range q.lines {
			if l.started {
				l.done <- lw.err
			} else {
				l.done <- NotSent(lw.err) // none of it went out
			}
		}
		*q = laneQueue{}
	}
}

// fillLocked makes the next write: the acknowledgement due, then frames in priority order while they fit in frameMax
// (the first always does). finished is the lines whose last frame it holds.
func (lw *laneWriter) fillLocked(batch []byte, finished []*outLine, now time.Time) ([]byte, []*outLine) {
	if lw.ackDue > lw.ackSent {
		var h [frameHeader + ackPayload]byte
		putHead(h[:], frameHead{kind: frameAck, size: ackPayload, seq: lw.ackDue})
		binary.BigEndian.PutUint32(h[frameHeader:], lw.ackAt)
		batch = append(batch, h[:]...)
		lw.ackSent = lw.ackDue
	}
	for {
		l, n, ok := lw.pickLocked(frameMax-len(batch), len(batch) == 0, now)
		if !ok {
			break
		}
		lw.seq++
		fin := l.off+n == len(l.b)
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: l.lane, fin: fin, slot: l.slot, size: n, seq: lw.seq})
		batch = append(append(batch, h[:]...), l.b[l.off:l.off+n]...)
		l.off += n
		lw.totals[l.lane].frames++
		if l.credit {
			lw.win.wrote(lw.seq, n, now, n == lw.most(l))
		}
		if fin {
			q := &lw.lanes[l.lane]
			q.slots &^= 1 << l.slot
			q.drop(l)
			finished = append(finished, l)
		}
	}
	return batch, finished
}

// pickLocked is the next frame: its line, advanced to its turn, and its size; ok false when there is none, or when
// the next one would not fit in room (and the batch is not empty: first). Within a lane, lines that fit in one frame
// take turns with the lane's oldest long line; the other long lines wait until it is through, as they would on the
// one writer: taking turns among them would finish every one of them only at the end, each later than its caller can
// wait (concurrent file chunks, each a minute's worth on a slow link).
func (lw *laneWriter) pickLocked(room int, first bool, now time.Time) (*outLine, int, bool) {
	for lane := LaneControl; lane < laneCount; lane++ {
		q := &lw.lanes[lane]
		if len(q.lines) == 0 {
			continue
		}
		long := q.long(lw.most, nil)
		var beside *outLine
		if long != nil && long.credit && !lw.win.room(min(long.left(), lw.most(long))) {
			// It waits for credit: the oldest long line that needs none (an answer) goes beside it, which a read loop
			// waiting for it (AfterReply) must never wait for credit through.
			beside = q.long(lw.most, func(l *outLine) bool { return !l.credit })
		}
		for k := 0; k < len(q.lines); k++ {
			i := (q.next + k) % len(q.lines)
			l := q.lines[i]
			most := lw.most(l)
			if l != long && l != beside && l.left() > most {
				continue // a long line behind the one going out
			}
			if !l.started && q.slots == 1<<laneSlots-1 {
				continue // every slot is busy with a line in progress: this one waits for its turn
			}
			n := min(l.left(), most)
			if l.credit && !lw.win.canSend(n) {
				continue // no credit for it; a line of the lane that needs none may go
			}
			if !first && frameHeader+n > room {
				return nil, 0, false // the next frame in priority goes first in the next write
			}
			if !l.started {
				// Filtered as it goes on the wire, as a line on the one writer was: a value handed over (node/secretenv.go)
				// while the line waited is hidden too. Under the lock, the scan holds up the writers and the reader's
				// acknowledgements for as long as it takes, only while values are handed over.
				if lw.filter != nil {
					l.b = lw.filter(l.b)
				}
				l.b = bytes.TrimSuffix(l.b, newline)
				n = min(len(l.b), most)
				l.slot = uint16(bits.TrailingZeros16(^q.slots)) // a free one: not every slot is busy (above)
				q.slots |= 1 << l.slot
				l.started = true
				lw.totals[lane].maxWait = max(lw.totals[lane].maxWait, now.Sub(l.queued))
			}
			q.next = (i + 1) % len(q.lines)
			return l, n, true
		}
	}
	return nil, 0, false
}

// left is what of l is still to go; before it starts, before filter, which may change it a little: a hint is enough.
func (l *outLine) left() int {
	if l.started {
		return len(l.b) - l.off
	}
	return len(bytes.TrimSuffix(l.b, newline))
}

// long is the lane's long line whose frames go now, among those only takes (nil: all): the one in progress, else the
// oldest that needs more than one frame (most); nil when there is none.
func (q *laneQueue) long(most func(*outLine) int, only func(*outLine) bool) *outLine {
	var oldest *outLine
	for _, l := range q.lines {
		if only != nil && !only(l) {
			continue
		}
		if l.started {
			return l // a line started and not through is a long one: a short one starts and ends in one frame
		}
		if oldest == nil && l.left() > most(l) {
			oldest = l
		}
	}
	return oldest
}

// LaneStats is a framed connection's writer as it is now (Peer.LaneStats).
type LaneStats struct {
	Framed   bool // this side's output is framed
	Lanes    [laneCount]LaneCounters
	Window   int           // bytes of the lanes with credit (interactive, bulk) that may be in flight
	InFlight int           // those sent and not acknowledged
	RTT      time.Duration // the latest round trip of such a frame
	Queued   time.Duration // how long the latest such frame spent queued on its way
}

// LaneCounters is one lane's: what waits now, the frames sent, and the longest a line waited for its first frame (one
// still waiting included).
type LaneCounters struct {
	QueuedFrames int
	QueuedBytes  int64
	SentFrames   int64
	MaxWait      time.Duration
}

// String is the counters on one line, for a log, each field per lane in priority order (control/interactive/events/
// bulk): "lanes=on queued=0/1/0/3 bytes=0B/1KiB/0B/96KiB maxwait=1ms/40ms/2ms/1.2s sent=12/340/88/51 window=64KiB
// inflight=32KiB rtt=410ms queued=30ms"; "lanes=off" for a connection of lines.
func (s LaneStats) String() string {
	if !s.Framed {
		return "lanes=off"
	}
	var queued, size, wait, sent [laneCount]string
	for i, c := range s.Lanes {
		queued[i] = fmt.Sprint(c.QueuedFrames)
		size[i] = sizeText(c.QueuedBytes)
		wait[i] = c.MaxWait.Round(time.Millisecond).String()
		sent[i] = fmt.Sprint(c.SentFrames)
	}
	return fmt.Sprintf("lanes=on queued=%s bytes=%s maxwait=%s sent=%s window=%s inflight=%s rtt=%s queued=%s",
		strings.Join(queued[:], "/"), strings.Join(size[:], "/"), strings.Join(wait[:], "/"), strings.Join(sent[:], "/"),
		sizeText(int64(s.Window)), sizeText(int64(s.InFlight)), s.RTT.Round(time.Millisecond), s.Queued.Round(time.Millisecond))
}

func sizeText(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 10<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	}
	return fmt.Sprintf("%dB", n)
}

// most is the most a frame of l carries now: a quarter of the window when it waits for credit.
func (lw *laneWriter) most(l *outLine) int {
	if l.credit {
		return lw.win.frameSize()
	}
	return framePayloadMax
}

func (lw *laneWriter) stats() LaneStats {
	now := time.Now()
	lw.mu.Lock()
	defer lw.mu.Unlock()
	s := LaneStats{Framed: true, Window: lw.win.cwnd, InFlight: lw.win.inFlight, RTT: lw.win.rtt, Queued: lw.win.queued}
	for i := range lw.lanes {
		c := &s.Lanes[i]
		c.SentFrames, c.MaxWait = lw.totals[i].frames, lw.totals[i].maxWait
		for _, l := range lw.lanes[i].lines {
			left, most := len(l.b)-l.off, lw.most(l)
			c.QueuedBytes += int64(left)
			c.QueuedFrames += max((left+most-1)/most, 1)
			if !l.started {
				c.MaxWait = max(c.MaxWait, now.Sub(l.queued))
			}
		}
	}
	return s
}
