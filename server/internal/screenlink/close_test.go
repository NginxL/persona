package screenlink

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

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

// With or without an already confirmed jump, moving a channel does not owe another close. With no fallback, its close
// still completes at the next tick; with a fallback, no second close frame appears there.
func TestVideoResetDeliveryVideoCloseRouteChange(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		for _, how := range []string{"loss", "move"} {
			name := how + "/without_a_jump"
			if confirmed {
				name = how + "/with_a_confirmed_jump"
			}
			t.Run(name, func(t *testing.T) {
				s, p, _, helper, id, _ := openVideo(t, true, resetTestLimits())
				if confirmed {
					prepareCloseTestJump(t, s, p, helper, id)
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

				if how == "move" {
					fallback := activeTransport(s, RFB)
					p.carriers[0].send(TypeUse, chanOffset(id, p.offset(id)))
					waitFor(t, "video to move to the other route", func() bool {
						return activeTransport(s, id) == fallback
					})
					select {
					case <-repeated.entered:
						t.Fatal("the new route received a second close without a new jump")
					case <-time.After(50 * time.Millisecond):
					}
					waitResetChannelFinished(t, s, id)
					closeWrite.complete(io.ErrClosedPipe)
					closeWrite.waitComplete(t)
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
