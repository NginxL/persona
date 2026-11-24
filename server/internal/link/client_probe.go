package link

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	probeOver  = 3 * time.Second
	probeEvery = 2 * time.Second
	healthPath = "/api/v1/health"
)

// probeRecovery signals only when the hub was unreachable at the start of this wait and answers a later probe.
// A hub that already answers keeps the original wait even if its WebSocket handshake failed.
func probeRecovery(ctx context.Context, reach func(context.Context) bool) <-chan struct{} {
	recovered := make(chan struct{})
	go func() {
		tick := time.NewTicker(probeEvery)
		defer tick.Stop()
		if ctx.Err() != nil || reach(ctx) {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if ctx.Err() != nil {
					return
				}
				if reach(ctx) {
					close(recovered)
					return
				}
			}
		}
	}()
	return recovered
}

// hubProbe returns a check that races direct and outbound proxy routes within one probe interval. SSH logins
// are reserved for full connection attempts. The check is nil when there are no candidates or SSH is required.
// Proxy-only mode (Config.ProxyAlways) probes only through the proxy, and not at all without one.
func hubProbe(c *Config) func(context.Context) bool {
	if len(c.Endpoints) == 0 || (c.SSH != nil && c.SSH.Always) {
		return nil
	}
	direct := ranked(c.Endpoints)
	proxy, proxiedEndpoints, _ := proxied(c)
	if c.ProxyAlways {
		if len(proxiedEndpoints) == 0 {
			return nil
		}
		direct = nil
	}
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, probeEvery)
		defer cancel()
		count := len(direct) + len(proxiedEndpoints)
		results := make(chan bool, count)
		start := func(eps []Endpoint, p *url.URL) {
			for _, ep := range eps {
				go func() { results <- hubHealthy(ctx, hubClient(c, p, nil), ep.URL) }()
			}
		}
		start(direct, nil)
		start(proxiedEndpoints, proxy)
		for range count {
			select {
			case <-ctx.Done():
				return false
			case healthy := <-results:
				if healthy {
					return true
				}
			}
		}
		return false
	}
}

// hubHealthy asks for health through the same TLS and routing checks used by the WebSocket. The health request
// carries no link credential. The client belongs to this probe, so closing idle connections also cancels any dial
// the transport kept alive after the request was canceled.
func hubHealthy(ctx context.Context, client *http.Client, base string) bool {
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+healthPath, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.Host == "" {
		return false
	}
	// net/http can derive Basic authentication from URL user info when it sends the request.
	req.URL.User = nil
	req.Header.Del("Authorization")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false
	}
	var health struct {
		OK bool `json:"ok"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&health) == nil && health.OK
}
