package link

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
)

// proxyEnv is every variable proxied reads; TestMain clears them for the package, tests set what they need.
var proxyEnv = []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

const (
	proxyUser   = "proxy-user"
	proxySecret = "proxy-secret"
)

// connectProxy is a minimal HTTP CONNECT proxy: every tunnel goes to upstream whatever it asks for (the hub
// addresses in these tests are not reachable directly), after the credentials proxyUser / proxySecret.
type connectProxy struct {
	url      string // with the credentials
	upstream atomic.Value
	refuse   atomic.Int32 // a status to answer CONNECT with instead
	reset    atomic.Bool  // answer 200, then reset the connection
	connects atomic.Int32
	mu       sync.Mutex
	targets  []string
	conns    []net.Conn
}

func newConnectProxy(t *testing.T, upstream string, cert *tls.Certificate) *connectProxy {
	t.Helper()
	p := &connectProxy{}
	p.upstream.Store(upstream)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	if cert != nil {
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(func() {
		srv.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	u, _ := url.Parse(srv.URL)
	u.User = url.UserPassword(proxyUser, proxySecret)
	p.url = u.String()
	return p
}

func (p *connectProxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxySecret)) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
		http.Error(w, "credentials", http.StatusProxyAuthRequired)
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, r.Host)
	p.mu.Unlock()
	if code := p.refuse.Load(); code != 0 {
		http.Error(w, "refused", int(code))
		return
	}
	up, err := net.DialTimeout("tcp", p.upstream.Load().(string), 3*time.Second)
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	down, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	p.connects.Add(1)
	p.mu.Lock()
	p.conns = append(p.conns, up, down)
	p.mu.Unlock()
	_, _ = io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n")
	if p.reset.Load() {
		_, _ = io.ReadFull(down, make([]byte, 1)) // the hub's TLS has begun
		if tc, ok := down.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = down.Close()
		_ = up.Close()
		return
	}
	go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
	go func() { _, _ = io.Copy(down, up); _ = down.Close() }()
}

func (p *connectProxy) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.targets)
}

// selfSigned is a certificate of its own for hosts (not httptest's, which every test server shares), with a pool that
// trusts it.
func selfSigned(t *testing.T, hosts ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hosts[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}

// unroutable is a hub address the tests never reach directly: only a proxy, which sends every tunnel to its hub.
const unroutable = "https://192.0.2.1:443"

// proxyableLocalhost is a host name for this computer that a proxy is used for: Go's NO_PROXY rules never send
// "localhost" or a loopback address through one, so the tests that also dial directly use a name under .localhost
// (RFC 6761), which the system resolves to loopback.
func proxyableLocalhost(t *testing.T) string {
	t.Helper()
	const host = "hub.localhost"
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil || !slices.Contains(addrs, "127.0.0.1") {
		t.Skipf("%s does not resolve to 127.0.0.1 here (%v %v)", host, addrs, err)
	}
	return host
}

// dialProxy is the proxy route alone: c's candidates through its proxy (proxied).
func dialProxy(ctx context.Context, c *Config) (*link, error) {
	p, eps, err := proxied(c)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("no proxy for these candidates")
	}
	return dialVia(ctx, c, eps, p)
}

