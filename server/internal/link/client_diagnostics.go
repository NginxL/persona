package link

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type diagnosticLogKey struct{}
type diagnosticTLSKey struct{}

var dialSequence atomic.Uint64

// diagnosticDial records only finite categories and monotonic durations. Trace callbacks
// deliberately discard addresses, DNS answers, certificates, headers and error text.
func diagnosticDial(ctx context.Context, url string, options *websocket.DialOptions, role, kind string, ordinal int) (*websocket.Conn, *http.Response, error) {
	logf, _ := ctx.Value(diagnosticLogKey{}).(func(string, ...any))
	if logf == nil {
		return websocket.Dial(ctx, url, options)
	}
	switch kind {
	case "lan", "ipv6", "ipv4", "name", KindSSH, KindProxy:
	default:
		kind = "other"
	}
	if role != "stream" {
		role = "control"
	}
	id, start := dialSequence.Add(1), time.Now()
	var mu sync.Mutex
	var dnsStart, tlsStart, upgradeStart time.Time
	tcpStarts := make(map[[2]string]time.Time)
	var dns, tcp, tlsTime, upgrade time.Duration
	var tlsDone bool
	phase := "dial"
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			mu.Lock()
			dnsStart = time.Now()
			if phase == "dial" {
				phase = "dns"
			}
			mu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) { mu.Lock(); dns += time.Since(dnsStart); mu.Unlock() },
		ConnectStart: func(network, addr string) {
			mu.Lock()
			tcpStarts[[2]string{network, addr}] = time.Now()
			if phase == "dial" || phase == "dns" || phase == "tcp" {
				phase = "tcp"
			}
			mu.Unlock()
		},
		ConnectDone: func(network, addr string, _ error) {
			mu.Lock()
			key := [2]string{network, addr}
			if start, ok := tcpStarts[key]; ok {
				tcp += time.Since(start)
				delete(tcpStarts, key)
			}
			mu.Unlock()
		},
		TLSHandshakeStart: func() {
			mu.Lock()
			// net/http may announce the already-complete custom handshake again.
			if tlsStart.IsZero() {
				tlsStart = time.Now()
				if phase != "upgrade" {
					phase = "tls"
				}
			}
			mu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			mu.Lock()
			if !tlsDone && !tlsStart.IsZero() {
				tlsTime = time.Since(tlsStart)
				tlsDone = true
			}
			mu.Unlock()
		},
		GotConn: func(httptrace.GotConnInfo) { mu.Lock(); upgradeStart = time.Now(); phase = "upgrade"; mu.Unlock() },
	}
	// WithClientTrace composes hooks in place. Keep the private custom-handshake
	// observer separate so it never invokes an existing caller's hooks twice.
	tlsTrace := &httptrace.ClientTrace{
		TLSHandshakeStart: trace.TLSHandshakeStart,
		TLSHandshakeDone:  trace.TLSHandshakeDone,
	}
	ctx = context.WithValue(ctx, diagnosticTLSKey{}, tlsTrace)
	conn, resp, err := websocket.Dial(httptrace.WithClientTrace(ctx, trace), url, options)
	mu.Lock()
	now := time.Now()
	// Include elapsed time for phases interrupted before their Done callback.
	if phase == "dns" && dns == 0 && !dnsStart.IsZero() {
		dns = now.Sub(dnsStart)
	}
	for _, start := range tcpStarts {
		tcp += now.Sub(start)
	}
	if phase == "tls" && tlsTime == 0 && !tlsStart.IsZero() {
		tlsTime = now.Sub(tlsStart)
	}
	if !upgradeStart.IsZero() {
		upgrade = now.Sub(upgradeStart)
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		var ne net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			outcome = "timeout"
		case errors.Is(err, context.Canceled):
			outcome = "canceled"
		case resp != nil:
			outcome = "http_refused"
		}
	}
	line := fmt.Sprintf("link: dial attempt=%d role=%s candidate=%d kind=%s phase=%s outcome=%s dns_us=%d tcp_us=%d tls_us=%d upgrade_us=%d total_us=%d", id, role, ordinal, kind, phase, outcome, dns.Microseconds(), tcp.Microseconds(), tlsTime.Microseconds(), upgrade.Microseconds(), now.Sub(start).Microseconds())
	mu.Unlock()
	logf("%s", line)
	return conn, resp, err
}

// Custom TLS dialers perform their real handshake before net/http's automatic
// callbacks. Trace that boundary too, including failures before returning a connection.
func tracedHandshake(ctx context.Context, c *tls.Conn) error {
	trace, _ := ctx.Value(diagnosticTLSKey{}).(*httptrace.ClientTrace)
	if trace != nil && trace.TLSHandshakeStart != nil {
		trace.TLSHandshakeStart()
	}
	err := c.HandshakeContext(ctx)
	if trace != nil && trace.TLSHandshakeDone != nil {
		trace.TLSHandshakeDone(c.ConnectionState(), err)
	}
	return err
}
