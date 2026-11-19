package ai

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"weak"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

type sessionIDKey struct{}

// WithSessionID attaches a stable session identifier to ctx for gateways that require session routing.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if s := strings.TrimSpace(sessionID); s != "" {
		return context.WithValue(ctx, sessionIDKey{}, s)
	}
	return ctx
}

// openCodeClientSessions maps weak.Pointer[http.Client] to a stable UUIDv4 session ID so Client copies (such as
// WithResult) share the underlying *http.Client's session without changing Client's fields.
var openCodeClientSessions sync.Map

func deleteOpenCodeClientSession(key weak.Pointer[http.Client]) {
	openCodeClientSessions.Delete(key)
}

func clientOpenCodeSession(hc *http.Client) string {
	if hc == nil {
		return modelhttp.OpenCodeSessionID("")
	}
	defer runtime.KeepAlive(hc)
	k := weak.Make(hc)
	if v, ok := openCodeClientSessions.Load(k); ok {
		return v.(string)
	}
	id := modelhttp.OpenCodeSessionID("")
	actual, loaded := openCodeClientSessions.LoadOrStore(k, id)
	if !loaded {
		runtime.AddCleanup(hc, deleteOpenCodeClientSession, k)
	}
	return actual.(string)
}

func (c *Client) openCodeSessionID(ctx context.Context, r Request) string {
	if s := strings.TrimSpace(r.SessionID); s != "" {
		return modelhttp.OpenCodeSessionID(s)
	}
	if ctx != nil {
		if s, _ := ctx.Value(sessionIDKey{}).(string); strings.TrimSpace(s) != "" {
			return modelhttp.OpenCodeSessionID(s)
		}
	}
	return clientOpenCodeSession(c.http)
}

func (c *Client) setGatewayHeaders(ctx context.Context, req *http.Request, r Request) {
	c.setUserAgent(req)
	if rawURL := req.URL.String(); modelhttp.OpenCodeGo(rawURL) {
		modelhttp.SetOpenCodeSession(req.Header, rawURL, c.openCodeSessionID(ctx, r))
	}
}