// runLink runs o until the test ends, or stop; its statuses come on the channel.
func runLink(t *testing.T, o Options) (chan Status, func()) {
	t.Helper()
	ch := make(chan Status, 256)
	o.Status = func(s Status) {
		select {
		case ch <- s:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = Run(ctx, o) }()
	stop := sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(stop)
	return ch, stop
}

func portOf(raw string) string {
	u, _ := url.Parse(raw)
	return u.Port()
}

func TestParseProxy(t *testing.T) {
	for raw, want := range map[string]string{
		"http://proxy.example:8080":       "http://proxy.example:8080",
		"https://u:p@proxy.example":       "https://u:p@proxy.example",
		"proxy.example:3128":              "http://proxy.example:3128", // as Go's net/http reads HTTPS_PROXY
		"user:pw@proxy.example:3128":      "http://user:pw@proxy.example:3128",
		" http://[::1]:3128/ ":            "http://[::1]:3128",
		"http://user%40corp:p%3Aw@p:8080": "http://user%40corp:p%3Aw@p:8080",
	} {
		got, err := ParseProxy(raw)
		if err != nil || got.String() != want {
			t.Errorf("ParseProxy(%q) = %v, %v; want %s", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "socks5://user:pw-secret@p:1080", "ftp://p", "http://p:8080/path", "http://p:99999",
		"http://user:pw-secret@", "http://p?x=1", "http://p#x", "https:user:pw-secret@p:443"} {
		_, err := ParseProxy(raw)
		if err == nil {
			t.Errorf("ParseProxy(%q) accepted", raw)
			continue
		}
		for _, l := range []i18n.Lang{i18n.Default, i18n.En, i18n.Ja} {
			if text := ErrorText(l, err); strings.Contains(text, "secret") || strings.Contains(text, "user") || text != i18n.T(l, "link-errors.proxy_invalid") {
				t.Errorf("ParseProxy(%q): %s", raw, text)
			}
		}
	}
	if got := RedactedProxy("http://user:pw-secret@proxy.example:8080"); got != "http://proxy.example:8080" {
		t.Fatalf("redacted: %s", got)
	}
	if got := RedactedProxy("user:pw-secret@proxy.example:8080"); got != "http://proxy.example:8080" {
		t.Fatalf("redacted bare: %s", got)
	}
	if got := RedactedProxy("socks5://user:pw-secret@p"); got != "" {
		t.Fatalf("an unusable setting is shown: %s", got)
	}
}

func TestProxiedFollowsTheSettingAndTheEnvironment(t *testing.T) {
	eps := []Endpoint{{URL: "https://192.0.2.2:18662", Kind: "lan"}, {URL: "https://hub.example.com:18662", Kind: "name", Manual: true},
		{URL: "https://127.0.0.1:18662", Kind: "lan"}, {URL: "https://localhost:18662", Kind: "name"}}
	via := func(c *Config) (string, []string) {
		t.Helper()
		p, got, err := proxied(c)
		if err != nil {
			t.Fatalf("proxied: %v", err)
		}
		var urls []string
		for _, e := range got {
			if e.Kind != KindProxy {
				t.Fatalf("candidate kind %q", e.Kind)
			}
			urls = append(urls, e.URL)
		}
		if p == nil {
			return "", urls
		}
		return p.String(), urls
	}
	c := &Config{Endpoints: eps}
	if p, got := via(c); p != "" || got != nil {
		t.Fatalf("no proxy anywhere: %s %v", p, got)
	}
	t.Setenv("ALL_PROXY", "http://all.example:1")
	if p, _ := via(c); p != "http://all.example:1" {
		t.Fatalf("ALL_PROXY: %s", p)
	}
	t.Setenv("HTTPS_PROXY", "upper.example:2")
	if p, _ := via(c); p != "http://upper.example:2" {
		t.Fatalf("HTTPS_PROXY before ALL_PROXY: %s", p)
	}
	t.Setenv("https_proxy", "http://lower.example:3") // lower case first, as Go's net/http reads them
	p, got := via(c)
	// Loopback and localhost never go through a proxy; LAN addresses go last.
	if p != "http://lower.example:3" || !slices.Equal(got, []string{"https://hub.example.com:18662", "https://192.0.2.2:18662"}) {
		t.Fatalf("https_proxy: %s %v", p, got)
	}
	c.Proxy = "http://u:pw@set.example:4"
	if p, _ := via(c); p != "http://u:pw@set.example:4" {
		t.Fatalf("link.json's before the environment: %s", p)
	}
	t.Setenv("NO_PROXY", ".example.com,192.0.2.0/24")
	if p, got := via(c); p != "" || got != nil {
		t.Fatalf("NO_PROXY exempts every candidate: %s %v", p, got)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "hub.example.com")
	if _, got := via(c); !slices.Equal(got, []string{"https://192.0.2.2:18662"}) {
		t.Fatalf("no_proxy: %v", got)
	}
	c.Proxy = ProxyOff
	if p, got := via(c); p != "" || got != nil {
		t.Fatalf("off, with HTTPS_PROXY set: %s %v", p, got)
	}
	// An environment proxy Persona cannot use (SOCKS) is the other programs'; one in link.json is the proxy route's
	// error, never a quoted URL, unless NO_PROXY leaves it nothing to carry.
	c.Proxy = ""
	t.Setenv("no_proxy", "")
	t.Setenv("https_proxy", "socks5://user:pw-secret@127.0.0.1:1080")
	if p, got, err := proxied(c); p != nil || got != nil || err != nil {
		t.Fatalf("socks5 in the environment: %v %v %v", p, got, err)
	}
	c.Proxy = "socks5://user:pw-secret@127.0.0.1:1080"
	if _, _, err := proxied(c); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("socks5 in link.json: %v", err)
	}
	t.Setenv("NO_PROXY", "*")
	if p, got, err := proxied(c); p != nil || got != nil || err != nil {
		t.Fatalf("socks5 in link.json, every candidate exempt: %v %v %v", p, got, err)
	}
}

func TestSetProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if err := SetProxy(path, "user:pw@proxy.example:3128"); err != nil {
		t.Fatal(err)
	}
	c, err := Read(path)
	if err != nil || c.Proxy != "http://user:pw@proxy.example:3128" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnpaired) {
		t.Fatalf("a proxy alone is not a pairing: %v", err)
	}
	if err := SetProxy(path, "socks5://user:pw-secret@p"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid: %v", err)
	}
	if c, _ := Read(path); c.Proxy != "http://user:pw@proxy.example:3128" {
		t.Fatalf("an invalid URL replaced the setting: %q", c.Proxy)
	}
	for _, raw := range []string{ProxyOff, ""} {
		if err := SetProxy(path, raw); err != nil {
			t.Fatal(err)
		}
		if c, _ := Read(path); c.Proxy != raw {
			t.Fatalf("SetProxy(%q) = %q", raw, c.Proxy)
		}
	}
}

