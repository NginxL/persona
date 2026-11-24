package link

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// link is one live link to the hub.
type link struct {
	conn    *websocket.Conn
	ep      Endpoint
	baseURL string       // https base URL for streams (ep.URL when direct; tunnelBaseURL when over SSH)
	tunnel  *sshTunnel   // non-nil when this link runs over an SSH tunnel
	proxy   string       // the outbound proxy it runs through, without credentials (Status.Proxy); "" when none
	local   netip.Addr   // this side's address of the link's TCP connection
	streams *http.Client // checked the same way (pin or system CAs); streams go where the link went
	token   string
	epsMu   sync.Mutex
	eps     []Endpoint
	// named: the hub said what it calls this computer (endpoints), so it renames computers (rename.go).
	named atomic.Bool
	// gen counts this run's links, this one included (Status.Conn): the app's confirmations say which link's they are.
	gen uint64
	// again: link.json turned out to hold another pairing while writing to it (errPairedAgain); serve ends the link.
	again chan struct{}
}

// directHandoff transfers ownership of an accepted direct candidate and the reloaded configuration it was validated
// against. serve returns one only after installing errDirectReady as its context's cause.
type directHandoff struct {
	candidate *link
	config    *Config
}

type directProbeResult struct {
	candidate *link
	err       error
}

func (l *link) closeNow() {
	l.conn.CloseNow()
	l.tunnel.close()
}

func (l *link) tunnelDone() <-chan struct{} {
	if l.tunnel == nil {
		return nil
	}
	return l.tunnel.done
}

func (l *link) setEndpoints(eps []Endpoint) {
	l.epsMu.Lock()
	l.eps = slices.Clone(eps)
	l.epsMu.Unlock()
}

func (l *link) getEndpoints() []Endpoint {
	l.epsMu.Lock()
	defer l.epsMu.Unlock()
	return slices.Clone(l.eps)
}

func dial(ctx context.Context, c *Config) (*link, error) {
	return dialVia(ctx, c, ranked(c.Endpoints), nil)
}

// dialVia races eps for the link: straight to the hub, or through outbound proxy p when it is set (proxied); every
// stream later opened on the link takes the same route. eps is already in its intended race order.
func dialVia(ctx context.Context, c *Config, eps []Endpoint, p *url.URL) (*link, error) {
	candidates := ranked(eps)
	l, ep, err := race(ctx, candidates, func(ctx context.Context, ep Endpoint) (*link, error) {
		var local netip.Addr
		hc := hubClient(c, p, func(raw net.Conn) {
			if a, err := netip.ParseAddrPort(raw.LocalAddr().String()); err == nil {
				local = a.Addr().Unmap()
			}
		})
		conn, resp, err := diagnosticDial(ctx, wsURL(ep.URL, Path), &websocket.DialOptions{HTTPClient: hc, HTTPHeader: bearer(c.Token)}, "control", ep.Kind, slices.Index(candidates, ep)+1)
		if err != nil {
			return nil, handshakeError(resp, err)
		}
		conn.SetReadLimit(1 << 20)
		return &link{conn: conn, local: local, streams: hubClient(c, p, nil), token: c.Token}, nil
	}, func(l *link) { l.closeNow() })
	if err != nil {
		return nil, err
	}
	l.ep = ep
	l.baseURL = ep.URL
	if p != nil {
		l.proxy = RedactedProxy(p.String())
	}
	return l, nil
}

var streamID = lazyre.New(`^[A-Za-z0-9_-]{1,64}$`)

