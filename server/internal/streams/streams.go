// Package streams carries byte streams over the hub↔node RPC connection (docs/operations/ports.md "Forwarding"): a TCP connection
// the hub forwards to a port on a node is one stream there, its bytes in stream.data events. Each direction has a
// window of bytes sent and not yet acknowledged (stream.ack), so a slow reader on one side holds its sender back
// instead of filling memory, and one big download cannot take the whole connection from everything else on it.
//
// Both ends run a Mux on their rpc.Peer: Open registers a stream (bytes for it are kept from then on), Start pumps
// it to and from a connection. The side that dials (the node) opens and starts before answering; the side that
// asked (the hub) opens before asking and starts once it has the answer, so no byte arrives for a stream nobody
// holds. By default an EOF ends the stream with stream.close. On a negotiated TCP stream, stream.eof carries one
// direction's EOF and the stream lives until both directions have finished or either end closes it outright.
//
// The side that asked can give up before the answer (its caller went away, the call timed out): its stream.close
// then reaches the dialing side before that side's Open. So a Mux remembers for goneFor the ids it heard closed
// without holding them, and Open refuses such an id: the dial ends instead of keeping a connection nobody reads.
// Data for a stream this side does not hold (it ended here, or this side restarted since) gets one stream.close
// back, so the other side ends it too. An id is therefore never used twice between the same two Muxes: a node's Mux
// outlives a hub restart, so the hub's ids carry a token of its process (hub/ports.go streamIDs).
package streams

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

// Events between the two Muxes.
const (
	EventData  = "stream.data"
	EventAck   = "stream.ack"
	EventClose = "stream.close"
	EventEOF   = "stream.eof"
)

// HalfCloseConn is a stream endpoint that can deliver EOF to its reader while still accepting bytes to send.
type HalfCloseConn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

const (
	chunk = 32 << 10
	// window is how much may be in flight in one direction: enough for a page's scripts at the speed of the link,
	// small enough that a stalled reader holds little.
	window = 512 << 10
	// goneFor is how long an id the peer closed before this side opened it stays refused: longer than the slowest
	// dial the peer waits for (a browser starting for a preview).
	goneFor = 2 * time.Minute
	// maxGone bounds the ids remembered; the oldest go first.
	maxGone = 1024
)

// goneID is an id the peer closed or wrote to while this side held no stream by it.
type goneID struct {
	at     time.Time
	closed bool // the peer closed it (data alone only means this side answered it once)
}

// Mux holds the streams of one RPC connection.
type Mux struct {
	send func(event string, params any) error

	mu      sync.Mutex
	streams map[string]*stream
	closed  bool
	gone    map[string]goneID
}

// New makes a Mux that sends its events with send (rpc.Peer.Notify).
func New(send func(event string, params any) error) *Mux {
	return &Mux{send: send, streams: map[string]*stream{}}
}

type stream struct {
	id string
	m  *Mux

	mu       sync.Mutex
	cond     *sync.Cond
	inflight int      // bytes sent and not yet acknowledged
	queue    [][]byte // bytes from the peer, not yet written to conn
	queued   int
	peerDone bool // the peer sent close: write what is queued, then close conn
	aborted  bool // window exceeded in Event: finish runs in its own goroutine
	done     bool // finished: nothing more is read, written or sent
	conn     io.ReadWriteCloser

	halfConn     HalfCloseConn
	peerEOF      bool // the peer sent its last byte
	readDone     bool // local EOF sent to the peer
	writeDone    bool // peer EOF delivered to conn
	reportClosed bool // the dialing node reports completion to its manager
}

type dataMsg struct {
	ID    string `json:"id"`
	Data  []byte `json:"data,omitempty"`
	N     int    `json:"n,omitempty"`
	Err   string `json:"error,omitempty"`
	Abort bool   `json:"abort,omitempty"`
}

type closeMode uint8

const (
	closeLocal closeMode = iota
	closePeer
	abortPeer
)

