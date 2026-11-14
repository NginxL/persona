package screenlink

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// udpLimits are testLimits with room for the video tests' frames and no silence rule.
func udpLimits() Limits {
	l := testLimits
	l.WindowVideo = 256 << 10
	l.StallAfter = 2 * time.Second
	l.VideoBacklog = time.Hour
	return l
}

// The video channel rides the flow: every message arrives whole and in order, nothing goes over the relays, RFB stays
// where it was, and the pong counts what was sent.
func TestTheVideoRidesAUDPFlow(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	msgs := videoStream(60)
	writeAll(l.helper, msgs)
	want := join(msgs)
	waitFor(t, "the video over udp", func() bool { return len(l.up.delivered()) >= len(want) })
	if !bytes.Equal(l.up.delivered(), want) {
		t.Fatal("the video over udp differs from what the helper sent")
	}
	if n := len(l.p.received(l.id)); n != 0 {
		t.Fatalf("%d video bytes went over the relays too", n)
	}
	go l.ss.Write(pattern(5000))
	waitFor(t, "rfb on its relay", func() bool { return len(l.p.received(RFB)) == 5000 })
	data, parity, resent := l.up.ping(0)
	if data < uint64(len(want)) || parity != 0 {
		t.Fatalf("pong counters data %d parity %d (sent %d)", data, parity, len(want))
	}
	t.Logf("%d bytes resent on loopback", resent)
}

// Lost packets cost the picture nothing: with parity most are rebuilt where they land, and what parity can't cover
// (or everything, without parity) is asked for again; a message lost whole is asked for by its place in the stream.
func TestLostPacketsAreRebuiltOrAskedForAgain(t *testing.T) {
	for _, c := range []struct {
		name  string
		ratio int
	}{{"with parity", 1500}, {"without parity", 0}} {
		t.Run(c.name, func(t *testing.T) {
			l := openUDPLink(t, udpLimits())
			rng := rand.New(rand.NewSource(int64(c.ratio) + 1))
			var mu sync.Mutex
			var dropped int
			lossy := false
			// 5 % of the node's packets while the video runs (the proof, resumed and pongs get through).
			relay := lossyRelay(t, l.nodeAt, func(int) bool {
				mu.Lock()
				defer mu.Unlock()
				if lossy && rng.Float64() < 0.05 {
					dropped++
					return true
				}
				return false
			})
			setLoss := func(on bool) { mu.Lock(); lossy = on; mu.Unlock() }
			l.up = newUDPPhone(t, l.dc, l.ready, relay)
			l.up.ch = l.id
			l.up.prove()
			l.up.use(l.id, 0)
			l.up.ping(c.ratio)
			msgs := videoStream(120)
			setLoss(true)
			writeAll(l.helper, msgs)
			want := join(msgs)
			waitFor(t, "the video through loss", func() bool { return len(l.up.delivered()) >= len(want) })
			setLoss(false)
			if !bytes.Equal(l.up.delivered(), want) {
				t.Fatal("the video through loss differs from what the helper sent")
			}
			_, parity, resent := l.up.ping(c.ratio)
			mu.Lock()
			defer mu.Unlock()
			l.up.mu.Lock()
			rebuilt, nacks := l.up.rebuilt, l.up.nacks
			l.up.mu.Unlock()
			t.Logf("dropped %d packets: %d rebuilt from parity, %d asked for again, %d parity bytes, %d resent bytes",
				dropped, rebuilt, nacks, parity, resent)
			if dropped == 0 {
				t.Fatal("nothing was dropped")
			}
			if c.ratio > 0 && (rebuilt == 0 || parity == 0 || nacks >= dropped) {
				t.Fatalf("parity rebuilt %d and %d were asked for, of %d dropped", rebuilt, nacks, dropped)
			}
			if c.ratio == 0 && (rebuilt != 0 || resent == 0) {
				t.Fatalf("without parity: %d rebuilt, %d bytes resent", rebuilt, resent)
			}
		})
	}
}

