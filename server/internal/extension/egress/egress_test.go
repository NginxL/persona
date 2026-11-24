package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newProxy is a proxy whose open internet is the test server at target: every name resolves to 192.0.2.1, which
// dials target (inside.example.com resolves to loopback).
func newProxy(t *testing.T, target string, upstream func(*url.URL) (*url.URL, error)) *Proxy {
	t.Helper()
	p := &Proxy{HubAddr: target, Upstream: upstream,
		Resolve: func(_ context.Context, name string) ([]net.IP, error) {
			if name == "inside.example.com" {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return []net.IP{net.ParseIP("192.0.2.1")}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			h, _, _ := net.SplitHostPort(addr)
			if h == "192.0.2.1" { // the open internet, played by the test server
				addr = target
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	if upstream == nil {
		p.Upstream = func(*url.URL) (*url.URL, error) { return nil, nil }
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// shortTempDir is a temporary directory for Unix sockets: sun_path holds 104 bytes on macOS, and t.TempDir() under
// /var/folders/…/T/ with a test's name does not fit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "egress")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func client(proxy string) *http.Client {
	u, _ := url.Parse(proxy)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func TestProxyAllowsOnlyItsHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s %s", r.Host, r.Header.Get("Proxy-Authorization"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	var decided []string
	p := newProxy(t, addr, nil)
	p.OnDecision = func(ext, host string, ok bool) { decided = append(decided, fmt.Sprintf("%s %s %v", ext, host, ok)) }
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com", "*.cdn.example.net", "192.0.2.7"}})

	c := client(purl)
	get := func(u string) (int, string) {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("http://api.example.com/x"); code != 200 || !strings.HasPrefix(body, "hello api.example.com ") || strings.Contains(body, "Basic") {
		t.Fatalf("allowed host: %d %q (the proxy's credential must not go on)", code, body)
	}
	if code, _ := get("http://a.b.cdn.example.net/"); code != 200 {
		t.Fatalf("wildcard: %d", code)
	}
	if code, _ := get("http://cdn.example.net/"); code != 403 {
		t.Fatalf("*.cdn.example.net is not cdn.example.net itself: %d", code)
	}
	if code, body := get("http://evil.example.org/"); code != 403 || !strings.Contains(body, "permissions.network") {
		t.Fatalf("other host: %d %q", code, body)
	}
	if code, _ := get("http://192.0.2.8/"); code != 403 {
		t.Fatalf("an IP not listed: %d", code)
	}
	if code, _ := get("http://" + egressHub() + "/"); code != 403 {
		t.Fatalf("the hub without a token of it: %d", code)
	}
	st := p.Stats("notes")
	if st.Allowed != 2 || st.Denied != 4 || st.LastDenied == "" {
		t.Fatalf("counts %+v", st)
	}
	if len(decided) != 6 {
		t.Fatalf("decisions %v", decided)
	}
}

func egressHub() string { return HubHost }

func TestProxyNeedsItsCredential(t *testing.T) {
	p := newProxy(t, "127.0.0.1:1", nil)
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	u, _ := url.Parse(purl)
	for _, bad := range []string{"http://" + p.Addr(), "http://ext:wrong@" + p.Addr(), "http://other:" + func() string { pw, _ := u.User.Password(); return pw }() + "@" + p.Addr()} {
		resp, err := client(bad).Get("http://api.example.com/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("%s: %d", bad, resp.StatusCode)
		}
	}
	// A new start: the old credential stops working.
	p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	resp, err := client(purl).Get("http://api.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("old credential: %d", resp.StatusCode)
	}
}

func TestProxyConnectAndHub(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls ok") }))
	defer srv.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hub %s %s", r.URL.Path, r.Header.Get("Authorization"))
	}))
	defer hub.Close()
	p := newProxy(t, strings.TrimPrefix(srv.URL, "https://"), nil)
	p.HubAddr = strings.TrimPrefix(hub.URL, "http://")
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}, Hub: true})
	u, _ := url.Parse(purl)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	tr.TLSClientConfig.InsecureSkipVerify = true
	c := &http.Client{Transport: tr}
	resp, err := c.Get("https://api.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "tls ok" {
		t.Fatalf("CONNECT: %q", b)
	}
	if _, err := c.Get("https://evil.example.org/"); err == nil {
		t.Fatal("CONNECT to a host not listed went through")
	}
	req, _ := http.NewRequest("GET", "http://"+HubHost+"/api/v1/kb/search", nil)
	req.Header.Set("Authorization", "Bearer pext_x")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hub /api/v1/kb/search Bearer pext_x" {
		t.Fatalf("hub: %q", b)
	}
}

