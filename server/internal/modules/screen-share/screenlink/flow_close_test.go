package screenlink

import (
	"bytes"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"
)

// A flow the node closes by itself (#10007). The released phone ignores the video's TCP data while it takes a UDP flow
// to carry it (openResetUDPLink's phone), so what the node sends on the route it moved the video to is dropped until the
// phone leaves the flow and says so with use. That use counts as a move even on the route the video is already on: the
// node sends again from the offset the phone states. A use on a route the phone already reads from keeps what is in
// flight there.

// udpDelivered writes msgs and waits until they came over the flow and the node has the phone's acknowledgement.
func udpDelivered(t *testing.T, l *udpLink, msgs [][]byte) {
	t.Helper()
	want := join(msgs)
	writeAll(l.helper, msgs)
	waitFor(t, "the video over the flow, acknowledged", func() bool {
		base, sent := channelPosition(l.s, l.id)
		return bytes.Equal(l.up.delivered(), want) && base == sent && base == uint64(len(want))
	})
}

// droppedOnTCP writes msgs, after which the helper has sent total bytes, and waits until the node has handed the
// video from its base to the end to its route and the phone has seen all of it there (raw: its TCP data before) and
// dropped it.
func droppedOnTCP(t *testing.T, l *udpLink, msgs [][]byte, total uint64, raw int) {
	t.Helper()
	off := l.p.offset(l.id)
	writeAll(l.helper, msgs)
	waitFor(t, "the video on TCP, dropped by the phone", func() bool {
		base, sent := channelPosition(l.s, l.id)
		return sent == total && l.p.rawTCPData(l.id) == raw+int(total-base)
	})
	if l.p.offset(l.id) != off {
		t.Fatal("the phone took TCP data while it took a flow to carry the video")
	}
}

// leaveFlowFor models the released phone losing its flow's route: the flow closes with it, without a use of its own,
// and the video goes on over its relay (via) with use there.
func leaveFlowFor(l *udpLink, via *carrier) {
	_ = l.up.conn.Close()
	l.p.mu.Lock()
	delete(l.p.flowParents, l.id)
	off := l.p.recv[l.id]
	l.p.mu.Unlock()
	via.send(TypeUse, chanOffset(l.id, off))
}

// laterUseKeepsInFlight: a use on the route the phone already reads the video from (tr; via, the phone's end of it)
// leaves what the node has in flight there. A data write is held while the node handles the use: the video must not
// go back behind it, and nothing goes out twice.
func laterUseKeepsInFlight(t *testing.T, l *udpLink, tr *transport, via *carrier) {
	t.Helper()
	waitFor(t, "the video acknowledged", func() bool {
		base, sent := channelPosition(l.s, l.id)
		return base == sent && l.p.offset(l.id) == base
	})
	raw := l.p.rawTCPData(l.id)
	held := installWriteGate(t, l.s, tr, TypeData, 0)
	msg := videoFrameMsg(40, false, 300)
	writeVideo(l.helper, msg)
	held.await(t)
	base, sent := channelPosition(l.s, l.id)
	if sent != base+uint64(len(msg)) {
		t.Fatalf("sent %d, want %d in flight", sent, base+uint64(len(msg)))
	}
	via.send(TypeUse, chanOffset(l.id, l.p.offset(l.id)))
	waitFor(t, "the node to answer the use", func() bool {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		for _, m := range tr.ctl {
			if len(m) > 4 && m[4] == TypeResumed {
				return true
			}
		}
		return false
	})
	if _, now := channelPosition(l.s, l.id); now != sent {
		t.Fatalf("a use on the route the phone reads from moved the video back from %d to %d", sent, now)
	}
	held.complete(nil)
	held.waitComplete(t)
	l.p.nextOf(TypeResumed)
	fence := videoFrameMsg(41, false, 300)
	writeVideo(l.helper, fence)
	waitFor(t, "the video after the use", func() bool { return bytes.HasSuffix(l.p.received(l.id), fence) || l.p.failed() })
	l.p.checkHealthy()
	if n := l.p.rawTCPData(l.id) - raw; n != len(msg)+len(fence) {
		t.Fatalf("%d video bytes on TCP, want %d: some went out twice", n, len(msg)+len(fence))
	}
}

