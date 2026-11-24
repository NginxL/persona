package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
)

// This bus uses only memory stores and synthetic credentials; it needs no Postgres instance.
func healthIntakeHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	h.bus.idents = &memIdents{}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	return h, srv
}

func TestHealthIntakeNewRoutesAuthenticateBeforeForwarding(t *testing.T) {
	h, srv := healthIntakeHub(t)
	m := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			var req assistantwire.HTTPRequest
			json.Unmarshal(raw, &req)
			if http.Header(req.Header).Get(assistantwire.RoleHeader) != roleFull {
				t.Errorf("forwarded role: %v", req.Header)
			}
			return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
		}
		return map[string]any{}, nil
	})
	for _, path := range []string{"/api/v1/assistant/health/intake/ui", "/api/v1/assistant/health/water/reminder", "/api/v1/assistant/health/intake/import"} {
		if code, body := call(t, h.routes(), "POST", path, "", `{}`); code != 401 {
			t.Fatalf("unauthenticated %s: %d %v", path, code, body)
		}
	}
	if strings.Contains(m.log(), assistantwire.MethodHTTP+" ") {
		t.Fatal("unauthenticated request reached the manager")
	}
	for _, path := range []string{"/api/v1/assistant/health/intake/ui", "/api/v1/assistant/health/water/reminder", "/api/v1/assistant/health/intake/import"} {
		if code, body := call(t, h.routes(), "POST", path+"?space=health", h.cfg.Token, `{}`); code != 200 {
			t.Fatalf("full access %s: %d %v", path, code, body)
		}
	}
}

func TestHealthIntakeAnswerRoutesBeforeForwarding(t *testing.T) {
	h, srv := healthIntakeHub(t)
	m := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			var p assistantwire.HTTPRequest
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Error(err)
			}
			if http.Header(p.Header).Get(assistantwire.RoleHeader) != roleAnswer {
				t.Errorf("forwarded role: %v", p.Header)
			}
			return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
		}
		return map[string]any{}, nil
	})
	mux := h.routes()
	code, watch := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Synthetic watch","kind":"watch"}`)
	if code != 200 || watch["role"] != roleAnswer {
		t.Fatalf("watch credential: %d %v", code, watch)
	}
	token := watch["token"].(string)
	for _, method := range []string{"GET", "POST"} {
		if code, body := call(t, mux, method, "/api/v1/assistant/health/intake?space=health", token, `{}`); code != 200 {
			t.Fatalf("watch %s: %d %v", method, code, body)
		}
	}
	before := m.log()
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/assistant/health/intake/changes"},
		{"DELETE", "/api/v1/assistant/health/intake/00000000-0000-4000-8000-000000000001"},
		{"POST", "/api/v1/assistant/health/intake/ui"},
		{"POST", "/api/v1/assistant/health/water/reminder"},
		{"POST", "/api/v1/assistant/health/intake/import"},
		{"PUT", "/api/v1/assistant/health/intake"},
	} {
		if code, body := call(t, mux, route.method, route.path, token, `{}`); code != http.StatusForbidden {
			t.Errorf("watch %s %s: %d %v", route.method, route.path, code, body)
		}
	}
	after := m.log()
	countHTTP := func(log string) int { return strings.Count(log, assistantwire.MethodHTTP+" ") }
	if countHTTP(before) != 2 || countHTTP(after) != 2 {
		t.Fatalf("forbidden requests reached the manager: before %q, after %q", before, after)
	}
}

