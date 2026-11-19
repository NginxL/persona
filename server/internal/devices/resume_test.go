package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// residentNode is a fake resident node: it keeps its connection when the transport ends and takes it back for an
// rpc.resume with its token, as node/attach_resume.go does; restarted, it has none to take back. With noResume it is a
// node from before resuming (main's): every transport is a new connection. link shapes its transports (zero: net.Pipe).
type residentNode struct {
	link      netshape.Config
	noResume  bool
	mu        sync.Mutex
	peer      *rpc.Peer
	conn      net.Conn
	manager   *deadConn // the manager's end of the transport in use
	transport int       // transports dialed
	fresh     int       // connections served from the start
	restarted bool
	release   chan struct{} // what a "slow" call waits for
	slowSeen  chan struct{} // closed when one came
	slowOnce  sync.Once
}

func (n *residentNode) dial(context.Context) (net.Conn, error) {
	var a, b net.Conn
	if n.link == (netshape.Config{}) {
		a, b = net.Pipe()
	} else {
		a, b = netshape.Pipe(n.link, n.link)
	}
	managerSide, nodeConn := newDeadConn(a), newDeadConn(b)
	var nodeSide net.Conn = nodeConn
	managerSide.other, nodeConn.other = nodeConn, managerSide
	n.mu.Lock()
	n.transport++
	n.manager = managerSide
	n.mu.Unlock()
	go func() {
		line, rest, _ := rpc.ReadFirstLine(nodeSide, 64<<10)
		n.mu.Lock()
		p, restarted := n.peer, n.restarted
		n.mu.Unlock()
		if req, ok := rpc.ParseResume(line); ok {
			if p != nil && !restarted && p.AcceptResume(req, rest, nodeSide) == nil {
				n.mu.Lock()
				old := n.conn
				n.conn = nodeSide
				n.mu.Unlock()
				old.Close()
				return
			}
			_ = rpc.RefuseResume(nodeSide, req)
			nodeSide.Close()
			return
		}
		p = rpc.New("n", nodeSide)
		p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		caps := append(append([]string{}, nodeproto.Required...), nodeproto.NodeLanes)
		if !n.noResume {
			p.EnableResume(resumeWindow)
			caps = append(caps, nodeproto.NodeResume)
		}
		p.OnLost = func(error) { n.cut() }
		p.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				return proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test",
					Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion, Capabilities: caps}, nil
			case "file.chunk": // a piece of a file being downloaded (resume_bench_test.go)
				var req struct {
					Length int `json:"length"`
				}
				_ = json.Unmarshal(params, &req)
				return map[string]any{"data": strings.Repeat("d", req.Length)}, nil
			case "sessions.list":
				return map[string]any{"sessions": []proto.Session{}}, nil
			case "slow":
				n.slowOnce.Do(func() { close(n.slowSeen) })
				select {
				case <-n.release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return map[string]any{"ok": true}, nil
		}
		n.mu.Lock()
		n.peer, n.conn, n.restarted = p, nodeSide, false
		n.fresh++
		n.mu.Unlock()
		go func() { _ = p.Serve(context.Background(), io.MultiReader(bytes.NewReader(line), rest)) }()
	}()
	return managerSide, nil
}

// deadConn is a transport that can die without a word (the computer's address changed: nothing it sends arrives, and
// nothing comes back, but neither end is told), or be closed as any.
type deadConn struct {
	net.Conn
	other  *deadConn
	dead   atomic.Bool
	gone   chan struct{} // closed by Close
	closed sync.Once
}

func newDeadConn(c net.Conn) *deadConn { return &deadConn{Conn: c, gone: make(chan struct{})} }

