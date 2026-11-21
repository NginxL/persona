package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestHealthSourceDisclosure(t *testing.T) {
	h := newPublicTestHub(t)
	h.public.port, h.public.fp = 19443, "test-pin"
	h.public.endpoints = []Endpoint{{URL: "https://example.com:19443", Kind: "name"}}
	for _, from := range []source{sourceLoopback, sourceLAN, sourcePublic} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		h.healthWithSource(rec, req, func(*http.Request) source { return from })
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || body["ok"] != true || body["hub_id"] != h.hubID() || body["hub_name"] != h.hubName() ||
			body["tls_port"] != float64(19443) || body["fp"] != "test-pin" {
			t.Fatalf("health identity fields: %v", body)
		}
		if _, ok := body["endpoints"]; ok {
			t.Fatal("health leaked candidate addresses")
		}
		if from == sourcePublic {
			if len(body) != 5 {
				t.Fatalf("public health leaked metadata: %v", body)
			}
		} else if len(body) != 13 || body["version"] != h.version || body["features"] == nil || body["protocol"] == nil ||
			body["min_app_build"] == nil || body["update"] == nil || body["commit"] == nil {
			// version, features, the update fields (channel, build, protocol, min_app_build, update) and the commit
			t.Fatalf("LAN health lost legacy fields: %v", body)
		}
	}
	// Exercise the actual route with the current sourceOf implementation, which is replaced by hub-auth.
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatal("health is no longer public")
	}
}

// iosEnvelopeKeys are the keys the iOS app's socket Envelope (apple/Shared/LiveConnection.swift) already types: a new
// top-level hello key must not be one of them.
var iosEnvelopeKeys = strings.Fields("type machine_id session_id machines sessions items backlog has_more more_after history from_seq to_seq text status interaction id call_id name args event message last_assistant_message error revs feed_ids fleet features epoch seq full cols rows data unsupported state")

func TestHelloCompatibility(t *testing.T) {
	h := newPublicTestHub(t)
	h.public.port, h.public.fp = 19443, "test-pin"
	h.public.endpoints = []Endpoint{{URL: "https://example.com:19443", Kind: "name", Port: 19443, Scheme: "https", SeenAt: time.Now().UTC()}}
	hello := h.hello()
	if hello["type"] != "hello" || hello["server_version"] != h.version || len(hello["client_id"].(string)) != 12 {
		t.Fatal("legacy hello fields changed")
	}
	features := hello["features"].([]string)
	if !slices.Contains(features, "tls") || !slices.Contains(features, "endpoints") || !slices.Contains(features, proto.FeatureMessageSendState) {
		t.Fatal("new capabilities were not declared")
	}
	if version, ok := hello[proto.FeatureMessageSendState].(int); !ok || version != proto.MessageSendStateVersion {
		t.Fatal("hello did not carry the hub-authored receipt version")
	}
	if hello["hub_id"] != h.hubID() || hello["tls_port"] != 19443 || hello["fp"] != "test-pin" || len(hello["endpoints"].([]Endpoint)) != 1 {
		t.Fatal("missing public connection metadata")
	}
	if hello["public_url"] != "http://example.com:18660" {
		t.Fatalf("public_url = %v", hello["public_url"])
	}
	h.cfg.PublicURL = " https://hub.example.com:18660/ "
	if got := h.hello()["public_url"]; got != "https://hub.example.com:18660" {
		t.Fatalf("public_url = %v", got)
	}
	h.cfg.PublicURL = ""
	if _, ok := h.hello()["public_url"]; ok {
		t.Fatal("public_url without one configured")
	}
	// The existing iOS Envelope must not see a new value under an already typed key.
	for _, key := range []string{"hub_id", "endpoints", "tls_port", "fp", "public_url", "push_grading", proto.FeatureMessageSendState} {
		if slices.Contains(iosEnvelopeKeys, key) {
			t.Fatalf("new hello field collides with iOS Envelope: %s", key)
		}
	}
	b, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Type          string   `json:"type"`
		ServerVersion string   `json:"server_version"`
		ClientID      string   `json:"client_id"`
		Features      []string `json:"features"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil || legacy.Type != "hello" || legacy.ServerVersion != h.version || len(legacy.ClientID) != 12 || !slices.Equal(legacy.Features, features) {
		t.Fatalf("legacy hello decoding failed: %v", err)
	}
}

func TestPublicDisabledKeepsPlaintextAndLegacyWebSocket(t *testing.T) {
	h := newPublicTestHub(t)
	h.cfg.Public.CertFile, h.cfg.Public.KeyFile = "unused.crt", "unused.key"
	h.public.addresses = func() ([]localAddress, error) { return nil, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.startPublic(ctx); err != nil {
		t.Fatal(err)
	}
	if port, fp := h.tlsPin(); port != 0 || fp != "" || h.public.server != nil || h.public.listener != nil {
		t.Fatal("TLS was enabled without configuration")
	}
	if _, err := os.Stat(filepath.Join(h.cfg.dir, "tls")); !os.IsNotExist(err) {
		t.Fatal("disabled TLS created identity files")
	}
	server := httptest.NewServer(h.routes())
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	err = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if err != nil || health["ok"] != true || health["version"] != h.version || health["tls_port"] != float64(0) || health["fp"] != "" {
		t.Fatalf("legacy plaintext health changed: %v, %v", health, err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	conn, _, err := websocket.Dial(wctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws?token=test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(wctx)
	if err != nil {
		t.Fatal(err)
	}
	var hello struct {
		Type             string     `json:"type"`
		Features         []string   `json:"features"`
		HubID            string     `json:"hub_id"`
		Endpoints        []Endpoint `json:"endpoints"`
		Port             int        `json:"tls_port"`
		Pin              string     `json:"fp"`
		ServerVersion    string     `json:"server_version"`
		MessageSendState int        `json:"message_send_state"`
	}
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" || hello.Port != 0 || hello.Pin != "" || hello.HubID != h.hubID() || hello.ServerVersion != h.version {
		t.Fatalf("legacy WebSocket hello: %s, %v", data, err)
	}
	if !slices.Contains(hello.Features, proto.FeatureMessageSendState) || hello.MessageSendState != proto.MessageSendStateVersion {
		t.Fatal("authenticated socket hello did not declare hub-owned receipts")
	}
	if hello.Endpoints == nil || len(hello.Endpoints) != 1 || hello.Endpoints[0].URL != h.cfg.PublicURL {
		t.Fatal("old public_url was not preserved as a single plaintext candidate")
	}
}
