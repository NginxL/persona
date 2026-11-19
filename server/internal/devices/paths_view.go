package devices

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// pathInfos is a machine's paths for the admin page and the machine list.
func (d *Devices) pathInfos(m *Machine) []pathInfo {
	active := m.activePathKey()
	self := d.config().HubID
	var out []pathInfo
	for _, p := range d.pathsOf(m) {
		st := d.pathState(p)
		pi := pathInfo{Via: p.via, HubID: self, OK: st.avail && time.Now().After(st.failUntil), RTTMS: st.rtt,
			Active: p.key == active, Error: st.err}
		if p.via == viaHub {
			pi.HubID, pi.ID = p.hub, p.mid
		} else {
			pi.ID = p.src.cfg.ID
			if pi.Active {
				if t := m.transportNow(); t != "" {
					pi.Via = t // ssh that fell back to tailcat
				}
			}
		}
		if pi.Active {
			pi.OK, pi.Error = true, ""
		}
		out = append(out, pi)
	}
	return out
}

// measureNodes measures the round trip to every computer this hub drives, over the path in use. A connection that
// leaves pingMissLimit pings in a row unanswered is closed so the machine reconnects (#7510): a half-open link (an
// ssh proxy that still answers keepalives while the node behind it is gone) otherwise holds every call sent on it
// until its own wait runs out, a task's start for minutes.
func (d *Devices) measureNodes(ctx context.Context) {
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		p, peer, rev, online := m.active, m.peer, m.routeRev, m.online
		m.mu.Unlock()
		if p == nil || !online {
			continue
		}
		if peer == nil || peer.Lost() { // waiting to be resumed: no transport to ping (resume.go)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, pingWait)
		start := time.Now()
		// Probe the connection whose activity and misses this round attributes the result to. m.call could wait for
		// or select a replacement peer, mixing one connection's timeout with another's traffic.
		err := peer.Call(cctx, "ping", map[string]any{}, nil)
		cancel()
		m.logCall("ping", err, time.Since(start), 1)
		m.mu.Lock()
		sameRoute := m.peer == peer && m.active == p && m.routeRev == rev
		m.mu.Unlock()
		if err == nil && sameRoute { // a preserved pong may arrive after a resume moved the route
			d.pathRTT(p, max(1, int(time.Since(start).Milliseconds())))
		}
		if ctx.Err() == nil {
			m.pingAnswered(peer, err)
		}
	}
}

// pingInterval is how often measureLoop pings every computer; pingWait bounds one ping of measureNodes;
// pingMissLimit is how many in a row may go unanswered before the connection is closed. Vars so tests shorten them.
var (
	pingInterval       = 30 * time.Second
	pingWait           = 5 * time.Second
	pingMissLimit      = 2
	pingActivityWindow = 30 * time.Second // looked back over by a connection's first ping, and by any if it reaches further
)

