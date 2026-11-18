package screenlink

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// prepareLostReset sends a backlog whose data is lost before the phone, then drops it. The phone is still at zero
// when the reset at the returned jump is dequeued.
func prepareLostReset(t *testing.T, s *Session, p *phone, helper net.Conn, id byte) (uint64, <-chan []byte) {
	t.Helper()
	p.dropData(id, true)
	backlog := videoFrameMsg(1, true, 100)
	go helper.Write(backlog)
	waitFor(t, "the backlog to be written and lost", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.base == 0 && ch.sent == uint64(len(backlog)) && len(ch.buf) == len(backlog)
	})
	requests := keyframeRequests(helper)
	s.mu.Lock()
	s.dropBacklogLocked(s.chans[id])
	s.cond.Broadcast()
	s.mu.Unlock()
	return uint64(len(backlog)), requests
}

func writeVideo(helper net.Conn, msg []byte) { go helper.Write(msg) }

func waitChannelActive(t *testing.T, s *Session, id byte, want *transport) {
	t.Helper()
	waitFor(t, "the channel's next transport", func() bool { return activeTransport(s, id) == want })
}

// R1: the reset's write fails, the node falls back, and the phone resumes from before the jump.
func TestVideoResetDeliveryR1FailedWriteThenUse(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	first := installWriteGate(t, s, old, TypeReset, 0)
	fallback := activeTransport(s, RFB)
	next := installWriteGate(t, s, fallback, 0, 0)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)

	first.await(t)
	first.complete(io.ErrClosedPipe)
	waitChannelActive(t, s, id, fallback)
	p.carriers[0].send(TypeUse, chanOffset(id, p.offset(id)))
	next.await(t)
	p.dropData(id, false)
	next.complete(nil)
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)

	p.waitReceived(id, key)
	reset := p.nextOf(TypeReset)
	rid, at, _, _ := readChanOffset(reset.body)
	if rid != id || at != jump || reset.via == cv {
		t.Fatalf("reset channel %d at %d via old=%v, want channel %d at %d on the fallback", rid, at, reset.via == cv, id, jump)
	}
}

// R2: two reset writes in a row are lost on two transports; the third transport still owes the reset.
func TestVideoResetDeliveryR2TwoFailedWrites(t *testing.T) {
	s, p, _, helper, id, tok := openVideo(t, true, resetTestLimits())
	first := installWriteGate(t, s, activeTransport(s, id), TypeReset, 0)
	second := installWriteGate(t, s, activeTransport(s, RFB), TypeReset, 0)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)

	first.await(t)
	first.complete(io.ErrClosedPipe)
	second.await(t)

	// A new relay is attached before the second loss, so it is the deterministic next fallback.
	again, ok := s.Reattach(true)
	if !ok {
		t.Fatal("reattach video")
	}
	thirdCarrier := p.carry(again)
	thirdCarrier.send(TypeAttach, attachBody(tok))
	p.nextOf(TypeAttached)
	third := thirdCarrierNode(s, id)
	if third == nil {
		t.Fatal("the third transport is not attached")
	}
	second.complete(io.ErrClosedPipe)
	waitChannelActive(t, s, id, third)
	p.dropData(id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)

	p.waitReceived(id, key)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != thirdCarrier {
		t.Fatalf("reset at %d on the wrong transport, want %d on the third", at, jump)
	}
	noKeyframeRequest(t, requests)
}

// thirdCarrierNode finds the attached relay other than the channel's current and dead transports.
func thirdCarrierNode(s *Session, id byte) *transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tr := range s.ts {
		if tr.relay && !tr.pending && !tr.dead && tr != s.chans[id].active && tr != s.chans[RFB].active {
			return tr
		}
	}
	return nil
}

// R3: no route is available when the reset is lost; a later use on a new route still owes it.
func TestVideoResetDeliveryR3WaitThenUse(t *testing.T) {
	s, p, _, helper, id, tok := openVideo(t, true, resetTestLimits())
	p.carriers[0].conn.Close() // RFB temporarily shares the remaining video relay
	waitFor(t, "RFB to share the only relay", func() bool {
		rfb, video := activeTransport(s, RFB), activeTransport(s, id)
		return rfb != nil && rfb == video
	})
	first := installWriteGate(t, s, activeTransport(s, id), TypeReset, 0)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)

	first.await(t)
	first.complete(io.ErrClosedPipe)
	waitChannelActive(t, s, id, nil)
	again, ok := s.Reattach(true)
	if !ok {
		t.Fatal("reattach video")
	}
	next := p.carry(again)
	next.send(TypeAttach, attachBody(tok))
	p.nextOf(TypeAttached)
	next.send(TypeUse, chanOffset(id, p.offset(id)))
	p.dropData(id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)

	p.waitReceived(id, key)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != next {
		t.Fatalf("reset at %d on the wrong transport, want %d on the new relay", at, jump)
	}
}

