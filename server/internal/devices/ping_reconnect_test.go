package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A connection whose node stops answering (a half-open link) is closed after pingMissLimit unanswered pings in a
// row, so the machine reconnects instead of holding every call until its own wait runs out (#7510).
func TestMeasureNodesClosesAConnectionThatStopsAnswering(t *testing.T) {
	oldWait := pingWait
	pingWait = 50 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	d.addEntry(m)
	peer := rpc.New("h", io.Discard) // writes go nowhere: no answer ever comes
	m.mu.Lock()
	m.peer, m.online, m.active, m.transport = peer, true, &nodePath{key: "m1"}, "ssh"
	m.mu.Unlock()
	for i := 1; i < pingMissLimit; i++ {
		d.measureNodes(context.Background())
		select {
		case <-peer.Done():
			t.Fatalf("closed after %d unanswered ping(s), want %d", i, pingMissLimit)
		default:
		}
	}
	d.measureNodes(context.Background())
	select {
	case <-peer.Done():
	default:
		t.Fatalf("still open after %d unanswered pings", pingMissLimit)
	}
	if err := peer.Err(); err == nil || err.Error() != "no answer to 2 pings in a row (a half-open link?)" {
		t.Fatalf("closed with %v", err)
	}
}

// The incident in #8771: the hub-to-node direction of an ssh link stalled (a full-size segment lost to a path MTU
// black hole) while the node, with Agents running, kept sending its own requests and events, some small lines cut in
// two by the read boundary. None of that shows a ping got there, so two unanswered pings in a row still close the
// connection, and a start waiting on it is dropped (what its retry acts on) instead of waiting out its timeout.
func TestNodeTrafficDoesNotKeepAStalledLinkOpen(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	lost := &lostWriter{started: make(chan struct{})}
	peer := rpc.New("h", lost) // nothing the hub writes gets to the node: no ping is ever answered
	var requests, events atomic.Int64
	peer.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
		requests.Add(1)
		return map[string]any{}, nil // its reply goes nowhere either
	}
	peer.OnEvent = func(string, json.RawMessage) { events.Add(1) }
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	stop, nodeDone := make(chan struct{}), make(chan struct{})
	go func() { // the node's own traffic, far more often than pingActivityWindow, each piece one raw read
		defer close(nodeDone)
		for i := 0; ; i++ {
			pieces := [][]string{
				{fmt.Sprintf(`{"id":"n%d","method":"state.load","params":{}}`+"\n", i)},
				{`{"event":"session.screen","params":{"session_id":"s1","status":"working"}}` + "\n"},
				{`{"event":"sessions.changed","par`, `ams":{"sessions":[]}}` + "\n"},
				{fmt.Sprintf(`{"id":"n%d","method":"state.lo`, i), `ad","params":{}}` + "\n"},
			}[i%4]
			for _, piece := range pieces {
				if _, err := io.WriteString(w, piece); err != nil {
					return
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = w.Close()
		_ = r.Close()
		<-nodeDone
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	started := make(chan error, 1)
	go func() { started <- peer.Call(context.Background(), "session.start", map[string]any{}, nil) }()
	select {
	case <-lost.started:
	case <-time.After(time.Second):
		t.Fatal("session.start was not written")
	}
	for i := 1; i <= pingMissLimit; i++ {
		// Fresh node traffic right before every ping: well inside the activity window when the ping times out.
		seen := events.Load()
		deadline := time.Now().Add(time.Second)
		for events.Load() == seen {
			if time.Now().After(deadline) {
				t.Fatal("the node's traffic stopped")
			}
			<-time.After(time.Millisecond)
		}
		d.measureNodes(context.Background())
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatalf("still open after %d unanswered pings: the node's own requests and events kept it alive", pingMissLimit)
	}
	if err := peer.Err(); err == nil || err.Error() != "no answer to 2 pings in a row (a half-open link?)" {
		t.Fatalf("closed with %v", err)
	}
	select {
	case err := <-started:
		if !errors.Is(err, rpc.ErrDropped) {
			t.Fatalf("session.start = %v, want ErrDropped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session.start still waits on the closed connection")
	}
	if requests.Load() == 0 || events.Load() == 0 {
		t.Fatalf("the node sent %d requests and %d events, want both", requests.Load(), events.Load())
	}
}

// Transport framing can split even a small node event across raw reads (streaming decompression, lost segments).
// Such a split is not a frame a pong can be queued behind, so it must not keep a stalled link alive (#8771).
func TestSmallSplitNodeTrafficDoesNotKeepAStalledLinkOpen(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	peer := rpc.New("h", io.Discard)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	stop := make(chan struct{})
	go func() {
		// A well under 16 KiB event, deliberately delivered in three raw reads, more often than the activity window.
		parts := []string{`{"event":"session.screen","par`, `ams":{"session_id":"s1","status":"wor`, `king"}}` + "\n"}
		for {
			for _, part := range parts {
				if _, err := io.WriteString(w, part); err != nil {
					return
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = w.Close()
		_ = r.Close()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	for i := 1; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatalf("still open after %d unanswered pings: small split node traffic kept it alive", pingMissLimit)
	}
}

// A node's own large event (a session list of hundreds of KiB, #8771) arrives in many reads, but as a whole line: the
// reads in its middle can explain a pong queued behind the frame only while it is still arriving. Once the event is
// through and no frame is moving, two unanswered pings close the link; the completed event must not keep clearing
// misses for the whole activity window.
func TestLargeNodeEventsDoNotKeepAStalledLinkOpen(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	peer := rpc.New("h", io.Discard) // nothing the hub writes gets to the node: no ping is ever answered
	var events atomic.Int64
	peer.OnEvent = func(string, json.RawMessage) { events.Add(1) }
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	line := []byte(`{"event":"sessions.changed","params":{"list":"` + strings.Repeat("x", 200<<10) + `"}}` + "\n")
	for i := 1; i <= pingMissLimit; i++ {
		// A whole large event, processed before the ping: every read in its middle is behind us when the ping misses.
		seen := events.Load()
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write event: %v", err)
		}
		deadline := time.Now().Add(time.Second)
		for events.Load() == seen {
			if time.Now().After(deadline) {
				t.Fatal("the node's event was not processed")
			}
			<-time.After(time.Millisecond)
		}
		d.measureNodes(context.Background())
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatalf("still open after %d unanswered pings: a whole large node event kept it alive", pingMissLimit)
	}
}

// A pong that comes after its ping gave up still shows that the node got the ping (#8771): the next ping's timeout is
// not a miss. Once nothing comes back any more, two silent pings after it close the connection as before.
func TestLatePongKeepsTheConnection(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	writes := &signalWriter{ch: make(chan string, 16)}
	peer := rpc.New("h", writes)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	for i := 0; i < pingMissLimit+1; i++ {
		d.measureNodes(context.Background())
		var ping struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(writes.wantPing(t)), &ping); err != nil || ping.ID == "" {
			t.Fatalf("ping id: %q, %v", ping.ID, err)
		}
		assertPeerOpen(t, peer)
		writePingActivity(t, w, peer, `{"id":"`+ping.ID+`","result":{}}`+"\n") // its pong, after measureNodes gave up
	}

	// However long ago it came, the last pong answers the ping before the next one, which is not a miss; two more
	// timeouts are.
	<-time.After(pingActivityWindow + pingWait)
	for i := 0; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		writes.wantPing(t)
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("silent connection stayed open after two more written ping timeouts")
	}
}

// A large inbound frame can hold the node's pong behind it. Recent chunks from its middle count even when none arrive
// during the ping itself; once the traffic stops, two silent written pings still close the connection.
func TestPingTimeoutsDoNotCloseDuringLargeTransfer(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	writes := &signalWriter{ch: make(chan string, 16)}
	peer := rpc.New("h", writes)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	head := `{"event":"file.chunk","params":"`
	chunks := []string{
		head,
		// This read takes the open frame past the rpc middle-frame threshold (16 KiB): only a frame that big can
		// genuinely hold the pong on the node's single writer, so only it counts while still arriving (#8771).
		strings.Repeat("x", 16<<10+1024),
		`the pong is still queued behind this frame; `,
		`more bytes before another ping`,
	}
	// The frame's first read could as well be a small line cut by the read boundary (#8771): only the reads after it,
	// which go on with the frame without finishing it, are activity.
	writeInput(t, w, chunks[0])
	for i := 1; i <= pingMissLimit+1; i++ {
		writePingActivity(t, w, peer, chunks[i])
		d.measureNodes(context.Background())
		writes.wantPing(t)
		assertPeerOpen(t, peer)
	}

	// No more bytes arrive. Once the recent-activity window expires, this same peer is indistinguishable from a
	// half-open link and must retain the original two-miss behavior.
	<-time.After(pingActivityWindow + pingWait)
	for i := 1; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		writes.wantPing(t)
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("silent connection stayed open after two more written ping timeouts")
	}
}

// A relayed deadline is itself an inbound response. It must not refresh activity and prevent every later relayed
// deadline from closing the peer.
func TestMeasureNodesClosesAfterRelayedTimeoutsWithoutOtherTraffic(t *testing.T) {
	hubConn, nodeConn := net.Pipe()
	hub, node := rpc.New("h", hubConn), rpc.New("n", nodeConn)
	node.OnRequest = func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Message: context.DeadlineExceeded.Error()}
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
	d, _ := pingMachine(t, hub)
	for i := 1; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		if i < pingMissLimit {
			assertPeerOpen(t, hub)
		}
	}
	select {
	case <-hub.Done():
	default:
		t.Fatal("relayed timeout responses kept the connection alive without other traffic")
	}
}

// A writer that consumed most of the probe left too little time for a pong, so those rounds do not count as misses.
func TestPingMostlyDelayedByWriterDoesNotCountAsMiss(t *testing.T) {
	oldWait := pingWait
	pingWait = 100 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &delayWriter{uploads: make(chan chan struct{}, 16), pings: make(chan string, 16)}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	for i := 0; i < pingMissLimit+1; i++ {
		runDelayedPing(t, d, peer, w, 90*time.Millisecond)
		assertPeerOpen(t, peer)
	}
}

// A brief queue is different: the ping was written with almost its whole response budget left, and a silent peer
// must still close after two misses. An unbounded ErrWriteDelayed exemption would keep it open forever.
func TestBrieflyDelayedPingStillClosesSilentPeer(t *testing.T) {
	oldWait := pingWait
	pingWait = 100 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &delayWriter{uploads: make(chan chan struct{}, 16), pings: make(chan string, 16)}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	for i := 1; i <= pingMissLimit; i++ {
		runDelayedPing(t, d, peer, w, 0)
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("silent peer stayed open after two briefly delayed but written pings")
	}
}

// The pre-existing not-written path is exercised through measureNodes too: only the upload reaches the writer, and
// repeated pings expiring behind it never close the connection.
func TestMeasureNodesDoesNotCloseWhenPingWaitsForLargeWrite(t *testing.T) {
	oldWait := pingWait
	pingWait = 50 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &stuckWriter{in: make(chan struct{}), release: make(chan struct{})}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- peer.Call(context.Background(), "file.upload.chunk", nil, nil) }()
	t.Cleanup(func() {
		close(w.release)
		peer.Close(io.EOF)
		select {
		case <-uploadDone:
		case <-time.After(time.Second):
			t.Error("upload did not stop")
		}
	})
	select {
	case <-w.in:
	case <-time.After(time.Second):
		t.Fatal("upload did not take the writer")
	}
	for i := 0; i < pingMissLimit+1; i++ {
		d.measureNodes(context.Background())
		assertPeerOpen(t, peer)
	}
	if got := w.writeCount(); got != 1 {
		t.Fatalf("writer calls = %d, want only the upload (no ping reached the wire)", got)
	}
}

