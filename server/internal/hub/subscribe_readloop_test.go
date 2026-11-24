package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// An app's "subscribe" and "unsubscribe" have the node watch the session or stop, and the node may take up to
// watchCallTimeout to answer with nothing at fault: a manager that just restarted waits for a computer it still shows
// online (devices warm.go waitWarm), a laptop was just closed, a node is restarting. The connection's read loop must
// not wait with it: nothing else the app sends on that socket would be read, its ping, a phone tool's result, its
// terminal keys, the pong of the hub's own ping (#5915). These tests keep the node's answer back and check that the
// socket is still read, and that what the node ends up told is what the subscriptions say.

// readLoopLimit is how long a message behind a subscribe may wait to be read. An app calls the connection slow (iOS)
// or reconnects (Android in the foreground) after 5 s without a pong, and it is well under watchCallTimeout, which is
// what such a message waited when the read loop made the call.
const readLoopLimit = 5 * time.Second

// got reports whether the app has a message ok takes.
func (a *app) got(ok func(map[string]any) bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.msgs, ok)
}

// pongWithin sends the app's own liveness ping and fails unless the hub answers it within readLoopLimit: it is read
// only after everything the app sent before it on the socket.
func (a *app) pongWithin(t *testing.T, id, when string) {
	t.Helper()
	a.send(t, map[string]any{"type": "ping", "id": id})
	eventually(t, readLoopLimit, "the app's ping answered "+when, func() bool { return a.got(typeIs("pong", "id", id)) })
}

// subscribeBox is a hub with its bus and its manager, which carries one computer, box: a watch goes hub, bus,
// manager, node, as it does in production. Only the node is played, and it pushes items the way a real one does,
// only on a connection that watches the session.
func subscribeBox(t *testing.T) (*testHub, *meshNode, string) {
	t.Helper()
	box := newMeshNode("uid-box", "box", "s1", "s2")
	box.resident, box.pushWatchedOnly = true, true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "box online", func() bool { return mh.machines(t, tok)["box"].Online })
	return mh, box, tok
}

// holdAnswers keeps the node's answer to the methods back until the returned func runs (the end of the test at the
// latest): the node is connected and says nothing about them.
func holdAnswers(t *testing.T, box *meshNode, methods ...string) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	hold := map[string]chan struct{}{}
	for _, m := range methods {
		hold[m] = ch
	}
	box.mu.Lock()
	box.hold = hold
	box.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return release
}

// boxWatches reports whether the node's current connection was told to watch the session.
func boxWatches(box *meshNode, session string) bool {
	box.mu.Lock()
	defer box.mu.Unlock()
	return len(box.conns) == 1 && box.conns[0].watching[session]
}

// Through the manager: while the node keeps its answer to session.watch back, what the app sends behind the
// subscribe is still taken, and once the node answers the subscriber gets its backlog and its live items.
func TestSubscribeWaitingForTheNodeLeavesTheConnectionRead(t *testing.T) {
	mh, box, tok := subscribeBox(t)
	release := holdAnswers(t, box, "session.watch")
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	eventually(t, 10*time.Second, "the watch at the node", func() bool { return box.heldCalls("session.watch") == 1 })

	// A phone tool's result that a turn of the assistant waits for comes in on this socket.
	result := make(chan proto.ToolResult, 1)
	mh.h.mu.Lock()
	mh.h.calls["call1"] = result
	mh.h.mu.Unlock()
	a.send(t, map[string]any{"type": "tool_result", "call_id": "call1", "content": []map[string]any{{"type": "text", "text": "ok"}}})
	select {
	case <-result:
	case <-time.After(readLoopLimit):
		t.Fatalf("a tool result sent behind a subscribe was not taken within %s: the read loop waits for the node", readLoopLimit)
	}
	a.pongWithin(t, "p1", "while the node keeps session.watch")
	// A protocol ping is answered only while the hub reads the socket, and the pong of the hub's own ping is taken
	// only then too: a hub that does not read drops a healthy app with pong_timeout.
	ctx, cancel := context.WithTimeout(context.Background(), readLoopLimit)
	defer cancel()
	if err := a.conn.Ping(ctx); err != nil {
		t.Fatalf("the hub did not answer a protocol ping while the node keeps session.watch: %v", err)
	}
	if got := box.heldCalls("session.watch"); got != 1 {
		t.Fatalf("session.watch reached the node %d times, want the one still waiting", got)
	}

	release()
	a.wait(t, "the backlog once the node answered", 0, hasItem("hi from s1"))
	eventually(t, 10*time.Second, "the node watching s1", func() bool { return boxWatches(box, "s1") })
	box.addItem("s1", "live after the wait")
	a.wait(t, "a live item", 0, hasItem("live after the wait"))
}