// R4: a use moves the channel while the old reset write is blocked. Its late completion changes no node state.
func TestVideoResetDeliveryR4UseDuringBlockedWrite(t *testing.T) {
	s, p, _, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	first := installWriteGate(t, s, old, TypeReset, 0)
	fallback := activeTransport(s, RFB)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)

	first.await(t)
	p.carriers[0].send(TypeUse, chanOffset(id, p.offset(id)))
	p.dropData(id, false)
	key := videoFrameMsg(2, true, dataChunk+100)
	writeVideo(helper, key)
	p.waitReceived(id, key)
	reset := p.nextOf(TypeReset)
	rid, at, _, _ := readChanOffset(reset.body)
	if rid != id || at != jump || reset.via != p.carriers[0] {
		t.Fatalf("reset channel %d at %d on the wrong transport, want channel %d at %d on the second", rid, at, id, jump)
	}
	before := snapshotChannel(s, id)
	first.completeLateWrite(t, s, old, nil) // the old route's reset reaches the phone late
	waitChannelActive(t, s, id, fallback)
	after := snapshotChannel(s, id)
	if !sameChannelState(before, after) || after.active != fallback {
		t.Fatalf("the late old write changed the channel: before %+v, after %+v", before, after)
	}
}

// R4 with the old route's reset landing inside the keyframe (#10005): the new route has sent its reset and the
// keyframe's first chunk when the old route's write of the same reset completes. That copy is at or behind the phone's
// receiver, so it keeps the half keyframe, and the rest completes it.
func TestVideoResetDeliveryLateResetInsideMessage(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	first := installWriteGate(t, s, old, TypeReset, 0)
	fallback := activeTransport(s, RFB)
	rest := installWriteGate(t, s, fallback, TypeData, 1) // the keyframe's second chunk
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)

	first.await(t)
	p.carriers[0].send(TypeUse, chanOffset(id, p.offset(id)))
	p.dropData(id, false)
	key := videoFrameMsg(2, true, dataChunk+100)
	writeVideo(helper, key)
	rest.await(t)
	reset := p.nextOf(TypeReset)
	if _, at, _, _ := readChanOffset(reset.body); at != jump || reset.via != p.carriers[0] {
		t.Fatalf("reset at %d on the wrong transport, want %d on the second", at, jump)
	}
	half := jump + dataChunk
	waitFor(t, "the keyframe's first chunk", func() bool { return p.offset(id) == half })

	first.completeLateWrite(t, s, old, nil) // the old route's reset reaches the phone inside the keyframe
	late := p.nextOf(TypeReset)
	if _, at, _, _ := readChanOffset(late.body); at != jump || late.via != cv {
		t.Fatalf("late reset at %d via old=%v, want %d on the old route", at, late.via == cv, jump)
	}
	if got, pending := p.offset(id), p.pendingVideo(id); got != half || pending != dataChunk {
		t.Fatalf("after the late reset the phone is at %d with %d bytes pending, want %d with %d", got, pending, half, dataChunk)
	}
	rest.complete(nil)
	p.waitReceived(id, key)
	waitChannelActive(t, s, id, fallback)
}

func channelPosition(s *Session, id byte) (base, sent uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chans[id].base, s.chans[id].sent
}

// channelSnapshot is the reset-relevant state, without copying the channel's mutexes.
type channelSnapshot struct {
	active        *transport
	base          uint64
	sent          uint64
	reset         bool
	keyWait       bool
	jumpOff       uint64
	jumpValid     bool
	jumpConfirmed bool
	jumpData      bool
	buf           []byte
}

func snapshotChannel(s *Session, id byte) channelSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.chans[id]
	return channelSnapshot{active: ch.active, base: ch.base, sent: ch.sent, reset: ch.reset, keyWait: ch.keyWait,
		jumpOff: ch.jumpOff, jumpValid: ch.jumpValid, jumpConfirmed: ch.jumpConfirmed, jumpData: ch.jumpData,
		buf: append([]byte(nil), ch.buf...)}
}

func sameChannelState(a, b channelSnapshot) bool {
	return a.active == b.active && a.base == b.base && a.sent == b.sent && a.reset == b.reset && a.keyWait == b.keyWait &&
		a.jumpOff == b.jumpOff && a.jumpValid == b.jumpValid && a.jumpConfirmed == b.jumpConfirmed &&
		a.jumpData == b.jumpData && bytes.Equal(a.buf, b.buf)
}