// A message lost whole at the end of a burst, with nothing after it to show it was sent, is found by the node's tail
// probe: its last shard comes again, and the phone asks for the rest.
func TestATailLostWholeIsProbed(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	first := videoStream(6)
	last := videoFrameMsg(50, false, 3000) // three shards, all lost the first time
	lastAt := uint64(len(join(first)))
	lost := 0
	l.up.mu.Lock()
	l.up.drop = func(h udpFragmentHead) bool {
		if h.Offset == lastAt && lost < 3 {
			lost++
			return true
		}
		return false
	}
	l.up.mu.Unlock()
	writeAll(l.helper, append(first, last))
	want := append(join(first), last...)
	waitFor(t, "the tail", func() bool { return len(l.up.delivered()) == len(want) })
	if !bytes.Equal(l.up.delivered(), want) {
		t.Fatal("the tail differs")
	}
	// Acknowledged: the probes stop.
	waitFor(t, "everything acknowledged", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.base == ch.sent
	})
}

// Fresh video starts the probe clock again even when the previous message's fourth probe already armed an 800 ms
// timer. Acknowledging that message does not let its timer postpone the fresh tail's first probe.
func TestFreshTailDoesNotInheritAnOldProbeTimer(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	isTail := func(h udpFragmentHead) bool {
		blocks := udpBlocks(int(h.Length))
		last := blocks[len(blocks)-1]
		return h.Block == uint16(len(blocks)-1) && h.Index == uint16(last.Data-1)
	}

	// Drop the original's last shard and its first three tail probes, with retransmission requests paused so their
	// copies are not mistaken for probes. The fourth probe completes the message, so the phone acknowledges it itself
	// after the probe has armed the sender's 800 ms timer.
	previous := videoFrameMsg(1, false, 3000)
	previousTails := make(chan time.Time, 16)
	previousTailAppearances := 0 // the fake phone calls drop under its lock
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.drop = func(h udpFragmentHead) bool {
		if h.Offset != 0 || !isTail(h) {
			return false
		}
		previousTailAppearances++
		previousTails <- time.Now()
		return previousTailAppearances <= 4
	}
	l.up.mu.Unlock()
	writeAll(l.helper, [][]byte{previous})
	for i := 0; i < 5; i++ {
		select {
		case <-previousTails:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d tail appearances before the fourth probe", i)
		}
	}
	waitFor(t, "the previous message acknowledged", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return l.s.chans[l.id].base == uint64(len(previous))
	})

	// The fresh message is lost whole on its first trip. Its first repeated last shard is therefore the tail probe,
	// which must come within 300 ms (the required 50 ms plus generous scheduling room), not inherit the old 800 ms.
	fresh := videoFrameMsg(2, false, 3000)
	freshAt := uint64(len(previous))
	freshTails := make(chan time.Time, 16)
	seen := map[[2]uint16]bool{} // the fake phone calls drop under its lock
	l.up.mu.Lock()
	l.up.expected = freshAt
	l.up.got = nil // only the fresh message's delivery is checked below
	l.up.autoAsk = true
	l.up.drop = func(h udpFragmentHead) bool {
		if h.Offset != freshAt {
			return false
		}
		if isTail(h) {
			freshTails <- time.Now()
		}
		key := [2]uint16{h.Block, h.Index}
		if !seen[key] {
			seen[key] = true
			return true
		}
		return false
	}
	l.up.mu.Unlock()
	started := time.Now()
	writeAll(l.helper, [][]byte{fresh})
	select {
	case <-freshTails: // the original's last shard
	case <-time.After(2 * time.Second):
		t.Fatal("the fresh message was not sent")
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case at := <-freshTails:
		if elapsed := at.Sub(started); elapsed > 300*time.Millisecond {
			t.Fatalf("the fresh tail's first probe came after %v, want within 300ms", elapsed)
		}
	case <-timer.C:
		t.Fatal("no probe within 300ms after fresh video followed an acknowledged fourth probe")
	}
	waitFor(t, "the fresh tail", func() bool { return len(l.up.delivered()) == len(fresh) })
	if !bytes.Equal(l.up.delivered(), fresh) {
		t.Fatal("the fresh tail differs")
	}
}