func (c *deadConn) Read(b []byte) (int, error) {
	if c.dead.Load() {
		<-c.gone // nothing comes any more: until closed
		return 0, net.ErrClosed
	}
	n, err := c.Conn.Read(b)
	if c.dead.Load() {
		<-c.gone
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *deadConn) Write(b []byte) (int, error) {
	if c.dead.Load() {
		return len(b), nil // goes nowhere
	}
	return c.Conn.Write(b)
}

func (c *deadConn) Close() error {
	c.closed.Do(func() { close(c.gone) })
	return c.Conn.Close()
}

// die makes the transport in use go silent both ways, as an address change does.
func (n *residentNode) die() {
	n.mu.Lock()
	m := n.manager
	n.mu.Unlock()
	if m != nil {
		m.dead.Store(true)
		m.other.dead.Store(true)
	}
}

// cut closes the transport in use, as a relay that ended or a link that dropped.
func (n *residentNode) cut() {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (n *residentNode) counts() (transports, fresh int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.transport, n.fresh
}

// resumingMachine runs a machine on a resident node and waits until it is connected, resumable.
func resumingMachine(t *testing.T) (*Machine, *residentNode, *benchLog) {
	t.Helper()
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) {
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	n := &residentNode{release: make(chan struct{}), slowSeen: make(chan struct{})}
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	m.dialNode = n.dial
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, 10*time.Second, "box connected, resumable", func() bool { return logs.count("lanes=on resume=on") > 0 })
	return m, n, logs
}

// A dropped transport is taken back: the call in flight answers once the connection is resumed, nothing connects or
// is watched again, and the machine never shows offline (#10663).
func TestMachineResumesADroppedConnection(t *testing.T) {
	m, n, logs := resumingMachine(t)
	call := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		call <- m.call(ctx, "slow", map[string]any{}, nil)
	}()
	select {
	case <-n.slowSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not reach the node")
	}
	n.cut()
	eventually(t, 10*time.Second, "resumed", func() bool { return logs.count("] connection over ") > 0 && logs.count("resumed after") > 0 })
	close(n.release)
	if err := <-call; err != nil {
		t.Fatalf("the call in flight: %v", err)
	}
	if transports, fresh := n.counts(); transports != 2 || fresh != 1 {
		t.Fatalf("%d transports, %d served from the start: want 2 and 1", transports, fresh)
	}
	if c := logs.count("] connected over "); c != 1 {
		t.Fatalf("connected %d times", c)
	}
	if logs.count("offline") > 0 || logs.count("disconnected") > 0 {
		t.Fatalf("shown offline: %q", logs.since(0))
	}
	if !m.reachable() {
		t.Fatal("not reachable after resuming")
	}
	if logs.count("resumed=1") == 0 {
		t.Fatalf("the counts: %q", logs.since(0))
	}
}

// A node that restarted has nothing to take back: the manager is refused, and connects anew at once.
func TestMachineFallsBackWhenTheNodeHasNoConnection(t *testing.T) {
	_, n, logs := resumingMachine(t)
	n.mu.Lock()
	n.restarted = true
	n.mu.Unlock()
	n.cut()
	eventually(t, 10*time.Second, "connected anew", func() bool { return logs.count("] connected over ") == 2 })
	if logs.count("fell_back=1") == 0 || logs.count("resume refused") == 0 && logs.count("not resumed") == 0 {
		t.Fatalf("the fall back is not logged: %q", logs.since(0))
	}
}

// Pings unanswered on a connection that can resume cut its transport, not the connection: a new transport takes it
// back.
func TestPingMissesCutAResumableConnection(t *testing.T) {
	old := pingActivityWindow
	pingActivityWindow = 0 // the handshake's replies do not excuse these misses
	t.Cleanup(func() { pingActivityWindow = old })
	m, n, logs := resumingMachine(t)
	m.mu.Lock()
	peer := m.peer
	m.mu.Unlock()
	for range pingMissLimit {
		m.pingAnswered(peer, context.DeadlineExceeded)
	}
	eventually(t, 10*time.Second, "resumed", func() bool { return logs.count("resumed after") > 0 })
	if logs.count("pings in a row over ssh") != 1 || logs.count("resuming on a new transport") != 1 {
		t.Fatalf("not cut: %q", logs.since(0))
	}
	if peer.Err() != nil {
		t.Fatalf("the connection closed: %v", peer.Err())
	}
	if _, fresh := n.counts(); fresh != 1 {
		t.Fatalf("%d connections from the start", fresh)
	}
}

// A transport's close runs once however often it is asked for: a resume cuts it while the connection ending closes
// it too, and an ssh pipe's close waits for its process, which two waits at once would never see end (#10829's review).
func TestPipeClosesOnce(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	var closes atomic.Int32
	m.dialNode = func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { b.Close() })
		return &countedConn{Conn: a, closes: &closes}, nil
	}
	d.addEntry(m)
	paths := d.pathsOf(m)
	if len(paths) == 0 {
		t.Fatal("no path")
	}
	l, err := m.openPipe(context.Background(), paths[0], "ssh")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); l.close() }()
	}
	wg.Wait()
	if n := closes.Load(); n != 1 {
		t.Fatalf("closed %d times", n)
	}
}

type countedConn struct {
	net.Conn
	closes *atomic.Int32
}

func (c *countedConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

// A resident node keeps a dropped connection past the longest the manager can take to notice the drop and try: it may
// have noticed first, from an end the manager never read (#10829's review). The manager notices a transport that died
// without a word by its next ping and the silence check after it (rpc resumeStale, 15 s).
func TestNodeKeepsPastTheManagersNotice(t *testing.T) {
	keep := time.Duration(nodeproto.ResumeKeepSeconds) * time.Second
	if notice := pingInterval + 15*time.Second + resumeTryWait; keep < notice {
		t.Fatalf("the node keeps %v, the manager may take %v to notice and try", keep, notice)
	}
	if keep < resumeWindow {
		t.Fatalf("the node keeps %v, less than the manager tries (%v)", keep, resumeWindow)
	}
}
