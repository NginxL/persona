package rpc

import "time"

// creditWindow is how many bytes of interactive requests and bulk lines a framed connection may have sent and not yet
// seen acknowledged (route). It adapts to the link by delay (as LEDBAT does): an acknowledgement says when the other
// side read the last frame it covers, by its clock; with the time the frame was written, by this side's, that is the
// frame's delay one way plus the difference between the two clocks. The smallest such delay of the last few minutes is
// the link's own (the difference cancels out), and what a frame takes beyond it is time it spent queued on its way (in
// the transport's buffers, behind other frames). However long the acknowledgement itself waits on the way back, behind
// what the other side sends, it does not count: a busy other direction does not close this one's window. Below
// queueTarget the window grows, quickly while the queue is short; above it the window shrinks once a round trip. It
// never falls below what the other side reads in a round trip with the queueing left out, nor grows much past it,
// and while frames queue on the way it holds no more than the latest round trip needs (acked). On a link of 30 KB/s the window settles near what the link carries in a round trip, so a pong or an answer
// written behind those frames waits about queueTarget; on a fast link it grows to what keeps the link busy, up to
// windowMax.
//
// Frames with credit are cut to a quarter of the window (frameSize): on a slow link a frame takes long to go out, and
// a frame behind it waits for the whole of it.
type creditWindow struct {
	cwnd        int
	inFlight    int
	sent        []sentFrame  // sent and not acknowledged, oldest first; one entry per frame
	limited     bool         // a frame waited for the window since it last grew
	base        [2]baseDelay // the link's own delay as short frames [0] and full ones [1] see it (sentFrame.full)
	lastCut     time.Time
	rtt         time.Duration // the latest round trip: how often the window may shrink
	queued      time.Duration // the time the latest sample spent queued on its way
	done        int64         // bytes acknowledged so far
	mark        rateMark      // where the delivery rate is measured from
	rates       [rateSlots]rateSample
	rate        float64 // bytes a second the other side has read, the most of the last rateFor (0: not known yet)
	paths       [rateSlots]pathSample
	lastRecv    uint32 // when the latest sample's frame was read, by the other side's clock
	lastRecvSet bool
}

// pathSample is how long the bytes an acknowledgement covers waited from being written to being acknowledged, the time
// they spent queued on the way left out: the link's own delay both ways, however long the acknowledgement waited
// behind the other side's frames, and how long before the frame it names the first of them was read (one
// acknowledgement for whatever was read while the other side was writing something else).
type pathSample struct {
	d  time.Duration
	at time.Time
}

// sentFrame is a frame with credit (or, past maxSentEntries, the frames through seq): its bytes, written from at.
// Only the frame an acknowledgement names is a sample: the other side's time is that frame's.
// full: it was cut to the frame size of its time. Full and shorter frames (short lines, a long one's last piece) are
// judged against bases of their own kind: a short frame takes less time to go out than a full one, and next to a base
// it set, a full frame's own time on the wire looks like queueing (a 4 KiB frame takes 137 ms at 30 KB/s, more than
// queueTarget), while traffic of short frames alone still needs round trips to open the window.
type sentFrame struct {
	seq   uint64
	bytes int
	at    time.Time
	full  bool
}

const (
	windowInit   = 32 << 10
	windowMin    = 8 << 10
	windowMax    = 4 << 20
	bulkFrameMin = 4 << 10
	queueTarget  = 100 * time.Millisecond
	// maxSentEntries bounds sent: past this many frames in flight (tiny bulk lines on a fast link) the newest entry
	// takes the next frame's bytes too, which only blurs its round trip.
	maxSentEntries = 4096
)

func newCreditWindow() creditWindow { return creditWindow{cwnd: windowInit} }

// rateMark is a sample the next delivery rate is measured from: bytes acknowledged through the frame, read at recv.
type rateMark struct {
	acked int64
	recv  uint32
	set   bool
}

type rateSample struct {
	rate float64
	at   time.Time
}

const (
	rateSlots = 16
	rateFor   = 10 * time.Second
	// rateSpan: samples closer than this in the other side's time (one read can bring several) are not a rate.
	rateSpan = 50
)