// The hub's own check runs inside the proxy's tunnel: its pin (or the system's CAs and its name) decides, with the
// same words as directly, and an HTTPS proxy is checked on its own with the system's CAs and its own name.
func TestProxyKeepsTheHubsCheck(t *testing.T) {
	hub, _ := fakeHub(t, func(c *websocket.Conn) { _, _, _ = c.Read(context.Background()) })
	hubAddr := hub.Listener.Addr().String()
	proxy := newConnectProxy(t, hubAddr, nil)
	ctx := context.Background()
	cfg := &Config{Machine: "laptop", FP: pinOf(hub), Token: "jd_x", Proxy: proxy.url, Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}
	l, err := dialProxy(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if l.ep != (Endpoint{URL: unroutable, Kind: KindProxy}) || l.proxy != RedactedProxy(proxy.url) || strings.Contains(l.proxy, proxySecret) {
		t.Fatalf("link %+v via %q", l.ep, l.proxy)
	}
	l.closeNow()
	if got := proxy.asked(); !slices.Equal(got, []string{"192.0.2.1:443"}) {
		t.Fatalf("CONNECT asked for %v", got)
	}

	// Another key through the proxy: refused, in the words a direct connection uses.
	wrong := *cfg
	wrong.FP = "not-its-pin"
	_, viaErr := dialProxy(ctx, &wrong)
	direct := Config{Machine: "laptop", FP: "not-its-pin", Token: "jd_x", Endpoints: []Endpoint{{URL: hub.URL, Kind: "ipv4"}}}
	_, directErr := dial(ctx, &direct)
	if !errors.Is(viaErr, errPin) || !errors.Is(directErr, errPin) || describe(viaErr) != describe(directErr) || describe(viaErr) != defaultText("link-errors.pin_mismatch") {
		t.Fatalf("pin through the proxy: %v (%s); direct: %v (%s)", viaErr, describe(viaErr), directErr, describe(directErr))
	}
	if s := summary(viaErr); !strings.HasPrefix(s, kindText(i18n.Default, KindProxy)+" 192.0.2.1:443") {
		t.Fatalf("summary does not say it went through the proxy: %s", s)
	}

	// A proxy sending the tunnel to an impostor, whose certificate the system even trusts: refused by the pin.
	impostorCert, impostorPool := selfSigned(t, "127.0.0.1", "hub.example.com")
	impostor := httptest.NewUnstartedServer(hub.Config.Handler)
	impostor.TLS = &tls.Config{Certificates: []tls.Certificate{impostorCert}}
	impostor.StartTLS()
	t.Cleanup(impostor.Close)
	restore := UseRootsForTesting(impostorPool)
	proxy.upstream.Store(impostor.Listener.Addr().String())
	if _, err := dialProxy(ctx, cfg); !errors.Is(err, errPin) {
		t.Fatalf("an impostor behind the proxy: %v", err)
	}
	// A pairing without a pin (a hub behind a reverse proxy): the system's CAs and the hub's name decide, inside the tunnel.
	system := &Config{Machine: "laptop", Trust: TrustSystem, Token: "jd_x", Proxy: proxy.url,
		Endpoints: []Endpoint{{URL: "https://hub.example.com:443", Kind: "name"}}}
	if l, err := dialProxy(ctx, system); err != nil {
		t.Fatalf("system trust through the proxy: %v", err)
	} else {
		l.closeNow()
	}
	system.Endpoints[0].URL = "https://other.example.com:443"
	if _, err := dialProxy(ctx, system); describe(err) != defaultText("link-errors.hostname_mismatch") {
		t.Fatalf("another name through the proxy: %v (%s)", err, describe(err))
	}
	restore()
	system.Endpoints[0].URL = "https://hub.example.com:443"
	if _, err := dialProxy(ctx, system); describe(err) != defaultText("link-errors.certificate_untrusted") {
		t.Fatalf("an untrusted hub through the proxy: %v (%s)", err, describe(err))
	}
	proxy.upstream.Store(hubAddr)

	// An HTTPS proxy: its certificate is checked with the system's CAs and its own name, the hub's pin after it.
	proxyCert, proxyPool := selfSigned(t, "127.0.0.1")
	secure := newConnectProxy(t, hubAddr, &proxyCert)
	viaTLS := *cfg
	viaTLS.Proxy = secure.url
	if _, err := dialProxy(ctx, &viaTLS); describe(err) != defaultText("link-errors.proxy_tls_failed") || secure.connects.Load() != 0 {
		t.Fatalf("an untrusted HTTPS proxy: %v (%s)", err, describe(err))
	}
	t.Cleanup(UseRootsForTesting(proxyPool))
	if l, err := dialProxy(ctx, &viaTLS); err != nil {
		t.Fatalf("a trusted HTTPS proxy: %v", err)
	} else {
		l.closeNow()
	}
	byName := viaTLS
	byName.Proxy = strings.Replace(secure.url, "127.0.0.1", "localhost", 1) // the certificate names 127.0.0.1 alone
	if _, err := dialProxy(ctx, &byName); describe(err) != defaultText("link-errors.proxy_tls_failed") {
		t.Fatalf("an HTTPS proxy under another name: %v (%s)", err, describe(err))
	}
	viaTLS.FP = "not-its-pin"
	if _, err := dialProxy(ctx, &viaTLS); !errors.Is(err, errPin) {
		t.Fatalf("the hub's pin behind a trusted HTTPS proxy: %v", err)
	}
}

// Whatever went wrong with the proxy says so in plain words, in every language, without the proxy's address or its
// credentials.
func TestProxyFailuresNameNeitherTheProxyNorItsCredentials(t *testing.T) {
	hub, _ := fakeHub(t, func(c *websocket.Conn) { _, _, _ = c.Read(context.Background()) })
	proxy := newConnectProxy(t, hub.Listener.Addr().String(), nil)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	closed.Close()
	badCredentials := strings.Replace(proxy.url, proxySecret, "wrong-secret", 1)
	for name, tc := range map[string]struct {
		proxy, key string
		refuse     int32
		status     int
	}{
		"credentials": {badCredentials, "link-errors.proxy_rejected", 0, http.StatusProxyAuthRequired},
		"refused":     {proxy.url, "link-errors.proxy_rejected", http.StatusForbidden, http.StatusForbidden},
		"unreachable": {"http://" + proxyUser + ":" + proxySecret + "@" + closedAddr, "link-errors.proxy_unreachable", 0, 0},
		// Not a certificate problem: the proxy does not speak TLS.
		"not https": {strings.Replace(proxy.url, "http://", "https://", 1), "link-errors.proxy_tls_handshake", 0, 0},
	} {
		proxy.refuse.Store(tc.refuse)
		c := &Config{Machine: "laptop", FP: pinOf(hub), Token: "jd_x", Proxy: tc.proxy, Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}
		_, err := dialProxy(context.Background(), c)
		var pe *proxyError
		if !errors.As(err, &pe) || pe.key != tc.key || pe.status != tc.status {
			t.Fatalf("%s: %v", name, err)
		}
		_, port, _ := net.SplitHostPort(closedAddr)
		for _, text := range []string{err.Error(), summary(err), ErrorText(i18n.En, err), ErrorText(i18n.Ja, err),
			StatusErrorText(i18n.En, summary(err)), fmt.Sprintf("%+v", err)} {
			for _, secret := range []string{proxyUser, proxySecret, "wrong-secret", portOf(proxy.url), port} {
				if strings.Contains(text, secret) {
					t.Fatalf("%s: %q in %q", name, secret, text)
				}
			}
		}
		if got, want := StatusErrorText(i18n.En, summary(err)), kindText(i18n.En, KindProxy)+" 192.0.2.1:443: "+pe.Text(i18n.En); got != want {
			t.Fatalf("%s: saved status in English = %q, want %q", name, got, want)
		}
	}
	proxy.refuse.Store(0)

	// A tunnel the proxy breaks after opening it fails as the hub's handshake does, still without the proxy's address.
	proxy.reset.Store(true)
	c := &Config{Machine: "laptop", FP: pinOf(hub), Token: "jd_x", Proxy: proxy.url, Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}
	_, err = dialProxy(context.Background(), c)
	if err == nil {
		t.Fatal("a reset tunnel connected")
	}
	for _, text := range []string{err.Error(), summary(err), fmt.Sprintf("%+v", err)} {
		if strings.Contains(text, portOf(proxy.url)) {
			t.Fatalf("the proxy's address in %q", text)
		}
	}
}

// A proxy that opens the tunnel and then says nothing does not hold the hub's handshake forever: tlsClient gives up
// after dialTimeout and closes the connection, also once the request itself was given up.
func TestProxyStallingInsideTheTunnelIsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out dialTimeout")
	}
	t.Parallel() // waits out dialTimeout alongside the direct route's test
	hub, closed := silentHub(t)
	proxy := newConnectProxy(t, hub, nil)
	c := &Config{Machine: "laptop", FP: "any", Token: "jd_x", Proxy: proxy.url, Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}
	requireStalledHandshakeClosed(t, closed, func(ctx context.Context) error { _, err := dialProxy(ctx, c); return err })
}

