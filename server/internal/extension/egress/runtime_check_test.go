package egress

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runtimeCheckProxy is a real runtime broker with synthetic public DNS answers pinned to local HTTP upstreams.
// The configured targets still select hosts and ports; only the final dial is mapped to the test listeners.
func runtimeCheckProxy(t *testing.T, saved string, handlers map[string]http.Handler) (*Proxy, *atomic.Int64) {
	t.Helper()
	addresses := map[string]string{}
	resolved := map[string][]net.IP{}
	servers := map[string]*httptest.Server{}
	next := 1
	for host, handler := range handlers {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		servers[host] = srv
		ip := net.ParseIP(fmt.Sprintf("192.0.2.%d", next))
		next++
		resolved[host] = []net.IP{ip}
		addresses[ip.String()] = strings.TrimPrefix(srv.URL, "http://")
	}
	var dialed atomic.Int64
	p := &Proxy{
		Resolve: func(_ context.Context, host string) ([]net.IP, error) {
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			if ips := resolved[host]; ips != nil {
				return ips, nil
			}
			if host == "mixed.test" {
				return []net.IP{net.ParseIP("192.0.2.20"), net.ParseIP("127.0.0.1")}, nil
			}
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.Add(1)
			host, _, err := net.SplitHostPort(addr)
			if err != nil || addresses[host] == "" {
				return nil, fmt.Errorf("unexpected synthetic dial %q", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addresses[host])
		},
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
	}
	p.Register("source", Rule{Targets: []Target{{Host: saved, Port: "80", HTTP: true}}})
	return p, &dialed
}

func runtimeCheckTarget(host string) []Target {
	return []Target{{Host: host, Port: "80", HTTP: true}}
}

// runtimeCheckCall makes one request on the source runtime's fixed-identity broker. cred is delivered as the
// settings-check proxy credential, never as a guest-selected extension identity.
func runtimeCheckCall(p *Proxy, ctx context.Context, cred, target string, body []byte) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]any{
		"url": target, "method": http.MethodGet, "headers": map[string]string{},
		"body": base64.StdEncoding.EncodeToString(body),
	})
	r := httptest.NewRequest(http.MethodPost, RuntimeFetchPath, bytes.NewReader(raw)).WithContext(ctx)
	if cred != "" {
		r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(User+":"+cred)))
	}
	w := httptest.NewRecorder()
	p.handler("source").ServeHTTP(w, r)
	return w
}

func TestRuntimeCheckCandidateBeforeSave(t *testing.T) {
	p, _ := runtimeCheckProxy(t, "a.test", map[string]http.Handler{
		"a.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "saved A") }),
		"b.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "candidate B") }),
	})
	p.Register("other", Rule{Targets: runtimeCheckTarget("a.test")})
	cred, revoke, ok := p.Check("source", runtimeCheckTarget("b.test"), time.Minute)
	if !ok {
		t.Fatal("source registration refused its settings check")
	}
	defer revoke()

	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/health", nil); got.Code != http.StatusOK || got.Body.String() != "candidate B" {
		t.Fatalf("candidate B before save: status %d body %q, want the real B upstream", got.Code, got.Body.String())
	}
	if got := runtimeCheckCall(p, t.Context(), "", "http://a.test/health", nil); got.Code != http.StatusOK || got.Body.String() != "saved A" {
		t.Fatalf("ordinary request while B is checked: %d %q", got.Code, got.Body.String())
	}
	for name, tc := range map[string][2]string{
		"ordinary credential cannot use B":   {"", "http://b.test/"},
		"candidate does not inherit saved A": {cred, "http://a.test/"},
		"arbitrary destination":              {cred, "http://arbitrary.test/"},
	} {
		c, target := tc[0], tc[1]
		if got := runtimeCheckCall(p, t.Context(), c, target, nil); got.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, got.Code)
		}
	}
	other, otherRevoke, _ := p.Check("other", runtimeCheckTarget("b.test"), time.Minute)
	defer otherRevoke()
	if got := runtimeCheckCall(p, t.Context(), other, "http://a.test/", nil); got.Code != http.StatusForbidden {
		t.Fatalf("another extension's candidate inherited saved A: %d, want 403", got.Code)
	}
	revoke()
	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/", nil); got.Code != http.StatusForbidden {
		t.Fatalf("completed/revoked candidate still reaches B: %d", got.Code)
	}
	if got := runtimeCheckCall(p, t.Context(), "", "http://a.test/", nil); got.Code != http.StatusOK {
		t.Fatalf("revoking the check disturbed saved A: %d", got.Code)
	}
}