// Open registers stream id: bytes the peer sends for it are kept until Start. It refuses an id the peer closed in
// the last goneFor before this: the peer gave up on it already.
func (m *Mux) Open(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("streams: connection closed")
	}
	if m.streams[id] != nil {
		return errors.New("streams: " + id + " is already open")
	}
	if g, ok := m.gone[id]; ok {
		delete(m.gone, id)
		if g.closed && time.Since(g.at) < goneFor {
			return errors.New("streams: " + id + " was closed before it opened")
		}
	}
	s := &stream{id: id, m: m}
	s.cond = sync.NewCond(&s.mu)
	m.streams[id] = s
	return nil
}

// markGoneLocked remembers that the peer closed (closed) or wrote to id while this side held no such stream; it
// reports whether id is new to the list. Once the list is full, expired ids are dropped, then the oldest; m.mu is
// held.
func (m *Mux) markGoneLocked(id string, closed bool) bool {
	now := time.Now()
	if g, ok := m.gone[id]; ok {
		if closed && !g.closed {
			m.gone[id] = goneID{at: now, closed: true}
		}
		return false
	}
	if m.gone == nil {
		m.gone = map[string]goneID{}
	}
	if len(m.gone) >= maxGone {
		oldest, oldestAt := "", now
		for k, g := range m.gone {
			if now.Sub(g.at) >= goneFor {
				delete(m.gone, k)
			} else if !g.at.After(oldestAt) {
				oldest, oldestAt = k, g.at
			}
		}
		if len(m.gone) >= maxGone {
			delete(m.gone, oldest)
		}
	}
	m.gone[id] = goneID{at: now, closed: closed}
	return true
}

// Start pumps stream id to and from conn with legacy full-close semantics. It returns at once.
func (m *Mux) Start(id string, conn io.ReadWriteCloser) {
	m.start(id, conn, nil, false)
}

// StartHalfClose pumps a negotiated TCP stream until both directions reach EOF or the stream closes outright.
// The dialing node sets reportClosed to retire the manager's tracking once its socket is closed.
func (m *Mux) StartHalfClose(id string, conn HalfCloseConn, reportClosed bool) {
	m.start(id, conn, conn, reportClosed)
}

func (m *Mux) start(id string, conn io.ReadWriteCloser, halfConn HalfCloseConn, reportClosed bool) {
	m.mu.Lock()
	s := m.streams[id]
	m.mu.Unlock()
	if s == nil {
		_ = conn.Close()
		return
	}
	s.mu.Lock()
	if s.done || s.conn != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.conn = conn
	s.halfConn, s.reportClosed = halfConn, reportClosed
	s.mu.Unlock()
	go s.read()
	go s.write()
}

// Abort ends stream id from this side (the dial failed, the caller went away), telling the peer.
func (m *Mux) Abort(id string, why error) {
	m.mu.Lock()
	s := m.streams[id]
	m.mu.Unlock()
	if s != nil {
		s.finish(why, abortPeer)
	}
}

// Event takes the Mux's events off the RPC connection; it reports whether event was one. It never blocks.
func (m *Mux) Event(event string, params json.RawMessage) bool {
	switch event {
	case EventData, EventAck, EventClose, EventEOF:
	default:
		return false
	}
	var p dataMsg
	if json.Unmarshal(params, &p) != nil {
		return true
	}
	m.mu.Lock()
	s := m.streams[p.ID]
	// Not one this side holds: a close before Open, or a stream that ended here (or before this side restarted). A
	// closed id is remembered so a late Open refuses it, and the first data or EOF for an id gets a close back, so the peer
	// ends its side too. The answer goes from its own goroutine: send may block, and Event never does.
	tell := false
	if s == nil && p.ID != "" && !m.closed && event != EventAck {
		tell = m.markGoneLocked(p.ID, event == EventClose) && event != EventClose
	}
	m.mu.Unlock()
	if s == nil {
		if tell {
			go func() {
				_ = m.send(EventClose, dataMsg{ID: p.ID, Err: "streams: no such stream", Abort: event == EventEOF})
			}()
		}
		return true
	}
	switch event {
	case EventData:
		if len(p.Data) == 0 {
			return true
		}
		s.mu.Lock()
		if s.done || s.peerDone || s.aborted || s.peerEOF && s.halfConn != nil {
			s.mu.Unlock()
			return true
		}
		if s.queued+len(p.Data) > window { // the peer ignored the window: drop the stream rather than the memory
			s.aborted = true
			s.queue, s.queued = nil, 0
			s.mu.Unlock()
			go s.finish(errors.New("streams: window exceeded"), abortPeer)
			return true
		}
		s.queue = append(s.queue, p.Data)
		s.queued += len(p.Data)
		s.cond.Broadcast()
		s.mu.Unlock()
	case EventAck:
		if p.N <= 0 {
			return true
		}
		s.mu.Lock()
		s.inflight = max(0, s.inflight-p.N)
		s.cond.Broadcast()
		s.mu.Unlock()
	case EventClose:
		s.mu.Lock()
		s.peerDone = true
		abort := p.Abort || s.writeDone
		s.cond.Broadcast()
		s.mu.Unlock()
		if abort {
			go s.finish(nil, closeLocal)
		}
	case EventEOF:
		s.mu.Lock()
		s.peerEOF = true
		s.cond.Broadcast()
		s.mu.Unlock()
	}
	return true
}