// R5: an inactive transport's late success or failure cannot re-arm, confirm, or replace a newer jump.
func TestVideoResetDeliveryR5InactiveLossKeepsNewerJump(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"late failure", io.ErrClosedPipe}, {"late success", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, _, helper, id, _ := openVideo(t, true, resetTestLimits())
			old := activeTransport(s, id)
			first := installWriteGate(t, s, old, TypeReset, 0)
			fallback := activeTransport(s, RFB)
			second := installWriteGate(t, s, fallback, TypeReset, 0)
			jump, requests := prepareLostReset(t, s, p, helper, id)
			nextKeyframeRequest(t, requests)
			first.await(t)

			// The old writer is still blocked, so this keyframe is buffered but not handed out. Move and drop again under
			// one lock: the other transport only ever sees the newer jump.
			key := videoFrameMsg(2, true, 100)
			writeVideo(helper, key)
			waitFor(t, "the next keyframe to be buffered", func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				ch := s.chans[id]
				return ch.base == jump && ch.sent == jump && len(ch.buf) == len(key)
			})
			s.mu.Lock()
			ch := s.chans[id]
			if !s.moveLocked(ch, fallback, 0) {
				s.mu.Unlock()
				t.Fatal("move to the second transport")
			}
			s.dropBacklogLocked(ch)
			s.cond.Broadcast()
			newer := ch.base
			s.mu.Unlock()
			nextKeyframeRequest(t, requests)
			second.await(t)
			if newer != jump+uint64(len(key)) {
				t.Fatalf("newer jump %d, want %d", newer, jump+uint64(len(key)))
			}

			before := snapshotChannel(s, id)
			if tc.err == nil {
				p.dropResets(id, true) // a successful old write need not have reached the phone
			}
			first.complete(tc.err)
			first.waitComplete(t)
			if tc.err == nil {
				p.dropResets(id, false)
			} else {
				waitFor(t, "the old transport to be lost", func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					_, present := s.ts[old]
					return old.dead && !present
				})
			}
			after := snapshotChannel(s, id)
			if !sameChannelState(before, after) || after.active != fallback {
				t.Fatalf("inactive completion changed the newer jump: before %+v, after %+v", before, after)
			}
			second.complete(io.ErrClosedPipe)
		})
	}
}

// R6: once the phone confirms the jump, fallback resumes a partial message without another reset.
func TestVideoResetDeliveryR6ConfirmedJumpKeepsPartialMessage(t *testing.T) {
	s, p, _, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump {
		t.Fatalf("reset at %d, want %d", at, jump)
	}
	p.dropData(id, false)
	secondData := installWriteGate(t, s, old, TypeData, 1)
	key := videoFrameMsg(2, true, dataChunk+100)
	writeVideo(helper, key)
	secondData.await(t)
	waitFor(t, "the phone to acknowledge half a message", func() bool {
		base, _ := channelPosition(s, id)
		return p.offset(id) == jump+dataChunk && base == jump+dataChunk && p.pendingVideo(id) > 0
	})
	secondData.complete(io.ErrClosedPipe)
	waitChannelActive(t, s, id, activeTransport(s, RFB))

	p.waitReceived(id, key)
	if p.pendingVideo(id) != 0 {
		t.Fatal("the partial message did not complete")
	}
	if p.resetCount() != 1 {
		t.Fatalf("the phone received %d resets, want only the confirmed one", p.resetCount())
	}
	noKeyframeRequest(t, requests)
}

// R7: the writes succeeded but reset and data were all lost with the connection. Fallback must make a new, safe jump.
func TestVideoResetDeliveryR7UnconfirmedDataForcesNewJump(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	resetWrite := installWriteGate(t, s, old, TypeReset, 0)
	fallback := activeTransport(s, RFB)
	next := installWriteGate(t, s, fallback, 0, 0)
	p.dropResets(id, true)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	resetWrite.await(t)
	resetWrite.complete(nil)
	resetWrite.waitComplete(t)

	firstKey := videoFrameMsg(2, true, 100)
	dataWrite := installWriteGate(t, s, old, TypeData, 0)
	writeVideo(helper, firstKey)
	dataWrite.await(t)
	dataWrite.complete(nil)
	dataWrite.waitComplete(t)
	if base, sent := channelPosition(s, id); base != jump || sent != jump+uint64(len(firstKey)) {
		t.Fatalf("after the lost writes base=%d sent=%d", base, sent)
	}

	cv.conn.Close()
	waitChannelActive(t, s, id, fallback)
	p.dropData(id, false)
	p.dropResets(id, false)
	secondKey := videoFrameMsg(3, true, 100)
	writeVideo(helper, secondKey)
	next.complete(nil)
	p.waitReceived(id, secondKey)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if want := jump + uint64(len(firstKey)); at != want {
		t.Fatalf("reset at %d, want a new jump at %d", at, want)
	}
	nextKeyframeRequest(t, requests)
}

// R10: once acknowledged, ordinary and stale route changes resend exactly the remaining bytes, with no reset or new
// keyframe request.
func TestVideoResetDeliveryR10ConfirmedJumpOrdinaryMoves(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, true, resetTestLimits())
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	p.nextOf(TypeReset)
	p.dropData(id, false)
	first := videoFrameMsg(2, true, 100)
	writeVideo(helper, first)
	p.waitReceived(id, first)
	waitFor(t, "the first keyframe to be acknowledged", func() bool {
		base, sent := channelPosition(s, id)
		return base == sent && base == jump+uint64(len(first))
	})

	second := videoFrameMsg(3, true, 100)
	p.carriers[0].send(TypeUse, chanOffset(id, 0)) // stale, behind an acknowledged base
	p.nextOf(TypeResumed)
	writeVideo(helper, second)
	p.waitReceived(id, join([][]byte{first, second}))
	cv.send(TypeUse, chanOffset(id, 0))
	p.nextOf(TypeResumed)
	third := videoFrameMsg(4, true, 100)
	writeVideo(helper, third)
	p.waitReceived(id, join([][]byte{first, second, third}))
	if p.resetCount() != 1 {
		t.Fatalf("confirmed route changes sent %d resets, want one", p.resetCount())
	}
	noKeyframeRequest(t, requests)
}