func TestRuntimeCheckRetainsBrokerPolicy(t *testing.T) {
	p, dialed := runtimeCheckProxy(t, "a.test", map[string]http.Handler{
		"a.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }),
		"b.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirect" {
				w.Header().Set("Location", "http://a.test/after")
				w.WriteHeader(http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "B")
		}),
	})
	cred, revoke, _ := p.Check("source", runtimeCheckTarget("b.test"), time.Minute)
	defer revoke()
	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/", nil); got.Code != http.StatusOK {
		t.Fatalf("candidate B did not reach the broker path under test: %d", got.Code)
	}
	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/redirect", nil); got.Code != http.StatusFound {
		t.Fatalf("candidate redirect was not preserved for normal redirect handling: %d", got.Code)
	}
	if got := runtimeCheckCall(p, t.Context(), cred, "http://a.test/after", nil); got.Code != http.StatusForbidden {
		t.Fatalf("redirect inherited saved A instead of reusing candidate policy: %d", got.Code)
	}
	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/", make([]byte, RuntimeBodyMost+1)); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("candidate request body limit: %d, want 413", got.Code)
	}

	p.mu.Lock()
	p.runtimeActive = map[string]int{"source": 4}
	p.runtimeTotal = 4
	p.mu.Unlock()
	if got := runtimeCheckCall(p, t.Context(), cred, "http://b.test/", nil); got.Code != http.StatusTooManyRequests {
		t.Fatalf("candidate bypassed runtime broker admission: %d, want 429", got.Code)
	}
	p.mu.Lock()
	p.runtimeActive = map[string]int{}
	p.runtimeTotal = 0
	p.mu.Unlock()

	for _, target := range []Target{{Host: "224.0.0.1", Port: "80", HTTP: true}, {Host: "mixed.test", Port: "80", HTTP: true}} {
		protected, end, _ := p.Check("source", []Target{target}, time.Minute)
		before := dialed.Load()
		if got := runtimeCheckCall(p, t.Context(), protected, "http://"+target.Host+"/", nil); got.Code != http.StatusBadGateway {
			t.Errorf("protected/DNS-rebinding candidate %s: %d, want 502", target.Host, got.Code)
		}
		if after := dialed.Load(); after != before {
			t.Errorf("protected/DNS-rebinding candidate %s was dialed (%d -> %d)", target.Host, before, after)
		}
		end()
	}
}

func TestRuntimeCheckConcurrentIsolation(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	upstream := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			started <- name
			<-release
			_, _ = io.WriteString(w, name)
		})
	}
	p, _ := runtimeCheckProxy(t, "a.test", map[string]http.Handler{
		"a.test": upstream("A"), "b.test": upstream("B"), "c.test": upstream("C"),
	})
	bTargets := runtimeCheckTarget("b.test")
	credB, endB, _ := p.Check("source", bTargets, time.Minute)
	defer endB()
	credC, endC, _ := p.Check("source", runtimeCheckTarget("c.test"), time.Minute)
	defer endC()
	if credB == credC {
		t.Fatal("concurrent checks share a credential")
	}
	bTargets[0].Host = "arbitrary.test" // Check owns an immutable candidate snapshot.

	type answer struct {
		name, body string
		code       int
	}
	answers := make(chan answer, 3)
	go func() {
		w := runtimeCheckCall(p, t.Context(), "", "http://a.test/", nil)
		answers <- answer{"A", w.Body.String(), w.Code}
	}()
	go func() {
		w := runtimeCheckCall(p, t.Context(), credB, "http://b.test/", nil)
		answers <- answer{"B", w.Body.String(), w.Code}
	}()
	go func() {
		w := runtimeCheckCall(p, t.Context(), credC, "http://c.test/", nil)
		answers <- answer{"C", w.Body.String(), w.Code}
	}()
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatalf("ordinary/check requests did not overlap on their own upstreams: %v", seen)
		}
	}
	close(release)
	for range 3 {
		got := <-answers
		if got.code != http.StatusOK || got.body != got.name {
			t.Errorf("%s request: %d %q; identity/target leaked", got.name, got.code, got.body)
		}
	}
}

func TestRuntimeCheckEndsPendingIO(t *testing.T) {
	for _, mode := range []string{"revoke", "caller cancellation", "expiry", "registration replacement"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			var once sync.Once
			p, _ := runtimeCheckProxy(t, "a.test", map[string]http.Handler{
				"a.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }),
				"b.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/wait" {
						_, _ = io.WriteString(w, "B")
						return
					}
					close(started)
					<-r.Context().Done()
					once.Do(func() { close(canceled) })
				}),
			})
			ttl := time.Minute
			if mode == "expiry" {
				ttl = 100 * time.Millisecond
			}
			cred, revoke, _ := p.Check("source", runtimeCheckTarget("b.test"), ttl)
			defer revoke()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- runtimeCheckCall(p, ctx, cred, "http://b.test/wait", nil) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("candidate B never started pending I/O; it was held to saved A")
			}
			switch mode {
			case "revoke":
				revoke()
			case "caller cancellation":
				cancel()
			case "registration replacement":
				p.Register("source", Rule{Targets: runtimeCheckTarget("a.test")})
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("candidate upstream I/O outlived its authority")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("candidate broker request retained its admission after teardown")
			}
			if got := runtimeCheckCall(p, t.Context(), "", "http://a.test/", nil); got.Code != http.StatusOK {
				t.Fatalf("ordinary saved-A I/O was disturbed: %d", got.Code)
			}
		})
	}
}

func TestRuntimeCheckResponseBodyLimit(t *testing.T) {
	p, _ := runtimeCheckProxy(t, "a.test", map[string]http.Handler{
		"a.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }),
		"b.test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", RuntimeBodyMost+1))
		}),
	})
	cred, revoke, _ := p.Check("source", runtimeCheckTarget("b.test"), time.Minute)
	defer revoke()
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("candidate oversized response was not cut off by the runtime body bound: %v", recovered)
		}
	}()
	_ = runtimeCheckCall(p, t.Context(), cred, "http://b.test/", nil)
}