// The same for leaving a session: the unwatch waits for the node, the connection does not.
func TestUnsubscribeWaitingForTheNodeLeavesTheConnectionRead(t *testing.T) {
	mh, box, tok := subscribeBox(t)
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.wait(t, "the backlog", 0, hasItem("hi from s1"))
	eventually(t, 10*time.Second, "the node watching s1", func() bool { return boxWatches(box, "s1") })

	release := holdAnswers(t, box, "session.unwatch")
	a.send(t, map[string]any{"type": "unsubscribe", "machine_id": "box", "session_id": "s1"})
	eventually(t, 10*time.Second, "the unwatch at the node", func() bool { return box.heldCalls("session.unwatch") == 1 })
	a.pongWithin(t, "p1", "while the node keeps session.unwatch")

	release()
	eventually(t, 10*time.Second, "the node no longer watching s1", func() bool { return !boxWatches(box, "s1") })
}

// Nothing is at fault here. The manager restarts (a deploy) while the computer is away (its lid closed): the new
// manager shows it as before for warmGrace and has a call for it wait until its node is back (devices warm.go). An
// app reconnects, as every app does after a restart, and subscribes to the session it has open.
func TestSubscribeWhileARestartedManagerWaitsForTheComputer(t *testing.T) {
	mh, box, tok := subscribeBox(t)
	mh.mgr.stop()
	box.mu.Lock()
	box.refuse[mh.h] = true // away: its node cannot be reached
	box.mu.Unlock()
	mh.mgr = startTestManager(t, mh.h, mh.srv, map[string]func(context.Context) (net.Conn, error){"box": box.dialer(mh.h)})
	eventually(t, 10*time.Second, "box shown as before the restart, its node not connected", func() bool {
		return mh.machines(t, tok)["box"].Online && box.current() == nil
	})

	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.pongWithin(t, "p1", "while the manager waits for the computer")
	// The watch is still out, waiting for the node: the ping did not just follow a call that failed at once. The
	// manager's own catch-up (watchAgain, run when the new manager attaches) may queue a second sync for the same
	// session behind the subscriber's, so the slot is held by one or more calls, not exactly one.
	m := mh.h.localMachine("box")
	eventually(t, readLoopLimit, "the watch still waiting for the node", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		slot := m.watchMu["s1"]
		_, told := m.nodeWatch["s1"]
		return m.watchers["s1"] == 1 && slot != nil && slot.refs >= 1 && !told
	})
}

// watchedHub is a hub over TLS with no manager, whose computer m1 is a watchNode: it answers the hub directly, keeps
// which sessions it watches the way a node does, and can keep an answer back.
func watchedHub(t *testing.T) (*testHub, *watchNode, *Machine) {
	t.Helper()
	n, m := newWatchNode(t)
	h := newTestHub(t)
	m.hub = h
	registerMachine(h, m)
	srv := httptest.NewTLSServer(h.routes())
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return &testHub{h: h, srv: srv}, n, m
}

// releaseHeld lets the node's held calls go, once, at the end of the test at the latest.
func releaseHeld(t *testing.T, n *watchNode) (release func()) {
	t.Helper()
	var once sync.Once
	release = func() { once.Do(func() { close(n.release) }) }
	t.Cleanup(release)
	return release
}