// frameSize is the payload of a frame with credit now: a quarter of the window, and no more than the link carries in
// queueTarget once its rate is known (on a slow link a large frame is a long wait for whatever comes after it).
func (w *creditWindow) frameSize() int {
	n := w.cwnd / 4
	if w.rate > 0 {
		n = min(n, int(w.rate*queueTarget.Seconds()))
	}
	return min(max(n, bulkFrameMin), framePayloadMax)
}

// canSend: a frame of n bytes may go out now (room); one that may not notes that the window held it back.
func (w *creditWindow) canSend(n int) bool {
	if w.room(n) {
		return true
	}
	w.limited = true
	return false
}

// room: a frame of n bytes fits in the window now. With nothing in flight one frame always does, whatever its size.
func (w *creditWindow) room(n int) bool { return w.inFlight == 0 || w.inFlight+n <= w.cwnd }

// wrote records a frame of n bytes with credit, numbered seq, written from at; full: cut to the frame size.
func (w *creditWindow) wrote(seq uint64, n int, at time.Time, full bool) {
	w.inFlight += n
	if k := len(w.sent); k >= maxSentEntries {
		// The newest entry takes this frame too, and its time: a round trip from the first frame it took would count
		// the time the others took to be written as queueing.
		e := &w.sent[k-1]
		e.seq, e.bytes, e.at, e.full = seq, e.bytes+n, at, full
		return
	}
	w.sent = append(w.sent, sentFrame{seq: seq, bytes: n, at: at, full: full})
}

// acked takes an acknowledgement of every frame through seq, the last of them read at recv by the other side's clock
// (clockMS), and adapts the window.
func (w *creditWindow) acked(seq uint64, recv uint32, now time.Time) {
	var freed int
	var sample *sentFrame
	i := 0
	for ; i < len(w.sent) && w.sent[i].seq <= seq; i++ {
		freed += w.sent[i].bytes
		if w.sent[i].seq == seq {
			sample = &w.sent[i]
		}
	}
	if i == 0 {
		return
	}
	oldest := w.sent[0].at
	var at time.Time
	full := false
	if sample != nil {
		at, full = sample.at, sample.full
	}
	w.sent = append(w.sent[:0], w.sent[i:]...)
	w.inFlight -= freed
	w.done += int64(freed)
	if at.IsZero() { // the frame it names is not one of these: no sample
		return
	}
	w.noteRate(recv, now)
	rtt := max(now.Sub(at), 0)
	w.rtt = rtt
	base := &w.base[0]
	if full {
		base = &w.base[1]
	}
	delay := recv - clockMS(at) // one way, plus the clocks' difference: only differences of it mean anything
	base.add(delay, now)
	queued := time.Duration(int32(delay-base.min())) * time.Millisecond
	w.queued = queued
	// need: what the other side reads in the time its bytes wait to be acknowledged (pathSample). The
	// acknowledgements wait behind what the other side sends (a busy other direction), and how long changes from one
	// to the next (one waits behind a whole frame, the next gets through between two): the longest of the last
	// rateFor counts, or the window runs out while acknowledgements are late and the link idles. A window much past
	// it only fills the queue, and on a long round trip the queue shows a round trip later: growing stops at what the
	// other side reads in queueTarget past it, which still lets the rate rise each round trip.
	// The first byte covered was read after the last sample's frame, and no sooner than it could arrive.
	from := clockMS(oldest) + base.min()
	if w.lastRecvSet && before(from, w.lastRecv) {
		from = w.lastRecv
	}
	var span time.Duration
	if before(from, recv) {
		span = time.Duration(recv-from) * time.Millisecond
	}
	w.lastRecv, w.lastRecvSet = recv, true
	latest := max(rtt-queued, 0) + span
	copy(w.paths[1:], w.paths[:rateSlots-1])
	w.paths[0] = pathSample{d: latest, at: now}
	need, limit := 0, windowMax
	if w.rate > 0 {
		var path time.Duration
		for _, p := range w.paths {
			if !p.at.IsZero() && now.Sub(p.at) < rateFor {
				path = max(path, p.d)
			}
		}
		need = min(int(w.rate*(path+queueTarget).Seconds()), windowMax)
		limit = max(min(need+int(w.rate*queueTarget.Seconds()), windowMax), w.cwnd)
	}
	switch {
	case queued > queueTarget:
		if now.Sub(w.lastCut) >= rtt {
			cut := w.cwnd / 4 // a quarter off, half when the queue is far over
			if queued > 4*queueTarget {
				cut = w.cwnd / 2
			}
			w.cwnd = max(w.cwnd-cut, windowMin)
			w.lastCut = now
		}
		w.limited = false
	case !w.limited:
		// The window was not what held these lines back: growing it would only allow a burst later.
	case queued < queueTarget/2:
		w.cwnd = min(w.cwnd+freed, limit) // about doubling each round trip
		w.limited = false
	default:
		w.cwnd = min(w.cwnd+max(freed*framePayloadMax/w.cwnd, 1), limit) // about a frame each round trip
		w.limited = false
	}
	w.cwnd = max(w.cwnd, need)
	// A queue on the way past queueTarget is a window larger than the link carries in the time bytes wait now. When
	// the other side's queue drains, the acknowledgements it held come quicker than the frames were read, and a window
	// still sized for the old wait would go into the queue on the way, ahead of pings, until that wait ages out of
	// need: the latest sample's wait bounds it then.
	if queued > queueTarget && w.rate > 0 {
		w.cwnd = max(min(w.cwnd, int(w.rate*(latest+2*queueTarget).Seconds())), windowMin)
	}
}