// R11: RFB continues byte for byte over the same route changes and never resets or drops.
func TestVideoResetDeliveryR11RFBUnchanged(t *testing.T) {
	reg := NewRegistry(resetTestLimits(), nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair-rfb", local)
	t.Cleanup(s.End)
	p := newPhone(t, true)
	firstCarrier := p.carry(relay)
	tok := token(t, p.next(TypePaths))
	again, ok := s.Reattach(false)
	if !ok {
		t.Fatal("reattach RFB")
	}
	secondCarrier := p.carry(again)
	secondCarrier.send(TypeAttach, attachBody(tok))
	p.nextOf(TypeAttached)
	secondCarrier.send(TypeUse, chanOffset(RFB, 0))
	p.nextOf(TypeResumed)
	first := pattern(20 << 10)
	go ss.Write(first)
	p.waitReceived(RFB, first)
	firstCarrier.send(TypeUse, chanOffset(RFB, p.offset(RFB)))
	p.nextOf(TypeResumed)
	second := pattern(5000)
	go ss.Write(second)
	p.waitReceived(RFB, append(first, second...))
	if p.resetCount() != 0 {
		t.Fatal("RFB received a reset")
	}
}

func prepareUDPReset(t *testing.T, l *udpLink, dropResets int) (uint64, <-chan []byte) {
	t.Helper()
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.dropResets = dropResets
	l.up.drop = func(udpFragmentHead) bool { return true }
	l.up.mu.Unlock()
	return prepareLostReset(t, l.s, l.p, l.helper, l.id)
}

func awaitUDPResetLoss(t *testing.T, u *udpPhone) {
	t.Helper()
	select {
	case <-u.resetLost:
	case <-time.After(2 * time.Second):
		t.Fatal("the UDP reset was not dequeued and lost")
	}
}

// R8: a UDP reset is dequeued and lost, with no later data on the flow. The phone leaves the dead path and its use moves
// the channel to the parent, which owes that reset before real TCP delivery resumes.
func TestVideoResetDeliveryR8UDPResetLostThenParent(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)

	l.up.leaveFlow()
	waitChannelActive(t, l.s, l.id, parent)
	l.p.dropData(l.id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(l.helper, key)
	l.p.waitReceived(l.id, key)
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != l.direct {
		t.Fatalf("reset at %d on the wrong transport, want %d on the UDP parent", at, jump)
	}
	l.p.checkHealthy()
}

// When the node closes a flow, its parent reset still precedes data. The released phone ignores that parent data until
// it leaves the flow, so this asserts only the node's wire order, not phone delivery.
func TestVideoResetDeliveryNodeClosedFlowParentResetPrecedesData(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	dataWrite := installWriteGate(t, l.s, parent, TypeData, 0)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	flow := udpFlowTransport(l.s)
	flow.flow.closeTransport()
	waitChannelActive(t, l.s, l.id, parent)
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != l.direct {
		t.Fatalf("reset at %d on the wrong route, want %d on the UDP parent", at, jump)
	}
	l.p.dropData(l.id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(l.helper, key)
	dataWrite.await(t)
	if n := l.p.rawTCPData(l.id); n != 0 {
		t.Fatalf("%d parent data bytes arrived before the reset", n)
	}
	dataWrite.complete(nil)
	dataWrite.waitComplete(t)
	l.p.checkHealthy()
}

// R9: data followed the lost UDP reset and only some datagrams arrived. When the phone leaves that dead path, the parent
// must drop again rather than send the old reset into a message the phone may already have started.
func TestVideoResetDeliveryR9UDPDataLostThenParent(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)

	firstKey := videoFrameMsg(2, true, 3000)
	l.up.mu.Lock()
	l.up.drop = func(h udpFragmentHead) bool {
		return h.Offset == jump && h.Block == 0 && h.Index == 0
	}
	l.up.mu.Unlock()
	writeVideo(l.helper, firstKey)
	waitFor(t, "some UDP data after the reset", func() bool {
		l.s.mu.Lock()
		ch := l.s.chans[l.id]
		sent := ch.base == jump && ch.sent == jump+uint64(len(firstKey))
		l.s.mu.Unlock()
		l.up.mu.Lock()
		m := l.up.msgs[jump]
		received := m != nil && len(m.blocks) > 0
		l.up.mu.Unlock()
		return sent && received
	})
	l.up.leaveFlow()
	waitChannelActive(t, l.s, l.id, parent)
	l.p.dropData(l.id, false)
	secondKey := videoFrameMsg(3, true, 100)
	writeVideo(l.helper, secondKey)
	l.p.waitReceived(l.id, secondKey)
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if want := jump + uint64(len(firstKey)); at != want || reset.via != l.direct {
		t.Fatalf("reset at %d on the wrong transport, want %d on the UDP parent", at, want)
	}
	nextKeyframeRequest(t, requests)
	l.p.checkHealthy()
}

// A close already handed to a writer does not prove the phone reached its offset. The channel must stay live until a
// final jump's reset is confirmed, or the phone can never reach the close.
func TestVideoResetDeliveryEOFWaitsForJumpConfirmation(t *testing.T) {
	limits := resetTestLimits()
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	p.dropData(id, true)
	backlog := videoFrameMsg(1, true, 100)
	go func() {
		_, _ = helper.Write(backlog)
		_ = helper.Close()
	}()
	closed := p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != uint64(len(backlog)) {
		t.Fatalf("close at %d, want %d", end, len(backlog))
	}
	clock.Advance(2 * limits.VideoBacklog)
	fallback := activeTransport(s, RFB)
	lastReset := installWriteGate(t, s, fallback, TypeReset, 0)
	p.carriers[0].send(TypeUse, chanOffset(id, 0))
	lastReset.await(t)

	finished := make(chan struct{})
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				s.mu.Lock()
				done := s.chans[id].finished
				s.mu.Unlock()
				if done {
					close(finished)
					return
				}
			case <-s.Done():
				return
			}
		}
	}()
	select {
	case <-finished:
		t.Fatal("the channel finished while its final reset was unconfirmed")
	case <-time.After(50 * time.Millisecond):
	}
	lastReset.complete(nil)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the channel did not finish after the phone confirmed the final reset")
	}
	if p.offset(id) != end {
		t.Fatalf("phone offset %d, want close offset %d", p.offset(id), end)
	}
	waitFor(t, "the phone to observe EOF after the confirmed reset", func() bool { return p.endedAtClose(id) })
}