// CloseAll ends every stream without telling the peer (the RPC connection is gone).
func (m *Mux) CloseAll() {
	m.mu.Lock()
	m.closed = true
	all := make([]*stream, 0, len(m.streams))
	for _, s := range m.streams {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.finish(nil, closeLocal)
	}
}

// Len is how many streams are open (tests, logs).
func (m *Mux) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

// read sends what conn says to the peer, holding back while a window's worth is unacknowledged.
func (s *stream) read() {
	buf := make([]byte, chunk)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.mu.Lock()
			for s.inflight+n > window && !s.done {
				s.cond.Wait()
			}
			if s.done {
				s.mu.Unlock()
				return
			}
			s.inflight += n
			s.mu.Unlock()
			if serr := s.m.send(EventData, dataMsg{ID: s.id, Data: append([]byte(nil), buf[:n]...)}); serr != nil {
				s.finish(serr, closeLocal)
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if s.halfConn != nil {
					s.mu.Lock()
					closed := s.done || s.peerDone
					s.mu.Unlock()
					if closed {
						return
					}
					if err := s.m.send(EventEOF, dataMsg{ID: s.id}); err != nil {
						s.finish(err, closeLocal)
						return
					}
					s.halfDone(true)
					return
				}
				s.finish(nil, closePeer) // legacy EOF closes the entire stream
				return
			}
			s.finish(err, abortPeer)
			return
		}
	}
}

// write delivers queued bytes and only then applies the peer's EOF or close.
func (s *stream) write() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.peerDone && !s.done && !(s.peerEOF && s.halfConn != nil) {
			s.cond.Wait()
		}
		if s.done {
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			half := !s.peerDone
			s.mu.Unlock()
			if !half {
				s.finish(nil, closeLocal)
			} else if err := s.halfConn.CloseWrite(); err != nil {
				s.finish(err, abortPeer)
			} else {
				s.halfDone(false)
			}
			return
		}
		b := s.queue[0]
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.queued -= len(b)
		conn := s.conn
		s.mu.Unlock()
		if _, err := conn.Write(b); err != nil {
			s.finish(err, abortPeer)
			return
		}
		_ = s.m.send(EventAck, dataMsg{ID: s.id, N: len(b)})
	}
}

// halfDone keeps the stream open until the outgoing EOF is sent and the incoming EOF reaches the local socket.
func (s *stream) halfDone(read bool) {
	s.mu.Lock()
	if read {
		s.readDone = true
	} else {
		s.writeDone = true
	}
	closed, complete := s.peerDone && s.writeDone, s.readDone && s.writeDone
	s.mu.Unlock()
	if complete && s.reportClosed {
		s.finish(nil, closePeer)
	} else if closed || complete {
		s.finish(nil, closeLocal)
	}
}

// finish ends the stream once and reports the close if requested and the peer has not already closed.
func (s *stream) finish(why error, mode closeMode) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	peerDone := s.peerDone
	conn := s.conn
	abort := s.halfConn != nil && mode == abortPeer
	s.queue, s.queued = nil, 0
	s.cond.Broadcast()
	s.mu.Unlock()

	s.m.mu.Lock()
	delete(s.m.streams, s.id)
	s.m.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if mode != closeLocal && !peerDone {
		msg := dataMsg{ID: s.id, Abort: abort}
		if why != nil {
			msg.Err = why.Error()
		}
		_ = s.m.send(EventClose, msg)
	}
}