func TestMeasureNodesSkipsAnOnlineMachineWithoutPeer(t *testing.T) {
	peer := rpc.New("h", io.Discard)
	d, m := pingMachine(t, peer)
	m.mu.Lock()
	m.peer = nil // a path switch can detach the peer while leaving the machine warm/online
	m.mu.Unlock()
	d.measureNodes(context.Background()) // must not panic or manufacture a miss on a replacement connection
}

func pingMachine(t *testing.T, peer *rpc.Peer) (*Devices, *Machine) {
	t.Helper()
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	d.addEntry(m)
	m.mu.Lock()
	m.peer, m.online, m.active, m.transport = peer, true, &nodePath{key: "m1"}, "ssh"
	m.mu.Unlock()
	return d, m
}

func assertPeerOpen(t *testing.T, peer *rpc.Peer) {
	t.Helper()
	select {
	case <-peer.Done():
		t.Fatalf("connection closed: %v", peer.Err())
	default:
	}
}

// delayWriter holds each upload until its own release channel closes; pings then complete immediately.
type delayWriter struct {
	uploads chan chan struct{}
	pings   chan string
}

func (w *delayWriter) Write(b []byte) (int, error) {
	line := string(b)
	if strings.Contains(line, `"method":"file.upload.chunk"`) {
		release := make(chan struct{})
		w.uploads <- release
		<-release
	} else if strings.Contains(line, `"method":"ping"`) {
		w.pings <- line
	}
	return len(b), nil
}

