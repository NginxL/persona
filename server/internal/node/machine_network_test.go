package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRedactProxyValue(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		want      string
		forbidden []string
	}{
		{
			name:  "empty",
			input: "   ",
			want:  "",
		},
		{
			name:  "plain http proxy",
			input: "http://proxy.example.com:8080",
			want:  "http://proxy.example.com:8080",
		},
		{
			name:      "http user and password",
			input:     "http://alice:s3cr3tPass@proxy.example.com:8080",
			want:      "http://[redacted]@proxy.example.com:8080",
			forbidden: []string{"alice", "s3cr3tPass"},
		},
		{
			name:      "password containing special url chars and query token",
			input:     "https://alice:p@ss/w?o#rd@proxy.example.com:8443/path?token=topsecret#frag",
			want:      "https://[redacted]@proxy.example.com:8443/path",
			forbidden: []string{"alice", "p@ss", "topsecret", "frag"},
		},
		{
			name:      "socks5 user only",
			input:     "socks5://svcuser@proxy.example.com:1080",
			want:      "socks5://[redacted]@proxy.example.com:1080",
			forbidden: []string{"svcuser"},
		},
		{
			name:      "scheme-less user and password",
			input:     "bob:hidden-pass@proxy.example.com:3128",
			want:      "[redacted]@proxy.example.com:3128",
			forbidden: []string{"bob", "hidden-pass"},
		},
		{
			name:      "ipv6 host with credentials",
			input:     "http://carol:ipv6secret@[2001:db8::1]:8080",
			want:      "http://[redacted]@[2001:db8::1]:8080",
			forbidden: []string{"carol", "ipv6secret"},
		},
		{
			name:      "query secret without userinfo",
			input:     "http://proxy.example.com:8080/?auth=mysecretkey",
			want:      "http://proxy.example.com:8080/",
			forbidden: []string{"mysecretkey"},
		},
		{
			name:      "malformed empty host with credentials",
			input:     "http://dave:orphanpass@",
			want:      "http://[redacted]",
			forbidden: []string{"dave", "orphanpass"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactProxyValue(tc.input)
			if got != tc.want {
				t.Fatalf("redactProxyValue(%q) = %q, want %q", tc.input, got, tc.want)
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(got, bad) {
					t.Errorf("redacted value %q leaked %q", got, bad)
				}
			}
		})
	}
}

func TestMachineNetworkRPCRejectsCustomTargets(t *testing.T) {
	n := &Node{
		networkDiagAt: func() machineNetworkOptions {
			return machineNetworkOptions{
				getenv:     func(string) string { return "" },
				interfaces: func() ([]proto.MachineNetInterface, error) { return []proto.MachineNetInterface{}, nil },
				targets:    []networkProbeTarget{},
			}
		},
	}

	for _, bad := range []string{
		`{"target":"https://custom.example.com/meta-data"}`,
		`{"targets":["https://internal.example"]}`,
		`not-json`,
		`{}{}`,
	} {
		if _, err := n.handle(context.Background(), proto.MethodMachineNetwork, json.RawMessage(bad)); err == nil {
			t.Errorf("expected error for params %s, got nil", bad)
		}
	}

	for _, good := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		out, err := n.handle(context.Background(), proto.MethodMachineNetwork, good)
		if err != nil {
			t.Fatalf("handle(%s) error = %v", string(good), err)
		}
		rep, ok := out.(proto.MachineNetworkReport)
		if !ok {
			t.Fatalf("unexpected report type %T", out)
		}
		if rep.Interfaces == nil || rep.Probes == nil {
			t.Fatalf("expected non-nil slices, got %+v", rep)
		}
	}
}