// A frame the phone gives up is not sent again: the node drops the backlog's frames, says where the stream goes on,
// asks the helper for a keyframe and drops frames until it comes. The backlog's other messages still arrive, after the
// reset and before the keyframe, so it decodes.
func TestAGivenUpFrameGoesOnAtAKeyframe(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	before := [][]byte{videoFrameMsg(1, true, 2000), videoFrameMsg(2, false, 2000)}
	lost := videoFrameMsg(3, false, 5000)
	format := videoMessage(videoFormat, pattern(30))
	after := [][]byte{videoFrameMsg(4, false, 2000), format, videoFrameMsg(5, false, 2000)}
	lostAt := uint64(len(join(before)))
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.drop = func(h udpFragmentHead) bool { return h.Offset == lostAt }
	l.up.mu.Unlock()

	writeAll(l.helper, append(append(append([][]byte{}, before...), lost), after...))
	waitFor(t, "the frames before the lost one", func() bool { return len(l.up.delivered()) == int(lostAt) })
	// Everything is read from the helper (the node's buffer holds the backlog) before the phone gives up.
	waitFor(t, "the backlog read", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.base+uint64(len(ch.buf)) == uint64(len(join(before))+len(lost)+len(join(after)))
	})
	keyReq := make(chan []byte, 1)
	go func() { b := make([]byte, 5); io.ReadFull(l.helper, b); keyReq <- b }()
	if off := l.up.skip(); off != lostAt {
		t.Fatalf("gave up %d, want %d", off, lostAt)
	}
	reset := l.up.next(udpReset)
	at := binary.BigEndian.Uint64(reset[2:])
	if reset[1] != l.id || at <= lostAt {
		t.Fatalf("reset of %d at %d", reset[1], at)
	}
	select {
	case b := <-keyReq:
		if !bytes.Equal(b, keyframeRequest) {
			t.Fatalf("the helper got %v", b)
		}
	case <-time.After(time.Second):
		t.Fatal("the helper was not asked for a keyframe")
	}
	l.up.mu.Lock()
	l.up.drop = nil
	l.up.mu.Unlock()
	writeAll(l.helper, [][]byte{videoFrameMsg(6, false, 1000), videoFrameMsg(7, true, 3000)})
	waitFor(t, "the format and the keyframe", func() bool { return len(l.up.delivered()) > int(lostAt) })
	waitFor(t, "the keyframe", func() bool {
		got := frames(t, l.up.delivered()[lostAt:])
		return len(got) == 2
	})
	got := frames(t, l.up.delivered()[lostAt:])
	if got[0][0] != int(videoFormat) || got[1][1] != 7 || got[1][2] != 1 {
		t.Fatalf("after giving up: %v, want the format message then keyframe 7", got)
	}
}

// RFB never rides a flow: a use for it on the flow is ignored, and paths keep going on the relays and TLS.
func TestRFBNeverRidesAUDPFlow(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	resumed := l.up.use(RFB, 0)
	if len(resumed) != 1 {
		t.Fatalf("resumed RFB on the flow: %v", resumed[1:])
	}
	go l.ss.Write(pattern(3000))
	waitFor(t, "rfb", func() bool { return len(l.p.received(RFB)) == 3000 })
	l.s.mu.Lock()
	onFlow := l.s.chans[RFB].active.flow != nil
	l.s.mu.Unlock()
	if onFlow {
		t.Fatal("RFB moved onto the flow")
	}
	// A transport the node picks by itself (fallbackLocked) is never the flow either.
	l.s.mu.Lock()
	for tr := range l.s.ts {
		if tr.flow == nil {
			continue
		}
		for _, ch := range l.s.chans {
			if alt := l.s.fallbackLocked(ch); alt == tr {
				l.s.mu.Unlock()
				t.Fatal("fallbackLocked picked the flow")
			}
		}
	}
	l.s.mu.Unlock()
}