// A direct candidate whose hub takes the connection and never answers the TLS hello is closed the same way (#11028):
// the handshake is tlsClient's own DialTLSContext, which the transport's TLSHandshakeTimeout does not cover, and the
// transport dials under a context a given-up request does not end.
func TestDirectStalledHandshakeIsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out dialTimeout")
	}
	t.Parallel()
	hub, closed := silentHub(t)
	c := &Config{Machine: "laptop", FP: "any", Token: "jd_x", Endpoints: []Endpoint{{URL: "https://" + hub, Kind: "lan"}}}
	requireStalledHandshakeClosed(t, closed, func(ctx context.Context) error { _, err := dial(ctx, c); return err })
}

// silentHub takes one connection and reads it without ever answering: a hub, or whatever a tunnel reaches, whose TLS
// never starts. closed is closed once the other side closes the connection.
func silentHub(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	closed := make(chan struct{})
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, c) // reads the ClientHello, answers nothing, until the other side closes
		close(closed)
		c.Close()
	}()
	return l.Addr().String(), closed
}

// requireStalledHandshakeClosed gives try a second, which a silent hub uses up, and then wants the stalled connection
// closed within dialTimeout (plus slack for a loaded machine): the handshake outlives the given-up request, never
// the deadline.
func requireStalledHandshakeClosed(t *testing.T, closed <-chan struct{}, try func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := try(ctx); err == nil {
		t.Fatal("a silent hub connected")
	}
	select {
	case <-closed:
	case <-time.After(dialTimeout + 5*time.Second):
		t.Fatal("the stalled handshake's connection is still open")
	}
}

