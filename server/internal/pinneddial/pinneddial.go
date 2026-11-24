// Package pinneddial dials addresses a caller has already checked: the numeric addresses a host name resolved to, raced
// a little apart (Pinned), straight or through the operator's proxy with a CONNECT to the checked address (ThroughProxy).
// A caller that resolves a name once, refuses it unless every answer passes its own policy, and then dials only those
// answers cannot be moved to another address by a second DNS answer (rebinding): the proxy, too, sees the number, never
// a name it could resolve again. Used by the repository check (assistant/repository) and the remote image route
// (remoteimage).
package pinneddial

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// ContextDial is a dial function that is also a proxy.Dialer and proxy.ContextDialer (for a SOCKS5 proxy).
type ContextDial func(context.Context, string, string) (net.Conn, error)

func (d ContextDial) Dial(network, address string) (net.Conn, error) {
	return d(context.Background(), network, address)
}

func (d ContextDial) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

// Pinned dials port at one of ips, every one already past the caller's destination policy. Bounded numeric dials are
// staggered (a new one every 250 ms, at most 4 at once) so a black-holed address cannot use up the caller's time; the
// first that connects wins and the others are closed. There is no second DNS lookup, request retry or change to TLS
// verification.
func Pinned(parent context.Context, network, port string, ips []net.IPAddr, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("no address to dial")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	next, active := 0, 0
	start := func() {
		target := net.JoinHostPort(ips[next].String(), port)
		next++
		active++
		go func() {
			conn, err := dial(ctx, network, target)
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					_ = conn.Close()
				}
			}
		}()
	}
	start()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case r := <-results:
			active--
			if r.err == nil && r.conn != nil {
				return r.conn, nil
			}
			if r.conn != nil {
				_ = r.conn.Close()
			}
			err := r.err
			if err == nil {
				err = errors.New("dial returned nil connection")
			}
			if next == len(ips) && active == 0 {
				return nil, err
			}
			if next < len(ips) {
				start()
			}
		case <-ticker.C:
			if next < len(ips) && active < 4 {
				start()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ThroughProxy reaches target (a checked numeric host:port) through upstream, or straight with dial when upstream is
// nil. The proxy is operator-controlled and may itself be on loopback: only the destination is subject to the caller's
// policy, and the proxy receives its checked numeric address, never a host name it could resolve differently. HTTP and
// HTTPS proxies get a CONNECT (3 s to answer), SOCKS5 ones a SOCKS connect.
func ThroughProxy(ctx context.Context, upstream *url.URL, network, target string, dial ContextDial) (net.Conn, error) {
	if upstream == nil {
		return dial(ctx, network, target)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	scheme := strings.ToLower(upstream.Scheme)
	if scheme == "" {
		scheme = "http"
	}
	port := upstream.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		}
	}
	if upstream.Hostname() == "" || port == "" {
		return nil, errors.New("invalid proxy")
	}
	address := net.JoinHostPort(upstream.Hostname(), port)
	if scheme == "socks5" || scheme == "socks5h" {
		var auth *proxy.Auth
		if upstream.User != nil {
			password, _ := upstream.User.Password()
			auth = &proxy.Auth{User: upstream.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", address, auth, dial)
		if err != nil {
			return nil, err
		}
		return d.(proxy.ContextDialer).DialContext(ctx, network, target)
	}
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("unsupported proxy scheme")
	}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Keep the cancellation callback on the raw connection while TLS wraps it.
	wire := conn
	if scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: upstream.Hostname()})
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		wire = secure
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(upstream.User.Username()+":"+password)))
	}
	if err := req.Write(wire); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(wire, 16<<10))
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("proxy refused CONNECT")
	}
	if !stop() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = wire.SetDeadline(time.Time{})
	connected = true
	// Any tunnel bytes read with the response stay before the live connection.
	// Do not consume or close CONNECT's body: the caller owns the tunnel now.
	return &proxyConn{Conn: wire, reader: io.MultiReader(reader, wire)}, nil
}

type proxyConn struct {
	net.Conn
	reader io.Reader
}

func (c *proxyConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