func TestProxyRefusesNamesThatLeadInside(t *testing.T) {
	p := newProxy(t, "127.0.0.1:1", nil)
	purl := p.Register("notes", Rule{Hosts: []string{"inside.example.com"}})
	resp, err := client(purl).Get("http://inside.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "loopback") {
		t.Fatalf("%d %q", resp.StatusCode, b)
	}
}

func TestProxyGoesOutThroughTheHubsProxy(t *testing.T) {
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		asked = append(asked, r.Method+" "+r.Host+" "+r.Header.Get("Proxy-Authorization"))
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", 400)
			return
		}
		c, buf, _ := w.(http.Hijacker).Hijack()
		defer c.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\n\r\n")
		_ = buf.Flush()
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		body := "via upstream " + req.Host
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	upURL.User = url.UserPassword("u", "p")
	p := newProxy(t, "127.0.0.1:1", func(*url.URL) (*url.URL, error) { return upURL, nil })
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	resp, err := client(purl).Get("http://api.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "via upstream api.example.com" || len(asked) != 1 || !strings.HasPrefix(asked[0], "CONNECT 192.0.2.1:80 Basic ") {
		t.Fatalf("%q %v", b, asked)
	}
}

func TestProxyUnixSocketIsTheExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	p := newProxy(t, strings.TrimPrefix(srv.URL, "http://"), nil)
	p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	sock := filepath.Join(shortTempDir(t), "proxy.sock")
	if err := p.ListenUnix("notes", sock); err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "sandbox"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
	c := &http.Client{Transport: tr}
	resp, err := c.Get("http://api.example.com/") // no credential: the socket says who it is
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	p.Unregister("notes")
	if resp, err := c.Get("http://api.example.com/"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden { // a connection kept from before: refused all the same
			t.Fatalf("after Unregister: %d", resp.StatusCode)
		}
	}
}

func TestMatches(t *testing.T) {
	for _, c := range []struct {
		host string
		ok   bool
	}{{"api.example.com", true}, {"API.example.com.", true}, {"x.api.example.com", false}, {"a.cdn.example.net", true},
		{"cdn.example.net", false}, {"evilcdn.example.net", false}} {
		if got := Matches(c.host, []string{"api.example.com", "*.cdn.example.net"}); got != c.ok {
			t.Errorf("%s: %v", c.host, got)
		}
	}
}

// localServer is a service on the hub machine's loopback: it echoes the Host it got and the path, and on an upgrade
// (a WebSocket's handshake) switches to echoing bytes.
func localServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "echo" {
			c, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\nX-Host: %s\r\n\r\n", r.Host)
			_ = buf.Flush()
			_, _ = io.Copy(c, buf)
			return
		}
		fmt.Fprintf(w, "local %s %s %s", r.Host, r.URL.Path, r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return srv, port
}