func subscribers(m *Machine, id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchers[id]
}

func toldNode(m *Machine, id string) nodeWatchState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodeWatch[id]
}

// watchCalls are the session.watch / session.unwatch calls the node got, in order.
func watchCalls(n *watchNode) []string {
	var out []string
	for _, c := range n.callList() {
		if c == "session.watch" || c == "session.unwatch" {
			out = append(out, c)
		}
	}
	return out
}

func liveItem(m *Machine, session, text string) {
	b, _ := json.Marshal(map[string]any{"session_id": session, "items": []proto.Item{{Seq: 9, Kind: "assistant", Text: text}}})
	m.onEvent("session.items", b)
}

// An app opens a session and leaves it before the node answered the watch: both are read at once, the unwatch goes
// after the watch, and nothing is left watched or kept.
func TestSubscribeThenUnsubscribeBeforeTheNodeAnswers(t *testing.T) {
	mh, n, m := watchedHub(t)
	release := releaseHeld(t, n)
	a := dialApp(t, mh, mh.h.cfg.Token)

	n.hold(true, false)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	waitEntered(t, n) // the watch is at the node, not answered
	n.hold(false, false)
	a.send(t, map[string]any{"type": "unsubscribe", "machine_id": "m1", "session_id": "s1"})
	eventually(t, readLoopLimit, "the unsubscribe read while the watch is out", func() bool { return subscribers(m, "s1") == 0 })

	release()
	waitWatchStateGone(t, m, "s1")
	if n.isWatched("s1") {
		t.Fatalf("the node keeps a watch nobody wants (calls %v)", n.callList())
	}
	if got := watchCalls(n); !slices.Equal(got, []string{"session.watch", "session.unwatch"}) {
		t.Fatalf("the node got %v, want the watch and then the unwatch", got)
	}
}

// Open, leave, open again before the node answered the first watch: the node ends up watching for the one
// subscription that is left, which gets its backlog and its live items, and leaving it again stops the watch.
func TestResubscribeBeforeTheNodeAnswers(t *testing.T) {
	mh, n, m := watchedHub(t)
	release := releaseHeld(t, n)
	a := dialApp(t, mh, mh.h.cfg.Token)

	n.hold(true, false)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	waitEntered(t, n)
	n.hold(false, false)
	a.send(t, map[string]any{"type": "unsubscribe", "machine_id": "m1", "session_id": "s1"})
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	a.pongWithin(t, "read", "behind the three, while the watch is out") // so all three were read
	if got := subscribers(m, "s1"); got != 1 {
		t.Fatalf("%d subscribers after subscribe, unsubscribe, subscribe, want 1", got)
	}

	release()
	eventually(t, 10*time.Second, "the node told to watch", func() bool { return toldNode(m, "s1") == nodeWatching })
	if !n.isWatched("s1") {
		t.Fatalf("the subscriber is left with the node not watching (calls %v)", n.callList())
	}
	if calls := watchCalls(n); calls[len(calls)-1] != "session.watch" {
		t.Fatalf("the last thing the node was told is %s (calls %v)", calls[len(calls)-1], calls)
	}
	a.wait(t, "the backlog of the session it is in", 0, func(msg map[string]any) bool {
		return msg["type"] == "items" && msg["session_id"] == "s1" && msg["backlog"] == true
	})
	liveItem(m, "s1", "live")
	a.wait(t, "a live item", 0, hasItem("live"))

	a.send(t, map[string]any{"type": "unsubscribe", "machine_id": "m1", "session_id": "s1"})
	eventually(t, 10*time.Second, "the unwatch after it left", func() bool {
		return subscribers(m, "s1") == 0 && toldNode(m, "s1") == nodeUnwatched && !n.isWatched("s1")
	})
}

