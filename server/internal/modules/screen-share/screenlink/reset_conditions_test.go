package screenlink

import (
	"encoding/binary"
	"io"
	"testing"
	"time"
)

// assertResetWatchdogAlive gives the watchdog several ticks at a frozen fake time, then proves the route survived.
func assertResetWatchdogAlive(t *testing.T, s *Session, tr *transport, what string) {
	t.Helper()
	timer := time.NewTimer(50 * time.Millisecond)
	<-timer.C
	s.mu.Lock()
	dead := tr.dead
	s.mu.Unlock()
	if dead {
		t.Fatalf("%s", what)
	}
}

// A final TCP close is not handed out while its jump is unconfirmed. After the reset's route is lost, the next route
// repeats the reset and only the phone's acknowledgement releases the close.
func TestVideoResetDeliveryCloseWaitsForConfirmationTCP(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, false, resetTestLimits())
	old := activeTransport(s, id)
	resetWrite := installWriteGate(t, s, old, TypeReset, 0)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	resetWrite.await(t)
	if err := helper.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "EOF with nothing after the jump", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.eof && ch.sent == ch.base && len(ch.buf) == 0
	})
	resetWrite.completeLateWrite(t, s, old, nil)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != cv {
		t.Fatalf("reset at %d on the wrong route, want %d on the video relay", at, jump)
	}
	s.mu.Lock()
	sentClose := s.chans[id].sentClose
	s.mu.Unlock()
	if sentClose {
		t.Fatal("a close was handed out before the phone confirmed the final jump")
	}
	select {
	case m := <-p.msgs:
		if m.typ == TypeClose {
			t.Fatal("the phone received a close before confirming the final jump")
		}
		t.Fatalf("unexpected frame %d after the unconfirmed reset", m.typ)
	default:
	}

	fallback := activeTransport(s, RFB)
	next := installWriteGate(t, s, fallback, TypeReset, 0)
	if err := cv.conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitChannelActive(t, s, id, fallback)
	next.await(t)
	next.complete(nil)
	reset = p.nextOf(TypeReset)
	_, at, _, _ = readChanOffset(reset.body)
	if at != jump || reset.via != p.carriers[0] {
		t.Fatalf("fallback reset at %d on the wrong route, want %d on the parent", at, jump)
	}
	p.carriers[0].send(TypeAck, chanOffset(id, jump))
	closed := p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != jump || closed.via != p.carriers[0] {
		t.Fatalf("close at %d on the wrong route, want %d on the parent", end, jump)
	}
	waitFor(t, "the phone to finish at the confirmed close", func() bool { return p.endedAtClose(id) })
	p.checkHealthy()
}

// A UDP flow's parent carries no close while the final jump is unconfirmed. Both a UDP ack and a UDP want at the jump
// release the close, and the shared phone receiver finishes at it.
func TestVideoResetDeliveryCloseWaitsForConfirmationUDP(t *testing.T) {
	for _, confirmation := range []string{"ack", "want"} {
		t.Run(confirmation, func(t *testing.T) {
			l := openResetUDPLink(t, resetTestLimits())
			l.up.prove()
			l.up.use(l.id, 0)
			l.up.mu.Lock()
			l.up.dropResetAcks = 1
			l.up.mu.Unlock()
			jump, requests := prepareUDPReset(t, l, 0)
			nextKeyframeRequest(t, requests)
			reset := l.up.next(udpReset)
			if at := binary.BigEndian.Uint64(reset[2:]); at != jump {
				t.Fatalf("reset at %d, want %d", at, jump)
			}
			waitFor(t, "the reset acknowledgement to be lost", func() bool {
				l.up.mu.Lock()
				defer l.up.mu.Unlock()
				return l.up.dropResetAcks == 0
			})
			if got := l.p.offset(l.id); got != jump {
				t.Fatalf("phone offset %d after reset, want %d", got, jump)
			}
			if err := l.helper.Close(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "EOF behind the outstanding UDP reset", func() bool {
				l.s.mu.Lock()
				defer l.s.mu.Unlock()
				ch := l.s.chans[l.id]
				return ch.eof && !ch.reset && ch.resetOut && ch.sent == ch.base && len(ch.buf) == 0
			})

			flow := udpFlowTransport(l.s)
			parent := directTransport(l.s)
			l.s.mu.Lock()
			before := len(parent.ctl)
			job, ok := l.s.nextUDPLocked(flow) // the writer's next turn after its outstanding reset
			after := len(parent.ctl)
			sentClose := l.s.chans[l.id].sentClose
			l.s.mu.Unlock()
			if ok {
				t.Fatalf("unexpected UDP job while only the close decision remained: %+v", job)
			}
			if after != before || sentClose {
				t.Fatal("a close was queued on the parent before the UDP jump was confirmed")
			}

			if confirmation == "ack" {
				l.up.send(chanOffsetBody(udpAck, l.id, jump))
			} else {
				body := make([]byte, 18)
				body[0], body[1] = udpWant, l.id
				binary.BigEndian.PutUint64(body[2:], jump)
				binary.BigEndian.PutUint64(body[10:], jump)
				l.up.send(body)
			}
			closed := l.p.nextOf(TypeClose)
			_, end, _, _ := readChanOffset(closed.body)
			if end != jump || closed.via != l.direct {
				t.Fatalf("close at %d on the wrong route, want %d on the UDP parent", end, jump)
			}
			waitFor(t, "the phone to finish at the confirmed close", func() bool { return l.p.endedAtClose(l.id) })
			l.p.checkHealthy()
		})
	}
}

