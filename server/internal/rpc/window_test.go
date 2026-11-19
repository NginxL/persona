package rpc

import (
	"testing"
	"time"
)

// fill sends bulk frames of the window's size until it is full, all from at; it returns the last seq.
func (w *creditWindow) fillFrom(seq uint64, at time.Time) uint64 {
	for w.canSend(w.frameSize()) {
		seq++
		w.wrote(seq, w.frameSize(), at, true)
	}
	return seq
}

func TestBulkWindowGrowsOnAnIdleLink(t *testing.T) {
	w := newCreditWindow()
	now := time.Unix(1_000_000, 0)
	seq := uint64(0)
	for range 20 {
		seq = w.fillFrom(seq, now)
		if !w.limited {
			t.Fatal("a full window was not noted")
		}
		now = now.Add(10 * time.Millisecond) // a round trip with no queue
		w.acked(seq, clockMS(now), now)
	}
	if w.cwnd != windowMax {
		t.Fatalf("window %d after 20 quiet round trips, want %d", w.cwnd, windowMax)
	}
	if w.inFlight != 0 || len(w.sent) != 0 {
		t.Fatalf("in flight %d, %d entries", w.inFlight, len(w.sent))
	}
	if w.frameSize() != framePayloadMax {
		t.Fatalf("frame %d", w.frameSize())
	}
}

// Round trips that grow past the link's own by more than queueTarget shrink the window, at most once a round trip,
// never below windowMin; frames shrink with it.
func TestBulkWindowShrinksWhenFramesQueue(t *testing.T) {
	w := newCreditWindow()
	w.cwnd = 1 << 20
	now := time.Unix(1_000_000, 0)
	w.wrote(1, 1000, now, true)
	w.acked(1, clockMS(now.Add(400*time.Millisecond)), now.Add(400*time.Millisecond)) // the link's own round trip
	before := w.cwnd
	seq := uint64(1)
	for i := 0; i < 3; i++ {
		seq++
		w.wrote(seq, 1000, now, true)
	}
	now = now.Add(time.Second)
	w.acked(seq-2, clockMS(now), now) // 1 s: 600 ms queued
	if w.cwnd >= before {
		t.Fatalf("window %d did not shrink", w.cwnd)
	}
	cut := w.cwnd
	w.acked(seq-1, clockMS(now.Add(10*time.Millisecond)), now.Add(10*time.Millisecond)) // the same round trip: no second cut
	if w.cwnd != cut {
		t.Fatalf("cut twice in a round trip: %d → %d", cut, w.cwnd)
	}
	for range 100 {
		seq++
		w.wrote(seq, 1000, now, true)
		now = now.Add(3 * time.Second)
		w.acked(seq, clockMS(now), now)
	}
	if w.cwnd != windowMin || w.frameSize() != bulkFrameMin {
		t.Fatalf("window %d, frame %d", w.cwnd, w.frameSize())
	}
}

// A window bulk did not fill does not grow: a burst later would find it larger than the link has shown it can take.
func TestBulkWindowGrowsOnlyWhenItWasTheLimit(t *testing.T) {
	w := newCreditWindow()
	now := time.Unix(1_000_000, 0)
	for i := uint64(1); i <= 50; i++ {
		w.wrote(i, 1000, now, true)
		now = now.Add(5 * time.Millisecond)
		w.acked(i, clockMS(now), now)
	}
	if w.cwnd != windowInit {
		t.Fatalf("window %d, want %d", w.cwnd, windowInit)
	}
}

func TestBulkWindowCanSend(t *testing.T) {
	w := newCreditWindow()
	if !w.canSend(10 * windowInit) {
		t.Fatal("nothing in flight: one frame of any size may go")
	}
	w.wrote(1, windowInit-100, time.Now(), true)
	if w.canSend(200) || !w.limited {
		t.Fatal("over the window")
	}
	if !w.canSend(100) {
		t.Fatal("exactly the window")
	}
}

func TestBulkWindowEntriesStayBounded(t *testing.T) {
	w := newCreditWindow()
	start := time.Unix(1_000_000, 0)
	for i := uint64(1); i <= 3*maxSentEntries; i++ {
		w.wrote(i, 10, start.Add(time.Duration(i)), true)
	}
	if len(w.sent) > maxSentEntries {
		t.Fatalf("%d entries", len(w.sent))
	}
	w.acked(3*maxSentEntries, clockMS(start.Add(time.Second)), start.Add(time.Second))
	if w.inFlight != 0 || len(w.sent) != 0 {
		t.Fatalf("in flight %d after acknowledging everything", w.inFlight)
	}
}