// Two apps in one session. The second one's subscribe waits its turn behind the first one's watch, and it
// disconnects while it waits: its subscription goes at once, the first app's watch goes through once, and only when
// that app leaves too is the node told to stop.
func TestTwoAppsOneLeavesWhileTheWatchIsOut(t *testing.T) {
	mh, n, m := watchedHub(t)
	release := releaseHeld(t, n)
	a, b := dialApp(t, mh, mh.h.cfg.Token), dialApp(t, mh, mh.h.cfg.Token)

	n.hold(true, false)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	waitEntered(t, n)
	n.hold(false, false)
	b.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	eventually(t, readLoopLimit, "both subscribed", func() bool { return subscribers(m, "s1") == 2 })
	_ = b.conn.CloseNow()
	eventually(t, readLoopLimit, "the second app's subscription gone while the watch is out", func() bool { return subscribers(m, "s1") == 1 })

	release()
	eventually(t, 10*time.Second, "the node told to watch", func() bool { return toldNode(m, "s1") == nodeWatching })
	liveItem(m, "s1", "live")
	a.wait(t, "a live item for the app that stayed", 0, hasItem("live"))
	if got := watchCalls(n); !slices.Equal(got, []string{"session.watch"}) || !n.isWatched("s1") {
		t.Fatalf("with one app left the node got %v (watching: %v), want one watch", got, n.isWatched("s1"))
	}

	a.send(t, map[string]any{"type": "unsubscribe", "machine_id": "m1", "session_id": "s1"})
	eventually(t, 10*time.Second, "the unwatch after both left", func() bool {
		return subscribers(m, "s1") == 0 && toldNode(m, "s1") == nodeUnwatched
	})
	if got := watchCalls(n); !slices.Equal(got, []string{"session.watch", "session.unwatch"}) || n.isWatched("s1") {
		t.Fatalf("after both left the node got %v (watching: %v), want the watch and then the unwatch", got, n.isWatched("s1"))
	}
}

// An app disconnects while its own watch is still out: the hub gives the call up, sends the unwatch after it and
// keeps nothing for the session.
func TestDisconnectWhileItsWatchIsOut(t *testing.T) {
	mh, n, m := watchedHub(t)
	releaseHeld(t, n)
	a := dialApp(t, mh, mh.h.cfg.Token)

	n.hold(true, false)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": "s1"})
	waitEntered(t, n)
	n.hold(false, false)
	_ = a.conn.CloseNow()

	waitWatchStateGone(t, m, "s1")
	if got := subscribers(m, "s1"); got != 0 {
		t.Fatalf("%d subscribers after the app left, want none", got)
	}
	if got := watchCalls(n); !slices.Equal(got, []string{"session.watch", "session.unwatch"}) {
		t.Fatalf("the node got %v, want the watch and then the unwatch", got)
	}
}

// A connection that ends does not wait for the node either: with the node keeping its answer to session.unwatch
// back, the handler still ends (a hub that is stopping waits for every handler), and each session is unwatched once
// the node answers.
func TestDisconnectDoesNotWaitForTheUnwatch(t *testing.T) {
	mh, n, m := watchedHub(t)
	release := releaseHeld(t, n)
	a := dialApp(t, mh, mh.h.cfg.Token)
	for _, s := range []string{"s1", "s2"} {
		a.send(t, map[string]any{"type": "subscribe", "machine_id": "m1", "session_id": s})
	}
	eventually(t, 10*time.Second, "both sessions watched", func() bool {
		return toldNode(m, "s1") == nodeWatching && toldNode(m, "s2") == nodeWatching
	})

	n.hold(false, true)
	_ = a.conn.CloseNow()
	ended := make(chan struct{})
	go func() { mh.h.wsHandlers.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(readLoopLimit):
		t.Fatalf("the connection's handler had not ended %s after the app left: it waits for the node's answer to session.unwatch", readLoopLimit)
	}
	waitEntered(t, n)
	waitEntered(t, n) // both unwatches are at the node, neither waiting for the other

	release()
	for _, s := range []string{"s1", "s2"} {
		waitWatchStateGone(t, m, s)
		if n.isWatched(s) {
			t.Fatalf("%s is still watched after the app left (calls %v)", s, n.callList())
		}
	}
}