// dialActive's order: direct candidates, then the proxy, then (with one) the SSH tunnel; the route that worked last first.
func TestDialActiveTriesTheProxyAfterDirect(t *testing.T) {
	host := proxyableLocalhost(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, true)
	proxy := newConnectProxy(t, hub.target, nil)
	ep := Endpoint{URL: "https://" + host + ":" + portOf(direct.url), Kind: "name"}
	cfg := &Config{Hub: "h1", Machine: "laptop", FP: hub.pin, Token: "jd_ssh", Proxy: proxy.url, Endpoints: []Endpoint{ep}}
	ctx := context.Background()
	try := func(c *Config, last string) (*link, error) {
		t.Helper()
		l, err := dialActive(ctx, c, "", last)
		if err == nil {
			l.closeNow()
		}
		return l, err
	}
	if l, err := try(cfg, ""); err != nil || l.ep != ep || proxy.connects.Load() != 0 {
		t.Fatalf("direct first: %+v %v (CONNECTs %d)", l, err, proxy.connects.Load())
	}
	// After a direct link, last is its address kind: direct candidates still go first.
	if l, err := try(cfg, ep.Kind); err != nil || l.ep != ep || proxy.connects.Load() != 0 {
		t.Fatalf("direct first after a direct link: %+v %v (CONNECTs %d)", l, err, proxy.connects.Load())
	}
	// The route that worked last goes first: through the proxy, direct candidates untried.
	before := direct.attempts.Load()
	if l, err := try(cfg, KindProxy); err != nil || l.ep.Kind != KindProxy || direct.attempts.Load() != before || proxy.connects.Load() != 1 {
		t.Fatalf("proxy last: %+v %v (direct %d → %d)", l, err, before, direct.attempts.Load())
	}

	direct.allowed.Store(false)
	before = direct.attempts.Load()
	if l, err := try(cfg, ""); err != nil || l.ep.Kind != KindProxy || direct.attempts.Load() == before || proxy.connects.Load() != 2 {
		t.Fatalf("proxy after direct: %+v %v", l, err)
	}
	// NO_PROXY and off are respected: no CONNECT, and the direct failure is what is said.
	t.Setenv("NO_PROXY", host)
	if _, err := try(cfg, KindProxy); err == nil || proxy.connects.Load() != 2 {
		t.Fatalf("NO_PROXY: %v (CONNECTs %d)", err, proxy.connects.Load())
	}
	t.Setenv("NO_PROXY", "")
	off := *cfg
	off.Proxy = ProxyOff
	t.Setenv("HTTPS_PROXY", proxy.url)
	if _, err := try(&off, KindProxy); err == nil || proxy.connects.Load() != 2 {
		t.Fatalf("off: %v (CONNECTs %d)", err, proxy.connects.Load())
	}
	// The environment's proxy when link.json has none.
	env := *cfg
	env.Proxy = ""
	if l, err := try(&env, ""); err != nil || l.ep.Kind != KindProxy || proxy.connects.Load() != 3 {
		t.Fatalf("HTTPS_PROXY: %+v %v", l, err)
	}

	// An environment proxy Persona cannot use is left alone: no proxy route, no error about it.
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:1080")
	if _, err := try(&env, ""); err == nil || strings.Contains(err.Error(), defaultText("link-errors.proxy_invalid")) {
		t.Fatalf("a SOCKS HTTPS_PROXY: %v", err)
	}
	t.Setenv("HTTPS_PROXY", "")
	// One in link.json fails its own route only: direct candidates still work, and are still tried.
	unusable := *cfg
	unusable.Proxy = "socks5://127.0.0.1:1080"
	direct.allowed.Store(true)
	if l, err := try(&unusable, KindProxy); err != nil || l.ep != ep {
		t.Fatalf("an unusable proxy kept direct candidates from working: %+v %v", l, err)
	}
	direct.allowed.Store(false)
	if _, err := try(&unusable, ""); err == nil || !strings.Contains(err.Error(), defaultText("link-errors.proxy_invalid")) {
		t.Fatalf("an unusable proxy is not said: %v", err)
	}

	// A pinned hub that refused the credential directly is not asked again through the proxy.
	refused := *cfg
	refused.Token = "jd_old"
	direct.allowed.Store(true)
	var he *httpError
	if _, err := try(&refused, ""); !errors.As(err, &he) || he.status != http.StatusUnauthorized || proxy.connects.Load() != 3 {
		t.Fatalf("refused directly: %v (CONNECTs %d)", err, proxy.connects.Load())
	}
}