func TestProxyLocalHost(t *testing.T) {
	_, port := localServer(t)
	p := newProxy(t, "127.0.0.1:1", nil)
	get := func(purl, u string) (int, string) {
		t.Helper()
		resp, err := client(purl).Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	at := "http://" + LocalHost + ":" + port + "/agent/v1"
	// Every loopback address listed as it is lets it in; the manifest needs no change.
	for _, hosts := range [][]string{{"127.0.0.1"}, {"localhost"}, {"::1"}, {LocalHost}, {"api.example.com", "127.0.0.1"}} {
		purl := p.Register("lark", Rule{Hosts: hosts})
		code, body := get(purl, at)
		if code != 200 || body != "local 127.0.0.1:"+port+" /agent/v1 " {
			t.Fatalf("%v: %d %q (Host goes on as the loopback, no proxy credential)", hosts, code, body)
		}
	}
	// No loopback address listed: refused, counted and logged as any other host.
	for _, r := range []Rule{{Hosts: []string{"api.example.com"}}, {Hub: true}, {Hosts: []string{"127.0.0.2"}}, {}} {
		var logged []string
		p.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
		purl := p.Register("lark", r)
		before := p.Stats("lark")
		code, body := get(purl, at)
		if code != 403 || !strings.Contains(body, "permissions.network") {
			t.Fatalf("%+v: %d %q", r, code, body)
		}
		if st := p.Stats("lark"); st.Denied != before.Denied+1 || st.LastDenied != LocalHost {
			t.Fatalf("%+v: counts %+v", r, st)
		}
		if len(logged) != 1 || !strings.Contains(logged[0], "refused "+LocalHost) {
			t.Fatalf("%+v: log %v", r, logged)
		}
	}
	p.Logf = nil
	// Not back into the proxy itself.
	purl := p.Register("lark", Rule{Hosts: []string{"127.0.0.1"}})
	_, own, _ := net.SplitHostPort(p.Addr())
	if code, body := get(purl, "http://"+LocalHost+":"+own+"/"); code != 403 || !strings.Contains(body, "this proxy") {
		t.Fatalf("the proxy's own port: %d %q", code, body)
	}
	// No port: the scheme's.
	var dialed []string
	dial := p.Dial
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, fmt.Errorf("not here")
	}
	if code, _ := get(purl, "http://"+LocalHost+"/"); code != http.StatusBadGateway || len(dialed) != 1 || dialed[0] != "127.0.0.1:80" {
		t.Fatalf("no port: %d %v", code, dialed)
	}
	p.Dial = dial
}

func TestProxyLocalHostConnect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls ok "+r.Host) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	p := newProxy(t, "127.0.0.1:1", nil)
	tlsClient := func(purl string) *http.Client {
		u, _ := url.Parse(purl)
		tr := srv.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(u)
		tr.TLSClientConfig.InsecureSkipVerify = true
		return &http.Client{Transport: tr}
	}
	at := "https://" + LocalHost + ":" + port + "/"
	resp, err := tlsClient(p.Register("lark", Rule{Hosts: []string{"127.0.0.1"}})).Get(at)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "tls ok "+LocalHost+":"+port { // inside TLS the proxy sees nothing: the name goes on as it is
		t.Fatalf("CONNECT: %q", b)
	}
	if _, err := tlsClient(p.Register("lark", Rule{Hosts: []string{"api.example.com"}})).Get(at); err == nil {
		t.Fatal("CONNECT to the loopback without it listed went through")
	}
	_, own, _ := net.SplitHostPort(p.Addr())
	if _, err := tlsClient(p.Register("lark", Rule{Hosts: []string{"127.0.0.1"}})).Get("https://" + LocalHost + ":" + own + "/"); err == nil {
		t.Fatal("CONNECT to the proxy's own port went through")
	}
	if st := p.Stats("lark"); st.Allowed != 1 || st.Denied != 2 {
		t.Fatalf("counts %+v", st)
	}
}