func runDelayedPing(t *testing.T, d *Devices, peer *rpc.Peer, w *delayWriter, delay time.Duration) {
	t.Helper()
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- peer.Call(context.Background(), "file.upload.chunk", nil, nil) }()
	select {
	case release := <-w.uploads:
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(func() {
			unblock()
			peer.Close(io.EOF)
			select {
			case <-uploadDone:
			case <-time.After(time.Second):
				t.Error("upload did not stop")
			}
		})
		measureDone := make(chan struct{})
		go func() {
			d.measureNodes(context.Background())
			close(measureDone)
		}()
		deadline := time.Now().Add(time.Second)
		for peer.WriteWaiters() == 0 {
			if time.Now().After(deadline) {
				unblock()
				t.Fatal("ping never queued behind the upload")
			}
			<-time.After(time.Millisecond)
		}
		if delay > 0 {
			<-time.After(delay)
		}
		unblock()
		select {
		case <-measureDone:
		case <-time.After(2 * time.Second):
			t.Fatal("measureNodes did not return")
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not take the writer")
	}
	select {
	case <-w.pings:
	case <-time.After(time.Second):
		t.Fatal("delayed ping was not written")
	}
}

// lostWriter is the hub's end of a link whose hub-to-node direction has stalled: every write succeeds and goes
// nowhere. started closes once a session.start went out.
type lostWriter struct {
	started chan struct{}
	once    sync.Once
}

func (w *lostWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), `"method":"session.start"`) {
		w.once.Do(func() { close(w.started) })
	}
	return len(b), nil
}

// signalWriter completes every write immediately and exposes each complete RPC line to the test.
type signalWriter struct{ ch chan string }

func (w *signalWriter) Write(b []byte) (int, error) {
	w.ch <- string(b)
	return len(b), nil
}

func (w *signalWriter) wantPing(t *testing.T) string {
	t.Helper()
	select {
	case got := <-w.ch:
		if !strings.Contains(got, `"method":"ping"`) {
			t.Fatalf("write = %q, want a ping", got)
		}
		return got
	case <-time.After(time.Second):
		t.Fatal("ping was not written")
	}
	return ""
}

// writeInput puts s on the wire as one raw read and waits until the reader took it.
func writeInput(t *testing.T, w io.Writer, s string) {
	t.Helper()
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, s)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write input: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("input write did not reach the reader")
	}
}

// writePingActivity puts input that counts as ping activity on the wire (another chunk from the middle of a frame, a
// reply) and waits until the reader recorded it.
func writePingActivity(t *testing.T, w io.Writer, peer *rpc.Peer, chunk string) {
	t.Helper()
	before := peer.PingActivity()
	writeInput(t, w, chunk)
	deadline := time.Now().Add(time.Second)
	for !peer.PingActivity().After(before) {
		if time.Now().After(deadline) {
			t.Fatal("reader did not record the activity")
		}
		<-time.After(time.Millisecond)
	}
}

// Only pings in a row on the same connection count: an answer (an error the node answered with too) clears the
// count, a connection already gone counts neither way, and a new connection starts over.
func TestPingMissesCountInARowOnOneConnection(t *testing.T) {
	d := newTestDevices(t)
	var log logLines
	d.logf = log.logf
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	peer := rpc.New("h", io.Discard)
	m.mu.Lock()
	m.peer = peer
	m.mu.Unlock()
	open := func() bool {
		select {
		case <-peer.Done():
			return false
		default:
			return true
		}
	}
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, nil)
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, &rpc.Error{Code: "busy", Message: "busy"})
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, rpc.ErrDropped) // gone: neither a miss nor an answer
	m.pingAnswered(peer, notWritten(t))  // never on the wire (a long write ahead): neither
	m.mu.Lock()
	m.reloadUntil = time.Now().Add(time.Minute) // a resident node handing over: calls wait for the new connection
	m.mu.Unlock()
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.mu.Lock()
	m.reloadUntil = time.Time{}
	m.mu.Unlock()
	if !open() {
		t.Fatal("closed although no two misses were in a row")
	}
	other := rpc.New("h", io.Discard)
	m.pingAnswered(other, context.DeadlineExceeded) // another connection's miss starts its own count
	m.pingAnswered(peer, context.DeadlineExceeded)
	if !open() {
		t.Fatal("misses on two connections were added up")
	}
	m.pingAnswered(peer, &rpc.Error{Message: "context deadline exceeded"}) // a relay's words for its wait running out
	if open() {
		t.Fatal("two misses in a row did not close the connection")
	}
	if got := log.with("no answer to 2 pings in a row over", "nothing received yet"); len(got) != 1 {
		t.Fatalf("close lines = %q, want one saying nothing was received", log.lines)
	}

	// A connection the machine no longer uses is not closed (it is the run loop's to end).
	m.mu.Lock()
	m.peer = rpc.New("h", io.Discard)
	m.mu.Unlock()
	m.pingAnswered(other, context.DeadlineExceeded)
	select {
	case <-other.Done():
		t.Fatal("closed a connection the machine no longer uses")
	default:
	}
}

// notWritten is what a ping that waited for the writer and gave up answers.
func notWritten(t *testing.T) error {
	t.Helper()
	w := &stuckWriter{in: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	p := rpc.New("h", w)
	go func() { _ = p.Call(context.Background(), "file.upload", nil, nil) }()
	<-w.in // the upload holds the writer, its write stuck
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.Call(ctx, "ping", nil, nil)
	if !errors.Is(err, rpc.ErrNotWritten) {
		t.Fatalf("ping behind a stuck write: %v", err)
	}
	return err
}

// stuckWriter's first write says so on in and blocks until release.
type stuckWriter struct {
	in, release chan struct{}
	once        sync.Once
	mu          sync.Mutex
	calls       int
}

func (w *stuckWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	w.once.Do(func() { close(w.in) })
	<-w.release
	return len(b), nil
}

func (w *stuckWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// A relayed link whose every pong comes back after pingWait but well within one cycle (a high-RTT path) is working:
// the previous ping's late pong clears the next miss, so it is never closed as half-open. Once the pongs stop, two
// misses in a row still close it.
func TestLatePongsOnEveryCycleKeepTheConnection(t *testing.T) {
	oldWait, oldInterval, oldWindow := pingWait, pingInterval, pingActivityWindow
	pingWait, pingInterval, pingActivityWindow = 100*time.Millisecond, 600*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { pingWait, pingInterval, pingActivityWindow = oldWait, oldInterval, oldWindow })
	var silent atomic.Bool
	peer := pongNode(t, func() time.Duration {
		if silent.Load() {
			return -1
		}
		return 140 * time.Millisecond
	})
	d, _ := pingMachine(t, peer)
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		d.measureLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})
	<-time.After(5*pingInterval + pingInterval/2) // five rounds, every pong late
	assertPeerOpen(t, peer)

	silent.Store(true)
	select {
	case <-peer.Done():
	case <-time.After(pingActivityWindow + time.Duration(pingMissLimit+2)*pingInterval):
		t.Fatal("silent connection stayed open")
	}
}

