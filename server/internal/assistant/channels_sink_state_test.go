package assistant

import (
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

// sinkState records what the channels ask of a sink's own state of a run.
type sinkState struct {
	mu              sync.Mutex
	aborts, nextTur int
}

func (st *sinkState) Abort()    { st.mu.Lock(); st.aborts++; st.mu.Unlock() }
func (st *sinkState) NextTurn() { st.mu.Lock(); st.nextTur++; st.mu.Unlock() }

func (st *sinkState) counts() (int, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.aborts, st.nextTur
}

// The channels tell the sink's own state of a run (chanLive.sink, the lark module's) when a held run's turn ends
// (NextTurn: its agent message goes with the turn) and when the watcher finds the run dropped (Abort, outside the
// channels' lock). With or without the lark module.
func TestChannelsTellTheSinkStateOfARun(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, prog := cotRun(s)
	st := &sinkState{}
	s.chans.mu.Lock()
	prog.sink = st
	s.chans.mu.Unlock()
	s.finishChannelRun(r, &channel.Result{Answer: "first part", Held: true})
	if aborts, next := st.counts(); aborts != 0 || next != 1 {
		t.Fatalf("held: aborts %d, next turns %d", aborts, next)
	}

	r2, _ := cotRun(s) // the same id again, followed by a watcher this time
	s.chans.mu.Lock()
	delete(s.chans.live, r2.ID)
	s.chans.mu.Unlock()
	watched := make(chan struct{})
	go func() { s.watchChannelRun(r2); close(watched) }()
	var p *chanLive
	waitFor(t, "watcher registration", func() bool {
		_, p = s.channelRunByID(r2.ID)
		return p != nil
	})
	st2 := &sinkState{}
	s.chans.mu.Lock()
	p.sink = st2
	s.chans.st.Runs = nil
	s.chans.mu.Unlock()
	select {
	case <-watched:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher of a dropped run did not stop")
	}
	if aborts, next := st2.counts(); aborts != 1 || next != 0 {
		t.Fatalf("dropped: aborts %d, next turns %d", aborts, next)
	}
}