// Over the proxy the link and every stream on it take the proxy, and a reconnect takes it straight away.
func TestProxyRouteCarriesTheLinkAndItsStreams(t *testing.T) {
	host := proxyableLocalhost(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, false)
	proxy := newConnectProxy(t, hub.target, nil)
	node := newFakeNode(t)
	ep := Endpoint{URL: "https://" + host + ":" + portOf(direct.url), Kind: "name"}
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		*c = Config{Hub: "h1", Machine: "laptop", FP: hub.pin, Token: "jd_ssh", Proxy: proxy.url, Endpoints: []Endpoint{ep}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	var logs strings.Builder
	ch, stop := runLink(t, Options{Path: path, Target: node.path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: time.Hour,
		Logf: func(f string, a ...any) { logMu.Lock(); fmt.Fprintf(&logs, f+"\n", a...); logMu.Unlock() }})

	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != KindProxy || st.Address != ep.URL || st.Proxy != RedactedProxy(proxy.url) || direct.attempts.Load() == 0 {
		t.Fatalf("status %+v (direct attempts %d)", st, direct.attempts.Load())
	}
	first := <-hub.links
	if first.hello.Address != ep.URL {
		t.Fatalf("hello address %q", first.hello.Address)
	}
	// A stream the hub opens goes through the proxy as well, and carries the node's bytes.
	sendJSON(t, first.conn, `{"type":"open","id":"s1"}`)
	var stream *websocket.Conn
	select {
	case stream = <-hub.streams:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not reach the hub")
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, b, err := stream.Read(rctx)
	rcancel()
	if err != nil || string(b) != nodeHello {
		t.Fatalf("stream: %q %v", b, err)
	}
	_ = stream.CloseNow()
	if n := proxy.connects.Load(); n != 2 {
		t.Fatalf("CONNECTs for the link and its stream: %d", n)
	}

	// The hub restarts: the next link goes through the proxy at once, without trying direct candidates first.
	attempts := direct.attempts.Load()
	_ = first.conn.Close(websocket.StatusGoingAway, "restart")
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind == KindProxy && s.Conn > st.Conn })
	<-hub.links
	if got := direct.attempts.Load(); got != attempts {
		t.Fatalf("a reconnect after a working proxy tried direct first: %d → %d", attempts, got)
	}

	// Turning the proxy off while connected through it moves the link off it: direct only, which fails here.
	if err := SetProxy(path, ProxyOff); err != nil {
		t.Fatal(err)
	}
	off := waitFor(t, ch, func(s Status) bool {
		return s.State == "waiting" && s.Error != "" && !strings.Contains(s.Error, defaultText("link-errors.repaired"))
	})
	if proxy.connects.Load() != 3 || strings.Contains(off.Error, kindText(i18n.Default, KindProxy)) {
		t.Fatalf("off: %+v (CONNECTs %d)", off, proxy.connects.Load())
	}
	stop()
	logMu.Lock()
	defer logMu.Unlock()
	for _, secret := range []string{proxyUser, proxySecret, portOf(proxy.url)} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log names the proxy (%q):\n%s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "("+KindProxy+")") {
		t.Fatalf("the log does not say the link went through a proxy:\n%s", logs.String())
	}
}

