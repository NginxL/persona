package link

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Pinned is an HTTP/1.1 client that only talks to the hub whose TLS key hashes to fp (Pin): what a manager on another
// computer dials the hub's public port with (docs/assistant/managers.md).
func Pinned(fp string) *http.Client { return pinned(fp, nil) }

func pinnedTLSConfig(fp, host string) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
		InsecureSkipVerify: true, // the pin below is the check
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errPin
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if base64.RawURLEncoding.EncodeToString(sum[:]) != fp {
				return errPin
			}
			return nil
		}}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	return cfg
}

// pinned is an HTTP/1.1 client that only talks to the hub whose TLS key hashes to fp. onConn sees each new
// connection (the link keeps its local address, to tell when the network under it is gone).
func pinned(fp string, onConn func(net.Conn)) *http.Client {
	return tlsClient(pinnedTLSConfig(fp, ""), directDial(onConn))
}

// systemRoots replaces the system's CAs for systemTLSConfig and an https proxy (UseRootsForTesting); nil means the system's.
var systemRoots atomic.Pointer[x509.CertPool]

// UseRootsForTesting makes the system-trust client trust pool instead of the system's CAs, until restore is called.
// For tests (a reverse proxy with a test CA), never the keychain.
func UseRootsForTesting(pool *x509.CertPool) (restore func()) {
	old := systemRoots.Swap(pool)
	return func() { systemRoots.Store(old) }
}

// systemTLSConfig checks a hub without a pin: it sits behind an HTTPS reverse proxy whose certificate comes from a
// public CA (docs/operations/link.md "Hub behind a reverse proxy"). The certificate must chain to the system's CAs and
// name the host dialed (the transport sets ServerName); nothing else is accepted.
func systemTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, RootCAs: systemRoots.Load()}
}

// tlsClient is an HTTP/1.1 client whose connections come from dial and whose TLS the transport runs with cfg, after
// whatever dial did (a CONNECT through a proxy): the hub's check is the same on every route. An empty ServerName
// becomes the host dialed.
func tlsClient(cfg *tls.Config, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // an outbound proxy is dial's (proxyDial), never one the transport picks itself
			DialContext:           dial,
			TLSClientConfig:       cfg,
			TLSHandshakeTimeout:   dialTimeout, // the transport dials under a context a given-up request does not end
			ResponseHeaderTimeout: dialTimeout,
			MaxIdleConns:          1,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// directDial dials the hub straight; onConn sees each new connection.
func directDial(onConn func(net.Conn)) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := d.DialContext(ctx, network, addr)
		if err == nil && onConn != nil {
			onConn(raw)
		}
		return raw, err
	}
}

// hubTLSConfig is how c's hub is checked: pinned to its key when the pairing gave one (never anything else then),
// else with the system's CAs.
func hubTLSConfig(c *Config) *tls.Config {
	if c.FP != "" {
		return pinnedTLSConfig(c.FP, "")
	}
	return systemTLSConfig()
}

// hubClient dials c's hub, straight or through outbound proxy p when it is set (proxyDial), and checks it with
// hubTLSConfig either way: inside the proxy's tunnel the check is the same.
func hubClient(c *Config, p *url.URL, onConn func(net.Conn)) *http.Client {
	dial := directDial(onConn)
	if p != nil {
		dial = proxyDial(p, onConn)
	}
	return tlsClient(hubTLSConfig(c), dial)
}

// trustOf is Status.Trust for c.
func trustOf(c *Config) string {
	if c.FP == "" {
		return TrustSystem
	}
	return ""
}

// wsURL joins path under base: base keeps the path a reverse proxy puts the hub under.
func wsURL(base, path string) string {
	return "wss" + strings.TrimPrefix(base, "https") + path
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }
