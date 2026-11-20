package screenlink

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// closeAckTypeForTest is the independently specified phone → node close confirmation: u8 channel, u64 final offset.
// It is deliberately not TypeAck: a final-data ack may cross with a close the phone has not processed yet.
const closeAckTypeForTest byte = 14

func waitResetChannelFinished(t *testing.T, s *Session, id byte) {
	t.Helper()
	waitFor(t, "the channel to finish at the next tick", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.chans[id].finished
	})
}

// A close already handed to a writer is final unless a later jump replaces it. Losing the only route does not delay
// ordinary RFB completion until Grace; finishing RFB ends the whole session at the next watchdog tick.
func TestVideoResetDeliveryRFBCloseLoss(t *testing.T) {
	reg := NewRegistry(resetTestLimits(), nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, true)
	p.carry(relay)
	p.next(TypePaths)
	closeWrite := installWriteGate(t, s, activeTransport(s, RFB), TypeClose, 0)
	if err := ss.Close(); err != nil {
		t.Fatal(err)
	}
	closeWrite.await(t)
	closeWrite.complete(io.ErrClosedPipe)
	closeWrite.waitComplete(t)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("RFB did not finish at the next tick after its only route was lost")
	}
}

// prepareCloseTestJump creates and delivers a backlog reset. With the phone acknowledging normally, a subsequent close
// cannot be dequeued on the reset-delivery implementation until that jump is confirmed; main has the same wire result.
func prepareCloseTestJump(t *testing.T, s *Session, p *phone, helper net.Conn, id byte) uint64 {
	t.Helper()
	p.dropData(id, true)
	lost := videoFrameMsg(1, true, 100)
	go func() { _, _ = helper.Write(lost) }()
	waitFor(t, "the backlog to be handed out and lost", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.sent == uint64(len(lost)) && len(ch.buf) == len(lost)
	})
	requests := keyframeRequests(helper)
	s.mu.Lock()
	s.dropBacklogLocked(s.chans[id])
	jump := s.chans[id].base
	s.cond.Broadcast()
	s.mu.Unlock()
	nextKeyframeRequest(t, requests)
	p.dropData(id, false)
	reset := p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	if at != jump {
		t.Fatalf("reset at %d, want %d", at, jump)
	}
	return jump
}

// A close remains owed after a route change until the phone acknowledges its final offset. With no fallback, the
// channel still completes at the next tick rather than waiting for a route on which to repeat it.
func TestVideoResetDeliveryVideoCloseRouteChange(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		for _, how := range []string{"loss", "move"} {
			name := how + "/without_a_jump"
			if confirmed {
				name = how + "/with_a_confirmed_jump"
			}
			t.Run(name, func(t *testing.T) {
				s, p, cv, helper, id, _ := openVideo(t, true, resetTestLimits())
				var jump uint64
				if confirmed {
					jump = prepareCloseTestJump(t, s, p, helper, id)
				}
				if how == "loss" {
					if err := p.carriers[0].conn.Close(); err != nil {
						t.Fatal(err)
					}
					waitFor(t, "the RFB relay to disappear, leaving only video's route", func() bool {
						s.mu.Lock()
						defer s.mu.Unlock()
						return len(s.ts) == 1 && s.chans[id].active != nil
					})
				}
				old := activeTransport(s, id)
				closeWrite := installWriteGate(t, s, old, TypeClose, 0)
				var repeated *writeGate
				if how == "move" {
					repeated = installWriteGate(t, s, activeTransport(s, RFB), TypeClose, 0)
				}
				if err := helper.Close(); err != nil {
					t.Fatal(err)
				}
				closeWrite.await(t)
				if confirmed && how == "move" {
					// A confirmation below the close's final offset is stale and must leave the close owed.
					cv.send(closeAckTypeForTest, chanOffset(id, jump-1))
					closeTestQueuedPongFence(t, s, old, cv)
				}

				if how == "move" {
					fallback := activeTransport(s, RFB)
					via := p.carriers[0]
					via.send(TypeUse, chanOffset(id, p.offset(id)))
					waitFor(t, "video to move to the other route", func() bool {
						return activeTransport(s, id) == fallback
					})
					repeated.await(t)
					repeated.complete(nil)
					repeated.waitComplete(t)
					closed := p.nextOf(TypeClose)
					_, end, _, _ := readChanOffset(closed.body)
					if end != p.offset(id) || closed.via != via {
						t.Fatalf("repeated close at %d on the wrong route, want %d on the fallback", end, p.offset(id))
					}
					closeWrite.complete(io.ErrClosedPipe)
					closeWrite.waitComplete(t)
					via.send(closeAckTypeForTest, chanOffset(id, end))
					waitResetChannelFinished(t, s, id)
					closeTestPingFence(t, p, via)
					requireSingleRepeatedClose(t, repeated)
					return
				}

				closeWrite.complete(io.ErrClosedPipe)
				closeWrite.waitComplete(t)
				waitResetChannelFinished(t, s, id)
			})
		}
	}
}

// EOF in the ordinary reset/acknowledgement round trip does not discard the final stream. Once the reset is confirmed,
// the phone receives every retained message and then the close, byte for byte.
func TestVideoResetDeliveryEOFRetainsMessagesUntilAcknowledgement(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, false, resetTestLimits())
	jump := prepareCloseTestJump(t, s, p, helper, id)
	dataWrite := installWriteGate(t, s, activeTransport(s, id), TypeData, 0)
	key := videoFrameMsg(2, true, 80)
	format := videoMessage(videoFormat, pattern(40))
	retained := append(append([]byte(nil), key...), format...)
	go func() {
		_, _ = helper.Write(retained)
		_ = helper.Close()
	}()
	dataWrite.await(t)
	waitFor(t, "EOF after the retained messages", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.chans[id].eof
	})
	s.mu.Lock()
	base, buffered := s.chans[id].base, append([]byte(nil), s.chans[id].buf...)
	s.mu.Unlock()
	if base != jump || !bytes.Equal(buffered, retained) {
		t.Fatalf("EOF changed the unconfirmed stream: base=%d, buffered=%d; want base=%d, bytes=%d", base, len(buffered), jump, len(retained))
	}

	cv.send(TypeAck, chanOffset(id, jump))
	dataWrite.complete(nil)
	dataWrite.waitComplete(t)
	p.waitReceived(id, retained)
	closed := p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != jump+uint64(len(retained)) {
		t.Fatalf("close at %d, want %d", end, jump+uint64(len(retained)))
	}
	if got := p.received(id); !bytes.Equal(got, retained) {
		t.Fatalf("close arrived after %d retained bytes, want %d", len(got), len(retained))
	}
	if !p.endedAtClose(id) {
		t.Fatal("the phone did not finish after the retained messages and close")
	}
	p.checkHealthy()
}

// closeTestPingFence proves that messages sent earlier on the same route have been handled.
func closeTestPingFence(t *testing.T, p *phone, c *carrier) {
	t.Helper()
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, 10006)
	c.send(TypePing, body)
	pong := p.nextOf(TypePong)
	if len(pong.body) < 8 || binary.BigEndian.Uint64(pong.body) != 10006 {
		t.Fatalf("pong %v, want the close test's fence", pong.body)
	}
}

// A writer gated on close cannot write a pong. Its queued pong is the fence that earlier frames on that route were read.
func closeTestQueuedPongFence(t *testing.T, s *Session, tr *transport, c *carrier) {
	t.Helper()
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, 10006)
	c.send(TypePing, body)
	waitFor(t, "the pong queued behind the gated close", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, m := range tr.ctl {
			if len(m) >= 13 && m[4] == TypePong && binary.BigEndian.Uint64(m[5:13]) == 10006 {
				return true
			}
		}
		return false
	})
}

func requireSingleRepeatedClose(t *testing.T, g *writeGate) {
	t.Helper()
	g.mu.Lock()
	writes := g.intercept
	g.mu.Unlock()
	if writes != 1 {
		t.Fatalf("the later route wrote %d closes, want exactly one", writes)
	}
}