func TestBaseDelayForgetsOldMinutesAndWraps(t *testing.T) {
	var b baseDelay
	now := time.Unix(1_000_000*60, 0)
	b.add(50, now)
	b.add(300, now.Add(time.Minute))
	if b.min() != 50 {
		t.Fatalf("%d", b.min())
	}
	b.add(300, now.Add(baseSlots*time.Minute))
	if b.min() != 300 {
		t.Fatalf("a sample %d minutes old still counts: %d", baseSlots, b.min())
	}
	// Two clocks far apart: delays near the top of the range and past its wrap compare by their difference.
	var w baseDelay
	w.add(1<<32-10, now)
	w.add(5, now) // 15 ms later than the first, past the wrap
	if w.min() != 1<<32-10 {
		t.Fatalf("wrapped: %d", w.min())
	}
}

// Short frames (short lines, a long line's last piece) and full ones are judged against bases of their own kind: next
// to a base short frames set, full frames' own time on the wire read as queueing and the window never opened on a slow
// link (13 KB/s on 30 KB/s in #10660's lane experiment), and with no samples from short frames a window of short lines
// alone never grew (a review: 238 KiB/s where lines did 1920).
func TestCreditWindowJudgesFramesByTheirKind(t *testing.T) {
	t.Run("full frames next to short ones", func(t *testing.T) {
		w := newCreditWindow()
		now := time.Unix(1_000_000, 0)
		w.wrote(1, 50, now, false)
		w.acked(1, clockMS(now.Add(400*time.Millisecond)), now.Add(400*time.Millisecond)) // a short frame: the link's delay alone
		seq := w.fillFrom(1, now)
		w.acked(seq, clockMS(now.Add(540*time.Millisecond)), now.Add(540*time.Millisecond)) // full frames: the delay and their own time on the wire
		if w.base[1].min() != 540 || w.cwnd <= windowInit {
			t.Fatalf("full frames: base %d ms, window %d", w.base[1].min(), w.cwnd)
		}
	})
	t.Run("short frames alone", func(t *testing.T) {
		w := newCreditWindow()
		now := time.Unix(1_000_000, 0)
		seq := uint64(0)
		for range 10 { // a window of 6 KiB lines each round trip, no queue
			for w.canSend(6 << 10) {
				seq++
				w.wrote(seq, 6<<10, now, false)
			}
			now = now.Add(200 * time.Millisecond)
			w.acked(seq, clockMS(now), now)
		}
		if w.cwnd < 16*windowInit {
			t.Fatalf("window %d after 10 quiet round trips of short frames", w.cwnd)
		}
	})
}

// Past maxSentEntries the newest entry takes the next frames, and their time: its round trip is the newest frame's,
// not the time since the first frame it took.
func TestCreditWindowMergedEntryTakesTheNewestTime(t *testing.T) {
	w := newCreditWindow()
	start := time.Unix(1_000_000, 0)
	for i := uint64(1); i <= maxSentEntries; i++ {
		w.wrote(i, 10, start, true)
	}
	later := start.Add(10 * time.Second)
	w.wrote(maxSentEntries+1, 10, later, true)
	w.acked(maxSentEntries+1, clockMS(later.Add(100*time.Millisecond)), later.Add(100*time.Millisecond))
	if w.rtt != 100*time.Millisecond {
		t.Fatalf("round trip %s, want the newest frame's 100ms", w.rtt)
	}
}

// Acknowledgements bunched behind what the other side sends make the round trip long, longer and longer here; the
// window is judged by the delay one way, and keeps what the other side reads in a round trip in flight, so this
// direction stays busy (judged by the round trip, the growing wait on the way back read as queueing).
func TestCreditWindowAcrossABusyWayBack(t *testing.T) {
	const rate = 30_000 // bytes a second the link carries this way
	w := newCreditWindow()
	now := time.Unix(1_000_000, 0)
	seq := uint64(0)
	var back time.Duration
	for round := range 8 {
		back = 2*time.Second + time.Duration(round)*1500*time.Millisecond // acknowledgements wait this long on the way back
		start := now
		var sent []sentFrame
		for w.canSend(w.frameSize()) {
			seq++
			n := w.frameSize()
			w.wrote(seq, n, start, true)
			sent = append(sent, sentFrame{seq: seq, bytes: n, at: start})
		}
		read := start.Add(200 * time.Millisecond) // one way, then each frame in its turn at the link's rate
		for _, f := range sent {
			read = read.Add(time.Duration(f.bytes) * time.Second / rate)
			now = read.Add(back)
			w.acked(f.seq, clockMS(read), now)
		}
	}
	if w.rate < rate*8/10 || w.rate > rate*12/10 {
		t.Fatalf("delivery rate %.0f, the link's %d", w.rate, rate)
	}
	if need := int(rate * back.Seconds() * 8 / 10); w.cwnd < need {
		t.Fatalf("window %d, under the %d the link carries in a round trip", w.cwnd, need)
	}
	if w.frameSize() > bulkFrameMin*2 {
		t.Fatalf("frames of %d on a %d B/s link", w.frameSize(), rate)
	}
}

