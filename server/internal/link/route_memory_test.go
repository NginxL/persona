package link

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func routeTestAddresses(ips ...string) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) {
		addrs := make([]net.Addr, 0, len(ips))
		for _, raw := range ips {
			ip, addr, _ := net.ParseCIDR(raw)
			addr.IP = ip
			addrs = append(addrs, addr)
		}
		return addrs, nil
	}
}

func startRouteMemoryRun(t *testing.T, o Options) (chan Status, func()) {
	t.Helper()
	statuses := make(chan Status, 32)
	o.Status = func(s Status) { statuses <- s }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, o)
	}()
	stop := func() { stopDirectHandoffRun(t, cancel, done) }
	t.Cleanup(stop)
	return statuses, stop
}

func TestRouteMemoryRestart(t *testing.T) {
	for _, kind := range []string{KindProxy, KindSSH} {
		t.Run(kind, func(t *testing.T) {
			for _, key := range []string{"https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "no_proxy", "NO_PROXY"} {
				t.Setenv(key, "")
			}
			hub := newSSHTestHub(t, "")
			var allowDirect atomic.Bool
			var directAttempts atomic.Int32
			direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				directAttempts.Add(1)
				if !allowDirect.Load() {
					// Simulate a slow direct failure; this delay is the network behavior, not test synchronization.
					timer := time.NewTimer(120 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-r.Context().Done():
						return
					case <-timer.C:
					}
					http.Error(w, "direct link unavailable", http.StatusServiceUnavailable)
					return
				}
				hub.srv.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(direct.Close)
			if pinOf(direct) != hub.pin {
				t.Fatal("the test's direct and fallback servers must share a pin")
			}
			cfg := Config{Hub: "hub", Machine: "laptop", Token: "jd_ssh", FP: hub.pin,
				Endpoints: []Endpoint{{URL: direct.URL, Kind: "name"}}}
			o := Options{
				Path:             filepath.Join(t.TempDir(), "link.json"),
				DirectRetryEvery: time.Hour,
				routeAddrs:       routeTestAddresses("192.0.2.5/24", "127.0.0.1/8", "2001:db8::8/64"),
			}
			var fallbackAttempts func() int
			if kind == KindProxy {
				proxy := newConnectProxy(t, hub.target, nil)
				cfg.Proxy = proxy.url
				cfg.Endpoints[0].URL = "https://" + proxyableLocalhost(t) + ":" + portOf(direct.URL)
				fallbackAttempts = func() int { return int(proxy.connects.Load()) }
			} else {
				ssh := newSSHHarness(t)
				o.SSHBin = ssh.bin
				cfg.SSH = &SSH{Server: "user@example.com", Target: hub.target}
				fallbackAttempts = func() int { return len(ssh.runs()) }
			}
			if err := Update(o.Path, func(c *Config) error { *c = cfg; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(RouteHintPath(o.Path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new pairing has unexpected route hint (%v)", err)
			}

			connected := func(statuses chan Status, kind string) Status {
				t.Helper()
				st := waitFor(t, statuses, func(s Status) bool {
					return s.State == "connected" && (kind == "" || s.Kind == kind)
				})
				// The client reports connected after writing hello. Let the hub finish its hello responses before
				// the test stops the run or triggers another connection.
				select {
				case <-hub.links:
				case <-time.After(5 * time.Second):
					t.Fatal("hub did not finish accepting the link")
				}
				return st
			}
			checkRoute := func(phase, got, want string, direct, fallback int) {
				t.Helper()
				if d, f := int(directAttempts.Load()), fallbackAttempts(); got != want || d != direct || f != fallback {
					t.Fatalf("%s route %q with %d direct and %d fallback attempts, want %q with %d and %d", phase, got, d, f, want, direct, fallback)
				}
			}
			start := time.Now()
			statuses, stop := startRouteMemoryRun(t, o)
			cold := connected(statuses, "")
			t.Logf("cold start reached %s in %v", cold.Kind, time.Since(start))
			checkRoute("cold start", cold.Kind, kind, 1, 1)
			stop()
			info, err := os.Stat(RouteHintPath(o.Path))
			if err != nil {
				t.Fatal(err)
			}
			// Windows does not report Unix permission bits through FileMode.
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("cache permissions %o, want 0600", info.Mode().Perm())
			}
			saved, err := os.ReadFile(RouteHintPath(o.Path))
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{cfg.Token, proxySecret, proxyUser, "example.com", "192.0.2.5", "2001:db8::8"} {
				if bytes.Contains(saved, []byte(value)) {
					t.Fatal("route hint contains raw pairing, proxy, tunnel or address data")
				}
			}

			// Address order, duplicates, loopback and IPv6 privacy address rotation do not change the network.
			// Timed probes remain disabled so every dial here comes from the startup route order.
			o.routeAddrs = routeTestAddresses("2001:db8::9/64", "192.0.2.5/24", "2001:db8::a/64", "::1/128")
			start = time.Now()
			statuses, stop = startRouteMemoryRun(t, o)
			warm := connected(statuses, "")
			t.Logf("restart reached %s in %v", warm.Kind, time.Since(start))
			checkRoute("restart", warm.Kind, kind, 1, 2)
			stop()
			if after, err := os.Stat(RouteHintPath(o.Path)); err != nil || !after.ModTime().Equal(info.ModTime()) {
				t.Fatalf("unchanged route hint was rewritten or is missing (%v)", err)
			}

			// The original IPv4 address disappeared while other addresses stayed, including SSH's loopback.
			o.routeAddrs = routeTestAddresses("2001:db8::8/64", "127.0.0.1/8")
			statuses, stop = startRouteMemoryRun(t, o)
			checkRoute("changed network", connected(statuses, "").Kind, kind, 2, 3)
			stop()

			o.DirectRetryEvery = 50 * time.Millisecond
			statuses, stop = startRouteMemoryRun(t, o)
			if got := connected(statuses, "").Kind; got != kind {
				t.Fatalf("before enabling direct, got %s, want %s", got, kind)
			}
			allowDirect.Store(true)
			connected(statuses, "name")
			stop()
			if _, err := os.Stat(RouteHintPath(o.Path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("direct handoff did not clear the route hint (%v)", err)
			}
			before := int(directAttempts.Load())
			statuses, stop = startRouteMemoryRun(t, o)
			checkRoute("restart after direct handoff", connected(statuses, "").Kind, "name", before+1, 4)
			stop()
		})
	}
}

func TestRouteMemoryInvalidation(t *testing.T) {
	t.Setenv("https_proxy", "http://first.example.com:3128")
	t.Setenv("no_proxy", "")
	addresses := routeTestAddresses("192.0.2.5/24", "2001:db8::8/64")
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, m *routeMemory, c *Config)
	}{
		{"credential", func(_ *testing.T, _ *routeMemory, c *Config) { c.Token = "new-token" }},
		{"hub identity", func(_ *testing.T, _ *routeMemory, c *Config) { c.Hub = "another-hub" }},
		{"SSH setting", func(_ *testing.T, _ *routeMemory, c *Config) { c.SSH.Target = "localhost:443" }},
		{"explicit proxy", func(_ *testing.T, _ *routeMemory, c *Config) { c.Proxy = "http://first.example.com:3128" }},
		{"environment proxy", func(t *testing.T, _ *routeMemory, _ *Config) {
			t.Setenv("https_proxy", "http://second.example.com:3128")
		}},
		{"environment bypass list", func(t *testing.T, _ *routeMemory, _ *Config) {
			t.Setenv("no_proxy", "other.example.com")
		}},
		{"IPv6 prefix changed", func(_ *testing.T, m *routeMemory, _ *Config) {
			m.addresses = routeTestAddresses("192.0.2.5/24", "2001:db8:1::8/64")
		}},
		{"missing file", func(t *testing.T, m *routeMemory, _ *Config) {
			if err := os.Remove(RouteHintPath(m.path)); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt file", func(t *testing.T, m *routeMemory, _ *Config) {
			if err := os.WriteFile(RouteHintPath(m.path), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Hub: "hub", Machine: "laptop", Token: "original-token", Trust: TrustSystem,
				Endpoints: []Endpoint{{URL: "https://hub.example.com"}}, SSH: &SSH{Server: "user@example.com"}}
			path := filepath.Join(t.TempDir(), "link.json")
			if err := Update(path, func(f *Config) error { *f = c; return nil }); err != nil {
				t.Fatal(err)
			}
			before := routeMemory{path: path, addresses: addresses}
			before.beforeDial(&c)
			before.connected(KindSSH)
			if h := readRouteHint(path); h.Kind != KindSSH {
				t.Fatalf("saved %q, want ssh", h.Kind)
			}

			after := routeMemory{path: path, addresses: addresses}
			tc.change(t, &after, &c)
			last := after.beforeDial(&c)
			if got := routeOrder(&c, true, last); last != "" || !slices.Equal(got, []string{"", KindProxy, KindSSH}) {
				t.Fatalf("invalidated hint %q produced routing order %v", last, got)
			}
			if _, err := os.Stat(RouteHintPath(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalidated cache was left on disk (%v)", err)
			}
		})
	}
}

func TestRouteMemoryNetworkChange(t *testing.T) {
	c, path := seedRouteMemory(t)
	m := routeMemory{path: path, addresses: routeTestAddresses("192.0.2.5/24", "2001:db8::8/64")}
	if got := m.beforeDial(&c); got != KindSSH {
		t.Fatalf("first dial: got %q, want ssh", got)
	}
	m.addresses = routeTestAddresses("192.0.2.5/24", "2001:db8::9/64")
	m.networkChanged()
	if got := m.beforeDial(&c); got != KindSSH || readRouteHint(path).Kind != KindSSH {
		t.Fatalf("rotating IPv6 on the same prefix lost the hint: got %q, want ssh", got)
	}
	m.addresses = routeTestAddresses("192.0.2.5/24", "2001:db8:1::9/64")
	m.networkChanged()
	if got := m.hint.Kind; got != "" || readRouteHint(path).Kind != "" {
		t.Fatalf("a different prefix kept the hint: got %q, want no hint", got)
	}
}

func TestRouteMemoryUnknownNetwork(t *testing.T) {
	for name, unknown := range map[string]func() ([]net.Addr, error){
		"loopback only": routeTestAddresses("127.0.0.1/8", "::1/128"),
		"lookup failed": func() ([]net.Addr, error) { return nil, errors.New("unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			c, path := seedRouteMemory(t)
			known := routeTestAddresses("192.0.2.5/24", "2001:db8::9/64")
			m := routeMemory{path: path, addresses: unknown}
			if got := m.beforeDial(&c); got != "" {
				t.Fatalf("used %q on an unknown network", got)
			}
			if saved := readRouteHint(path).Kind; saved != KindSSH {
				t.Fatalf("unknown network discarded the saved hint: got %q, want ssh", saved)
			}
			// A fallback that connects before the network is known must not overwrite the verified hint.
			m.connected(KindProxy)
			m.networkChanged()
			m.addresses = known
			if got := m.beforeDial(&c); got != KindSSH {
				t.Fatalf("network returned: got %q, want ssh", got)
			}
			m.addresses = unknown
			m.connected(KindSSH) // temporarily unknown when a dial that started on a known network succeeds
			m.networkChanged()
			m.addresses = known
			if got := m.beforeDial(&c); got != KindSSH {
				t.Fatalf("transient lookup failure: got %q, want ssh", got)
			}
			m.addresses = unknown
			c.Token = "new-token"
			if got := m.beforeDial(&c); got != "" || readRouteHint(path).Kind != "" {
				t.Fatalf("new credential kept the old hint on an unknown network: got %q", got)
			}
		})
	}
}

func seedRouteMemory(t *testing.T) (Config, string) {
	t.Helper()
	c := Config{Hub: "hub", Machine: "laptop", Token: "original-token", Trust: TrustSystem}
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(f *Config) error { *f = c; return nil }); err != nil {
		t.Fatal(err)
	}
	m := routeMemory{path: path, addresses: routeTestAddresses("192.0.2.5/24", "2001:db8::8/64")}
	m.beforeDial(&c)
	m.connected(KindSSH)
	if got := readRouteHint(path).Kind; got != KindSSH {
		t.Fatalf("seeded hint %q, want ssh", got)
	}
	return c, path
}

func TestRouteMemoryKeepsSSHOnlyOrder(t *testing.T) {
	for _, always := range []bool{true, false} {
		c := &Config{SSH: &SSH{Always: always}}
		if always {
			c.Endpoints = []Endpoint{{URL: "https://hub.example.com"}}
		}
		if got := routeOrder(c, true, KindProxy); !slices.Equal(got, []string{KindSSH}) {
			t.Errorf("SSH-only routing order %v with Always=%v", got, always)
		}
	}
}

func TestRouteMemoryDoesNotSaveOldPairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	c := Config{Hub: "hub", Machine: "laptop", Token: "old-token", Trust: TrustSystem}
	if err := Update(path, func(f *Config) error { *f = c; return nil }); err != nil {
		t.Fatal(err)
	}
	m := routeMemory{path: path, addresses: routeTestAddresses("192.0.2.5/24")}
	m.beforeDial(&c)
	if err := Update(path, func(f *Config) error { f.Token = "new-token"; return nil }); err != nil {
		t.Fatal(err)
	}
	m.connected(KindSSH)
	if _, err := os.Stat(RouteHintPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("found a route hint for the old pairing (%v)", err)
	}
}