// serve runs the link until it ends and says why. A direct handoff is already connected and owned by the caller;
// every other return leaves no direct candidate behind.
func (l *link) serve(ctx context.Context, c *Config, o Options, rep *reporter, memory *routeMemory) (*directHandoff, error) {
	l.again = make(chan struct{}, 1)
	l.setEndpoints(c.Endpoints)
	ctx, cancel := context.WithCancelCause(ctx)
	directReady := o.directReady
	if directReady == nil {
		directReady = make(chan *directHandoff, 1)
	}
	var heartbeatDone chan struct{}
	defer func() {
		// Stop and join the publisher before releasing ownership. A read failure may race with the winning probe; if that
		// probe publishes after the read loop has chosen a nil handoff, reclaim it here rather than orphaning its socket.
		cancel(nil)
		if heartbeatDone != nil {
			<-heartbeatDone
		}
		select {
		case handoff := <-directReady:
			handoff.candidate.closeNow()
		default:
		}
	}()
	defer l.tunnel.close()
	defer l.conn.CloseNow()
	defer l.streams.CloseIdleConnections()
	helloAddr := l.ep.URL
	if l.ep.Kind == KindSSH {
		helloAddr = ""
	}
	hello := Message{Type: "hello", Machine: c.Machine, Version: o.Version, Address: helloAddr, Features: []string{FeatureAttention}}
	if err := l.send(ctx, hello); err != nil {
		return nil, err
	}
	since := time.Now()
	rep.set(func(s *Status) {
		*s = Status{State: "connected", Conn: l.gen, Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Address: l.ep.URL,
			Kind: l.ep.Kind, Proxy: l.proxy, Since: &since, Node: "unknown", Trust: trustOf(c), Endpoints: ranked(c.Endpoints), SSH: cloneSSH(c.SSH)}
	})
	if memory != nil {
		memory.connected(l.ep.Kind)
	}
	go l.renames(ctx, o, rep)
	go l.presence(ctx, o)
	go l.confirms(ctx, o)
	o.Logf("link: connected to %s (%s)", l.ep.URL, l.ep.Kind) // a proxy is only "proxy" here: the log never names it
	drop := func(err error) {
		cancel(err)
		l.conn.CloseNow()
	}
	// reconnect ends the link the way the hub expects for one that comes straight back (runActive dials at once).
	reconnect := func(err error) {
		cancel(err)
		_ = l.conn.Close(websocket.StatusNormalClosure, "reconnect")
	}
	// The heartbeat is o's until the hub says another (endpoints); beat hands the pinger the latest, the link stays.
	beat := make(chan heartbeat, 1)
	heartbeatDone = make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		hb := heartbeat{every: o.PingEvery, timeout: o.PingTimeout}
		t := time.NewTicker(hb.every)
		defer t.Stop()
		// A new pairing (persona-link pair, the tray's or the App's) is followed while the link is up, not only after
		// it drops. The zero stamp makes the first look compare: a pairing written while this link was dialing counts.
		pairT := time.NewTicker(cmp.Or(o.PairCheck, pairCheck))
		defer pairT.Stop()
		// On a fallback route (a proxy, or SSH it need not use), direct candidates are probed now and then to switch
		// back, and a new proxy setting is followed at once. Proxy-only mode keeps a proxy link off direct: it is no
		// fallback, but still follows the proxy setting.
		fallback := (l.ep.Kind == KindProxy && !c.ProxyAlways) || (l.ep.Kind == KindSSH && c.SSH != nil && !c.SSH.Always)
		followProxy := fallback || l.ep.Kind == KindProxy
		var directC <-chan time.Time
		if fallback {
			directT := time.NewTicker(cmp.Or(o.DirectRetryEvery, DirectRetryEvery))
			defer directT.Stop()
			directC = directT.C
		}
		var seen fileStamp
		var looked bool
		// A ping waits for its pong on its own, so a reconnect or a network change is not held up behind one that is
		// out (with a slow link's heartbeat, for up to the hub's timeout). One of each kind at a time; the first that
		// fails drops the link.
		type pong struct {
			check bool
			err   error
		}
		pongs := make(chan pong)
		directProbe := make(chan directProbeResult, 1)
		var beating, checking, probingDirect bool
		var directProbeDone chan struct{}
		defer func() {
			// The buffered send always completes, including when this loop has already returned. Join the dial and close
			// a candidate that was too late to be handed off before serve releases its own link.
			if probingDirect {
				result := <-directProbe
				<-directProbeDone
				if result.candidate != nil {
					result.candidate.closeNow()
				}
			}
		}()
		ping := func(check bool, d time.Duration) {
			go func() {
				err := l.ping(ctx, d)
				select {
				case pongs <- pong{check, err}:
				case <-ctx.Done():
				}
			}()
		}
		tryDirect := func() {
			eps := l.getEndpoints()
			if len(eps) == 0 {
				return
			}
			probingDirect = true
			directProbeDone = make(chan struct{})
			probeDone := directProbeDone
			probeCfg := Config{FP: c.FP, Trust: c.Trust, Token: c.Token, Endpoints: eps}
			go func() {
				defer close(probeDone)
				candidate, err := dial(ctx, &probeCfg)
				directProbe <- directProbeResult{candidate: candidate, err: err}
			}()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.tunnelDone():
				if ctx.Err() == nil {
					drop(l.tunnel.classifyExit(l.tunnel.waitErr))
				}
				return
			case result := <-directProbe:
				<-directProbeDone
				directProbeDone = nil
				probingDirect = false
				if result.err != nil || ctx.Err() != nil {
					if result.candidate != nil {
						result.candidate.closeNow()
					}
					continue
				}
				candidate := result.candidate
				if candidate == nil {
					continue
				}
				next := directHandoffConfig(o.Path, c, candidate.ep)
				if next == nil {
					candidate.closeNow()
					reconnect(errRepaired)
					return
				}
				o.Logf("link: direct hub reachable again; switching off %s", l.ep.Kind)
				cancel(errDirectReady)
				if !errors.Is(context.Cause(ctx), errDirectReady) {
					// Cancellation won while the configuration was checked or the handoff was logged. The candidate was
					// never adopted, so it is still this probe's to close.
					candidate.closeNow()
					return
				}
				directReady <- &directHandoff{candidate: candidate, config: next}
				_ = l.conn.Close(websocket.StatusNormalClosure, "reconnect")
				return
			case <-directC:
				if !probingDirect {
					tryDirect()
				}
			case p := <-pongs:
				why := "link-errors.heartbeat_timeout"
				if p.check {
					checking, why = false, "link-errors.heartbeat_after_network_change"
				} else {
					beating = false
				}
				if p.err != nil && ctx.Err() == nil {
					drop(newTextError(why, p.err))
					return
				}
			case b := <-beat:
				if b == hb {
					continue
				}
				if b.every != hb.every {
					t.Reset(b.every)
				}
				hb = b
				o.Logf("link: heartbeat every %s, %s for a pong", hb.every, hb.timeout)
			case <-t.C:
				if !beating {
					beating = true
					ping(false, hb.timeout)
				}
			case <-o.Check:
				if memory != nil {
					memory.networkChanged()
				}
				if l.localGone() {
					drop(newTextError("link-errors.network_address_gone", nil))
					return
				}
				if !checking {
					checking = true
					ping(true, hb.check())
				}
				if directC != nil && !probingDirect {
					tryDirect()
				}
			case <-o.Redial:
				reconnect(errRedial)
				return
			case <-pairT.C:
				// Only the mtime says when to look: endpoints and renames rewrite link.json with the same pairing.
				if now := stampOf(o.Path); !looked || !now.Equal(seen) {
					looked, seen = true, now
					if repaired(o.Path, c, followProxy) {
						reconnect(errRepaired)
						return
					}
				}
			case <-l.again:
				reconnect(errRepaired)
				return
			}
		}
	}()
	for {
		_, data, err := l.conn.Read(ctx)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				if errors.Is(cause, errDirectReady) {
					// The heartbeat installs the cause before publishing the candidate, so receiving here cannot redial
					// or mistake an earlier cancellation for a successful transfer.
					return <-directReady, cause
				}
				return nil, cause
			}
			return nil, err
		}
		var m Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "endpoints":
			l.endpoints(m, c, o, rep)
			select { // only the latest counts
			case <-beat:
			default:
			}
			beat <- o.heartbeatOf(m)
		case "renamed":
			l.renamed(m, c, o, rep)
		case "attention":
			if m.Attention != nil { // the status is reset on every connect: a summary never outlives its link
				rep.set(func(s *Status) { s.Attention = m.Attention })
			}
		case "node":
			rep.set(func(s *Status) {
				s.Node, s.NodeError = "offline", m.Error
				if m.Online {
					s.Node, s.NodeError = "online", ""
				}
			})
		case "open":
			if !streamID.MatchString(m.ID) {
				continue
			}
			go func(id string) {
				if err := l.forward(ctx, id, o.Target); err != nil {
					if ctx.Err() != nil { // called off because the link ended: that says nothing about the node
						return
					}
					o.Logf("link: stream %s: %s", id, ErrorText(i18n.En, err))
					_ = l.send(ctx, Message{Type: "open_failed", ID: id, Error: err.Error()})
					rep.set(func(s *Status) { s.Node, s.NodeError = "offline", err.Error() })
				}
			}(m.ID)
		}
	}
}