// Pings go out one computer after another, so computers ahead of this one that start timing out push its next ping
// later and later after the last (#10442). Its late pongs, each arriving after its ping gave up, still clear the next
// miss however far apart its pings fall.
func TestLatePongsCountWhenSlowMachinesAheadDelayThePing(t *testing.T) {
	oldWait, oldInterval, oldWindow := pingWait, pingInterval, pingActivityWindow
	pingWait, pingInterval, pingActivityWindow = 100*time.Millisecond, 600*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { pingWait, pingInterval, pingActivityWindow = oldWait, oldInterval, oldWindow })
	d := newTestDevices(t)
	var slowAhead atomic.Int32
	for i := range 8 {
		addPingMachine(d, fmt.Sprintf("ahead%d", i), pongNode(t, func() time.Duration {
			if int32(i) < slowAhead.Load() {
				return -1 // times out
			}
			return 0
		}))
	}
	var silent atomic.Bool
	target := pongNode(t, func() time.Duration {
		if silent.Load() {
			return -1
		}
		return 150 * time.Millisecond
	})
	addPingMachine(d, "target", target)

	// Rounds a pingInterval apart; 0, then 4, then 8 computers ahead time out, so the target's pings are about
	// pingInterval + 4*pingWait apart, and each late pong arrives longer than pingActivityWindow before the next miss.
	round := func(slow int32) {
		slowAhead.Store(slow)
		start := time.Now()
		d.measureNodes(context.Background())
		<-time.After(time.Until(start.Add(pingInterval)))
	}
	for _, slow := range []int32{0, 4, 8} {
		round(slow)
		assertPeerOpen(t, target)
	}

	silent.Store(true) // the last late pong clears one more miss, then two in a row close it
	for range pingMissLimit + 1 {
		round(0)
	}
	select {
	case <-target.Done():
	case <-time.After(time.Second):
		t.Fatal("silent connection stayed open")
	}
}

// pongNode is a node on a connection of its own that answers each ping after delay(), or never when that is below
// zero.
func pongNode(t *testing.T, delay func() time.Duration) *rpc.Peer {
	t.Helper()
	r, w := io.Pipe()
	writes := &signalWriter{ch: make(chan string, 16)}
	peer := rpc.New("h", writes)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	ctx, cancel := context.WithCancel(context.Background())
	var answers sync.WaitGroup
	answerDone := make(chan struct{})
	go func() {
		defer close(answerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-writes.ch:
				var ping struct {
					ID string `json:"id"`
				}
				wait := delay()
				if json.Unmarshal([]byte(line), &ping) != nil || ping.ID == "" || wait < 0 {
					continue
				}
				answers.Add(1)
				go func() {
					defer answers.Done()
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
					_, _ = io.WriteString(w, `{"id":"`+ping.ID+`","result":{}}`+"\n")
				}()
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-answerDone
		_ = w.Close()
		_ = r.Close()
		answers.Wait()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	return peer
}

// addPingMachine adds an online ssh machine whose connection is peer.
func addPingMachine(d *Devices, id string, peer *rpc.Peer) *Machine {
	m := newMachine(d, MachineConfig{ID: id, Kind: "ssh", SSHHost: id})
	d.addEntry(m)
	m.mu.Lock()
	m.peer, m.online, m.active, m.transport = peer, true, &nodePath{key: id}, "ssh"
	m.mu.Unlock()
	return m
}
