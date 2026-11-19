package link

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type diagnosticLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *diagnosticLines) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}
func (l *diagnosticLines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
func diagnosticContext(ctx context.Context, l *diagnosticLines) context.Context {
	return context.WithValue(ctx, diagnosticLogKey{}, l.logf)
}
func requireDiagnostic(t *testing.T, l *diagnosticLines, want ...string) {
	t.Helper()
	text := l.text()
	for _, part := range want {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q: %s", part, text)
		}
	}
	for _, forbidden := range []string{"127.0.0.1", "secret-token", "private-stream", "sensitive.invalid", "raw-private-error"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("leaked %q: %s", forbidden, text)
		}
	}
}

func TestDiagnosticDialRealTLSAndUpgrade(t *testing.T) {
	for _, role := range []string{"control", "stream"} {
		t.Run(role, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err == nil {
					defer c.CloseNow()
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			logs := &diagnosticLines{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client := pinned(pinOf(srv), nil)
			defer client.CloseIdleConnections()
			c, _, err := diagnosticDial(diagnosticContext(ctx, logs), wsURL(srv.URL, "/private-stream"), &websocket.DialOptions{HTTPClient: client, HTTPHeader: bearer("secret-token")}, role, "lan", 2)
			if err != nil {
				t.Fatal(err)
			}
			c.CloseNow()
			requireDiagnostic(t, logs, "role="+role, "candidate=2 kind=lan", "phase=upgrade outcome=success", "dns_us=0", "tcp_us=", "tls_us=", "upgrade_us=")
		})
	}
}

func TestDiagnosticDialFailedTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	logs := &diagnosticLines{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := pinned("unused", nil)
	defer client.CloseIdleConnections()
	_, _, err = diagnosticDial(diagnosticContext(ctx, logs), "wss://"+addr, &websocket.DialOptions{HTTPClient: client}, "control", "raw-private-error", 1)
	if err == nil {
		t.Fatal("TCP unexpectedly connected")
	}
	requireDiagnostic(t, logs, "kind=other phase=tcp outcome=error", "tls_us=0 upgrade_us=0")
}

func TestDiagnosticDialStalledTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	logs := &diagnosticLines{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	client := pinned("unused", nil)
	defer client.CloseIdleConnections()
	start := time.Now()
	_, _, err = diagnosticDial(diagnosticContext(ctx, logs), "wss://"+listener.Addr().String(), &websocket.DialOptions{HTTPClient: client}, "control", "lan", 1)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline changed: %v", err)
	}
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(time.Second):
		t.Fatal("no TCP connection")
	}
	requireDiagnostic(t, logs, "phase=tls outcome=timeout", "upgrade_us=0")
}

func TestDiagnosticDialStalledUpgradeAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelEarly), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
			defer srv.Close()
			defer close(release)
			logs := &diagnosticLines{}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if cancelEarly {
				go func() { <-entered; cancel() }()
			}
			client := pinned(pinOf(srv), nil)
			defer client.CloseIdleConnections()
			start := time.Now()
			_, _, err := diagnosticDial(diagnosticContext(ctx, logs), wsURL(srv.URL, "/private-stream"), &websocket.DialOptions{HTTPClient: client}, "stream", "lan", 1)
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("deadline changed: %v", err)
			}
			outcome := "timeout"
			if cancelEarly {
				outcome = "canceled"
			}
			requireDiagnostic(t, logs, "phase=upgrade outcome="+outcome)
		})
	}
}

func TestDiagnosticDialRefusalAndPinCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "raw-private-error", http.StatusForbidden) }))
	defer srv.Close()
	for _, wrongPin := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongPin), func(t *testing.T) {
			logs := &diagnosticLines{}
			fp := pinOf(srv)
			if wrongPin {
				fp = "unused"
			}
			client := pinned(fp, nil)
			defer client.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, resp, err := diagnosticDial(diagnosticContext(ctx, logs), wsURL(srv.URL, ""), &websocket.DialOptions{HTTPClient: client}, "control", "lan", 1)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if wrongPin {
				if resp != nil {
					t.Fatal("pin accepted")
				}
				requireDiagnostic(t, logs, "phase=tls outcome=error")
			} else {
				if resp.StatusCode != 403 {
					t.Fatal(resp.StatusCode)
				}
				requireDiagnostic(t, logs, "phase=upgrade outcome=http_refused")
			}
		})
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDiagnosticDialDNSAndConcurrentTCPRedaction(t *testing.T) {
	logs := &diagnosticLines{}
	client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		trace.DNSStart(httptrace.DNSStartInfo{Host: "sensitive.invalid"})
		trace.DNSDone(httptrace.DNSDoneInfo{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				trace.ConnectStart("tcp", "sensitive.invalid")
				trace.ConnectDone("tcp", "sensitive.invalid", errors.New("raw-private-error"))
			})
		}
		wg.Wait()
		return nil, errors.New("raw-private-error")
	})}
	_, _, err := diagnosticDial(diagnosticContext(context.Background(), logs), "wss://sensitive.invalid/private-stream", &websocket.DialOptions{HTTPClient: client}, "control", "name", 1)
	if err == nil {
		t.Fatal("expected synthetic failure")
	}
	requireDiagnostic(t, logs, "phase=tcp outcome=error", "dns_us=", "tcp_us=")
}