// A route owns only the reset handed to its own writer. Moving to a writer blocked on an earlier frame does not carry
// the old route's outstanding reset into the new route's stall accounting.
func TestVideoResetDeliveryMoveDoesNotCarryOutstandingReset(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	fallback := activeTransport(s, RFB)
	first := installWriteGate(t, s, old, TypeReset, 0)
	earlier := installWriteGate(t, s, fallback, TypeAck, 0)
	next := installWriteGate(t, s, fallback, TypeReset, 0)
	s.mu.Lock()
	fallback.ctl = append(fallback.ctl, frame(TypeAck, chanOffset(RFB, 0)))
	s.cond.Broadcast()
	s.mu.Unlock()
	earlier.await(t)
	_, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	first.await(t)
	first.complete(io.ErrClosedPipe)
	waitChannelActive(t, s, id, fallback)

	clock.Advance(limits.StallAfter)
	assertResetWatchdogAlive(t, s, fallback, "the new route inherited the old route's outstanding reset")
	earlier.complete(nil)
	earlier.waitComplete(t)
	next.await(t)
	clock.Advance(limits.StallAfter - time.Millisecond)
	assertResetWatchdogAlive(t, s, fallback, "the new route stalled before StallAfter after its own reset handout")
	clock.Advance(time.Millisecond)
	waitFor(t, "the new route to stall after its own reset handout", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return fallback.dead
	})
	next.complete(io.ErrClosedPipe)
}

// Data behind an outstanding TCP reset does not restart its stall interval.
func TestVideoResetDeliveryDataDoesNotRestartResetStallClockTCP(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	resetWrite := installWriteGate(t, s, old, TypeReset, 0)
	dataWrite := installWriteGate(t, s, old, TypeData, 1) // skip the backlog whose loss creates the jump
	p.dropResets(id, true)
	_, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	resetWrite.await(t)
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)
	waitFor(t, "the first message behind the blocked reset", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.sent == ch.base && len(ch.buf) == len(key)
	})
	clock.Advance(limits.StallAfter - time.Millisecond)
	assertResetWatchdogAlive(t, s, old, "the reset route stalled before the first data handout")
	resetWrite.complete(nil)
	resetWrite.waitComplete(t)
	dataWrite.await(t)
	clock.Advance(time.Millisecond)
	waitFor(t, "data not to extend the reset's stall interval", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return old.dead
	})
	dataWrite.complete(io.ErrClosedPipe)
}

// Data behind an outstanding UDP reset does not restart its stall interval either.
func TestVideoResetDeliveryDataDoesNotRestartResetStallClockUDP(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	l := openResetUDPLink(t, limits)
	l.up.prove()
	l.up.use(l.id, 0)
	clock := useClock(l.s)
	flow := udpFlowTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	clock.Advance(limits.StallAfter - time.Millisecond)
	assertResetWatchdogAlive(t, l.s, flow, "the UDP reset route stalled before the first data handout")
	key := videoFrameMsg(2, true, 100)
	writeVideo(l.helper, key)
	waitFor(t, "the first UDP message behind the reset", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.base == jump && ch.sent == jump+uint64(len(key))
	})
	clock.Advance(time.Millisecond)
	waitFor(t, "UDP data not to extend the reset's stall interval", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return flow.dead
	})
}

// A TCP acknowledgement for bytes before the reset does not confirm that reset.
func TestVideoResetDeliveryAckBelowResetDoesNotConfirmTCP(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, cv, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	fallback := activeTransport(s, RFB)
	first := installWriteGate(t, s, old, TypeReset, 0)
	next := installWriteGate(t, s, fallback, TypeReset, 0)
	p.dropResets(id, true)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	first.await(t)
	cv.send(TypeAck, chanOffset(id, 0))
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], 42)
	cv.send(TypePing, stamp[:])
	first.complete(nil)
	first.waitComplete(t)
	pong := p.nextOf(TypePong)
	if len(pong.body) < 8 || binary.BigEndian.Uint64(pong.body) != 42 {
		t.Fatalf("pong %v", pong.body)
	}
	p.dropResets(id, false)
	clock.Advance(limits.StallAfter)
	waitChannelActive(t, s, id, fallback)
	next.await(t)
	next.complete(nil)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != p.carriers[0] {
		t.Fatalf("fallback reset at %d on the wrong route, want %d on the parent", at, jump)
	}
	p.checkHealthy()
}

// A UDP acknowledgement below the reset likewise leaves the flow outstanding and due to stall.
func TestVideoResetDeliveryAckBelowResetDoesNotConfirmUDP(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	l := openResetUDPLink(t, limits)
	l.up.prove()
	l.up.use(l.id, 0)
	clock := useClock(l.s)
	parent := directTransport(l.s)
	next := installWriteGate(t, l.s, parent, TypeReset, 0)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	l.up.send(chanOffsetBody(udpAck, l.id, 0))
	l.up.ping(0) // the round trip proves the earlier acknowledgement was handled
	clock.Advance(limits.StallAfter)
	waitChannelActive(t, l.s, l.id, parent)
	next.await(t)
	next.complete(nil)
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != l.direct {
		t.Fatalf("fallback reset at %d on the wrong route, want %d on the UDP parent", at, jump)
	}
	l.p.checkHealthy()
}
