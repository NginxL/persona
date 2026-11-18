package hub

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// session.live goes to the session's subscribers that asked for session_live (URL or client_hello), as a live event;
// not to older apps, nor to apps that do not watch the session.
func TestSessionLiveGoesToSubscribersWithTheFeature(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	key := [2]string{"box", "s1"}
	newClient := func(sub bool, url bool) *client {
		c := &client{out: make(chan []byte, 8), subs: map[[2]string]bool{key: sub},
			urlFeatures: map[string]bool{proto.FeatureSessionLive: url}}
		addTestClient(h, c)
		return c
	}
	viaURL, legacy, other := newClient(true, true), newClient(true, false), newClient(false, true)
	viaHello := newClient(true, false)
	h.onClientHello(viaHello, []byte(`{"features":["session_live"]}`))
	<-viaHello.out // the ack

	m.onEvent("session.live", json.RawMessage(`{"session_id":"s1","live":{"verb":"Frosting","elapsed_s":975,"tokens_down":22800,"thinking":true}}`))
	m.onEvent("session.live", json.RawMessage(`{"session_id":"s1","live":{"retry_error":"429 Synthetic rate limit for retry capture","retry_attempt":6,"retry_max":10}}`))
	m.onEvent("session.live", json.RawMessage(`{"session_id":"s1","live":null}`))
	for name, c := range map[string]*client{"url": viaURL, "client_hello": viaHello} {
		if len(c.out) != 3 {
			t.Fatalf("%s: %d messages, want 3", name, len(c.out))
		}
		var on struct {
			Type      string             `json:"type"`
			MachineID string             `json:"machine_id"`
			SessionID string             `json:"session_id"`
			Live      *proto.SessionLive `json:"live"`
		}
		_ = json.Unmarshal(<-c.out, &on)
		if on.Type != "live" || on.MachineID != "box" || on.SessionID != "s1" || on.Live == nil ||
			*on.Live != (proto.SessionLive{Verb: "Frosting", ElapsedS: 975, TokensDown: 22800, Thinking: true}) {
			t.Fatalf("%s: %+v", name, on)
		}
		on.Live = nil
		_ = json.Unmarshal(<-c.out, &on)
		if on.Type != "live" || on.MachineID != "box" || on.SessionID != "s1" || on.Live == nil ||
			*on.Live != (proto.SessionLive{RetryError: "429 Synthetic rate limit for retry capture", RetryAttempt: 6, RetryMax: 10}) {
			t.Fatalf("%s retry: %+v", name, on)
		}
		raw := <-c.out
		off := map[string]any{}
		_ = json.Unmarshal(raw, &off)
		if v, has := off["live"]; off["type"] != "live" || !has || v != nil {
			t.Fatalf("%s: want live null, got %s", name, raw)
		}
	}
	if len(legacy.out) != 0 {
		t.Fatal("a client without session_live got it")
	}
	if len(other.out) != 0 {
		t.Fatal("a client not watching the session got it")
	}
}

// ?features= on the WebSocket URL is where the web asks for session_live.
func TestSessionLiveFeatureFromTheURL(t *testing.T) {
	h := newTestHub(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	for query, want := range map[string]bool{"?features=sessions_delta,session_live": true, "?features=sessions_delta": false} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws" + query
		conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + h.cfg.Token}}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		clients := func() []*client {
			h.mu.Lock()
			defer h.mu.Unlock()
			return slices.Collect(maps.Keys(h.clients))
		}
		eventually(t, 2*time.Second, "the connection", func() bool { return len(clients()) == 1 })
		if got := clients()[0].wants(proto.FeatureSessionLive); got != want {
			t.Fatalf("%s: session_live %v, want %v", query, got, want)
		}
		conn.CloseNow()
		eventually(t, 2*time.Second, "the connection gone", func() bool { return len(clients()) == 0 })
	}
}
