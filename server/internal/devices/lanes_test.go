package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The manager asks for lanes when node.info announces them, and only then; a node that announces them and refuses
// keeps lines. Calls work every way, a bulk one included.
func TestMachineNegotiatesLanes(t *testing.T) {
	for _, c := range []struct {
		name            string
		announce, able  bool
		framed, refused bool
	}{
		{"new node", true, true, true, false},
		{"node without the capability", false, true, false, false},
		{"node that announces and refuses", true, false, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDevices(t)
			logs := &benchLog{}
			d.logf = func(f string, a ...any) { logs.add(fmt.Sprintf(f, a...)) }
			info := proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test",
				Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion, Capabilities: nodeproto.Required}
			if c.announce {
				info.Capabilities = append(info.Capabilities, nodeproto.NodeLanes)
			}
			n := &testNode{info: &info, lanes: c.able, unknown: map[string]bool{"rpc.lanes": !c.able}}
			m := connectedMachine(t, d, "lanes", n)
			m.mu.Lock()
			peer := m.peer
			m.mu.Unlock()
			if peer.Framed() != c.framed {
				t.Fatalf("framed=%t", peer.Framed())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			chunk := strings.Repeat("d", 300_000) // several frames on the bulk lane
			if err := m.call(ctx, "file.upload.chunk", map[string]any{"upload_id": "u1", "offset": 0, "data": chunk}, nil); err != nil {
				t.Fatal(err)
			}
			if got := n.seen("file.upload.chunk"); len(got) != 1 || !strings.Contains(got[0], chunk) {
				t.Fatalf("the node read %d upload chunks", len(got))
			}
			want := "lanes=" + onOff(c.framed)
			eventually(t, 2*time.Second, "the connected line", func() bool { return logs.count(want) > 0 })
			if got := logs.count("staying with lines") > 0; got != c.refused {
				t.Fatalf("refusal logged=%t: %q", got, logs.since(0))
			}
		})
	}
}