func TestProxyLocalHostUpgrade(t *testing.T) {
	_, port := localServer(t)
	p := newProxy(t, "127.0.0.1:1", nil)
	purl := p.Register("lark", Rule{Hosts: []string{"127.0.0.1"}})
	req, _ := http.NewRequest("GET", "http://"+LocalHost+":"+port+"/agent/v1/events", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")
	resp, err := client(purl).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("X-Host") != "127.0.0.1:"+port {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("body %T", resp.Body)
	}
	if _, err := io.WriteString(rw, "ping"); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(rw, b); err != nil || string(b) != "ping" {
		t.Fatalf("echo %q %v", b, err)
	}
	// Refused all the same without the loopback listed.
	purl = p.Register("lark", Rule{Hosts: []string{"api.example.com"}})
	resp2, err := client(purl).Do(req.Clone(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("upgrade without the loopback listed: %d", resp2.StatusCode)
	}
}

func TestRuleLoopback(t *testing.T) {
	for _, c := range []struct {
		hosts []string
		ok    bool
	}{{[]string{"127.0.0.1"}, true}, {[]string{"::1"}, true}, {[]string{"[::1]"}, true}, {[]string{"localhost"}, true},
		{[]string{LocalHost}, true}, {[]string{"127.0.0.2"}, false}, {[]string{"*.persona.internal"}, false}, {nil, false}} {
		if got := (Rule{Hosts: c.hosts}).Loopback(); got != c.ok {
			t.Errorf("%v: %v", c.hosts, got)
		}
	}
}

// TestProxyTunnelHalfClose: an extension that shuts its write side of a CONNECT tunnel (nc -N, the sandbox's relay
// passing on a half-close) still gets the reply the far end sends after reading to EOF.
func TestProxyTunnelHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if b, _ := io.ReadAll(c); string(b) == "ping" {
					_, _ = io.WriteString(c, "reply")
				}
			}()
		}
	}()
	p := newProxy(t, ln.Addr().String(), nil)
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	pu, _ := url.Parse(purl)
	sock := filepath.Join(shortTempDir(t), "proxy.sock")
	if err := p.ListenUnix("notes", sock); err != nil {
		t.Fatal(err)
	}
	auth := ""
	if pass, ok := pu.User.Password(); ok {
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(pu.User.Username()+":"+pass)) + "\r\n"
	}
	for _, c := range []struct{ name, network, addr, auth string }{
		{"loopback", "tcp", pu.Host, auth},
		{"unix socket", "unix", sock, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn, err := net.Dial(c.network, c.addr)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			fmt.Fprintf(conn, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n%s\r\n", c.auth)
			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("CONNECT: %d", resp.StatusCode)
			}
			if _, err := io.WriteString(conn, "ping"); err != nil {
				t.Fatal(err)
			}
			if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(br)
			if err != nil || string(b) != "reply" {
				t.Fatalf("after a half-close: %q %v", b, err)
			}
		})
	}
}

func TestProxyForwardCutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// More than the proxy's own write buffer, so its head and part of the body reach the client first.
		_, _ = io.WriteString(w, "{\"half\":\""+strings.Repeat("x", 16<<10))
		w.(http.Flusher).Flush() // chunked, the head and the first chunk are out
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close() // the far server goes away mid-body
	}))
	defer srv.Close()
	p := newProxy(t, strings.TrimPrefix(srv.URL, "http://"), nil)
	logged := make(chan string, 4)
	p.Logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})

	resp, err := client(purl).Get("http://api.example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("a body cut off upstream reads as complete: %d bytes", len(b))
	}
	select {
	case m := <-logged:
		if !strings.Contains(m, "notes") || !strings.Contains(m, "cut off") {
			t.Fatalf("log %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cut off answer is not logged")
	}
}

func TestProxyForwardClientGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 16<<10))
		w.(http.Flusher).Flush()
		select { // keep the body open until the proxy drops the request
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	p := newProxy(t, strings.TrimPrefix(srv.URL, "http://"), nil)
	logged := make(chan string, 4)
	p.Logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	purl := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})

	resp, err := client(purl).Get("http://api.example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resp.Body.Read(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() // the extension's client gives up mid-download
	select {
	case m := <-logged:
		t.Fatalf("a client giving up is logged as an upstream cut-off: %q", m)
	case <-time.After(time.Second):
	}
}
