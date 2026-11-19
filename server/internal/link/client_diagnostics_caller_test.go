package link

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDiagnosticPreservesCallerTLSCounts(t *testing.T) {
	for _, diagnostics := range []bool{false, true} {
		t.Run(fmt.Sprint(diagnostics), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Upgrade") != "websocket" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept: %v", err)
					return
				}
				conn.CloseNow()
			}))
			defer srv.Close()
			var starts, dones atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				TLSHandshakeStart: func() { starts.Add(1) },
				TLSHandshakeDone:  func(tls.ConnectionState, error) { dones.Add(1) },
			})
			client := Pinned(pinOf(srv))
			defer client.CloseIdleConnections()
			if diagnostics {
				logs := &diagnosticLines{}
				conn, _, err := diagnosticDial(diagnosticContext(ctx, logs), wsURL(srv.URL, Path), &websocket.DialOptions{HTTPClient: client}, "control", "lan", 1)
				if err != nil {
					t.Fatal(err)
				}
				conn.CloseNow()
				requireDiagnostic(t, logs, "phase=upgrade outcome=success")
			} else {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("caller TLS callbacks: start=%d done=%d", starts.Load(), dones.Load())
			if starts.Load() != 1 || dones.Load() != 1 {
				t.Fatal("single handshake changed the caller TLS callback count")
			}
		})
	}
}