// Connected through the proxy, a direct candidate that answers again takes over, the way it does from SSH.
func TestProxyRouteSwitchesBackToDirect(t *testing.T) {
	host := proxyableLocalhost(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, false)
	proxy := newConnectProxy(t, hub.target, nil)
	ep := Endpoint{URL: "https://" + host + ":" + portOf(direct.url), Kind: "name"}
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		*c = Config{Hub: "h1", Machine: "laptop", FP: hub.pin, Token: "jd_ssh", Endpoints: []Endpoint{ep}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPS_PROXY", proxy.url)
	ch, _ := runLink(t, Options{Path: path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: 200 * time.Millisecond})
	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != KindProxy || st.Proxy != RedactedProxy(proxy.url) {
		t.Fatalf("status %+v", st)
	}
	overProxy := <-hub.links
	direct.allowed.Store(true)
	back := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind != KindProxy })
	if back.Kind != "name" || back.Address != ep.URL || back.Proxy != "" {
		t.Fatalf("back direct: %+v", back)
	}
	<-hub.links
	select {
	case <-overProxy.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the link through the proxy stayed open")
	}
}

// Pairing goes through the proxy set before it, when the hub's addresses do not answer directly, and keeps it.
func TestPairThroughTheProxy(t *testing.T) {
	host := proxyableLocalhost(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, false)
	proxy := newConnectProxy(t, hub.target, nil)
	path := filepath.Join(t.TempDir(), "link.json")
	if err := SetProxy(path, proxy.url); err != nil {
		t.Fatal(err)
	}
	rawLink := "persona://link?" + url.Values{
		"url": {"http://" + host + ":1"}, "tls": {portOf(direct.url)}, "fp": {hub.pin}, "hub": {"h1"}, "token": {"jp_pair"}, "machine": {"laptop"},
	}.Encode()
	c, err := Pair(context.Background(), rawLink, "", "", path)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if c.Token != "jd_ssh" || direct.attempts.Load() == 0 || proxy.connects.Load() != 1 {
		t.Fatalf("paired %+v (direct %d, CONNECTs %d)", c, direct.attempts.Load(), proxy.connects.Load())
	}
	if f, err := Load(path); err != nil || f.Proxy != proxy.url || f.Token != "jd_ssh" {
		t.Fatalf("link.json after pairing: %+v %v", f, err)
	}
	// A hub that refuses the pairing through the proxy says so.
	bad := strings.Replace(rawLink, "jp_pair", "jp_spent", 1)
	if _, err := Pair(context.Background(), bad, "", "", path); err == nil || !pairAnswered(err) {
		t.Fatalf("refused pairing: %v", err)
	}
}

func TestRepairedFollowsTheProxyOnAFallbackRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	c := &Config{Hub: "h1", Machine: "laptop", FP: "pin", Token: "jd_x", Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}
	if err := Update(path, func(f *Config) error { *f = *c; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(path, "http://proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if repaired(path, c, false) {
		t.Fatal("a direct link reconnects for a new proxy setting")
	}
	if !repaired(path, c, true) {
		t.Fatal("a link on a fallback route keeps the old proxy setting")
	}
}
