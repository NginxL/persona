package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A route move ends the generation that a planned switch or a late resume may have started with.
func TestResumeRouteGenerationGuards(t *testing.T) {
	newConn := func() *nodeConn {
		return &nodeConn{
			path: &nodePath{key: "old"}, via: "ssh", routeRev: 4, routeStarted: time.Now(),
			ctx: context.Background(), peer: rpc.New("h", io.Discard),
		}
	}
	m := &Machine{} // not serving c: these cases exercise only the connection's route claim
	t.Run("stale claim after a move", func(t *testing.T) {
		c := newConn()
		oldRev := c.routeRev
		if err := m.moveRoute(c, &nodePath{key: "better"}, "local", nil); err != nil {
			t.Fatal(err)
		}
		if c.claimRouteExit(oldRev) {
			t.Fatal("an old route generation was allowed to claim the moved connection")
		}
	})
	t.Run("late move after a planned switch claim", func(t *testing.T) {
		c := newConn()
		if !c.claimRouteExit(c.routeRev) {
			t.Fatal("the current route generation could not be claimed")
		}
		if err := m.moveRoute(c, &nodePath{key: "better"}, "local", nil); !errors.Is(err, errSwitched) {
			t.Fatalf("late moveRoute error = %v, want errSwitched", err)
		}
	})
}

// Moving after a failed old-path try attributes that failure to the old path, not to the route just resumed.
func TestMoveRouteRecordsOldPathFailureAndResetsStart(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Kind: "ssh"})
	other := newMachine(d, MachineConfig{ID: "box-local", Kind: "local"})
	oldPath, newPath := cfgPath(m), cfgPath(other)
	peer := rpc.New("h", io.Discard)
	oldStart := time.Now().Add(-2 * time.Minute)
	c := &nodeConn{path: oldPath, via: "ssh", routeRev: 3, routeStarted: oldStart, ctx: context.Background(), peer: peer}
	m.mu.Lock()
	m.peer, m.active, m.routeRev = peer, oldPath, c.routeRev
	m.mu.Unlock()
	moveCause := errors.New("the old path resume failed deliberately")
	before := time.Now()
	if err := m.moveRoute(c, newPath, "local", moveCause); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	oldHealth := d.pathState(oldPath)
	if !oldHealth.failUntil.After(time.Now()) || oldHealth.err != moveCause.Error() {
		t.Errorf("old path health = %+v, want a failure backoff and error %q", oldHealth, moveCause)
	}
	_, via, rev, started := c.routeState()
	if via != "local" || rev != 4 {
		t.Errorf("new route = %q generation %d, want local generation 4", via, rev)
	}
	if started.Before(before) || started.After(after) {
		t.Errorf("new route started at %s, outside the move [%s, %s]; old start %s", started, before, after, oldStart)
	}
}

// A preserved peer can answer a ping after a resume moved its route. That pong says nothing about either route's RTT.
func TestMeasureNodesIgnoresPongFromPreviousRoute(t *testing.T) {
	hubConn, nodeConn := net.Pipe()
	hub, node := rpc.New("h", hubConn), rpc.New("n", nodeConn)
	pingStarted, answer := make(chan struct{}), make(chan struct{})
	node.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "ping" {
			close(pingStarted)
			select {
			case <-answer:
				return map[string]any{"ok": true}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return map[string]any{"ok": true}, nil
	}
	hubDone, nodeDone := make(chan error, 1), make(chan error, 1)
	go func() { hubDone <- hub.Serve(context.Background(), hubConn) }()
	go func() { nodeDone <- node.Serve(context.Background(), nodeConn) }()
	t.Cleanup(func() {
		_ = hubConn.Close()
		_ = nodeConn.Close()
		hub.Close(io.EOF)
		node.Close(io.EOF)
		for name, done := range map[string]<-chan error{"hub": hubDone, "node": nodeDone} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Errorf("%s Serve did not stop", name)
			}
		}
	})
	d, m := pingMachine(t, hub)
	oldPath := m.activePath()
	measureDone := make(chan struct{})
	go func() {
		d.measureNodes(context.Background())
		close(measureDone)
	}()
	select {
	case <-pingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the ping did not reach the node")
	}
	newPath := &nodePath{key: "better"}
	m.mu.Lock()
	m.active = newPath
	m.routeRev++ // the same peer remains in flight on a new route generation
	m.mu.Unlock()
	close(answer)
	select {
	case <-measureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("measureNodes did not finish after the pong")
	}
	f := &d.fleet
	f.mu.Lock()
	var oldRTT, newRTT int
	if h := f.health[oldPath.key]; h != nil {
		oldRTT = h.rtt
	}
	if h := f.health[newPath.key]; h != nil {
		newRTT = h.rtt
	}
	f.mu.Unlock()
	if oldRTT != 0 || newRTT != 0 {
		t.Errorf("late pong recorded RTTs old=%d new=%d, want neither route updated", oldRTT, newRTT)
	}
}