// A final UDP reset has no data tail to probe. If it or its acknowledgement is lost, the ordinary stall watchdog must
// still move the quiet channel so its parent can owe the reset again instead of waiting forever for confirmation.
func TestVideoResetDeliveryEOFLostUDPResetFallsBack(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	clock := useClock(l.s)
	parent := directTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 1)
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	if err := l.helper.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the quiet final jump with its close held", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.eof && !ch.sentClose && ch.sent == ch.base && len(ch.buf) == 0 && ch.jumpValid && !ch.jumpConfirmed
	})

	clock.Advance(2 * l.s.limits.StallAfter)
	waitChannelActive(t, l.s, l.id, parent)
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != l.direct {
		t.Fatalf("final reset at %d on the wrong route, want %d on the UDP parent", at, jump)
	}
	closed := l.p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != jump || closed.via != l.direct {
		t.Fatalf("final close at %d on the wrong route, want %d on the UDP parent", end, jump)
	}
	waitFor(t, "the phone to observe EOF", func() bool { return l.p.endedAtClose(l.id) })
	waitFor(t, "the confirmed final jump to finish", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return l.s.chans[l.id].finished
	})
	l.p.checkHealthy()
}

// A reset blocked in a writer is outstanding even when no post-jump byte has been handed out. At EOF this retained
// backlog is young (VideoBacklog remains an hour, beyond StallAfter), so it stays intact; the next route repeats the same
// reset before messages and close.
func TestVideoResetDeliveryEOFBlockedResetRetainsBufferAndFallsBack(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	fallback := activeTransport(s, RFB)
	blocked := installWriteGate(t, s, old, TypeReset, 0)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	blocked.await(t)
	p.dropData(id, false)

	key := videoFrameMsg(2, true, 80)
	format := videoMessage(videoFormat, pattern(40))
	retained := append(append([]byte(nil), key...), format...)
	go func() {
		_, _ = helper.Write(retained)
		_ = helper.Close()
	}()
	waitFor(t, "EOF behind the blocked reset", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.chans[id].eof
	})
	s.mu.Lock()
	ch := s.chans[id]
	base, sent, jumpAt, data := ch.base, ch.sent, ch.jumpOff, ch.jumpData
	buffered := append([]byte(nil), ch.buf...)
	s.mu.Unlock()
	if base != jump || sent != jump || jumpAt != jump || data || !bytes.Equal(buffered, retained) {
		t.Fatalf("blocked reset changed the final stream: base=%d sent=%d jump=%d data=%v buffered=%d", base, sent, jumpAt, data, len(buffered))
	}

	clock.Advance(limits.StallAfter)
	waitChannelActive(t, s, id, fallback)
	blocked.complete(io.ErrClosedPipe)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != p.carriers[0] {
		t.Fatalf("fallback reset at %d on the wrong route, want %d on the parent", at, jump)
	}
	p.waitReceived(id, retained)
	closed := p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != jump+uint64(len(retained)) {
		t.Fatalf("close at %d, want %d", end, jump+uint64(len(retained)))
	}
	if !p.endedAtClose(id) {
		t.Fatal("the phone did not finish after the fallback reset, retained messages and close")
	}
	noKeyframeRequest(t, requests)
	p.checkHealthy()
}