// wayBack is what the other side sends while a simulated window sends bulk (simWayBack).
type wayBack struct {
	frame int           // its frames' size; 0: nothing, and each frame is acknowledged as it is read
	queue int           // the most it keeps waiting
	stop  time.Duration // when it stops sending (0: never), its queue draining then; until then it keeps queue waiting
}

// simWayBack runs w for d against a link of rate bytes a second and oneWay delay each way whose other direction
// carries back: an acknowledgement goes out when the other side's frame being written ends, naming the newest frame
// read by then, and waits behind what is queued, which drains and fills again (no faster than a frame a frame) unless
// back.stop is set. The window always has bulk to send. It returns what the other side read after warmUp, a second,
// and the longest a frame written after warmUp waited to go on the link.
func simWayBack(w *creditWindow, rate float64, oneWay time.Duration, back wayBack, d, warmUp time.Duration) (float64, time.Duration) {
	type inFlight struct {
		seq  uint64
		read time.Time // the other side reads it
	}
	type ack struct {
		seq   uint64
		read  time.Time
		leave time.Time // it goes out
		at    time.Time // it arrives
	}
	perByte := float64(time.Second) / rate
	frameTime := time.Duration(float64(back.frame) * perByte)
	backQueue := time.Duration(float64(back.queue) * perByte)
	start := time.Unix(1_000_000, 0)
	free := start // when the link is free to start the next frame
	var frames []inFlight
	var acks []ack
	seq := uint64(0)
	read := 0
	var worst time.Duration
	for now := start; now.Before(start.Add(d)); now = now.Add(time.Millisecond) {
		for len(acks) > 0 && !acks[0].at.After(now) {
			w.acked(acks[0].seq, clockMS(acks[0].read), now)
			acks = acks[1:]
		}
		for len(frames) > 0 && !frames[0].read.After(now) {
			f := frames[0]
			frames = frames[1:]
			// Acknowledged when the other side's frame being written ends, behind the queue.
			leave, waits := f.read, time.Duration(0)
			if frameTime > 0 {
				n := f.read.Sub(start)/frameTime + 1
				leave = start.Add(n * frameTime)
				half := (backQueue + frameTime - 1) / frameTime
				switch k := n % (2*half + 1); {
				case back.stop > 0 && leave.Before(start.Add(back.stop)):
					waits = backQueue
				case back.stop > 0:
					if waits = backQueue - leave.Sub(start.Add(back.stop)); waits <= 0 { // drained: nothing to wait for
						leave, waits = f.read, 0
					}
				case k <= half:
					waits = max(backQueue-k*frameTime, 0)
				default:
					waits = min((k-half)*frameTime, backQueue)
				}
			}
			a := ack{seq: f.seq, read: f.read, leave: leave, at: leave.Add(waits + oneWay)}
			if k := len(acks); k > 0 && acks[k-1].leave.Equal(leave) {
				a.at = acks[k-1].at
				acks[k-1] = a // one acknowledgement for what was read before it went out: of the newest frame
			} else {
				if k > 0 {
					a.at = maxTime(a.at, acks[k-1].at) // in order
				}
				acks = append(acks, a)
			}
		}
		for {
			n := w.frameSize()
			if !w.canSend(n) {
				break
			}
			seq++
			w.wrote(seq, n, now, true)
			begin := maxTime(free, now)
			if now.Sub(start) >= warmUp {
				worst = max(worst, begin.Sub(now))
			}
			free = begin.Add(time.Duration(float64(n) * perByte))
			frames = append(frames, inFlight{seq: seq, read: free.Add(oneWay)})
			if free.Sub(start) >= warmUp && free.Sub(start) < d {
				read += n
			}
		}
	}
	return float64(read) / (d - warmUp).Seconds(), worst
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// TestCreditWindowKeepsTheLinkBusyWhenTheWayBackIsFull: a port's bytes go one way while the other side's events keep
// the way back busy, so acknowledgements wait behind them, each covering whatever was read while the other side wrote
// a frame (#10660's review: the window, judged by round trips, let through 5.5 KB/s of a 30 KB/s link). Frames still
// keep the link busy, with that queue full and with it draining and filling again.
func TestCreditWindowKeepsTheLinkBusyWhenTheWayBackIsFull(t *testing.T) {
	const rate = 30_000
	for _, queue := range []int{0, 64 << 10, 256 << 10} {
		w := newCreditWindow()
		if got, _ := simWayBack(&w, rate, 200*time.Millisecond, wayBack{frame: 32 << 10, queue: queue, stop: time.Hour}, time.Minute, 30*time.Second); got < rate*97/100 {
			t.Errorf("%d KiB queued on the way back: %.0f B/s of a %d B/s link", queue>>10, got, rate)
		}
		w = newCreditWindow()
		if got, _ := simWayBack(&w, rate, 200*time.Millisecond, wayBack{frame: 32 << 10, queue: queue}, 2*time.Minute, 30*time.Second); got < rate*95/100 {
			t.Errorf("%d KiB draining and filling on the way back: %.0f B/s of a %d B/s link", queue>>10, got, rate)
		}
	}
}

// TestCreditWindowWhenTheWayBackDrains: when the other side stops sending, the acknowledgements its queue held come
// quicker than the frames were read; a window still sized for the old wait put up to that queue's worth on the way, ahead
// of pings, for up to 10 s (#10660's second review: 10 s at 256 KiB). Frames wait about as long as acknowledgements
// bunched behind the other side's frames did, and once its queue is gone, as on a plain link.
func TestCreditWindowWhenTheWayBackDrains(t *testing.T) {
	const rate = 30_000
	for _, queue := range []int{64 << 10, 256 << 10} {
		back := wayBack{frame: 32 << 10, queue: queue, stop: time.Minute}
		w := newCreditWindow()
		if _, worst := simWayBack(&w, rate, 200*time.Millisecond, back, 90*time.Second, back.stop); worst > 2*time.Second {
			t.Errorf("%d KiB draining on the way back: a frame waited %s to go out", queue>>10, worst)
		}
		drained := back.stop + time.Duration(float64(queue)/rate*float64(time.Second))
		w = newCreditWindow()
		if _, worst := simWayBack(&w, rate, 200*time.Millisecond, back, 90*time.Second, drained+2*time.Second); worst > 700*time.Millisecond {
			t.Errorf("%d KiB drained on the way back: a frame waited %s to go out 2 s later", queue>>10, worst)
		}
	}
}

// TestCreditWindowOnAPlainLink: with nothing in the way back the window keeps the link busy and what waits on the way
// short.
func TestCreditWindowOnAPlainLink(t *testing.T) {
	for _, c := range []struct {
		rate  float64
		worst time.Duration
	}{{30_000, 600 * time.Millisecond}, {1_000_000, 200 * time.Millisecond}} {
		w := newCreditWindow()
		got, worst := simWayBack(&w, c.rate, 200*time.Millisecond, wayBack{}, 2*time.Minute, 30*time.Second)
		if got < c.rate*95/100 {
			t.Errorf("%.0f B/s link: %.0f B/s", c.rate, got)
		}
		if worst > c.worst {
			t.Errorf("%.0f B/s link: a frame waited %s to go out, want under %s", c.rate, worst, c.worst)
		}
	}
}

// TestCreditWindowGrowsLittlePastTheLink: once the window knows what the other side reads, it grows no further than
// what it reads in queueTarget past what a round trip needs, however short the queue looks: on a long round trip the
// queue shows only a round trip later.
func TestCreditWindowGrowsLittlePastTheLink(t *testing.T) {
	w := newCreditWindow()
	now := time.Unix(1_000_000, 0)
	w.cwnd, w.rate = 40_000, 30_000 // a round trip of 400 ms needs 15 KB, 18 KB with queueTarget's
	for seq := uint64(1); seq <= 8; seq++ {
		w.wrote(seq, 4096, now, true)
	}
	w.limited = true                                                                  // the window held frames back
	w.acked(8, clockMS(now.Add(200*time.Millisecond)), now.Add(400*time.Millisecond)) // nothing queued
	if w.cwnd != 40_000 {
		t.Fatalf("window %d after a round trip with no queue, want it kept at 40000", w.cwnd)
	}
}

// TestCreditWindowSpanStartsWhenTheBytesCouldArrive: an acknowledgement covers what was read since the last one, but
// not the time before its bytes were even sent: after a pause, the window stays what the link needs.
func TestCreditWindowSpanStartsWhenTheBytesCouldArrive(t *testing.T) {
	w := newCreditWindow()
	now := time.Unix(1_000_000, 0)
	w.rate = 30_000
	w.wrote(1, 4096, now, true)
	w.acked(1, clockMS(now.Add(200*time.Millisecond)), now.Add(400*time.Millisecond))
	later := now.Add(5 * time.Second) // nothing to send meanwhile
	w.rates[0] = rateSample{rate: 30_000, at: later}
	w.wrote(2, 4096, later, true)
	w.wrote(3, 4096, later, true)
	w.acked(3, clockMS(later.Add(500*time.Millisecond)), later.Add(700*time.Millisecond))
	if w.cwnd > 64<<10 {
		t.Fatalf("window %d after a pause, on a link that carries 30 KB/s with a 400 ms round trip", w.cwnd)
	}
}
