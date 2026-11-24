package hub

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The relay streams to each manager (relayTo, extensions_link.go). Opening one costs several round trips between the
// hub and the manager: extensions.accept on the bus, the manager's WebSocket to /api/v1/bus/relay (a connection or a
// tunnel channel, then the upgrade) and its answer, before the request itself goes out: about a second per request
// over a long link (#9202). So each manager bus connection has an http.Transport that keeps its streams open between
// requests (HTTP keep-alive; the manager's side serves any number of requests on one, extmgr Accept), and a request
// on a warm stream costs one round trip.
//
// A stream carries one request at a time and goes back to the pool once its answer was read whole. One whose request
// was cancelled (its credential revoked: trackRelayed, or the client gone) or that became a WebSocket is closed, never
// pooled (net/http). Who asked goes in each request's own headers and the manager keeps nothing per stream, so
// nothing of a request outlives it on its stream. A manager's new bus connection (it reconnected, or another process
// took over) gets a new transport; the old one's idle streams are closed then and when that connection ends
// (retireRelays), as at the hub's shutdown.

const (
	relayIdleMost = 4                // idle streams kept per manager
	relayIdleFor  = 90 * time.Second // how long an idle stream is kept
	// An idle stream is pinged this often, and closed when no pong comes back in time: one that died quietly (a
	// tunnel that stalled, a NAT that forgot it) is not sent the next request.
	relayPingEvery   = 15 * time.Second
	relayPingTimeout = 10 * time.Second
)

// relayPools are the managers' relay transports, by manager id.
type relayPools struct {
	mu   sync.Mutex
	by   map[string]*relayPool
	ping [2]time.Duration // tests: relayPingEvery and relayPingTimeout
	// rounds, when a test sets it, hears each round of pingRelay: whether the stream was idle, whether it was pinged,
	// and the ping's error (the stream is closed by then).
	rounds func(idle, pinged bool, err error)
	// probed, when a test sets it, hears each pre-write liveness probe in RoundTrip.
	probed func(reused bool, err error)
}

// relayPool is the relay streams of one manager bus connection (bus; nil: none, a pool of one request).
type relayPool struct {
	ps      *relayPools
	bus     *busConn
	tr      *http.Transport
	retired atomic.Bool // no longer its manager's current: it keeps no stream idle
}

// relayPool is the relay streams of manager id's current bus connection. The pool of a connection that is no longer
// its current one is retired here, if its ending has not done it yet.
func (h *Hub) relayPool(id string) *relayPool {
	c := h.busConnOf(id)
	ps := &h.ext.relays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := ps.by[id]
	if p != nil && p.bus == c {
		return p
	}
	if p != nil {
		delete(ps.by, id)
		p.retire()
	}
	p = &relayPool{ps: ps, bus: c, tr: &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return h.dialManager(ctx, id) },
		MaxIdleConns:          relayIdleMost,
		MaxIdleConnsPerHost:   relayIdleMost,
		IdleConnTimeout:       relayIdleFor,
		ResponseHeaderTimeout: 5 * time.Minute,
	}}
	if c == nil { // off the bus since relayTo looked: the dial says so, and nothing is kept
		p.retired.Store(true)
		return p
	}
	if ps.by == nil {
		ps.by = map[string]*relayPool{}
	}
	ps.by[id] = p
	return p
}

// retireRelays closes the idle relay streams of manager id's bus connection c (it ended); those still answering are
// closed as they finish.
func (h *Hub) retireRelays(id string, c *busConn) {
	ps := &h.ext.relays
	ps.mu.Lock()
	p := ps.by[id]
	if p == nil || p.bus != c {
		ps.mu.Unlock()
		return
	}
	delete(ps.by, id)
	ps.mu.Unlock()
	p.retire()
}

// retire keeps no more streams idle in p. Closing a stream waits for the WebSocket's close handshake, so not here.
func (p *relayPool) retire() {
	p.retired.Store(true)
	go p.tr.CloseIdleConnections()
}

// settle is a request's end: a stream it put back in a pool retired meanwhile is closed.
func (p *relayPool) settle() {
	if p.retired.Load() {
		go p.tr.CloseIdleConnections()
	}
}

const relayRetryBodyLimit = 1 << 20 // 1 MiB: extension UI and API payloads are small JSON; larger bodies stream once

func isIdempotentRelayRequest(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return req.Header.Get("Idempotency-Key") != "" || req.Header.Get("X-Idempotency-Key") != ""
}