// A reset starts its own stall interval when it is handed out. An old acknowledgement must not make a newly handed-out
// reset stuck early, and a quiet route with no data tail still becomes stuck exactly at StallAfter.
func TestVideoResetDeliveryResetStallClockStartsAtHandout(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	p.dropData(id, true)
	backlog := videoFrameMsg(1, true, 100)
	go helper.Write(backlog)
	waitFor(t, "the last unacknowledged data before the jump", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.base == 0 && ch.sent == uint64(len(backlog)) && len(ch.buf) == len(backlog)
	})
	old := activeTransport(s, id)
	blocked := installWriteGate(t, s, old, TypeReset, 0)
	requests := keyframeRequests(helper)
	s.mu.Lock()
	clock.Advance(10 * limits.StallAfter) // the last acknowledgement is long ago; the reset has not been handed out
	s.dropBacklogLocked(s.chans[id])
	s.cond.Broadcast()
	s.mu.Unlock()
	nextKeyframeRequest(t, requests)
	blocked.await(t)

	clock.Advance(limits.StallAfter - time.Millisecond)
	timer := time.NewTimer(50 * time.Millisecond) // let several watchdog ticks observe the frozen clock
	<-timer.C
	s.mu.Lock()
	dead := old.dead
	s.mu.Unlock()
	if dead {
		t.Fatal("the reset's route stalled before StallAfter after its handout")
	}
	clock.Advance(time.Millisecond)
	waitFor(t, "the reset's route to stall at StallAfter", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return old.dead
	})
	blocked.complete(io.ErrClosedPipe)
}

// A live stream has no EOF special case. A reset lost on an otherwise quiet route still stalls, moves, and is repeated
// before the next keyframe.
func TestVideoResetDeliveryLiveLostResetOnQuietRouteFallsBack(t *testing.T) {
	limits := resetTestLimits()
	limits.StallAfter = 100 * time.Millisecond
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	fallback := activeTransport(s, RFB)
	lost := installWriteGate(t, s, old, TypeReset, 0)
	p.dropResets(id, true)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	lost.await(t)
	lost.complete(nil)
	lost.waitComplete(t)

	next := installWriteGate(t, s, fallback, TypeReset, 0)
	clock.Advance(limits.StallAfter)
	waitChannelActive(t, s, id, fallback)
	next.await(t)
	p.dropResets(id, false)
	p.dropData(id, false)
	next.complete(nil)
	next.waitComplete(t)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump || reset.via != p.carriers[0] {
		t.Fatalf("fallback reset at %d on the wrong route, want %d on the parent", at, jump)
	}
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)
	p.waitReceived(id, key)
	noKeyframeRequest(t, requests)
	p.checkHealthy()
}

// An earlier close at E must not finish the phone after the first retained message re-based at E. At EOF there is no
// later keyframe, so the skip drops those messages and repeats the close at E only after its reset is confirmed.
func TestVideoResetDeliveryEOFSkipDoesNotOutrunClose(t *testing.T) {
	limits := resetTestLimits()
	l := openResetUDPLink(t, limits)
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	finalClose := installWriteGate(t, l.s, parent, TypeClose, 1)
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.drop = func(h udpFragmentHead) bool { return h.Offset == 0 }
	l.up.mu.Unlock()

	lost := videoFrameMsg(1, false, 3000)
	first := videoMessage(videoFormat, pattern(40))
	second := videoMessage(videoFormat, pattern(50))
	total := uint64(len(lost) + len(first) + len(second))
	writeAll(l.helper, [][]byte{lost, first, second})
	waitFor(t, "the initial UDP messages", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.sent == total && len(ch.buf) == int(total)
	})
	if err := l.helper.Close(); err != nil {
		t.Fatal(err)
	}
	closed := l.p.nextOf(TypeClose)
	_, oldEnd, _, _ := readChanOffset(closed.body)
	if oldEnd != total || l.p.offset(l.id) != 0 || l.p.endedAtClose(l.id) {
		t.Fatalf("unexpected initial close state: end=%d offset=%d ended=%v", oldEnd, l.p.offset(l.id), l.p.endedAtClose(l.id))
	}

	if off := l.up.skip(); off != 0 {
		t.Fatalf("skip offset %d", off)
	}
	finalClose.await(t)
	timer := time.NewTimer(50 * time.Millisecond) // allow any erroneously retained UDP messages to arrive
	<-timer.C
	if off := l.p.offset(l.id); off != total || l.p.endedAtClose(l.id) {
		t.Fatalf("retained data outran the replacement close: offset=%d, want %d, ended=%v", off, total, l.p.endedAtClose(l.id))
	}
	finalClose.complete(nil)
	finalClose.waitComplete(t)
	waitFor(t, "the phone to observe the repeated close", func() bool { return l.p.endedAtClose(l.id) })
	if off := l.p.offset(l.id); off != total {
		t.Fatalf("phone offset %d after final close, want %d", off, total)
	}
	l.p.checkHealthy()
}