// A flow that goes silent hands the video back to its TLS route, from what the phone acknowledged; when the route
// itself drops, the flow goes with it and the video goes on over the relay.
func TestAFlowFallsBackToItsRoute(t *testing.T) {
	t.Run("silent", func(t *testing.T) {
		limits := udpLimits()
		limits.FlowSilence = 150 * time.Millisecond
		l := openUDPLink(t, limits)
		route := directTransport(l.s)
		l.up.prove()
		l.up.use(l.id, 0)
		first := videoStream(20)
		writeAll(l.helper, first)
		waitFor(t, "video over udp", func() bool { return len(l.up.delivered()) == len(join(first)) })
		// The phone stops sending on the flow; it picks the bytes up on its TLS route from where it acknowledged.
		l.up.mu.Lock()
		l.up.autoAsk = false
		at := l.up.expected
		l.up.mu.Unlock()
		l.p.mu.Lock()
		l.p.recv[l.id] = at
		l.p.mu.Unlock()
		waitFor(t, "the flow to be dropped", func() bool {
			l.s.mu.Lock()
			defer l.s.mu.Unlock()
			return l.s.chans[l.id].active == route
		})
		more := videoStream(10)
		writeAll(l.helper, more)
		waitFor(t, "video over the TLS route", func() bool { return len(l.p.received(l.id)) == len(join(more)) })
		if !bytes.Equal(l.p.received(l.id), join(more)) {
			t.Fatal("the video over the TLS route differs")
		}
	})
	t.Run("the route drops", func(t *testing.T) {
		l := openUDPLink(t, udpLimits())
		l.up.prove()
		l.up.use(l.id, 0)
		first := videoStream(10)
		writeAll(l.helper, first)
		waitFor(t, "video over udp", func() bool { return len(l.up.delivered()) == len(join(first)) })
		l.up.mu.Lock()
		at := l.up.expected
		l.up.mu.Unlock()
		l.p.mu.Lock()
		l.p.recv[l.id] = at
		l.p.mu.Unlock()
		l.dc.Close()
		waitFor(t, "no flow left", func() bool {
			l.s.mu.Lock()
			defer l.s.mu.Unlock()
			for tr := range l.s.ts {
				if tr.flow != nil {
					return false
				}
			}
			return l.s.chans[l.id].active != nil && l.s.chans[l.id].active.relay
		})
		more := videoStream(10)
		writeAll(l.helper, more)
		waitFor(t, "video over the relay", func() bool { return len(l.p.received(l.id)) == len(join(more)) })
		// The flow is gone: its pings get nothing.
		b := make([]byte, 15)
		b[0] = udpPing
		l.up.send(b)
		select {
		case m := <-l.up.ctl:
			if m[0] == udpPong {
				t.Fatal("a closed flow answered")
			}
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// directTransport is the session's one direct TLS route.
func directTransport(s *Session) *transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t := range s.ts {
		if t.tls != nil {
			return t
		}
	}
	return nil
}

// Only frames that aren't keyframes may be given up; every other message, a keyframe included, must arrive.
func TestOnlyFramesThatArentKeyframesMayBeGivenUp(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  []byte
		want byte
	}{
		{"a frame", videoFrameMsg(7, false, 100), udpDroppable},
		{"a keyframe", videoFrameMsg(8, true, 100), 0},
		{"format", videoMessage(videoFormat, pattern(20)), 0},
		{"a frame too short to say", videoMessage(videoFrame, pattern(5)), 0},
	} {
		if got := udpFlags(c.msg); got != c.want {
			t.Errorf("%s: flags %d, want %d", c.name, got, c.want)
		}
	}
}

// The pacing follows the phone's request down to a slow link's rate: only 1.25 × the video's own rate and 1 Mbit/s
// hold it up, and 150 Mbit/s caps it.
func TestThePacingFollowsThePhone(t *testing.T) {
	for _, c := range []struct{ want, video, got int }{
		{0, 0, udpPaceDefault},
		{3_750, 1_500, 3_750}, // a 3 Mbit/s cellular bottleneck: what the phone asks for
		{1_000, 1_500, 1_875}, // never below 1.25 × the video
		{100, 0, udpPaceMin},
		{900_000, 0, udpPaceMax},
	} {
		if got := udpPaceFloor(c.want, c.video); got != c.got {
			t.Errorf("want %d, video %d: paced at %d, want %d", c.want, c.video, got, c.got)
		}
	}
}
