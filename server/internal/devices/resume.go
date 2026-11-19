package devices

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Resuming a node connection (#10663, docs/protocol/protocol.md "Resuming"): a resident node that announces
// node_resume_v1 keeps the connection when its transport ends, and so does the manager (rpc.Peer.EnableResume). The
// transport going (a relay ended, a link dropped, pings unanswered over a half-open one) then leaves the peer waiting:
// its calls pending, its watches on, the machine shown as it was. resumeConn opens a new transport on the same path,
// or on a better path when the same-path try failed, and takes the connection back over it, the node writing again
// what the manager did not read and the other way round; nothing is watched again. Refused (the node restarted), or
// not done within resumeWindow, the peer closes and the run loop connects anew as it always did.

// resumeWindow is how long a dropped connection waits to be resumed, from when the manager noticed (the node keeps it
// longer, nodeproto.ResumeKeepSeconds: it may have noticed first).
var resumeWindow = time.Duration(nodeproto.ResumeWindowSeconds) * time.Second

// resumeTryWait bounds one try: opening a transport and the rpc.resume exchange on it.
var resumeTryWait = 10 * time.Second

// resumeCounts is a computer's dropped connections that could resume, and how they ended (logged with the lanes').
type resumeCounts struct {
	drops, resumed, fellBack int
	last, most               time.Duration // to resume: the latest, the longest
}

func (r resumeCounts) String() string {
	return fmt.Sprintf("drops=%d resumed=%d fell_back=%d recover=%s/%s", r.drops, r.resumed, r.fellBack,
		r.last.Round(time.Millisecond), r.most.Round(time.Millisecond))
}

// cut closes the transport in use; the connection, kept for a resume, waits for a new one (resumeConn).
func (c *nodeConn) cut() { c.pipe().close() }

// openPipe opens a new transport to the node on path p, by via; it lasts as long as ctx. Its close may come twice
// (a resume cutting it while the connection ends), and runs once: an ssh pipe's waits for its process.
func (m *Machine) openPipe(ctx context.Context, p *nodePath, via string) (*nodePipe, error) {
	var l *nodePipe
	var err error
	if via == viaHub {
		l, err = m.d.openThroughHub(ctx, p.hub, p.mid)
	} else {
		l, err = p.src.open(ctx, via)
	}
	if err != nil {
		if via == "tailcat" {
			p.src.tailcatReset()
		}
		return nil, err
	}
	l.close = sync.OnceFunc(l.close)
	return l, nil
}

// resumeConn takes c back after its transport was lost with err (rpc.Peer.OnLost): the old transport is closed, and
// new ones are tried on its path until one resumes it, the node refuses, or the window ends. When a try on that path
// fails and another path is better by then, one resume is tried there before the connection falls back to a reconnect.
func (m *Machine) resumeConn(c *nodeConn, err error) {
	lostAt := time.Now()
	m.mu.Lock()
	m.resumes.drops++
	m.mu.Unlock()
	if c.ctx.Err() != nil {
		return // the connection is ending anyway (its run loop closes the transport)
	}
	c.cut() // a write still stuck on it returns
	if c.restarting.Load() {
		// The resident node said it restarts: the next process has none of this connection.
		c.peer.Close(fmt.Errorf("the node restarts: %w", err))
		m.fellBack(lostAt, "the node restarts")
		return
	}
	_, lostVia := c.route()
	m.d.logf("[%s] connection over %s lost (%v): resuming within %s", m.cfg.ID, lostVia, oneLineErr(err), resumeWindow)
	deadline := lostAt.Add(resumeWindow)
	wait := 200 * time.Millisecond
	for try := 1; time.Until(deadline) > 0; try++ {
		p, via := c.route()
		rerr := m.tryResume(c, p, via, nil)
		if rerr == nil {
			m.resumeSucceeded(c, lostAt, try)
			return
		}
		if errors.Is(rerr, rpc.ErrResumeRefused) {
			c.peer.Close(fmt.Errorf("resume refused (%v) after: %w", rerr, err))
			m.fellBack(lostAt, oneLineErr(rerr))
			return
		}
		if better, _ := m.d.choosePath(m); better != nil && better.key != p.key {
			// Its path failed and the machine has a better one now (a network changed): the same resident node can take
			// the connection back over that path too. Only a failed resume there makes the run loop connect anew.
			oldErr := rerr
			rerr = m.tryResumePath(c, better, oldErr)
			if rerr == nil {
				m.resumeSucceeded(c, lostAt, try)
				return
			}
			c.peer.Close(fmt.Errorf("%s failed (%v); resume over %s failed (%v) after: %w",
				pathName(m.d, p), oneLineErr(oldErr), pathName(m.d, better), oneLineErr(rerr), err))
			m.fellBack(lostAt, "another path: "+pathName(m.d, better)+": "+oneLineErr(rerr))
			return
		}
		select {
		case <-time.After(min(wait, time.Until(deadline))):
		case <-c.peer.Done():
		case <-c.ctx.Done():
			return
		}
		if c.peer.Err() != nil {
			break
		}
		wait = min(wait*2, 3*time.Second)
	}
	select { // the peer closes itself as its window ends; the run loop connects anew
	case <-c.peer.Done():
		m.fellBack(lostAt, "not resumed in time")
	case <-c.ctx.Done():
	}
}

// resumeSucceeded counts and logs a connection taken back, naming the path it now uses.
func (m *Machine) resumeSucceeded(c *nodeConn, lostAt time.Time, try int) {
	took := time.Since(lostAt)
	m.mu.Lock()
	m.resumes.resumed++
	m.resumes.last, m.resumes.most = took, max(m.resumes.most, took)
	counts := m.resumes
	m.mu.Unlock()
	p, via := c.route()
	m.d.logf("[%s] connection over %s resumed after %s (try %d, path %s): %s %s", m.cfg.ID, via, took.Round(time.Millisecond),
		try, pathName(m.d, p), counts, c.peer.LaneStats())
	// A link that came back meanwhile (the one the connection was resumed over, often) has not heard the node is
	// online: only a new connection said so (serve). Not when it is lost again by then, or no longer the machine's.
	m.tellLinksIf(true, "", func() bool { return m.nodeConnected(c.peer) })
}

// tryResumePath tries a newly better path's transports in the same order establish would. If it moves the route,
// moveCause is the failed same-path try to record against the route left behind.
func (m *Machine) tryResumePath(c *nodeConn, p *nodePath, moveCause error) error {
	vias := []string{viaHub}
	if p.src != nil {
		vias = p.src.transports()
	}
	var err error
	for _, via := range vias {
		err = m.tryResume(c, p, via, moveCause)
		if err == nil || errors.Is(err, rpc.ErrResumeRefused) {
			return err
		}
		if c.ctx.Err() != nil || errors.Is(err, errSwitched) {
			return err
		}
		if via == "ssh" && p.src != nil && p.src.cfg.TailcatAddr != "" {
			p.src.noteDirectFailed()
		}
	}
	return err
}

// tryResume opens one transport on p by via and resumes c over it, within resumeTryWait: a dial that hangs (the old
// route gone quiet) gives up for the next try. The transport lasts as long as the connection. The recorded route moves
// only after the peer accepted the resume.
func (m *Machine) tryResume(c *nodeConn, p *nodePath, via string, moveCause error) error {
	tctx, cancel := context.WithTimeout(c.ctx, resumeTryWait)
	defer cancel()
	type opened struct {
		l   *nodePipe
		err error
	}
	ch := make(chan opened, 1)
	go func() {
		l, err := m.openPipe(c.ctx, p, via)
		ch <- opened{l, err}
	}()
	var o opened
	select {
	case o = <-ch:
	case <-tctx.Done():
		go func() { // let the late one go once it is there
			if o := <-ch; o.l != nil {
				o.l.close()
			}
		}()
		return tctx.Err()
	}
	if o.err != nil {
		return o.err
	}
	prev := c.pipeNow.Swap(o.l) // in use from the moment it may be: a loss right after the resume cuts this one
	if err := c.peer.Resume(tctx, o.l.r, o.l.w); err != nil {
		c.pipeNow.CompareAndSwap(o.l, prev)
		o.l.close()
		return err
	}
	if err := m.moveRoute(c, p, via, moveCause); err != nil {
		o.l.close()
		return err
	}
	return nil
}

// moveRoute records a successful resume's path and transport on the live connection and, when it is the machine's
// serving connection, on the machine's route snapshot. A generation ends stale path-switch and probe callbacks, and
// each route keeps its own start for failure and flap accounting.
func (m *Machine) moveRoute(c *nodeConn, p *nodePath, via string, moveCause error) error {
	c.routeMu.Lock()
	if c.switching {
		c.routeMu.Unlock()
		return errSwitched
	}
	if err := c.peer.Err(); err != nil {
		c.routeMu.Unlock()
		return err
	}
	if err := c.ctx.Err(); err != nil {
		c.routeMu.Unlock()
		return err
	}
	previousPath, previousVia, previousStarted := c.path, c.via, c.routeStarted
	if previousPath.key == p.key && previousVia == via {
		c.routeMu.Unlock()
		return nil
	}
	c.path, c.via = p, via
	c.routeRev++
	c.routeStarted = time.Now()
	rev := c.routeRev
	m.mu.Lock()
	active := m.peer == c.peer
	if active {
		m.active, m.transport, m.via, m.path = p, via, via, ""
		m.routeRev = rev
		if via == "ssh" && p.src == m {
			m.directFailed = time.Time{}
		}
	}
	m.mu.Unlock()
	c.routeMu.Unlock()
	if !active {
		return nil // serve will install this route and its path-specific watchers
	}
	if moveCause != nil && !previousStarted.IsZero() {
		m.d.pathEnded(previousPath, true, moveCause, time.Since(previousStarted), false)
	}
	m.d.pathConnected(p)
	if via == "ssh" && p.src != nil && p.src != m {
		p.src.mu.Lock()
		p.src.directFailed = time.Time{}
		p.src.mu.Unlock()
	}
	if via == "tailcat" && p.src == m && (previousVia != "tailcat" || previousPath.src != m) {
		go m.watchTailcat(c.ctx, c)
	}
	m.d.changed()
	return nil
}

// fellBack counts and logs a drop that was not resumed: the run loop connects anew.
func (m *Machine) fellBack(lostAt time.Time, why string) {
	m.mu.Lock()
	m.resumes.fellBack++
	counts := m.resumes
	m.mu.Unlock()
	m.d.logf("[%s] connection not resumed after %s (%s): connecting anew; %s", m.cfg.ID,
		time.Since(lostAt).Round(time.Millisecond), why, counts)
}

// resumeCounts is the counts for a line about this computer's connections: none until one could resume.
func (m *Machine) resumeCounts() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumes == (resumeCounts{}) {
		return ""
	}
	return " " + m.resumes.String()
}

// resumeOn is the "connected" line's word for whether the connection can resume (lanes=on only).
func resumeOn(p *rpc.Peer) string {
	if !p.Framed() {
		return ""
	}
	return " resume=" + onOff(p.Resumable())
}