// A second safety drop must retain the non-frame messages skipBacklogLocked kept for the decoder. The phone leaves the
// flow before parent delivery. A keyframe request does not make the helper repeat unchanged parameter sets.
func TestVideoResetDeliveryHandoffKeepsFormat(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.dropResets = 1
	l.up.drop = func(udpFragmentHead) bool { return true }
	l.up.mu.Unlock()

	lost := videoFrameMsg(1, false, 100)
	format := videoMessage(videoFormat, pattern(40))
	after := videoFrameMsg(2, false, 100)
	initial := join([][]byte{lost, format, after})
	writeAll(l.helper, [][]byte{lost, format, after})
	waitFor(t, "the skip backlog", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.base == 0 && ch.sent == uint64(len(initial)) && len(ch.buf) == len(initial)
	})
	requests := keyframeRequests(l.helper)
	if off := l.up.skip(); off != 0 {
		t.Fatalf("skip offset %d, want 0", off)
	}
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	jump := uint64(len(initial))
	waitFor(t, "the retained format to be dequeued and lost", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.base == jump && ch.sent == jump+uint64(len(format)) && len(ch.buf) == len(format)
	})

	parent := directTransport(l.s)
	l.up.leaveFlow()
	waitChannelActive(t, l.s, l.id, parent)
	nextKeyframeRequest(t, requests)
	key := videoFrameMsg(3, true, 100)
	writeVideo(l.helper, key)
	waitFor(t, "the keyframe after the second drop", func() bool {
		return len(l.p.received(l.id)) >= len(key) || l.p.failed()
	})
	l.p.checkHealthy()
	want := append(append([]byte(nil), format...), key...)
	if got := l.p.received(l.id); !bytes.Equal(got, want) {
		t.Fatalf("the parent delivered %d bytes, want retained format plus keyframe (%d bytes)", len(got), len(want))
	}
	reset := l.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if want := jump + uint64(len(format)); at != want {
		t.Fatalf("reset at %d, want the new message boundary %d", at, want)
	}
}

// A reset whose acknowledgement was lost is confirmed by the phone's TypeUse at the jump, not written again.
func TestVideoResetDeliveryTypeUseConfirmsJump(t *testing.T) {
	s, p, _, helper, id, _ := openVideo(t, false, resetTestLimits())
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	p.nextOf(TypeReset)
	p.carriers[0].send(TypeUse, chanOffset(id, jump))
	p.nextOf(TypeResumed)
	p.dropData(id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(helper, key)
	p.waitReceived(id, key)
	if p.resetCount() != 1 {
		t.Fatalf("TypeUse at the jump produced %d resets, want one", p.resetCount())
	}
	noKeyframeRequest(t, requests)
}

// A phone-driven move with a confirmed partial message resumes exactly at the acknowledged byte, like automatic
// fallback in R6, without another reset or keyframe request.
func TestVideoResetDeliveryTypeUseResumesPartialMessage(t *testing.T) {
	s, p, _, helper, id, _ := openVideo(t, true, resetTestLimits())
	old := activeTransport(s, id)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	p.nextOf(TypeReset)
	p.dropData(id, false)
	secondData := installWriteGate(t, s, old, TypeData, 1)
	key := videoFrameMsg(2, true, dataChunk+100)
	writeVideo(helper, key)
	secondData.await(t)
	waitFor(t, "the phone to acknowledge half a message", func() bool {
		base, _ := channelPosition(s, id)
		return p.offset(id) == jump+dataChunk && base == jump+dataChunk && p.pendingVideo(id) > 0
	})
	p.carriers[0].send(TypeUse, chanOffset(id, p.offset(id)))
	p.nextOf(TypeResumed)
	secondData.complete(io.ErrClosedPipe)
	p.waitReceived(id, key)
	if p.pendingVideo(id) != 0 || p.resetCount() != 1 {
		t.Fatalf("partial handoff left pending=%d resets=%d", p.pendingVideo(id), p.resetCount())
	}
	noKeyframeRequest(t, requests)
}

// A delivered UDP reset is acknowledged even without a completed message. When the phone later leaves the flow, that
// confirmed jump adds nothing and parent data arrives for real.
func TestVideoResetDeliveryUDPResetConfirmedBeforeFallback(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 0)
	nextKeyframeRequest(t, requests)
	reset := l.up.next(udpReset)
	if at := binary.BigEndian.Uint64(reset[2:]); at != jump {
		t.Fatalf("UDP reset at %d, want %d", at, jump)
	}
	waitFor(t, "the UDP reset acknowledgement", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return l.s.chans[l.id].jumpConfirmed
	})
	l.up.leaveFlow()
	waitChannelActive(t, l.s, l.id, parent)
	l.p.dropData(l.id, false)
	key := videoFrameMsg(2, true, 100)
	writeVideo(l.helper, key)
	l.p.waitReceived(l.id, key)
	if l.p.resetCount() != 1 {
		t.Fatalf("a confirmed UDP reset was repeated: %d resets", l.p.resetCount())
	}
	noKeyframeRequest(t, requests)
}

