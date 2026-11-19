package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// watchNode is a node over an rpc.Peer pair that keeps which sessions it watches the way the node does: a watch for
// a session it already watches does nothing, an unwatch drops it. Each request runs in its own goroutine (rpc.Serve),
// so a held watch or unwatch lets later calls overtake it, as they can on the way through the manager.
type watchNode struct {
	mu          sync.Mutex
	watched     map[string]bool
	since       map[string]int
	history     map[string]*string
	calls       []string
	failWatch   int // the next n session.watch calls fail
	failUnwatch int // the next n session.unwatch calls fail

	// Held: the handler signals entered, then waits for release before it acts.
	holdWatch, holdUnwatch bool
	entered, release       chan struct{}
}

func newWatchNode(t *testing.T) (*watchNode, *Machine) {
	t.Helper()
	n := &watchNode{watched: map[string]bool{}, since: map[string]int{}, history: map[string]*string{}, entered: make(chan struct{}, 4), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p struct {
			SessionID string  `json:"session_id"`
			SinceSeq  int     `json:"since_seq"`
			History   *string `json:"history"`
		}
		_ = json.Unmarshal(params, &p)
		n.mu.Lock()
		n.calls = append(n.calls, method)
		hold := (method == "session.watch" && n.holdWatch) || (method == "session.unwatch" && n.holdUnwatch)
		fail := (method == "session.watch" && n.failWatch > 0) || (method == "session.unwatch" && n.failUnwatch > 0)
		if fail && method == "session.watch" {
			n.failWatch--
		} else if fail {
			n.failUnwatch--
		}
		n.mu.Unlock()
		if hold {
			n.entered <- struct{}{}
			<-n.release
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		switch {
		case fail:
			return nil, errors.New("node busy")
		case method == "session.watch":
			n.since[p.SessionID] = p.SinceSeq
			n.history[p.SessionID] = p.History
			if !n.watched[p.SessionID] {
				n.watched[p.SessionID] = true
			}
		case method == "session.unwatch":
			delete(n.watched, p.SessionID)
		}
		return map[string]any{}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(&Hub{logger: log.New(io.Discard, "", 0)}, MachineConfig{ID: "m1", Name: "m1"})
	m.peer = hubSide
	return n, m
}

func (n *watchNode) isWatched(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.watched[id]
}

func (n *watchNode) callList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

func (n *watchNode) count(method string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, m := range n.calls {
		if m == method {
			c++
		}
	}
	return c
}

func (n *watchNode) hold(watch, unwatch bool) {
	n.mu.Lock()
	n.holdWatch, n.holdUnwatch = watch, unwatch
	n.mu.Unlock()
}

func waitEntered(t *testing.T, n *watchNode) {
	t.Helper()
	select {
	case <-n.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the node never got the held call")
	}
}

// waitDone fails the test when done is not closed in time.
func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never returned", what)
	}
}

// The last app leaves (unwatch on its way, slow) while another subscribes: the node must end up watching.
func TestWatchAfterSlowUnwatchKeepsWatching(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()
	m.addWatcher(ctx, "s1")
	if !n.isWatched("s1") {
		t.Fatal("first subscriber: node not watching")
	}

	n.hold(false, true)
	left := make(chan struct{})
	go func() { m.removeWatcher("s1"); close(left) }()
	waitEntered(t, n) // the unwatch is at the node, not done yet
	n.hold(false, false)

	joined := make(chan struct{})
	go func() { m.addWatcher(ctx, "s1"); close(joined) }()
	// Before the fix the watch went out now, reached the node still watching (a no-op) and the unwatch then dropped it.
	time.Sleep(50 * time.Millisecond)
	close(n.release)
	waitDone(t, left, "removeWatcher")
	waitDone(t, joined, "addWatcher")

	if !n.isWatched("s1") {
		t.Fatalf("a subscriber is left with the node not watching (calls %v)", n.callList())
	}
	if got := n.count("session.watch"); got != 2 {
		t.Fatalf("session.watch sent %d times, want 2", got)
	}
}

// A subscriber leaves while its watch is still on its way: the node must not keep a watch nobody wants.
func TestUnwatchDuringSlowWatchLeavesNoWatch(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()

	n.hold(true, false)
	joined := make(chan struct{})
	go func() { m.addWatcher(ctx, "s1"); close(joined) }()
	waitEntered(t, n)
	n.hold(false, false)

	left := make(chan struct{})
	go func() { m.removeWatcher("s1"); close(left) }()
	time.Sleep(50 * time.Millisecond)
	close(n.release)
	waitDone(t, joined, "addWatcher")
	waitDone(t, left, "removeWatcher")

	if n.isWatched("s1") {
		t.Fatalf("the node keeps a watch with no subscriber (calls %v)", n.callList())
	}
}