// pingAnswered counts a ping sent on peer: one written that ran out of time without an answer is a miss, anything
// else (an answer, an error the node answered with) clears the count; a connection already gone (closed, offline), a
// ping that could not be written in time, and one during a resident node's handover count neither way. A ping whose
// writer consumed most of its response budget also counts neither way. Recent incoming activity that can explain a
// late pong clears a miss (rpc.Peer.PingActivity): a reply, late ones included, shows the node gets the hub's calls,
// and a large frame still arriving can hold the pong behind it. Requests and events the node starts itself do not
// (#8771): they kept coming for 15 minutes over a link whose hub-to-node direction had stalled, and every call on it
// waited out its whole timeout. pingMissLimit misses in a row on the same connection close it.
func (m *Machine) pingAnswered(peer *rpc.Peer, err error) {
	if peer == nil {
		return
	}
	now := time.Now()
	var re *rpc.Error
	gone := errors.Is(err, rpc.ErrClosed) || errors.Is(err, rpc.ErrSuspended) || errors.Is(err, errOffline) || errors.Is(err, context.Canceled)
	relayedMiss := errors.As(err, &re) && re.Code == "" && re.Message == context.DeadlineExceeded.Error()
	missed := errors.Is(err, context.DeadlineExceeded) || relayedMiss
	// Not a sign of a dead link by itself: a ping that never got on the wire (a long write ahead of it on a slow but
	// working link), or one made while a resident node hands over to its next process.
	if gone && !missed || errors.Is(err, rpc.ErrNotWritten) || m.expectedReload() {
		return
	}
	mostlyDelayed := false
	if errors.Is(err, rpc.ErrWriteDelayed) {
		// The writer consumed most of this probe's budget, leaving too little time for a pong. A brief queue followed
		// by a substantial but silent response window is still a miss, so repeated short delays cannot hide a dead link.
		if remaining, ok := rpc.WriteDelayRemaining(err); ok {
			mostlyDelayed = remaining < pingWait/2
		}
	}
	if missed {
		// rpc.Peer excludes a deadline error response itself, including one that arrives late, so this cannot be kept
		// alive by a relay answering its own timeout, nor by the node's own requests and events. Activity counts since
		// the previous ping on this connection was judged, or pingActivityWindow back if that reaches further, so the
		// previous ping's late pong always counts however far apart the two pings fall: pings go out one computer after
		// another, and slow ones ahead of this one can stretch the time between its pings past pingInterval (#10442).
		activity := peer.PingActivity()
		m.mu.Lock()
		since := now.Add(-pingActivityWindow)
		if m.pingPeer == peer && !m.pingJudged.IsZero() && m.pingJudged.Before(since) {
			since = m.pingJudged
		}
		m.mu.Unlock()
		if !activity.IsZero() && activity.After(since) {
			missed = false
		} else if mostlyDelayed {
			return
		}
	}
	m.mu.Lock()
	if m.pingPeer != peer {
		m.pingPeer, m.pingMisses = peer, 0
	}
	m.pingJudged = now
	if missed {
		m.pingMisses++
	} else {
		m.pingMisses = 0
	}
	misses, current := m.pingMisses, m.peer == peer
	if misses >= pingMissLimit {
		m.pingMisses = 0
	}
	m.mu.Unlock()
	if misses < pingMissLimit || !current {
		return
	}
	last := "nothing received yet"
	if at := peer.PingActivity(); !at.IsZero() {
		last = "last reply or large frame " + now.Sub(at).Round(time.Second).String() + " ago"
	}
	why := fmt.Errorf("no answer to %d pings in a row (a half-open link?)", misses)
	if peer.Lose(why) {
		// The node keeps the connection: only the transport is dead, and a new one takes it back (resume.go).
		m.d.logf("[%s] no answer to %d pings in a row over %s (%s, %s): resuming on a new transport", m.cfg.ID, misses, m.transportNow(), last, peer.LaneStats())
		return
	}
	m.d.logf("[%s] no answer to %d pings in a row over %s (%s, %s): closing the connection to reconnect", m.cfg.ID, misses, m.transportNow(), last, peer.LaneStats())
	peer.Close(why)
	m.wake()
}

// decorate fills in what the apps see of how a machine is reached: the hub it is reached through, its own id, the
// route, its paths.
func (d *Devices) decorate(m *Machine, s proto.Machine) proto.Machine {
	cfg := d.config()
	s.HubID, s.HubName = cfg.HubID, cfg.HubName
	f := &d.fleet
	f.mu.Lock()
	for _, p := range f.paths[m] {
		if uid := f.uids[p.key]; uid != "" {
			s.MachineUID = uid
			break
		}
	}
	f.mu.Unlock()
	if uid := m.nodeUID(); uid != "" {
		s.MachineUID = uid
	}
	infos := d.pathInfos(m)
	route := &proto.MachineRoute{Via: kindOf(m.cfg)}
	if m.derived {
		route.Via = viaHub
	}
	for i, pi := range infos {
		if pi.Active || i == 0 && !s.Online {
			route = &proto.MachineRoute{Via: pi.Via}
			if pi.Via == viaHub {
				route.Hops = 1
				s.HubID, s.HubName = pi.HubID, d.hubNameOf(pi.HubID)
			}
			if s.Online && pi.RTTMS > 0 {
				rtt := pi.RTTMS
				route.RTTMS = &rtt
			}
			if pi.Active {
				break
			}
		}
	}
	s.Route = route
	if len(infos) > 1 || m.derived {
		for _, pi := range infos {
			s.Paths = append(s.Paths, proto.MachinePath{Via: pi.Via, HubID: pi.HubID, OK: pi.OK, RTTMS: pi.RTTMS, Active: pi.Active})
		}
	}
	return s
}

// measureLoop measures the round trip to every computer every pingInterval (the choice between paths uses it).
func (d *Devices) measureLoop(ctx context.Context) {
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		d.measureNodes(ctx)
	}
}