// Losing a UDP reset does not require a route change: want from behind the jump re-arms it on the surviving flow,
// then ordinary nack recovery completes the keyframe.
func TestVideoResetDeliveryUDPSameFlowWantRetriesReset(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	flow := udpFlowTransport(l.s)
	jump, requests := prepareUDPReset(t, l, 1)
	l.p.dropData(l.id, false) // raw TCP accounting must see any unexpected relay copy after the UDP preparation
	nextKeyframeRequest(t, requests)
	awaitUDPResetLoss(t, l.up)
	key := videoFrameMsg(2, true, 3000)
	lostFirstShard := false
	l.up.mu.Lock()
	l.up.drop = func(h udpFragmentHead) bool {
		if h.Offset == jump && h.Block == 0 && h.Index == 0 && !lostFirstShard {
			lostFirstShard = true
			return true
		}
		return false
	}
	l.up.autoAsk = true
	l.up.mu.Unlock()
	writeVideo(l.helper, key)
	waitFor(t, "the keyframe repaired on the same flow", func() bool {
		return len(l.up.delivered()) == len(key) || l.p.failed()
	})
	l.p.checkHealthy()
	if got := l.up.delivered(); !bytes.Equal(got, key) {
		t.Fatal("the same-flow keyframe differs")
	}
	if activeTransport(l.s, l.id) != flow {
		t.Fatal("want recovery moved the channel off its flow")
	}
	if n := l.p.rawTCPData(l.id); n != 0 {
		t.Fatalf("%d video bytes unexpectedly arrived on TCP", n)
	}
	noKeyframeRequest(t, requests)
}

// A stale TCP reset write completing after a UDP handoff cannot change the flow's jump state, whether it succeeds or
// fails. Once the phone leaves the flow, the UDP reset is still owed to the parent before real delivery resumes.
func TestVideoResetDeliveryStaleTCPCompletionAfterUDP(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"late failure", io.ErrClosedPipe}, {"late success", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			l := openResetUDPLink(t, resetTestLimits())
			l.up.prove()
			l.up.mu.Lock()
			l.up.autoAsk = false
			l.up.dropResets = 1
			l.up.mu.Unlock()
			old := activeTransport(l.s, l.id)
			first := installWriteGate(t, l.s, old, TypeReset, 0)
			jump, requests := prepareLostReset(t, l.s, l.p, l.helper, l.id)
			nextKeyframeRequest(t, requests)
			first.await(t)
			l.up.use(l.id, 0)
			awaitUDPResetLoss(t, l.up)
			flow := udpFlowTransport(l.s)
			before := snapshotChannel(l.s, l.id)
			if before.active != flow {
				t.Fatal("video did not move to UDP")
			}
			if tc.err == nil {
				l.p.dropResets(l.id, true)
			}
			first.completeLateWrite(t, l.s, old, tc.err)
			if tc.err == nil {
				l.p.dropResets(l.id, false)
			}
			after := snapshotChannel(l.s, l.id)
			if !sameChannelState(before, after) {
				t.Fatalf("stale TCP completion changed UDP state: before %+v, after %+v", before, after)
			}
			parent := directTransport(l.s)
			l.up.leaveFlow()
			waitChannelActive(t, l.s, l.id, parent)
			l.p.dropData(l.id, false)
			key := videoFrameMsg(2, true, 100)
			writeVideo(l.helper, key)
			l.p.waitReceived(l.id, key)
			reset := l.p.nextOf(TypeReset)
			_, at, _, _ := readChanOffset(reset.body)
			if at != jump || reset.via != l.direct {
				t.Fatalf("reset at %d on the wrong route, want %d on the UDP parent", at, jump)
			}
			noKeyframeRequest(t, requests)
		})
	}
}

// udpWant's from is phone-stated delivery evidence even when no data acknowledgement has arrived.
func TestVideoResetDeliveryUDPWantConfirmsJump(t *testing.T) {
	tr := &transport{acks: map[byte]uint64{}}
	ch := &channel{id: FirstVideo, active: tr, base: 42, sent: 42, reset: true, jumpOff: 42, jumpValid: true}
	s := &Session{chans: map[byte]*channel{FirstVideo: ch}, now: time.Now, logf: func(string, ...any) {}}
	s.cond = sync.NewCond(&s.mu)
	body := make([]byte, 17)
	body[0] = FirstVideo
	binary.BigEndian.PutUint64(body[1:], 42)
	binary.BigEndian.PutUint64(body[9:], 84)
	s.udpMessage(tr, nil, udpWant, body)
	if !ch.jumpConfirmed || ch.reset {
		t.Fatalf("udpWant did not confirm the jump: confirmed=%v reset=%v", ch.jumpConfirmed, ch.reset)
	}
}