// The final data ack can cross with the close: the node has dequeued close, but its gated write has not let the phone
// process it. That ordinary TypeAck, even at the close's final offset, is not close confirmation; the next route must
// still offer close. Only the distinct close_ack releases the channel.
func TestCloseAcknowledgementFinalDataAckCrossingDoesNotConfirm(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, false, resetTestLimits())
	old := activeTransport(s, id)
	first := installWriteGate(t, s, old, TypeClose, 0)
	fallback := activeTransport(s, RFB)
	via := p.carriers[0]
	repeated := installWriteGate(t, s, fallback, TypeClose, 0)
	last := videoFrameMsg(1, true, 100)
	end := uint64(len(last))
	go func() {
		_, _ = helper.Write(last)
		_ = helper.Close()
	}()
	first.await(t)
	waitFor(t, "the final data at the phone", func() bool { return p.offset(id) == end })
	if got := p.offset(id); got != end {
		t.Fatalf("phone offset %d, want final data offset %d", got, end)
	}
	cv.send(TypeAck, chanOffset(id, end)) // ordinary data acknowledgement, not a close acknowledgement
	closeTestQueuedPongFence(t, s, old, cv)
	cv.send(closeAckTypeForTest, chanOffset(id, end-1)) // stale close confirmation is not enough either
	closeTestQueuedPongFence(t, s, old, cv)

	via.send(TypeUse, chanOffset(id, end))
	waitChannelActive(t, s, id, fallback)
	repeated.await(t)
	repeated.complete(nil)
	repeated.waitComplete(t)
	closed := p.nextOf(TypeClose)
	closeID, closeEnd, _, _ := readChanOffset(closed.body)
	if closeID != id || closeEnd != end || closed.via != via {
		t.Fatalf("repeated close channel %d at %d on the wrong route, want channel %d at %d on the fallback", closeID, closeEnd, id, end)
	}
	first.complete(io.ErrClosedPipe)
	first.waitComplete(t)
	via.send(closeAckTypeForTest, chanOffset(id, end))
	waitResetChannelFinished(t, s, id)
	closeTestPingFence(t, p, via)
	requireSingleRepeatedClose(t, repeated)
	p.checkHealthy()
}

// A legacy phone acknowledges data but sends no extra acknowledgement for a close. An unacknowledged RFB close still
// ends its channel and session at the next watchdog tick, with Grace deliberately too long to be responsible.
func TestCloseAcknowledgementLegacyRFBCloseFinishesAtNextTick(t *testing.T) {
	reg := NewRegistry(resetTestLimits(), nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, true)
	p.ackClose = false // legacy: keep data/reset acks, but send nothing new for a close
	p.carry(relay)
	p.next(TypePaths)
	go func() {
		_, _ = ss.Write([]byte("bye"))
		_ = ss.Close()
	}()
	closed := p.next(TypeClose)
	if id, end, _, _ := readChanOffset(closed.body); id != RFB || end != 3 {
		t.Fatalf("close channel %d at %d, want RFB at 3", id, end)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an unconfirmed RFB close waited for a route or Grace instead of finishing at the next tick")
	}
}

// A UDP flow's close is written on its parent TLS route. If that write fails after the final jump was confirmed, the
// fallback owes exactly the same close; acknowledging its final offset lets the channel finish.
func TestCloseAcknowledgementUDPParentWriteFailure(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	jump, requests := prepareUDPReset(t, l, 0)
	nextKeyframeRequest(t, requests)
	reset := l.up.next(udpReset)
	if at := binary.BigEndian.Uint64(reset[2:]); at != jump {
		t.Fatalf("reset at %d, want %d", at, jump)
	}
	waitFor(t, "the final UDP jump to be confirmed", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return l.s.chans[l.id].jumpConfirmed
	})

	parent := directTransport(l.s)
	first := installWriteGate(t, l.s, parent, TypeClose, 0)
	l.s.mu.Lock()
	fallback := l.s.chans[l.id].home
	l.s.mu.Unlock()
	if fallback == nil {
		t.Fatal("the video channel has no relay fallback")
	}
	repeated := installWriteGate(t, l.s, fallback, TypeClose, 0)
	if err := l.helper.Close(); err != nil {
		t.Fatal(err)
	}
	first.await(t)
	first.complete(io.ErrClosedPipe)
	first.waitComplete(t)
	waitChannelActive(t, l.s, l.id, fallback)
	repeated.await(t)
	repeated.complete(nil)
	repeated.waitComplete(t)

	closed := l.p.nextOf(TypeClose)
	id, end, _, _ := readChanOffset(closed.body)
	if id != l.id || end != jump || closed.via != l.p.carriers[1] {
		t.Fatalf("repeated close channel %d at %d on the wrong route, want channel %d at %d on the video relay", id, end, l.id, jump)
	}
	l.p.carriers[1].send(closeAckTypeForTest, chanOffset(l.id, end))
	waitResetChannelFinished(t, l.s, l.id)
	closeTestPingFence(t, l.p, l.p.carriers[1])
	requireSingleRepeatedClose(t, repeated)
	l.p.checkHealthy()
}