// noteRate measures the rate the other side reads at, between this sample and the last one far enough before it by
// its clock: the spacing of its reads, which acknowledgements bunched on the way back do not change.
func (w *creditWindow) noteRate(recv uint32, now time.Time) {
	m := &w.mark
	if !m.set || before(recv, m.recv) {
		*m = rateMark{acked: w.done, recv: recv, set: true}
		return
	}
	span := recv - m.recv
	if span < rateSpan {
		return
	}
	r := float64(w.done-m.acked) / (float64(span) / 1000)
	*m = rateMark{acked: w.done, recv: recv, set: true}
	copy(w.rates[1:], w.rates[:rateSlots-1])
	w.rates[0] = rateSample{rate: r, at: now}
	w.rate = 0
	for _, s := range w.rates {
		if !s.at.IsZero() && now.Sub(s.at) < rateFor {
			w.rate = max(w.rate, s.rate)
		}
	}
}

// baseDelay is the smallest delay (clockMS differences, which wrap) seen in the last baseSlots minutes: the link's
// own, which a new route can raise.
type baseDelay struct {
	slot [baseSlots]uint32
	at   [baseSlots]int64 // the minute each slot is for
	set  [baseSlots]bool
}

const baseSlots = 5

// before: a is less than b, as wrapping millisecond counts are (they never differ by half their range here).
func before(a, b uint32) bool { return int32(a-b) < 0 }

func (b *baseDelay) add(d uint32, now time.Time) {
	minute := now.Unix() / 60
	i := int(minute % baseSlots)
	if !b.set[i] || b.at[i] != minute {
		b.slot[i], b.at[i], b.set[i] = d, minute, true
	} else if before(d, b.slot[i]) {
		b.slot[i] = d
	}
}

// min is the smallest delay of the slots still current; with none, 0.
func (b *baseDelay) min() uint32 {
	newest := int64(0)
	for i, m := range b.at {
		if b.set[i] {
			newest = max(newest, m)
		}
	}
	var out uint32
	found := false
	for i, d := range b.slot {
		if b.set[i] && newest-b.at[i] < baseSlots && (!found || before(d, out)) {
			out, found = d, true
		}
	}
	return out
}

// resumed: the connection goes on over a new transport (resume.go), whose other side read every frame through peerRead.
// Those are acknowledged without a sample; the ones after it go again now, so they count from now. What the window
// learned of the old route (its delays, its rate) is let go: the new one is measured afresh, from the start's window.
func (w *creditWindow) resumed(peerRead uint64, now time.Time) {
	i := 0
	for ; i < len(w.sent) && w.sent[i].seq <= peerRead; i++ {
		w.inFlight -= w.sent[i].bytes
		w.done += int64(w.sent[i].bytes)
	}
	w.sent = append(w.sent[:0], w.sent[i:]...)
	for i := range w.sent {
		w.sent[i].at = now
	}
	sent, inFlight, done := w.sent, w.inFlight, w.done
	*w = newCreditWindow()
	w.sent, w.inFlight, w.done = sent, inFlight, done
}