// A failed watch is sent again on the next sync, and a second subscriber of a watched session sends nothing.
func TestWatchRetriedAfterFailure(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()
	n.mu.Lock()
	n.failWatch = 1
	n.mu.Unlock()

	m.addWatcher(ctx, "s1")
	if n.isWatched("s1") {
		t.Fatal("the failed watch took")
	}
	m.addWatcher(ctx, "s1")
	if !n.isWatched("s1") {
		t.Fatalf("the watch was not retried (calls %v)", n.callList())
	}
	m.addWatcher(ctx, "s1")
	if got := n.count("session.watch"); got != 2 {
		t.Fatalf("session.watch sent %d times, want 2 (calls %v)", got, n.callList())
	}

	for range 3 {
		m.removeWatcher("s1")
	}
	if n.isWatched("s1") {
		t.Fatal("still watched after every subscriber left")
	}
	if got := n.count("session.unwatch"); got != 1 {
		t.Fatalf("session.unwatch sent %d times, want 1", got)
	}
}

// A new manager watches nothing: watchAgain sends the watch again even though the old one was told.
func TestWatchAgainResendsToFreshManager(t *testing.T) {
	n, m := newWatchNode(t)
	m.managed = true
	m.addWatcher(context.Background(), "s1")
	n.mu.Lock()
	n.watched = map[string]bool{} // the new manager's node watches nothing
	n.mu.Unlock()

	(&Hub{logger: log.New(io.Discard, "", 0)}).watchAgain([]*Machine{m})
	if !n.isWatched("s1") {
		t.Fatalf("watchAgain did not watch the session again (calls %v)", n.callList())
	}
	m.removeWatcher("s1")
	if n.isWatched("s1") {
		t.Fatal("the unwatch after watchAgain did not go out")
	}
}

// The node never answers the first subscriber's watch: a second app's subscribe and unsubscribe must not wait on it
// past their own ctx, and the hung call itself ends at watchCallTimeout.
func TestHungWatchDoesNotBlockOtherSubscribers(t *testing.T) {
	old := watchCallTimeout
	watchCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { watchCallTimeout = old })
	n, m := newWatchNode(t)
	t.Cleanup(func() { close(n.release) }) // after the test: the node's handler goroutines end

	n.hold(true, false)
	first := make(chan struct{})
	go func() { m.addWatcher(context.Background(), "s1"); close(first) }()
	waitEntered(t, n)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	m.addWatcher(ctx, "s1") // a second app's subscribe, its connection gone soon
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("the second subscribe waited %v for the hung watch", d)
	}
	waitDone(t, first, "the hung addWatcher")
	m.mu.Lock()
	state := m.nodeWatch["s1"]
	m.mu.Unlock()
	if state != nodeWatchUnknown {
		t.Fatalf("after the timed-out watch the state is %v, want unknown", state)
	}
}

// A caller that gives up waiting for the turn loses nothing: the holder sends what changed while its call was out.
func TestHolderSendsWhatAWaiterGaveUpOn(t *testing.T) {
	n, m := newWatchNode(t)
	m.addWatcher(context.Background(), "s1")

	n.hold(false, true)
	left := make(chan struct{})
	go func() { m.removeWatcher("s1"); close(left) }()
	waitEntered(t, n)
	n.hold(false, false)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	m.addWatcher(ctx, "s1") // gives up while the unwatch holds the turn
	close(n.release)
	waitDone(t, left, "removeWatcher")

	if !n.isWatched("s1") {
		t.Fatalf("the subscriber that gave up waiting is left unwatched (calls %v)", n.callList())
	}
}

// TestMachineWatchStatePrunedWithSessionList: the per-session watch state a session left behind goes once no app
// watches it and the node no longer lists it, while a listed session keeps its since_seq and a watched one keeps all.
func TestMachineWatchStatePrunedWithSessionList(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()
	items := func(id string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"session_id": id, "items": []map[string]any{{"seq": 3}}})
		return b
	}
	for i := range 500 {
		id := fmt.Sprintf("s%d", i)
		m.addWatcher(ctx, id)
		m.onEvent("session.items", items(id))
		m.removeWatcher(id)
	}
	for _, id := range []string{"listed", "watched"} {
		m.addWatcher(ctx, id)
		m.onEvent("session.items", items(id))
	}
	m.removeWatcher("listed")

	m.setSessionList([]proto.Session{{SessionID: "listed"}})

	m.mu.Lock()
	slots, seqs := len(m.watchMu), len(m.lastSeq)
	listedSeq, watchedSeq := m.lastSeq["listed"], m.lastSeq["watched"]
	_, watchedSlot := m.watchMu["watched"]
	m.mu.Unlock()
	if slots != 1 || !watchedSlot {
		t.Fatalf("watchMu has %d slots (watched kept: %v), want only the watched session's", slots, watchedSlot)
	}
	if seqs != 2 || listedSeq != 3 || watchedSeq != 3 {
		t.Fatalf("lastSeq has %d entries (listed %d, watched %d), want listed and watched at 3", seqs, listedSeq, watchedSeq)
	}
	if !n.isWatched("watched") {
		t.Fatal("the watched session is no longer watched on the node")
	}

	// The watched session leaves nothing once its last app goes and the node lists none.
	m.removeWatcher("watched")
	m.setSessionList([]proto.Session{})
	m.mu.Lock()
	slots, seqs = len(m.watchMu), len(m.lastSeq)
	m.mu.Unlock()
	if slots != 0 || seqs != 0 {
		t.Fatalf("after the last unwatch and an empty list: %d watchMu slots, %d lastSeq entries, want none", slots, seqs)
	}
}