func TestHealthIntakeExtensionsCannotReachRoutes(t *testing.T) {
	h, srv := healthIntakeHub(t)
	m := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
		if method == extwire.MethodAuthorize {
			var p extwire.Authorize
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Error(err)
			}
			// As with workout uploads, no scope grants these routes. An otherwise valid
			// extension token is recognized but refused by the manager's authorizer.
			if scope := extwire.RouteScopes[p.Pattern]; scope != "" {
				t.Errorf("intake unexpectedly has extension scope %q", scope)
			}
			return extwire.Authorized{Extension: "synthetic", Name: "Synthetic"}, nil
		}
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{}`)}, nil
	})
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/assistant/health/intake"},
		{"POST", "/api/v1/assistant/health/intake"},
		{"DELETE", "/api/v1/assistant/health/intake/00000000-0000-4000-8000-000000000001"},
		{"GET", "/api/v1/assistant/health/intake/changes"},
		{"POST", "/api/v1/assistant/health/intake/ui"},
		{"POST", "/api/v1/assistant/health/water/reminder"},
		{"POST", "/api/v1/assistant/health/intake/import"},
	} {
		if code, body := call(t, h.routes(), route.method, route.path, extTokenPrefix+"synthetic-token", `{}`); code != 403 {
			t.Errorf("extension %s %s: %d %v", route.method, route.path, code, body)
		}
	}
	if strings.Contains(m.log(), assistantwire.MethodHTTP+" ") {
		t.Fatalf("an extension reached intake: %s", m.log())
	}
}

func TestHealthIntakeFeatureFollowsDefaultManager(t *testing.T) {
	h, srv := healthIntakeHub(t)
	answer := func(string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answer)
	b := connectGuardManager(t, h, srv.URL, "m-0d0d0d", answer)
	hello := func(m *guardManager, intake bool) {
		t.Helper()
		caps := []string{assistantwire.CapabilityConfigOwn}
		if intake {
			caps = append(caps, assistantwire.CapabilityHealthIntake)
		}
		if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": caps}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), assistantwire.CapabilityHealthIntake) }
	if listed() {
		t.Fatal("listed before the default manager named it")
	}
	hello(b, true)
	if listed() {
		t.Fatal("another manager enabled intake")
	}
	hello(a, true)
	if !listed() {
		t.Fatal("the default manager named intake, but the feature is absent")
	}
	hello(a, false)
	if err := a.peer.Notify(extwire.EventReport, extwire.Report{Features: []string{assistantwire.CapabilityHealthIntake, "synthetic_feature"}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "extension report", func() bool { return slices.Contains(h.features(), "synthetic_feature") })
	if listed() {
		t.Fatal("an extension enabled intake")
	}
	hello(a, true)
	if err := h.busConnOf(a.id).ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "default manager disconnected", func() bool { return h.busConnOf(a.id) == nil })
	if listed() {
		t.Fatal("listed after the default manager disconnected")
	}
}

func TestHealthIntakeBroadcastRelayAndWidgetRevision(t *testing.T) {
	h := newTestHub(t)
	app := wsApp(t, h)
	fleetBefore, _ := json.Marshal(h.live.snapshot())
	signalsBefore := len(h.live.signal)
	if !managerMessageTypes["health_intake"] {
		t.Fatal("intake is not a manager message")
	}
	if err := h.relayBroadcastFrom(assistantwire.AssistantID, json.RawMessage(`{"type":"health_intake"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-app.out:
		if !strings.Contains(string(raw), `"type":"health_intake"`) {
			t.Fatalf("relayed message: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("open app did not receive intake invalidation")
	}
	if h.live.widgets.intake.Load() != 1 {
		t.Fatal("no widget revision for default manager intake")
	}
	fleetAfter, _ := json.Marshal(h.live.snapshot())
	if string(fleetBefore) != string(fleetAfter) || len(h.live.signal) != signalsBefore {
		t.Fatal("intake changed fleet content or signalled a Live Activity update")
	}
	select {
	case <-h.live.widgetChanges:
	default:
		t.Fatal("no widget-only wake signal")
	}
	if err := h.relayBroadcastFrom("m-0d0d0d", json.RawMessage(`{"type":"health_intake"}`)); err != nil {
		t.Fatal(err)
	}
	if h.live.widgets.intake.Load() != 1 {
		t.Fatal("another manager's message changed the default intake revision")
	}
	// Routing and hello features follow the user's selected default, which may differ
	// from the built-in assistant. Widget invalidation follows that same default.
	store := &profileManagerStore{settings: settingsStore{vals: map[string][]byte{}}, ids: map[string]bool{"m-0d0d0d": true}}
	h.bus.idents = store
	if err := store.PutSetting(context.Background(), managerDefaultKey, "m-0d0d0d"); err != nil {
		t.Fatal(err)
	}
	h.ownersChanged()
	if err := h.relayBroadcastFrom("m-0d0d0d", json.RawMessage(`{"type":"health_intake"}`)); err != nil {
		t.Fatal(err)
	}
	if h.live.widgets.intake.Load() != 2 {
		t.Fatal("the selected default did not advance intake")
	}
	if err := h.relayBroadcastFrom(assistantwire.AssistantID, json.RawMessage(`{"type":"health_intake"}`)); err != nil {
		t.Fatal(err)
	}
	if h.live.widgets.intake.Load() != 2 {
		t.Fatal("the previous default still advances intake")
	}
}
