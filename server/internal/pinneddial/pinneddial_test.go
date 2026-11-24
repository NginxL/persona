package pinneddial

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestPinnedPreservesIPv6Zone(t *testing.T) {
	var gotTarget string
	c1, c2 := net.Pipe()
	defer c2.Close()

	conn, err := Pinned(
		context.Background(),
		"tcp",
		"443",
		[]net.IPAddr{{IP: net.ParseIP("fe80::1"), Zone: "en0"}},
		func(_ context.Context, _, target string) (net.Conn, error) {
			gotTarget = target
			return c1, nil
		},
	)
	if err != nil {
		t.Fatalf("Pinned returned unexpected error: %v", err)
	}
	defer conn.Close()

	if gotTarget != "[fe80::1%en0]:443" {
		t.Fatalf("Pinned dial target = %q, want %q", gotTarget, "[fe80::1%en0]:443")
	}
}

func TestPinnedRejectsNilConnNilErrDialer(t *testing.T) {
	conn, err := Pinned(
		context.Background(),
		"tcp",
		"443",
		[]net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}},
		func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, nil
		},
	)
	if conn != nil || err == nil {
		t.Fatalf("Pinned with (nil, nil) dialer = (%v, %v), want (nil, non-nil error)", conn, err)
	}
}

func TestThroughProxyNormalizesUppercaseScheme(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv, err := ln.Accept()
		if err != nil {
			return
		}
		defer srv.Close()
		req, err := http.ReadRequest(bufio.NewReader(srv))
		if err != nil || req.Method != http.MethodConnect {
			return
		}
		_, _ = io.WriteString(srv, "HTTP/1.1 200 Connection Established\r\n\r\ntunnel-ready")
	}()

	upstream := &url.URL{Scheme: "HTTP", Host: ln.Addr().String()}
	var dialer net.Dialer
	conn, err := ThroughProxy(context.Background(), upstream, "tcp", "93.184.216.34:443", dialer.DialContext)
	if err != nil {
		t.Fatalf("ThroughProxy with uppercase HTTP scheme failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len("tunnel-ready"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("reading tunnel payload: %v", err)
	}
	if string(buf) != "tunnel-ready" {
		t.Fatalf("tunnel payload = %q, want %q", string(buf), "tunnel-ready")
	}
	<-done
}
