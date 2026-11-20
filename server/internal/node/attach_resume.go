package node

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A resident node keeps the connection it serves when the transport under it ends (#10663, docs/protocol/protocol.md
// "Resuming"): for resumeWindow its Node and its calls go on, and a manager that comes back with the connection's token
// (rpc.resume, the first line of the new transport) takes it back where it stopped. A connection that starts any other
// way replaces the one served, as before; one with a token this node does not have (it restarted, or the token was used)
// is refused, and the manager connects anew.

// resumeWindow is how long a resident node keeps a connection whose transport ended (nodeproto.ResumeKeepSeconds).
var resumeWindow = time.Duration(nodeproto.ResumeKeepSeconds) * time.Second

// firstLineWait bounds the wait for a new transport's first line: the manager writes it at once (rpc.resume, or the
// handshake's first call), and an old one too.
var firstLineWait = 10 * time.Second

// attached is the connection a resident node serves: its transport, which a resume replaces, and its peer once made.
type attached struct {
	mu   sync.Mutex
	conn net.Conn
	peer *rpc.Peer
}

func (a *attached) setPeer(p *rpc.Peer) {
	a.mu.Lock()
	a.peer = p
	a.mu.Unlock()
	go func() { <-p.Done(); a.closeConn() }() // its transport ends with it: a read stuck on a silent one returns
}

// closeConn closes the transport now in use.
func (a *attached) closeConn() {
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	_ = c.Close()
}

// end lets the connection go: its transport, and its peer, which may be waiting for a resume.
func (a *attached) end(err error) {
	a.mu.Lock()
	c, p := a.conn, a.peer
	a.mu.Unlock()
	_ = c.Close()
	if p != nil {
		p.Close(err)
	}
}

// resume takes the connection back over c, which brought req (rest: what follows it); its old transport is closed,
// which ends a read or write still stuck on it. rpc.ErrResumeRefused: nothing was written, c is the caller's.
//
// Nothing but AcceptResume writes on c, and only once the peer is lost and c installed under its lock: a refused or
// failed resume never takes a frame from the transport in use (#10829's review).
func (a *attached) resume(req rpc.ResumeRequest, c net.Conn, rest io.Reader) error {
	a.mu.Lock()
	p := a.peer
	a.mu.Unlock()
	if p == nil {
		return rpc.ErrResumeRefused
	}
	if f := resumeDeciding.Load(); f != nil {
		(*f)()
	}
	if err := p.AcceptResume(req, rest, c); err != nil {
		return err
	}
	a.mu.Lock()
	old := a.conn
	a.conn = c
	a.mu.Unlock()
	_ = old.Close()
	select {
	case <-p.Done(): // it ended while c was installed: closeConn may have closed the old one only
		_ = c.Close()
	default:
	}
	return nil
}

// resumeDeciding, in tests only, runs as a resume on a new transport is about to be decided: the connection in use goes
// on writing meanwhile.
var resumeDeciding atomic.Pointer[func()]

// connReader is what a Node reads: the attached transport's bytes, the first line read ahead of them included. Close
// ends the connection, its transport and its peer: a resident node reloading ends it that way (node.Serve), and a
// connection kept for a resume must not wait out its window there.
type connReader struct {
	io.Reader
	a *attached
}

func (r connReader) Close() error {
	r.a.end(errReloading)
	return nil
}

var errReloading = errors.New("the node is restarting")

// peekedConn is a new transport, numbered in the order transports came, and what readFirstLine read of it.
type peekedConn struct {
	c     net.Conn
	came  uint64
	first []byte
	rest  io.Reader
	err   error
}

// readFirstLine reads c's first line (rpc.ReadFirstLine), waiting at most firstLineWait. line is what it read: the
// line, or what came before giving up; rest is what follows it.
func readFirstLine(c net.Conn) (line []byte, rest io.Reader, err error) {
	_ = c.SetReadDeadline(time.Now().Add(firstLineWait))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	return rpc.ReadFirstLine(c, firstLineMax)
}

// firstLineMax bounds a new transport's first line: rpc.resume, or the manager's first call, both short.
const firstLineMax = 64 << 10

var errReplaced = errors.New("replaced by a new connection")
