package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	xproxy "golang.org/x/net/proxy"

	"github.com/nwverse/persona/server/internal/netproxy"
)

// Gmail traffic can go through an HTTP proxy (hub.json google.proxy): some networks reach only some of Google's
// IPv4 addresses, so imap.gmail.com and smtp.gmail.com time out on most resolutions. The proxy carries IMAP
// and SMTP with CONNECT tunnels, and the OAuth token endpoint and the Gmail API as ordinary HTTPS.

// SetGoogleProxy routes Gmail accounts and Google's OAuth / Gmail API through an http:// (CONNECT) or socks5:// proxy ("" = direct).
func (s *Service) SetGoogleProxy(raw string) error {
	u, err := netproxy.ParseHTTPOrSOCKS5(raw)
	if err != nil {
		return fmt.Errorf("mail: google.proxy: %w", err)
	}
	if u == nil {
		s.googleProxy, s.googleHTTP = nil, httpClient
		return nil
	}
	s.googleProxy = u
	s.googleHTTP = netproxy.HTTPClient(u, networkTimeout)
	return nil
}

// proxyError is a failure to reach the server through the proxy (the proxy is down, or refused the CONNECT).
type proxyError struct {
	proxy string
	addr  string
	err   error
}

func (e *proxyError) Error() string {
	if e.addr != "" {
		return fmt.Sprintf("proxy %s: CONNECT %s: %v", e.proxy, e.addr, e.err)
	}
	return fmt.Sprintf("proxy %s: %v", e.proxy, e.err)
}

func (e *proxyError) Unwrap() error { return e.err }

// proxyFor returns the proxy for an account's servers, nil to dial directly.
func (s *Service) proxyFor(a *MailAccount) *url.URL {
	if a.Provider == ProviderGmail {
		return s.googleProxy
	}
	return nil
}

// serverAddr is the address to dial for a server setting. An IPv6 literal may be set with or without brackets; either
// way it goes in brackets before the port.
func serverAddr(host string, port int) string {
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), strconv.Itoa(port))
}

// dialTCP connects to addr directly, through an HTTP CONNECT tunnel, or through a SOCKS5 proxy (socks5://, which
// resolves addr's name at the proxy).
func dialTCP(ctx context.Context, proxy *url.URL, addr string) (net.Conn, error) {
	if proxy == nil {
		return netDialer.DialContext(ctx, "tcp", addr)
	}
	if proxy.Scheme == "socks5" {
		return dialSOCKS5(ctx, proxy, addr)
	}
	c, err := netDialer.DialContext(ctx, "tcp", proxy.Host)
	if err != nil {
		return nil, &proxyError{proxy: proxy.Host, err: err}
	}
	if d, ok := ctx.Deadline(); ok {
		c.SetDeadline(d)
	}
	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)) + "\r\n"
	}
	if _, err := c.Write([]byte(req + "\r\n")); err != nil {
		c.Close()
		return nil, &proxyError{proxy: proxy.Host, err: err}
	}
	// bufio reads ahead (16 bytes at the least, whatever size is asked): bytes of the tunnelled stream that came in
	// with the reply stay in br, so the returned conn reads them first.
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, &proxyError{proxy: proxy.Host, err: err}
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, &proxyError{proxy: proxy.Host, addr: addr, err: errors.New(resp.Status)}
	}
	c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufferedConn is a net.Conn whose reads go through r, a reader on the Conn holding bytes already read off it.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// dialTLS connects and completes a TLS handshake for serverName.
func dialTLS(ctx context.Context, proxy *url.URL, addr, serverName string) (net.Conn, error) {
	c, err := dialTCP(ctx, proxy, addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(c, &tls.Config{ServerName: serverName})
	if err := tc.HandshakeContext(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return tc, nil
}

// dialSMTP opens an SMTP client with the given security, optionally through the proxy.
func dialSMTP(ctx context.Context, proxy *url.URL, addr, security string, tlsConfig *tls.Config) (*smtp.Client, error) {
	switch security {
	case "tls":
		c, err := dialTLS(ctx, proxy, addr, tlsConfig.ServerName)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(c), nil
	case "starttls":
		c, err := dialTCP(ctx, proxy, addr)
		if err != nil {
			return nil, err
		}
		// NewClientStartTLS reads the greeting and runs EHLO and STARTTLS with go-smtp's own five-minute
		// timeout and no ctx: close the connection when ctx ends so a silent server cannot hold the caller
		// past its deadline (#4616).
		stop := context.AfterFunc(ctx, func() { c.Close() })
		client, err := smtp.NewClientStartTLS(c, tlsConfig)
		if !stop() {
			if client != nil {
				client.Close()
			}
			return nil, ctx.Err()
		}
		return client, err
	case "none":
		c, err := dialTCP(ctx, proxy, addr)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(c), nil
	default:
		return nil, fmt.Errorf("mail: unknown smtp security %q", security)
	}
}

// dialSOCKS5 connects to addr through the SOCKS5 proxy, with its user:pass if the URL has one.
func dialSOCKS5(ctx context.Context, proxy *url.URL, addr string) (net.Conn, error) {
	var auth *xproxy.Auth
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		auth = &xproxy.Auth{User: u.Username(), Password: pw}
	}
	d, err := xproxy.SOCKS5("tcp", proxy.Host, auth, &netDialer)
	if err != nil {
		return nil, &proxyError{proxy: proxy.Host, err: err}
	}
	var c net.Conn
	if cd, ok := d.(xproxy.ContextDialer); ok {
		c, err = cd.DialContext(ctx, "tcp", addr)
	} else {
		c, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return nil, &proxyError{proxy: proxy.Host, addr: addr, err: err}
	}
	return c, nil
}
