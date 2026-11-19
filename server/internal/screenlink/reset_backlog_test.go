package screenlink

import (
	"net"
	"testing"
	"time"
)

type bufferedJump struct {
	s        *Session
	p        *phone
	helper   net.Conn
	id       byte
	jump     uint64
	requests <-chan []byte
	fallback *transport
	clock    *fakeClock
	total    uint64
}

// prepareBufferedJump holds the old writer after its reset, or after its first post-jump data message, while complete
// messages accumulate. Every mark uses the fake clock; resetTestLimits' other hour-long values do not hide backlog age.
func prepareBufferedJump(t *testing.T, limits Limits, withData, eof bool, msgs ...[]byte) bufferedJump {
	t.Helper()
	s, p, _, helper, id, _ := openVideo(t, true, limits)
	clock := useClock(s)
	old := activeTransport(s, id)
	resetWrite := installWriteGate(t, s, old, TypeReset, 0)
	var dataWrite *writeGate
	if withData {
		dataWrite = installWriteGate(t, s, old, TypeData, 1)
	}
	p.dropResets(id, true)
	jump, requests := prepareLostReset(t, s, p, helper, id)
	nextKeyframeRequest(t, requests)
	resetWrite.await(t)
	if withData {
		resetWrite.complete(nil)
		resetWrite.waitComplete(t)
	}
	if eof {
		go func() {
			for _, msg := range msgs {
				if _, err := helper.Write(msg); err != nil {
					return
				}
			}
			_ = helper.Close()
		}()
	} else {
		writeAll(helper, msgs)
	}
	if withData {
		dataWrite.await(t)
	}
	var total uint64
	for _, msg := range msgs {
		total += uint64(len(msg))
	}
	waitFor(t, "the buffered messages behind the unconfirmed jump", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		return ch.base == jump && len(ch.buf) == int(total) && ch.jumpData == withData && ch.eof == eof &&
			((withData && ch.sent > jump) || (!withData && ch.sent == jump))
	})
	return bufferedJump{s: s, p: p, helper: helper, id: id, jump: jump, requests: requests,
		fallback: activeTransport(s, RFB), clock: clock, total: total}
}

func (b bufferedJump) moveAfterAge(t *testing.T, watchdog bool) {
	t.Helper()
	b.p.dropResets(b.id, false)
	b.p.dropData(b.id, false)
	if watchdog {
		waitChannelActive(t, b.s, b.id, b.fallback)
		return
	}
	b.p.carriers[0].send(TypeUse, chanOffset(b.id, 0)) // below the unconfirmed jump
	waitChannelActive(t, b.s, b.id, b.fallback)
}

// Backlog age is considered before the unconfirmed-jump decision on both a phone move and watchdog fallback. A young
// backlog keeps the same reset and every message; an old one is replaced by a reset at its end and a new keyframe.
func TestVideoResetDeliveryMoveBacklogAgePrecedesJumpState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		old      bool
		watchdog bool
		backlog  time.Duration
		stall    time.Duration
		advance  time.Duration
	}{
		{name: "old_phone_move", old: true, backlog: 40 * time.Millisecond, stall: 100 * time.Millisecond, advance: 50 * time.Millisecond},
		{name: "old_watchdog", old: true, watchdog: true, backlog: 40 * time.Millisecond, stall: 100 * time.Millisecond, advance: 100 * time.Millisecond},
		{name: "young_phone_move", backlog: 40 * time.Millisecond, stall: 100 * time.Millisecond},
		{name: "young_watchdog", watchdog: true, backlog: 200 * time.Millisecond, stall: 100 * time.Millisecond, advance: 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := resetTestLimits()
			limits.VideoBacklog, limits.StallAfter = tc.backlog, tc.stall
			messages := [][]byte{videoFrameMsg(2, true, 100), videoMessage(videoFormat, pattern(40)), videoFrameMsg(3, false, 100)}
			b := prepareBufferedJump(t, limits, false, false, messages...)
			b.clock.Advance(tc.advance)
			b.moveAfterAge(t, tc.watchdog)

			wantReset := b.jump
			want := join(messages)
			if tc.old {
				wantReset += b.total
				nextKeyframeRequest(t, b.requests)
				want = videoFrameMsg(10, true, 100)
			}
			reset := b.p.nextOf(TypeReset)
			_, at, _, _ := readChanOffset(reset.body)
			if at != wantReset || reset.via != b.p.carriers[0] {
				t.Fatalf("reset at %d on the wrong route, want %d on fallback", at, wantReset)
			}
			if tc.old {
				writeVideo(b.helper, want)
			}
			b.p.waitReceived(b.id, want)
			if !tc.old {
				noKeyframeRequest(t, b.requests) // a young backlog reuses the first request and the same reset
			}
			b.p.checkHealthy()
		})
	}
}

// Once post-jump data was handed out, age still chooses main's full drop for an old backlog. Only a young backlog uses
// the frame-only safety drop that retains decoder messages.
func TestVideoResetDeliveryMoveBacklogAgeChoosesDropAfterData(t *testing.T) {
	for _, old := range []bool{true, false} {
		name := "young_retains_format"
		if old {
			name = "old_drops_format_too"
		}
		t.Run(name, func(t *testing.T) {
			limits := resetTestLimits()
			limits.VideoBacklog = 40 * time.Millisecond
			messages := [][]byte{videoFrameMsg(2, true, 100), videoMessage(videoFormat, pattern(40)), videoFrameMsg(3, false, 100)}
			b := prepareBufferedJump(t, limits, true, false, messages...)
			if old {
				b.clock.Advance(50 * time.Millisecond)
			}
			b.moveAfterAge(t, false)
			nextKeyframeRequest(t, b.requests)
			reset := b.p.nextOf(TypeReset)
			_, at, _, _ := readChanOffset(reset.body)
			if want := b.jump + b.total; at != want || reset.via != b.p.carriers[0] {
				t.Fatalf("reset at %d on the wrong route, want %d on fallback", at, want)
			}
			key := videoFrameMsg(10, true, 100)
			writeVideo(b.helper, key)
			want := key
			if !old {
				want = append(append([]byte(nil), messages[1]...), key...)
			}
			b.p.waitReceived(b.id, want)
			b.p.checkHealthy()
		})
	}
}

// EOF does not make an old retained backlog young. The move drops it, and the reset at its end is followed by close once
// the phone confirms that jump; none of the retained messages is replayed.
func TestVideoResetDeliveryEOFOldBacklogDropsBeforeClose(t *testing.T) {
	limits := resetTestLimits()
	limits.VideoBacklog = 40 * time.Millisecond
	b := prepareBufferedJump(t, limits, false, true,
		videoFrameMsg(2, true, 100), videoMessage(videoFormat, pattern(40)))
	b.clock.Advance(50 * time.Millisecond)
	b.moveAfterAge(t, false)
	reset := b.p.nextOf(TypeReset)
	_, at, _, _ := readChanOffset(reset.body)
	want := b.jump + b.total
	if at != want || reset.via != b.p.carriers[0] {
		t.Fatalf("reset at %d on the wrong route, want %d on fallback", at, want)
	}
	closed := b.p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if end != want {
		t.Fatalf("close at %d, want %d", end, want)
	}
	if got := b.p.received(b.id); len(got) != 0 {
		t.Fatalf("EOF replayed %d old buffered bytes", len(got))
	}
	waitFor(t, "the phone to finish after the end reset and close", func() bool { return b.p.endedAtClose(b.id) })
	b.p.checkHealthy()
}