// A holder that ends while another call waits for the turn must not drop the slot the waiter has: a third caller
// would make a new slot and run its call alongside the waiter's.
func TestWatchSlotKeptForAWaiter(t *testing.T) {
	_, m := newWatchNode(t)
	ctx := context.Background()
	unlockHolder, ok := m.watchLock(ctx, "s1") // idle session: no watcher, nothing told to the node
	if !ok {
		t.Fatal("the first watchLock failed")
	}
	waiterHas := make(chan func())
	go func() {
		unlock, _ := m.watchLock(ctx, "s1")
		waiterHas <- unlock
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		refs := m.watchMu["s1"].refs
		m.mu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiter never queued for the turn")
		}
		time.Sleep(time.Millisecond)
	}
	unlockHolder()
	var unlockWaiter func()
	select {
	case unlockWaiter = <-waiterHas:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never got the turn")
	}

	third, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if unlock, ok := m.watchLock(third, "s1"); ok {
		unlock()
		t.Fatal("a third caller got the turn while the waiter holds it")
	}
	unlockWaiter()
	m.mu.Lock()
	slots := len(m.watchMu)
	m.mu.Unlock()
	if slots != 0 {
		t.Fatalf("%d watchMu slots after every call ended, want none", slots)
	}
}

// waitWatchStateGone waits until the hub keeps no watch state for session id.
func waitWatchStateGone(t *testing.T, m *Machine, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, slot := m.watchMu[id]
		_, told := m.nodeWatch[id]
		_, seq := m.lastSeq[id]
		m.mu.Unlock()
		if !slot && !told && !seq {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s keeps watch state (slot %v, nodeWatch %v, lastSeq %v)", id, slot, told, seq)
		}
		time.Sleep(time.Millisecond)
	}
}

// A failed unwatch leaves the watch unknown: once the node no longer lists the session, the hub sends the unwatch
// again and keeps nothing for it; a session still listed keeps its unknown state for the next sync.
func TestUnknownWatchOfEndedSessionSettled(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()
	for _, id := range []string{"ended", "listed"} {
		m.addWatcher(ctx, id)
		m.onEvent("session.items", json.RawMessage(fmt.Sprintf(`{"session_id":%q,"items":[{"seq":3}]}`, id)))
	}
	n.mu.Lock()
	n.failUnwatch = 2
	n.mu.Unlock()
	m.removeWatcher("ended")
	m.removeWatcher("listed")
	m.mu.Lock()
	ended, listed := m.nodeWatch["ended"], m.nodeWatch["listed"]
	m.mu.Unlock()
	if ended != nodeWatchUnknown || listed != nodeWatchUnknown {
		t.Fatalf("after the failed unwatches: ended %v, listed %v, want both unknown", ended, listed)
	}

	m.setSessionList([]proto.Session{{SessionID: "listed"}})
	waitWatchStateGone(t, m, "ended")
	if n.isWatched("ended") {
		t.Fatalf("the ended session's unwatch was not sent again (calls %v)", n.callList())
	}
	m.mu.Lock()
	listed = m.nodeWatch["listed"]
	_, listedSlot := m.watchMu["listed"]
	m.mu.Unlock()
	if listed != nodeWatchUnknown || !listedSlot {
		t.Fatalf("the listed session's state is %v (slot kept: %v), want unknown and kept", listed, listedSlot)
	}
	if got := n.count("session.unwatch"); got != 3 {
		t.Fatalf("session.unwatch sent %d times, want 3 (calls %v)", got, n.callList())
	}
}

// A node that fails the unwatch again still leaves no state for a session it no longer lists, and an app watching
// an unlisted session keeps all of its state.
func TestUnknownWatchOfEndedSessionDroppedWhenUnwatchFailsAgain(t *testing.T) {
	n, m := newWatchNode(t)
	ctx := context.Background()
	m.addWatcher(ctx, "ended")
	m.addWatcher(ctx, "watched")
	n.mu.Lock()
	n.failUnwatch = 2
	n.mu.Unlock()
	m.removeWatcher("ended")

	m.setSessionList([]proto.Session{})
	waitWatchStateGone(t, m, "ended")
	if got := n.count("session.unwatch"); got != 2 {
		t.Fatalf("session.unwatch sent %d times, want 2 (calls %v)", got, n.callList())
	}
	m.mu.Lock()
	watched := m.nodeWatch["watched"]
	m.mu.Unlock()
	if watched != nodeWatching || !n.isWatched("watched") {
		t.Fatalf("the watched session's state is %v, want watching", watched)
	}
}