// resumesAfterNodeClosedFlow goes on from a flow the node is closing (first: what the helper sent before): video on
// the flow's route that the phone drops, the phone leaving the flow with use there, video after it.
func resumesAfterNodeClosedFlow(t *testing.T, l *udpLink, first [][]byte) {
	t.Helper()
	parent := directTransport(l.s)
	waitChannelActive(t, l.s, l.id, parent)
	waitFor(t, "the flow closed", func() bool { return udpFlowTransport(l.s) == nil })
	second := [][]byte{videoFrameMsg(10, false, 2000), videoMessage(videoFormat, pattern(60)), videoFrameMsg(11, false, 3000)}
	all := append(append([][]byte{}, first...), second...)
	droppedOnTCP(t, l, second, uint64(len(join(all))), 0)

	// The flow's pings go unanswered: the phone leaves it with use on its route, which the video is already on.
	off := l.p.offset(l.id)
	l.up.leaveFlow()
	l.p.nextOf(TypeResumed)
	third := [][]byte{videoFrameMsg(12, false, 500), videoFrameMsg(13, false, 20000)}
	all = append(all, third...)
	writeAll(l.helper, third)
	l.p.waitReceived(l.id, join(all)[off:])
	if l.p.resetCount() != 0 || activeTransport(l.s, l.id) != parent {
		t.Fatalf("%d resets, want none, and the video on the flow's route", l.p.resetCount())
	}
	laterUseKeepsInFlight(t, l, parent, l.direct)
}

// The issue's sequence, with each of the node's watchdogs closing the flow: no gap, and the video goes on from where
// the phone is; a later use on the same route does not send again what is in flight.
func TestNodeClosedFlowUseSendsAgain(t *testing.T) {
	t.Run("flow silence", func(t *testing.T) {
		limits := resetTestLimits()
		limits.FlowSilence = 300 * time.Millisecond
		l := openResetUDPLink(t, limits)
		l.up.prove()
		l.up.use(l.id, 0)
		first := videoStream(4)
		udpDelivered(t, l, first)
		// The phone sends nothing more on the flow: the node closes it after FlowSilence.
		resumesAfterNodeClosedFlow(t, l, first)
	})
	t.Run("stall", func(t *testing.T) {
		limits := resetTestLimits()
		limits.StallAfter = 10 * time.Second
		l := openResetUDPLink(t, limits)
		clock := useClock(l.s)
		l.up.prove()
		l.up.use(l.id, 0)
		l.up.mu.Lock()
		l.up.drop = func(udpFragmentHead) bool { return true } // nothing on the flow arrives, nothing is acknowledged
		l.up.mu.Unlock()
		first := videoStream(4)
		writeAll(l.helper, first)
		waitFor(t, "the video sent on the flow", func() bool {
			_, sent := channelPosition(l.s, l.id)
			return sent == uint64(len(join(first)))
		})
		clock.Advance(limits.StallAfter + time.Second) // the node closes the stuck flow
		resumesAfterNodeClosedFlow(t, l, first)
	})
}

// The use after a flow the node closed applies the move's age rule: a backlog the phone dropped that is older than
// VideoBacklog is not sent again; the stream goes on at a reset and a new keyframe.
func TestNodeClosedFlowUseDropsAnOldBacklog(t *testing.T) {
	limits := resetTestLimits()
	limits.VideoBacklog = 200 * time.Millisecond
	l := openResetUDPLink(t, limits)
	clock := useClock(l.s)
	requests := keyframeRequests(l.helper)
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	first := videoStream(4)
	udpDelivered(t, l, first)
	udpFlowTransport(l.s).flow.closeTransport() // as when its packet numbers run out
	waitChannelActive(t, l.s, l.id, parent)
	second := [][]byte{videoFrameMsg(10, false, 2000), videoFrameMsg(11, false, 3000)}
	all := join(append(append([][]byte{}, first...), second...))
	droppedOnTCP(t, l, second, uint64(len(all)), 0)

	clock.Advance(time.Second)
	l.up.leaveFlow()
	reset := l.p.nextOf(TypeReset)
	if _, at, _, _ := readChanOffset(reset.body); at != uint64(len(all)) || reset.via != l.direct {
		t.Fatalf("reset at %d, want %d on the flow's route", at, len(all))
	}
	nextKeyframeRequest(t, requests)
	key := videoFrameMsg(12, true, 500)
	writeVideo(l.helper, key)
	l.p.waitReceived(l.id, key)
}

// The phone leaves the flow first: its use moves the video as before. The node closing the flow afterwards changes
// nothing, and a later use on the route does not send again what is in flight.
func TestPhoneLeavingTheFlowFirstIsUnchanged(t *testing.T) {
	l := openResetUDPLink(t, resetTestLimits())
	l.up.prove()
	l.up.use(l.id, 0)
	parent := directTransport(l.s)
	udpDelivered(t, l, videoStream(4))
	flow := udpFlowTransport(l.s)
	l.up.leaveFlow()
	waitChannelActive(t, l.s, l.id, parent)
	l.p.nextOf(TypeResumed)
	flow.flow.closeTransport()
	waitFor(t, "the flow closed", func() bool { return udpFlowTransport(l.s) == nil })
	second := videoStream(3)
	writeAll(l.helper, second)
	l.p.waitReceived(l.id, join(second))
	if n := l.p.rawTCPData(l.id); n != len(join(second)) || l.p.resetCount() != 0 {
		t.Fatalf("%d video bytes on TCP and %d resets, want %d and none", n, l.p.resetCount(), len(join(second)))
	}
	laterUseKeepsInFlight(t, l, parent, l.direct)
}

