package screenlink

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// writeGate holds one selected write after nextLocked or nextUDPLocked has dequeued it. The test then chooses whether
// that write reaches the phone, fails, or completes late. Reads stay on the original connection.
type writeGate struct {
	io.ReadWriteCloser
	typ       byte
	skip      int
	result    chan error
	entered   chan struct{}
	finished  chan struct{}
	proceeded chan struct{}

	mu        sync.Mutex
	intercept int
	fired     bool
}

func installWriteGate(t *testing.T, s *Session, tr *transport, typ byte, skip int) *writeGate {
	t.Helper()
	g := &writeGate{typ: typ, skip: skip, result: make(chan error, 1), entered: make(chan struct{}), finished: make(chan struct{}), proceeded: make(chan struct{})}
	s.mu.Lock()
	g.ReadWriteCloser = tr.conn
	tr.conn = g
	s.mu.Unlock()
	t.Cleanup(func() {
		select {
		case g.result <- net.ErrClosed:
		default:
		}
	})
	return g
}

func activeTransport(s *Session, id byte) *transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chans[id].active
}

func udpFlowTransport(s *Session) *transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tr := range s.ts {
		if tr.flow != nil {
			return tr
		}
	}
	return nil
}

func (g *writeGate) Write(b []byte) (int, error) {
	g.mu.Lock()
	if g.fired {
		select {
		case <-g.proceeded:
		default:
			close(g.proceeded) // a later write proves the writer loop processed the gated write's result
		}
	}
	selected := len(b) >= 5 && (g.typ == 0 || b[4] == g.typ)
	intercept := selected && !g.fired && g.intercept >= g.skip
	if selected {
		if intercept {
			g.fired = true
		}
		g.intercept++
	}
	g.mu.Unlock()
	if !intercept {
		return g.ReadWriteCloser.Write(b)
	}
	close(g.entered)
	defer close(g.finished)
	if err := <-g.result; err != nil {
		return 0, err
	}
	return g.ReadWriteCloser.Write(b)
}

func (g *writeGate) await(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the selected write was not dequeued")
	}
}

func (g *writeGate) complete(err error) { g.result <- err }

func (g *writeGate) waitComplete(t *testing.T) {
	t.Helper()
	select {
	case <-g.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the selected write did not complete")
	}
}

// completeLateWrite waits until the session has handled the write result. A successful writer is probed with a harmless
// acknowledgement on the same connection; a failed writer is observed through the transport's dead fence.
func (g *writeGate) completeLateWrite(t *testing.T, s *Session, tr *transport, err error) {
	t.Helper()
	g.complete(err)
	g.waitComplete(t)
	if err != nil {
		waitFor(t, "the late write failure to close its transport", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return tr.dead
		})
		return
	}
	s.mu.Lock()
	if tr.dead {
		s.mu.Unlock()
		t.Fatal("the old transport died after a successful write")
	}
	tr.ctl = append(tr.ctl, frame(TypeAck, chanOffset(RFB, 0)))
	s.cond.Broadcast()
	s.mu.Unlock()
	select {
	case <-g.proceeded:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer loop did not process the late successful write")
	}
}

func resetTestLimits() Limits {
	l := testLimits
	l.StallAfter = time.Hour
	l.Grace = time.Hour
	l.VideoBacklog = time.Hour
	return l
}

// openResetUDPLink enables the released phone's TCP-data rule for reset-delivery tests without changing main's fixtures.
func openResetUDPLink(t *testing.T, limits Limits) *udpLink {
	t.Helper()
	l := openUDPLink(t, limits)
	l.p.enableFlowTCP()
	return l
}

func keyframeRequests(helper net.Conn) <-chan []byte {
	out := make(chan []byte, 16)
	go func() {
		for {
			b := make([]byte, len(keyframeRequest))
			if _, err := io.ReadFull(helper, b); err != nil {
				return
			}
			out <- b
		}
	}()
	return out
}

func nextKeyframeRequest(t *testing.T, requests <-chan []byte) {
	t.Helper()
	select {
	case b := <-requests:
		if !bytes.Equal(b, keyframeRequest) {
			t.Fatalf("the helper received %v, want a keyframe request", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the helper was not asked for a keyframe")
	}
}

func noKeyframeRequest(t *testing.T, requests <-chan []byte) {
	t.Helper()
	select {
	case <-requests:
		t.Fatal("the helper was asked for an unnecessary keyframe")
	case <-time.After(50 * time.Millisecond):
		// dropBacklogLocked injects asynchronously; allow an erroneous request to reach the reader before passing.
	}
}