func TestMachineNetworkRPCProbesTimeoutsAndRedactsCredentials(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	badGatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer badGatewaySrv.Close()

	slowBlock := make(chan struct{})
	defer close(slowBlock)
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-slowBlock:
		}
	}))
	defer slowSrv.Close()

	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()
	go func() {
		for {
			c, aErr := tcpLn.Accept()
			if aErr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	// Closed local port for a failing proxy so Go's HTTP transport fails with a proxyconnect error.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadProxyAddr := deadLn.Addr().String()
	_ = deadLn.Close()

	okURL, err := url.Parse(okSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	badURL, err := url.Parse(badGatewaySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	slowURL, err := url.Parse(slowSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	const (
		proxyUser = "diaguser"
		proxyPass = "s3cr3t-proxy-token-xyz"
		allPass   = "socks-secret-999"
	)

	env := map[string]string{
		"HTTP_PROXY":  "http://" + proxyUser + ":" + proxyPass + "@" + deadProxyAddr,
		"HTTPS_PROXY": "https://" + proxyUser + ":" + proxyPass + "@" + deadProxyAddr + "/?token=leakedquery",
		"ALL_PROXY":   "socks5://socksuser:" + allPass + "@proxy.example.com:1080",
		"NO_PROXY":    strings.Join([]string{okURL.Host, badURL.Host, slowURL.Host}, ","),
	}

	fixedTime := time.Date(2026, 11, 21, 7, 0, 0, 0, time.UTC)
	n := &Node{
		networkDiagAt: func() machineNetworkOptions {
			return machineNetworkOptions{
				getenv: func(k string) string { return env[k] },
				interfaces: func() ([]proto.MachineNetInterface, error) {
					return []proto.MachineNetInterface{
						{Name: "eth0", Up: true, Addresses: []string{"192.0.2.10"}},
					}, nil
				},
				targets: []networkProbeTarget{
					{ID: "direct_ok", Name: "Direct OK", Target: okSrv.URL, Protocol: "https"},
					{ID: "direct_502", Name: "Direct 502", Target: badGatewaySrv.URL, Protocol: "https"},
					{ID: "direct_timeout", Name: "Direct Timeout", Target: slowSrv.URL, Protocol: "https"},
					{ID: "proxied_fail", Name: "Proxied Target", Target: "http://proxied.example.com", Protocol: "https"},
					{ID: "tcp_ok", Name: "TCP OK", Target: tcpLn.Addr().String(), Protocol: "tcp"},
				},
				probeTimeout: 150 * time.Millisecond,
				totalTimeout: 2 * time.Second,
				now:          func() time.Time { return fixedTime },
			}
		},
	}

	start := time.Now()
	out, err := n.handle(context.Background(), proto.MethodMachineNetwork, json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("machine.network failed: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("probes did not honor bounded timeout: took %v", elapsed)
	}

	rep, ok := out.(proto.MachineNetworkReport)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	if rep.CheckedAt != fixedTime.Unix() {
		t.Errorf("CheckedAt = %d, want %d", rep.CheckedAt, fixedTime.Unix())
	}
	if !rep.Proxy.Active {
		t.Error("expected Proxy.Active = true")
	}
	if rep.Proxy.HTTPProxy != "http://[redacted]@"+deadProxyAddr {
		t.Errorf("HTTPProxy = %q", rep.Proxy.HTTPProxy)
	}
	if rep.Proxy.HTTPSProxy != "https://[redacted]@"+deadProxyAddr+"/" {
		t.Errorf("HTTPSProxy = %q", rep.Proxy.HTTPSProxy)
	}
	if rep.Proxy.ALLProxy != "socks5://[redacted]@proxy.example.com:1080" {
		t.Errorf("ALLProxy = %q", rep.Proxy.ALLProxy)
	}

	rawJSON, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(rawJSON)
	for _, secret := range []string{proxyUser, proxyPass, allPass, "socksuser", "leakedquery"} {
		if strings.Contains(payload, secret) {
			t.Fatalf("RPC report JSON leaked secret %q: %s", secret, payload)
		}
	}

	byID := map[string]proto.MachineEndpointProbe{}
	for _, p := range rep.Probes {
		byID[p.ID] = p
	}
	if p := byID["direct_ok"]; !p.Reachable || p.StatusCode != http.StatusOK || p.ViaProxy {
		t.Errorf("direct_ok = %+v", p)
	}
	if p := byID["direct_502"]; p.Reachable || p.StatusCode != http.StatusBadGateway || p.Error != "HTTP 502" {
		t.Errorf("direct_502 = %+v", p)
	}
	if p := byID["direct_timeout"]; p.Reachable || p.Error == "" {
		t.Errorf("direct_timeout = %+v", p)
	}
	if p := byID["proxied_fail"]; p.Reachable || !p.ViaProxy || p.Error == "" {
		t.Errorf("proxied_fail = %+v", p)
	}
	if p := byID["tcp_ok"]; !p.Reachable || p.Protocol != "tcp" || p.ViaProxy {
		t.Errorf("tcp_ok = %+v", p)
	}
}

func TestCollectNetworkInterfacesExcludesLoopback(t *testing.T) {
	ifaces, err := collectNetworkInterfaces()
	if err != nil {
		t.Fatalf("collectNetworkInterfaces: %v", err)
	}
	for _, iface := range ifaces {
		for _, addr := range iface.Addresses {
			ip := net.ParseIP(addr)
			if ip == nil {
				t.Errorf("interface %s has unparseable IP %q", iface.Name, addr)
				continue
			}
			if ip.IsLoopback() {
				t.Errorf("interface %s leaked loopback IP %q", iface.Name, addr)
			}
		}
	}
}