// directHandoffConfig reloads link.json before an accepted candidate may replace SSH. The dial used the old link's
// current endpoints, credential and TLS checks; a change to any identity or tunnel setting, or to this exact endpoint,
// makes the candidate stale and returns the run to normal routing instead.
func directHandoffConfig(path string, current *Config, ep Endpoint) *Config {
	next, err := Load(path)
	if err != nil || next.Hub != current.Hub || next.Machine != current.Machine || next.FP != current.FP ||
		next.Trust != current.Trust || next.Token != current.Token || !next.SSH.Equal(current.SSH) || next.ProxyAlways ||
		!slices.Contains(next.Endpoints, ep) {
		return nil
	}
	return next
}

// endpoints takes in what the hub says about where it is and who it is: link.json keeps the candidates (the ones added
// here stay), the hub's name and what it calls this computer; the mesh size is only shown while connected.
func (l *link) endpoints(m Message, c *Config, o Options, rep *reporter) {
	name, machine := clipName(m.HubName), clipName(m.MachineName)
	if machine != "" {
		l.named.Store(true)
	}
	if len(m.Endpoints) > 0 || (name != "" && name != c.HubName) || (machine != "" && machine != c.MachineName) {
		var eps []Endpoint
		err := Update(o.Path, func(f *Config) error {
			if f.Token != c.Token {
				return errPairedAgain
			}
			if name != "" {
				f.HubName = name
			}
			if machine != "" {
				f.MachineName = machine
			}
			if len(m.Endpoints) > 0 {
				var manual []Endpoint
				for _, e := range f.Endpoints {
					if e.Manual {
						manual = append(manual, e)
					}
				}
				hub := make([]Endpoint, 0, len(m.Endpoints))
				for _, e := range m.Endpoints {
					if strings.HasPrefix(e.URL, "https://") {
						hub = append(hub, Endpoint{URL: e.URL, Kind: e.Kind})
					}
				}
				f.Endpoints = append(manual, hub...)
			}
			eps = f.Endpoints
			return nil
		})
		if err != nil {
			if errors.Is(err, errPairedAgain) {
				l.pairedAgain()
			}
			return
		}
		c.Endpoints = eps
		l.setEndpoints(eps)
		if name != "" {
			c.HubName = name
		}
		if machine != "" {
			c.MachineName = machine
		}
	}
	rep.set(func(s *Status) {
		s.Endpoints = ranked(c.Endpoints)
		s.HubName = c.HubName
		s.MachineName = c.MachineName
		if m.MeshHubs > 0 {
			s.MeshHubs = min(m.MeshHubs, 999)
		}
		s.Pending = m.Pending
	})
}