// The flow's route drops before the phone's use, with the flow or after it: the node moves the video to its relay by
// itself, and the phone, which dropped what came there, moves it there with use. That use counts as a move too.
func TestNodeClosedFlowRouteDropsBeforeUse(t *testing.T) {
	for _, flowFirst := range []bool{false, true} {
		name := "with the flow"
		if flowFirst {
			name = "after the flow"
		}
		t.Run(name, func(t *testing.T) {
			l := openResetUDPLink(t, resetTestLimits())
			relay := l.p.carriers[1] // the video's relay, as openUDPLink carries it
			l.up.prove()
			l.up.use(l.id, 0)
			parent := directTransport(l.s)
			l.s.mu.Lock()
			home := l.s.chans[l.id].home
			l.s.mu.Unlock()
			first := videoStream(4)
			udpDelivered(t, l, first)
			all := append([][]byte{}, first...)
			raw := 0
			if flowFirst {
				udpFlowTransport(l.s).flow.closeTransport()
				waitChannelActive(t, l.s, l.id, parent)
				second := [][]byte{videoFrameMsg(10, false, 2000), videoFrameMsg(11, false, 3000)}
				all = append(all, second...)
				droppedOnTCP(t, l, second, uint64(len(join(all))), 0)
				raw = l.p.rawTCPData(l.id)
			}
			l.dc.Close()
			waitChannelActive(t, l.s, l.id, home)
			more := [][]byte{videoFrameMsg(20, false, 1000)}
			all = append(all, more...)
			droppedOnTCP(t, l, more, uint64(len(join(all))), raw)

			off := l.p.offset(l.id)
			leaveFlowFor(l, relay)
			l.p.nextOf(TypeResumed)
			last := [][]byte{videoFrameMsg(30, false, 700)}
			all = append(all, last...)
			writeAll(l.helper, last)
			l.p.waitReceived(l.id, join(all)[off:])
			laterUseKeepsInFlight(t, l, home, relay)
		})
	}
}

// Review of #10025 (P1): a route left unread by a flow the node closed does not outlive the phone's move onto another
// flow. The phone's UDP use there is a move like any other (moveLocked clears unread), so once it later leaves that
// flow its use on the route counts as the move it is, and a use on a route it reads from keeps what is in flight.
func TestNodeClosedFlowThenAnotherFlowClearsUnread(t *testing.T) {
	limits := resetTestLimits()
	limits.FlowSilence = 300 * time.Millisecond
	l := openResetUDPLink(t, limits)
	l.up.prove()
	l.up.use(l.id, 0)
	udpDelivered(t, l, videoStream(4))
	parent := directTransport(l.s)
	waitChannelActive(t, l.s, l.id, parent)
	waitFor(t, "the flow closed", func() bool { return udpFlowTransport(l.s) == nil })
	unread := func() *transport {
		l.s.mu.Lock()
		defer l.s.mu.Unlock()
		return l.s.chans[l.id].unread
	}
	if unread() != parent {
		t.Fatal("the node's own close did not leave the flow's route unread")
	}

	// The phone moves the video straight onto another flow on the same route, with no use on TCP in between (it opened
	// the new flow before its pings on the old one went unanswered).
	_ = l.up.conn.Close()
	l.direct.send(TypeUDPOpen)
	var ready UDPReady
	if err := json.Unmarshal(l.p.nextOf(TypeUDPReady).body, &ready); err != nil || ready.Port == 0 {
		t.Fatalf("udp_ready %+v %v", ready, err)
	}
	nodeAt, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(l.paths.Candidates[0].Addr, strconv.Itoa(ready.Port)))
	l.up = newUDPPhone(t, l.p, l.dc, ready, nodeAt)
	l.up.ch = l.id
	l.up.parent = l.direct
	l.up.prove()
	l.up.use(l.id, l.p.offset(l.id))
	flow := udpFlowTransport(l.s)
	if flow == nil || activeTransport(l.s, l.id) != flow {
		t.Fatal("the video is not on the phone's new flow")
	}
	if u := unread(); u != nil {
		t.Fatalf("the phone's move onto another flow left an unread route (%p)", u)
	}
}