// Older nodes ignore the new confirmation type, so duplicates and unknown channels must remain harmless to new ones.
// They and a phone's repeated close must leave an ended channel untouched and its transport healthy.
func TestCloseAcknowledgementDuplicateStaleAndUnknownAreHarmless(t *testing.T) {
	s, p, cv, helper, id, _ := openVideo(t, false, resetTestLimits())
	jump := prepareCloseTestJump(t, s, p, helper, id)
	if err := helper.Close(); err != nil {
		t.Fatal(err)
	}
	cv.send(TypeAck, chanOffset(id, jump)) // confirm the final jump, releasing the close
	closed := p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != jump {
		t.Fatalf("close at %d, want %d", end, jump)
	}
	cv.send(closeAckTypeForTest, chanOffset(id, end))
	waitResetChannelFinished(t, s, id)
	base, sent := channelPosition(s, id)
	tr := activeTransport(s, id)

	cv.send(closeAckTypeForTest, chanOffset(id, end))   // duplicate close confirmation
	cv.send(closeAckTypeForTest, chanOffset(id, end-1)) // stale confirmation
	cv.send(closeAckTypeForTest, chanOffset(0xff, end)) // a channel this session never had
	cv.send(TypeClose, chanOffset(id, end))             // a repeated phone close is idempotent too
	cv.send(TypeClose, chanOffset(id, end))
	closeTestPingFence(t, p, cv)

	afterBase, afterSent := channelPosition(s, id)
	if afterBase != base || afterSent != sent {
		t.Fatalf("confirmations changed the ended channel from (%d, %d) to (%d, %d)", base, sent, afterBase, afterSent)
	}
	s.mu.Lock()
	dead, finished := tr.dead, s.chans[id].finished
	s.mu.Unlock()
	if dead || !finished {
		t.Fatalf("confirmations after EOF left transport dead=%v, channel finished=%v", dead, finished)
	}
	p.checkHealthy()
}

func newCloseAckUnitSession(id byte) (*Session, *channel) {
	s := &Session{limits: resetTestLimits(), logf: func(string, ...any) {}, now: time.Now,
		chans: map[byte]*channel{}, ts: map[*transport]bool{}, done: make(chan struct{}), onEnd: func() {}}
	s.cond = sync.NewCond(&s.mu)
	ch := &channel{id: id, local: nopCloser{&bytes.Buffer{}}}
	s.chans[id] = ch
	return s, ch
}

// A route can lose a repeated close before its writer dequeues it. The close still went out on an earlier route, so
// having no route now finishes at the next tick just as it does after the first close's write fails.
func TestCloseAcknowledgementNoRouteBeforeRepeatedCloseFinishes(t *testing.T) {
	for _, id := range []byte{RFB, FirstVideo} {
		t.Run(fmt.Sprintf("channel_%d", id), func(t *testing.T) {
			s, ch := newCloseAckUnitSession(id)
			defer s.End()
			old, next := &transport{}, &transport{conn: nopCloser{&bytes.Buffer{}}}
			ch.eof, ch.sentClose, ch.closeRoute, ch.active = true, true, old, old
			s.ts[next] = true
			s.moveLocked(ch, next, 0)
			if ch.sentClose || ch.closeRoute != old {
				t.Fatal("the test did not owe the first close on the next route")
			}
			s.lose(next)
			if ch.active != nil || ch.sentClose || ch.closeRoute == nil {
				t.Fatal("the test did not lose the route before its repeated close went out")
			}
			go s.watch()
			waitResetChannelFinished(t, s, id)
		})
	}
}

// A repeated close can still be in a UDP flow parent's control queue when close_ack arrives. Confirmation removes every
// queued copy of that close, without disturbing earlier control traffic or a close for a different final offset.
func TestCloseAcknowledgementCancelsQueuedUDPParentClose(t *testing.T) {
	s, ch := newCloseAckUnitSession(FirstVideo)
	defer s.End()
	parent := &transport{}
	s.ts[parent] = true
	ch.eof, ch.sentClose, ch.closeEnd, ch.closeRoute = true, true, 9, parent
	ping := frame(TypePing, make([]byte, 8))
	otherClose := frame(TypeClose, chanOffset(ch.id, 10))
	parent.ctl = [][]byte{ping, frame(TypeClose, chanOffset(ch.id, ch.closeEnd)), otherClose,
		frame(TypeClose, chanOffset(ch.id, ch.closeEnd))}
	if !s.handle(parent, closeAckTypeForTest, chanOffset(ch.id, ch.closeEnd)) {
		t.Fatal("a valid close acknowledgement dropped the transport")
	}
	if !ch.closeAcked {
		t.Fatal("the close was not confirmed")
	}
	if len(parent.ctl) != 2 || !bytes.Equal(parent.ctl[0], ping) || !bytes.Equal(parent.ctl[1], otherClose) {
		t.Fatalf("confirmation left a queued repeated close: %x", parent.ctl)
	}
}