// A forwarded port's bytes waiting for credit do not hold the hub's other events to the node (terminal input) behind
// them: they queue apart.
func TestNotesPortBytesDoNotHoldTerminalInput(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	nodeIn, managerOut := io.Pipe()
	managerIn, nodeOut := io.Pipe()
	seen := &seenBytes{} // what reaches a node that reads everything and never acknowledges
	go func() { _, _ = io.Copy(seen, nodeIn) }()
	peer := rpc.New("h", managerOut)
	peer.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
	go func() { _ = peer.Serve(context.Background(), managerIn) }()
	t.Cleanup(func() { managerOut.Close(); nodeOut.Close(); peer.Close(nil) })
	upgraded := make(chan error, 1)
	go func() { upgraded <- peer.UpgradeLanes(context.Background()) }()
	eventually(t, 5*time.Second, "rpc.lanes asked", func() bool { return strings.Contains(seen.String(), "rpc.lanes") })
	if _, err := nodeOut.Write([]byte(`{"id":"h1","result":{"version":1}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-upgraded; err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.peer = peer
	m.mu.Unlock()
	chunk, _ := json.Marshal(map[string]any{"id": "p1", "data": strings.Repeat("A", 48<<10)})
	for range 4 { // more than a window: the later ones wait for credit that never comes
		m.queueNote("stream.data", chunk)
	}
	eventually(t, 5*time.Second, "port bytes waiting for credit", func() bool { return peer.LaneStats().InFlight >= 32<<10 })
	m.queueNote("terminal.input", json.RawMessage(`{"id":"t1","data":"bHM="}`))
	eventually(t, 5*time.Second, "terminal input on the wire", func() bool { return strings.Contains(seen.String(), "terminal.input") })
}

// seenBytes is what a node read, for a test to look into while it goes on.
type seenBytes struct {
	mu sync.Mutex
	b  []byte
}

func (s *seenBytes) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *seenBytes) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

// A forwarded port's close reaches the node after all its bytes, framed or not: the close used to queue apart from the
// bytes, and framed it went ahead of bytes waiting for credit, so the node dropped the stream's last bytes.
func TestPortStreamClosesAfterItsBytes(t *testing.T) {
	for _, framed := range []bool{false, true} {
		t.Run(fmt.Sprintf("framed=%t", framed), func(t *testing.T) {
			d := newTestDevices(t)
			info := proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test",
				Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion, Capabilities: nodeproto.Required}
			if framed {
				info.Capabilities = append(info.Capabilities, nodeproto.NodeLanes)
			}
			n := &testNode{info: &info, lanes: framed}
			m := connectedMachine(t, d, "box", n)
			const chunks = 8
			for i := range chunks { // more than a window's worth: framed, the later ones wait for credit
				raw, _ := json.Marshal(map[string]any{"id": "p1", "i": i, "data": strings.Repeat("A", 24<<10)})
				m.queueNote("stream.data", raw)
			}
			m.queueNote("stream.close", json.RawMessage(`{"id":"p1"}`))
			var stream []string
			eventually(t, 10*time.Second, "the stream's events at the node", func() bool {
				stream = stream[:0]
				for _, e := range n.eventsSeen() {
					if event, _, _ := strings.Cut(e, " "); strings.HasPrefix(event, "stream.") {
						stream = append(stream, event)
					}
				}
				return len(stream) == chunks+1
			})
			if stream[chunks] != "stream.close" {
				t.Fatalf("the close came before the stream's last bytes: %v", stream)
			}
		})
	}
}

// The two notes queues share one budget: past maxNoteBytes in both together the node is connected again, though
// neither queue alone holds that much (each had a budget of its own: twice as much per computer).
func TestNotesShareOneBudget(t *testing.T) {
	old := maxNoteBytes
	maxNoteBytes = 64 << 10
	t.Cleanup(func() { maxNoteBytes = old })
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) {
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	_, managerOut := io.Pipe() // a node that reads nothing: each queue's first note waits in its write
	peer := rpc.New("h", managerOut)
	t.Cleanup(func() {
		managerOut.Close()
		peer.Close(nil)
		// The writers end before the test does: they read noteStall, which other tests set.
		eventually(t, 5*time.Second, "the notes' writers done", func() bool {
			for _, q := range []*nodeNotes{&m.notes, &m.bulkNotes} {
				q.mu.Lock()
				writing := q.writing
				q.mu.Unlock()
				if writing {
					return false
				}
			}
			return true
		})
	})
	m.mu.Lock()
	m.peer = peer
	m.mu.Unlock()
	for i := range 4 { // about 37 KiB waiting in each queue, 74 KiB together
		raw, _ := json.Marshal(map[string]any{"id": "p1", "i": i, "data": strings.Repeat("A", 12<<10)})
		m.queueNote("terminal.input", raw)
		m.queueNote("stream.data", raw)
	}
	eventually(t, 5*time.Second, "the notes dropped and the node connected again", func() bool { return logs.count("takes no events") > 0 })
	// What was dropped left the budget, which holds what is still queued, and the notes after it are queued again,
	// not dropped one by one.
	queued := func() bool {
		var n int64
		for _, q := range []*nodeNotes{&m.notes, &m.bulkNotes} {
			q.mu.Lock()
			n += int64(q.bytes)
			q.mu.Unlock()
		}
		return m.noteBytes.Load() == n
	}
	eventually(t, 5*time.Second, "the budget what is queued", queued)
	for i := range 4 {
		m.queueNote("terminal.input", json.RawMessage(fmt.Sprintf(`{"id":"t1","i":%d}`, i)))
	}
	eventually(t, 5*time.Second, "the budget what is queued", queued)
	if n := logs.count("takes no events"); n != 1 {
		t.Fatalf("the node connected again %d times, want once", n)
	}
}

// A note waiting to be written closes the connection only once nothing it writes has gone through for noteStall: on a
// framed connection a note can wait for credit while everything else goes on.
func TestNoteStallIsNoProgressAtAll(t *testing.T) {
	old := noteStall
	noteStall = 300 * time.Millisecond // well over the writes' spacing, so a busy test machine does not look stalled
	t.Cleanup(func() { noteStall = old })
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	written := func() time.Time { return time.Unix(0, last.Load()) }
	stalled := make(chan time.Time, 1)
	start := time.Now()
	w := watchNote(written, func() bool { return false }, func() { stalled <- time.Now() })
	defer w.stop()
	moving := time.Now().Add(time.Second)
	for time.Now().Before(moving) { // the connection keeps writing
		last.Store(time.Now().UnixNano())
		select {
		case at := <-stalled:
			t.Fatalf("closed after %s while writes went through", at.Sub(start))
		case <-time.After(20 * time.Millisecond):
		}
	}
	select { // then nothing goes through
	case at := <-stalled:
		if idle := at.Sub(written()); idle < noteStall {
			t.Fatalf("closed %s after the last write", idle)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never closed once nothing went through")
	}
}

// A note whose connection waits to be resumed is watched again once it is: a resume whose transport takes nothing
// either still ends in a stall (#10829's review).
func TestNoteStallWatchesOnAfterAResume(t *testing.T) {
	old := noteStall
	noteStall = 100 * time.Millisecond
	t.Cleanup(func() { noteStall = old })
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	written := func() time.Time { return time.Unix(0, last.Load()) }
	var lost atomic.Bool
	lost.Store(true)
	stalled := make(chan struct{}, 1)
	w := watchNote(written, lost.Load, func() { stalled <- struct{}{} })
	defer w.stop()
	select {
	case <-stalled:
		t.Fatal("stalled while the connection waited to be resumed")
	case <-time.After(4 * noteStall):
	}
	last.Store(time.Now().UnixNano()) // resumed: it counts as a write, then nothing goes through
	lost.Store(false)
	select {
	case <-stalled:
	case <-time.After(3 * time.Second):
		t.Fatal("not watched after the resume")
	}
}

// A computer whose framed connections keep ending on malformed frames (something between the ends changes the bytes)
// is not asked for lanes for a while, and stays connected with lines.
func TestLanesOffAfterConnectionsBreakOnFrames(t *testing.T) {
	old := lanesFailLimit
	lanesFailLimit = 2
	t.Cleanup(func() { lanesFailLimit = old })
	d := newTestDevices(t)
	logs := &benchLog{}
	d.logf = func(f string, a ...any) { logs.add(fmt.Sprintf(f, a...)) }
	var mu sync.Mutex
	var peers []*rpc.Peer
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	m.dialNode = func(context.Context) (net.Conn, error) {
		managerSide, nodeSide := net.Pipe()
		p := rpc.New("n", nodeSide)
		p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				return proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test", Protocol: nodeproto.Version,
					ProtocolMin: nodeproto.MinManagerVersion, Capabilities: append(slices.Clone(nodeproto.Required), nodeproto.NodeLanes)}, nil
			case "sessions.list":
				return map[string]any{"sessions": []proto.Session{}}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
		go func() { // once framed, bytes that are no frames: a relay that changed them
			for range 200 {
				if p.Framed() {
					_, _ = nodeSide.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				time.Sleep(5 * time.Millisecond) // sleep-ok: polling for the switch in a fake node
			}
		}()
		mu.Lock()
		peers = append(peers, p)
		mu.Unlock()
		t.Cleanup(func() { managerSide.Close(); nodeSide.Close() })
		return managerSide, nil
	}
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, 20*time.Second, "lanes turned off", func() bool { return logs.count("lanes off for") > 0 })
	eventually(t, 20*time.Second, "a connection with lines", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.peer != nil && m.online && !m.peer.Framed() && logs.count("not asked until") > 0
	})
}

// An answer to rpc.lanes after the wait for it still switches the connection, and the log says so.
func TestLanesLateAnswerIsLogged(t *testing.T) {
	old := lanesWait
	lanesWait = 5 * time.Millisecond
	t.Cleanup(func() { lanesWait = old })
	d := newTestDevices(t)
	logs := &benchLog{}
	d.logf = func(f string, a ...any) { logs.add(fmt.Sprintf(f, a...)) }
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	m.dialNode = func(context.Context) (net.Conn, error) {
		link := netshape.Config{Delay: 30 * time.Millisecond} // the answer comes after lanesWait
		managerSide, nodeSide := netshape.Pipe(link, link)
		p := rpc.New("n", nodeSide)
		p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				return proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test", Protocol: nodeproto.Version,
					ProtocolMin: nodeproto.MinManagerVersion, Capabilities: append(slices.Clone(nodeproto.Required), nodeproto.NodeLanes)}, nil
			case "sessions.list":
				return map[string]any{"sessions": []proto.Session{}}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
		t.Cleanup(func() { managerSide.Close(); nodeSide.Close() })
		return managerSide, nil
	}
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, 10*time.Second, "the late switch logged", func() bool { return logs.count("on after all") > 0 })
	m.mu.Lock()
	framed := m.peer != nil && m.peer.Framed()
	m.mu.Unlock()
	if !framed || logs.count("lines unless the node's answer still comes") == 0 {
		t.Fatalf("framed=%t:\n%s", framed, strings.Join(logs.since(0), "\n"))
	}
}
