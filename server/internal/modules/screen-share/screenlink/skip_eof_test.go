package screenlink

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// With EOF known but no close handed out, a phone skip keeps whole decoder messages exactly as on main. Holding the
// session lock applies EOF and the skip before the UDP writer can make a close decision.
func TestVideoResetDeliveryEOFSkipWithoutCloseKeepsFormat(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	l.up.mu.Lock()
	l.up.autoAsk = false
	l.up.drop = func(udpFragmentHead) bool { return true }
	l.up.mu.Unlock()

	lost := videoFrameMsg(1, false, 3000)
	first := videoMessage(videoFormat, pattern(40))
	second := videoMessage(videoFormat, pattern(50))
	initial := join([][]byte{lost, first, second})
	writeAll(l.helper, [][]byte{lost, first, second})
	waitFor(t, "the initial messages before EOF and skip", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		ch := l.s.chans[l.id]
		return ch.sent == uint64(len(initial)) && len(ch.buf) == len(initial)
	})
	requests := keyframeRequests(l.helper)
	l.up.mu.Lock()
	l.up.drop = func(h udpFragmentHead) bool { return h.Offset < uint64(len(initial)) }
	l.up.mu.Unlock()
	l.s.mu.Lock()
	ch := l.s.chans[l.id]
	if ch.sentClose {
		l.s.mu.Unlock()
		t.Fatal("the setup handed out a close before the skip")
	}
	ch.eof = true
	l.s.skipBacklogLocked(ch)
	l.s.cond.Broadcast()
	l.s.mu.Unlock()
	nextKeyframeRequest(t, requests)

	reset := l.up.next(udpReset)
	if at := binary.BigEndian.Uint64(reset[2:]); at != uint64(len(initial)) {
		t.Fatalf("reset at %d, want %d", at, len(initial))
	}
	want := join([][]byte{first, second})
	waitFor(t, "the retained decoder messages or an early close", func() bool {
		return len(l.up.delivered()) >= len(want) || l.p.endedAtClose(l.id)
	})
	if got := l.up.delivered(); !bytes.Equal(got, want) {
		t.Fatalf("skip after EOF delivered %d decoder bytes, want %d", len(got), len(want))
	}
	closed := l.p.nextOf(TypeClose)
	_, end, _, _ := readChanOffset(closed.body)
	if wantEnd := uint64(len(initial) + len(want)); end != wantEnd {
		t.Fatalf("close at %d, want %d", end, wantEnd)
	}
	waitFor(t, "the phone to finish after retained messages and close", func() bool { return l.p.endedAtClose(l.id) })
	l.p.checkHealthy()
}