func TestDiagnosticWaitWakeReasons(t *testing.T) {
	for _, reason := range []string{"timer", "canceled", "network_check", "redial", "config_changed"} {
		t.Run(reason, func(t *testing.T) {
			logs := &diagnosticLines{}
			check := make(chan struct{}, 1)
			redial := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "link.json")
			o := Options{Path: path, Check: check, Redial: redial, PairCheck: time.Millisecond, Logf: logs.logf}
			d := time.Hour
			switch reason {
			case "timer":
				d = 0
			case "canceled":
				cancel()
			case "network_check":
				check <- struct{}{}
			case "redial":
				redial <- struct{}{}
			case "config_changed":
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			o.wait(ctx, d, time.Time{})
			requireDiagnostic(t, logs, "retry_wait planned_us=", "actual_us=", "wake="+reason)
		})
	}
}

func TestDiagnosticRunSeparatesControlAndNodeStream(t *testing.T) {
	node := newFakeNode(t)
	logs := &diagnosticLines{}
	loggedStream := make(chan struct{}, 1)
	logf := func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		if strings.HasPrefix(line, "link: dial ") {
			logs.logf("%s", line)
		}
		if strings.Contains(line, "role=stream") {
			select {
			case loggedStream <- struct{}{}:
			default:
			}
		}
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if r.URL.Path == StreamPath {
			_, _, _ = c.Read(r.Context())
			return
		}
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		if err := c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"open","id":"private-stream"}`)); err != nil {
			return
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	ch := runAgainst(t, srv, "secret-token", Options{Target: node.path, Logf: logf})
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	select {
	case <-loggedStream:
	case <-time.After(3 * time.Second):
		t.Fatal("no node stream diagnostic")
	}
	requireDiagnostic(t, logs, "role=control candidate=1 kind=lan phase=upgrade outcome=success", "role=stream candidate=1 kind=lan phase=upgrade outcome=success")
}

func TestDiagnosticDialPreservesCallerTrace(t *testing.T) {
	called := false
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{DNSStart: func(httptrace.DNSStartInfo) { called = true }})
	logs := &diagnosticLines{}
	client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).DNSStart(httptrace.DNSStartInfo{Host: "sensitive.invalid"})
		return nil, errors.New("raw-private-error")
	})}
	_, _, err := diagnosticDial(diagnosticContext(ctx, logs), "wss://sensitive.invalid", &websocket.DialOptions{HTTPClient: client}, "control", "name", 1)
	if err == nil || !called {
		t.Fatalf("caller trace lost: called=%v err=%v", called, err)
	}
	requireDiagnostic(t, logs, "phase=dns outcome=error")
}

func TestDiagnosticDialLateConnectCannotRegressPhase(t *testing.T) {
	logs := &diagnosticLines{}
	client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{})
		trace.ConnectStart("tcp", "sensitive.invalid")
		trace.DNSStart(httptrace.DNSStartInfo{Host: "sensitive.invalid"})
		trace.ConnectDone("tcp", "sensitive.invalid", context.Canceled)
		return nil, errors.New("raw-private-error")
	})}
	_, _, _ = diagnosticDial(diagnosticContext(context.Background(), logs), "wss://sensitive.invalid", &websocket.DialOptions{HTTPClient: client}, "control", "name", 1)
	requireDiagnostic(t, logs, "phase=upgrade outcome=error")
}

func TestDiagnosticDialKeepsFirstTLSMeasurement(t *testing.T) {
	logs := &diagnosticLines{}
	client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		trace.TLSHandshakeStart()
		trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
		// The transport's second handshake is already complete. Its callbacks must not
		// replace or add to the real custom handshake measurement, even if delayed.
		trace.TLSHandshakeStart()
		<-time.NewTimer(25 * time.Millisecond).C
		trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
		trace.GotConn(httptrace.GotConnInfo{})
		return nil, errors.New("raw-private-error")
	})}
	_, _, _ = diagnosticDial(diagnosticContext(context.Background(), logs), "wss://sensitive.invalid", &websocket.DialOptions{HTTPClient: client}, "control", "name", 1)
	var measured, total int64
	for _, field := range strings.Fields(logs.text()) {
		if strings.HasPrefix(field, "tls_us=") {
			fmt.Sscanf(field, "tls_us=%d", &measured)
		}
		if strings.HasPrefix(field, "total_us=") {
			fmt.Sscanf(field, "total_us=%d", &total)
		}
	}
	if total < 25000 || measured >= total/2 {
		t.Fatalf("completed handshake counted twice: %s", logs.text())
	}
	requireDiagnostic(t, logs, "phase=upgrade outcome=error")
}

func TestDiagnosticDialThroughHTTPSProxy(t *testing.T) {
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	hub, _ := fakeHub(t, func(c *websocket.Conn) { _, _, _ = c.Read(context.Background()) })
	cert, roots := selfSigned(t, "127.0.0.1")
	proxy := newConnectProxy(t, hub.Listener.Addr().String(), &cert)
	t.Cleanup(UseRootsForTesting(roots))
	cfg := &Config{Token: "jd_x", FP: pinOf(hub), Proxy: proxy.url,
		Endpoints: []Endpoint{{URL: unroutable, Kind: "ipv4"}}}

	logs := &diagnosticLines{}
	ctx := diagnosticContext(context.Background(), logs)
	conn, err := dialProxy(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn.closeNow()
	requireDiagnostic(t, logs, "role=control candidate=1 kind=proxy phase=upgrade outcome=success")

	cfg.FP = "different-pin"
	if _, err := dialProxy(ctx, cfg); !errors.Is(err, errPin) {
		t.Fatalf("wrong hub key through a trusted proxy: %v", err)
	}
	requireDiagnostic(t, logs, "role=control candidate=1 kind=proxy phase=tls outcome=error")
	for _, private := range []string{"192.0.2.1", proxyUser, proxySecret, cfg.Token, cfg.FP} {
		if strings.Contains(logs.text(), private) {
			t.Fatalf("diagnostics exposed %q: %s", private, logs.text())
		}
	}
}
