package hubclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestHelloWithZeroTimeoutUsesCallerContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"capabilities":["export"]}`))
	}))
	defer srv.Close()

	base, err := BaseURL(srv.URL)
	if err != nil {
		t.Fatalf("BaseURL: %v", err)
	}
	client := New(base, "test-token", i18n.En, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hello struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := client.Hello(ctx, &hello); err != nil {
		t.Fatalf("Hello with zero client timeout failed: %v", err)
	}
	if len(hello.Capabilities) != 1 || hello.Capabilities[0] != "export" {
		t.Fatalf("Hello capabilities = %#v, want [export]", hello.Capabilities)
	}
}

type trackingCloser struct {
	io.Reader
	closed *atomic.Bool
}

func (tc trackingCloser) Close() error {
	tc.closed.Store(true)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHelloPreservesStructuredHTTPErrorAndClosesBody(t *testing.T) {
	base, err := BaseURL("http://127.0.0.1:18660")
	if err != nil {
		t.Fatalf("BaseURL: %v", err)
	}
	const secret = "super-secret-hub-token"
	client := New(base, secret, i18n.En, 5*time.Second)

	var bodyClosed atomic.Bool
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		payload := `{"code":"node_outdated","error":"hub requires update (` + secret + `)"}`
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       trackingCloser{Reader: strings.NewReader(payload), closed: &bodyClosed},
			Request:    r,
		}, nil
	})

	err = client.Hello(context.Background(), &struct{}{})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("Hello error = %v, want *HTTPError", err)
	}
	if !bodyClosed.Load() {
		t.Errorf("Hello did not close HTTP error response body")
	}
	if httpErr.Status != http.StatusBadRequest {
		t.Errorf("HTTPError.Status = %d, want %d", httpErr.Status, http.StatusBadRequest)
	}
	if httpErr.Code != "node_outdated" {
		t.Errorf("HTTPError.Code = %q, want %q", httpErr.Code, "node_outdated")
	}
	if !httpErr.Named {
		t.Errorf("HTTPError.Named = false, want true")
	}
	if !strings.Contains(httpErr.Detail, "hub requires update") {
		t.Errorf("HTTPError.Detail = %q, want structured message", httpErr.Detail)
	}
	if strings.Contains(httpErr.Detail, secret) {
		t.Errorf("HTTPError.Detail leaked token: %q", httpErr.Detail)
	}
}