// pairedAgain tells serve that link.json holds another pairing than this link's: the link moves to it.
func (l *link) pairedAgain() {
	select {
	case l.again <- struct{}{}:
	default:
	}
}

// repaired: link.json now holds another credential, computer, hub, SSH tunnel or proxy-only setting than c, or
// another proxy setting for a link that follows it (on the proxy, or on SSH it need not use, as the proxy comes
// before SSH), or was removed (unpaired). A file that cannot be read for another reason changes nothing: the link
// stays as it is.
func repaired(path string, c *Config, followProxy bool) bool {
	f, err := Read(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return f.Token != c.Token || f.FP != c.FP || f.Trust != c.Trust || f.Machine != c.Machine || !f.SSH.Equal(c.SSH) ||
		f.ProxyAlways != c.ProxyAlways || (followProxy && f.Proxy != c.Proxy)
}

// heartbeat is how a link is pinged: every, and how long a pong may take; hub: the hub said so (endpoints).
type heartbeat struct {
	every, timeout time.Duration
	hub            bool
}

// heartbeatOf is the ping interval and timeout the hub asks for in m (endpoints: hub.json link), brought into range;
// o's when it asks for none or for nothing usable (older hubs, hubs that leave it at the default).
func (o Options) heartbeatOf(m Message) heartbeat {
	if every, timeout, ok := PingTimesMS(int64(m.PingMS), int64(m.PingTimeoutMS)); ok {
		return heartbeat{every, timeout, true}
	}
	return heartbeat{o.PingEvery, o.PingTimeout, false}
}

// check is how long the ping after a network change waits: checkPing, to notice a dead network quickly, but not less
// than the hub's timeout when it set one (on its slow link a pong may take that long, network change or not).
func (h heartbeat) check() time.Duration {
	if h.hub {
		return max(checkPing, h.timeout)
	}
	return checkPing
}

// clipName keeps a hub's name to one short line of printable text: it is shown in a menu and a terminal.
func clipName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

// presence tells the hub the app's presence (presence.go): the latest as the link comes up, then again for every line
// the app writes. It never repeats one by itself: the app's own repeats keep it alive, so an app that went quiet lapses.
func (l *link) presence(ctx context.Context, o Options) {
	if o.Presence == nil {
		return
	}
	for {
		if p, ok := o.Presence.Latest(time.Now()); ok {
			m := Message{Type: "presence", Active: p.Active, Notifies: p.Notifies, TTLMS: p.TTLMS}
			if err := l.send(ctx, m); err != nil && ctx.Err() == nil {
				o.Logf("link: presence: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-o.Presence.Poked():
		}
	}
}

// confirms passes on the app's confirmations for this link (presence.go). Another link's go: an earlier one's still in
// the pipe; a later one's cannot come before this link is gone (the app learns its number from the status of a link
// that replaced this one).
func (l *link) confirms(ctx context.Context, o Options) {
	if o.Confirms == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-o.Confirms.C():
			if c.Conn != l.gen {
				continue
			}
			if err := l.send(ctx, Message{Type: "rang", InteractionID: c.Card}); err != nil && ctx.Err() == nil {
				o.Logf("link: rang: %v", err)
			}
		}
	}
}

func (l *link) send(ctx context.Context, m Message) error {
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	return l.conn.Write(ctx, websocket.MessageText, b)
}

func (l *link) ping(ctx context.Context, d time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return l.conn.Ping(ctx)
}

// localGone: the address the link's TCP connection left from is no longer on this computer (another network).
func (l *link) localGone() bool {
	if !l.local.IsValid() {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if interfaceIP(a) == l.local.WithZone("") {
			return false
		}
	}
	return true
}

// forward joins stream id to the resident node: nothing else, and nowhere else (docs/operations/link.md "Forwarding"). It returns an
// error only when the stream could not be opened; once bytes flow, its end is the node's or the hub's business.
func (l *link) forward(ctx context.Context, id, target string) error {
	node, err := net.DialTimeout("unix", target, 2*time.Second)
	if err != nil {
		return nodeMissing(runtime.GOOS, target)
	}
	defer node.Close()
	dctx, dcancel := context.WithTimeout(ctx, dialTimeout) // the handshake only: the stream lives as long as the link
	defer dcancel()
	ws, resp, err := diagnosticDial(dctx, wsURL(cmp.Or(l.baseURL, l.ep.URL), StreamPath+"?id="+id), &websocket.DialOptions{
		HTTPClient: l.streams, HTTPHeader: bearer(l.token), CompressionMode: StreamCompression}, "stream", l.ep.Kind, 1)
	if err != nil {
		return newTextError("link-errors.stream_open_failed", handshakeError(resp, err))
	}
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer nc.Close()
	// hub -> node: when the hub ends the stream, half-close so the node sees EOF and finishes. node -> hub decides
	// when the stream is over, as node.Relay does: everything the node wrote must reach the hub.
	go func() {
		_, _ = io.Copy(node, nc)
		if u, ok := node.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
	}()
	_, _ = io.Copy(nc, node)
	return nil
}