// cloneRelayRequest clones req and buffers up to relayRetryBodyLimit (1 MiB) so Go's net/http Transport can safely
// replay a non-idempotent request on nothingWrittenError (after a failed pre-write probe) without mutating the
// caller's *http.Request fields. Bodies larger than relayRetryBodyLimit stream unbuffered without GetBody.
func cloneRelayRequest(req *http.Request) (*http.Request, error) {
	base := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return base, nil
	}
	if req.Header.Get("Upgrade") != "" || req.ContentLength > relayRetryBodyLimit {
		return base, nil
	}
	orig := req.Body
	b, err := io.ReadAll(io.LimitReader(orig, relayRetryBodyLimit+1))
	if err != nil {
		_ = orig.Close()
		return nil, err
	}
	if len(b) > relayRetryBodyLimit {
		base.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(b), orig), orig}
		return base, nil
	}
	_ = orig.Close()
	base.Body = io.NopCloser(bytes.NewReader(b))
	base.ContentLength = int64(len(b))
	base.TransferEncoding = nil
	base.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return base, nil
}

func (p *relayPool) probeTimeout() time.Duration {
	if p.ps != nil {
		_, timeout := p.ps.pingTimes()
		return timeout
	}
	return relayPingTimeout
}

// RoundTrip sends req on one of p's streams, noting when the stream goes back to the pool idle (pingRelay).
// For non-idempotent requests (e.g. POST without an idempotency key) reusing an idle stream, GotConn probes the
// stream with a WebSocket ping (1 ping RTT) before writeLoop writes any request bytes: if the probe fails, the dead
// stream is closed immediately so writeLoop fails with nothingWrittenError, and net/http automatically replays the
// request on a fresh stream via GetBody. Requests that pass the probe or already wrote bytes are never retried.
func (p *relayPool) RoundTrip(req *http.Request) (*http.Response, error) {
	idempotent := isIdempotentRelayRequest(req)
	outReq := req
	if !idempotent {
		var err error
		outReq, err = cloneRelayRequest(req)
		if err != nil {
			return nil, err
		}
	}

	var rc *relayConn
	var use uint64
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			c, ok := info.Conn.(*relayConn)
			if !ok {
				return
			}
			rc, use = c, c.uses.Add(1)
			if !idempotent && info.Reused && c.ws != nil {
				pctx, cancel := context.WithTimeout(outReq.Context(), p.probeTimeout())
				perr := c.ws.Ping(pctx)
				cancel()
				if perr != nil {
					_ = c.ws.CloseNow()
					_ = c.Close()
				}
				if p.ps != nil && p.ps.probed != nil {
					p.ps.probed(info.Reused, perr)
				}
			}
		},
		// Called after the stream is back in the pool, where the next request may already have taken it: the
		// count says whether one did.
		PutIdleConn: func(err error) {
			if err == nil && rc != nil {
				rc.idleAt.Store(use)
			}
		},
	}
	return p.tr.RoundTrip(outReq.WithContext(httptrace.WithClientTrace(outReq.Context(), trace)))
}

// pingTimes are relayPingEvery and relayPingTimeout, or what a test set.
func (ps *relayPools) pingTimes() (every, timeout time.Duration) {
	return durationOr(ps.ping[0], relayPingEvery), durationOr(ps.ping[1], relayPingTimeout)
}

// pingRelay pings rc's stream ws while it waits in the pool, and closes it when no pong comes back in time, even if
// a request took it meanwhile (such a request would wait for an answer that is not coming; one that has not been
// written yet fails to write, and net/http sends it again on another stream). A stream with a request on it is not
// pinged: the pong may wait behind an answer the client reads slowly.
func (h *Hub) pingRelay(ctx context.Context, ws *websocket.Conn, rc *relayConn) {
	every, timeout := h.ext.relays.pingTimes()
	rounds := h.ext.relays.rounds
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		idle, pinged := rc.idle(), false
		var err error
		if idle {
			pinged = true
			pctx, cancel := context.WithTimeout(ctx, timeout)
			err = ws.Ping(pctx)
			cancel()
		}
		if err != nil && ctx.Err() == nil {
			_ = ws.CloseNow() // no close handshake: a side that sends no pong sends no close either
			_ = rc.Close()
		}
		if rounds != nil {
			rounds(idle, pinged, err)
		}
		if err != nil {
			return
		}
	}
}
