package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestStarredRouteIsolation(t *testing.T) {
	const path = "/api/v1/assistant/starred"
	h := newTestHub(t)
	api := h.routes()
	tokens := extTokensOf(t, h)
	token := tokens.issue("starred-check", "Starred check", []string{extwire.ScopeAgentsRead, extwire.ScopeAgentsControl})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, path}, {http.MethodPost, path}, {http.MethodDelete, path + "/example"},
		{http.MethodGet, assistantwire.StarredImportPath},
		{http.MethodPost, assistantwire.StarredImportPath},
		{http.MethodPost, assistantwire.StarredImportPreviewPath},
	} {
		if allowed(principal{Role: roleAnswer}, route.method, route.path) || allowed(principal{Role: roleLink}, route.method, route.path) {
			t.Fatalf("%s %s widened a restricted device's access", route.method, route.path)
		}
		if status, _ := call(t, api, route.method, route.path, token, "{}"); status != http.StatusForbidden {
			t.Fatalf("%s %s should refuse an extension token, got %d", route.method, route.path, status)
		}
	}
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if len(tokens.served) != 0 {
		t.Fatalf("restricted requests reached a manager: %v", tokens.served)
	}
}

func TestStarredImportUsesOnlyItsRouteBodyLimit(t *testing.T) {
	h, srv := bodyHub(t)
	manager := &bodyManager{}
	connectBodyManager(t, h, srv, nil, true, manager)
	api := h.routes()
	const limit = assistantwire.StarredImportBodyMax
	body := bytes.Repeat([]byte{'x'}, limit+1)
	post := func(path string, size, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body[:size]))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("POST %s with %d bytes: got %d, want %d (%s)", path, size, res.Code, want, res.Body)
		}
	}

	for _, path := range []string{assistantwire.StarredImportPath, assistantwire.StarredImportPreviewPath} {
		post(path, limit, http.StatusOK)
		post(path, limit+1, http.StatusRequestEntityTooLarge)
	}
	// An existing route's relay still accepts requests larger than the archive limit.
	post("/api/v1/assistant/starred", limit+1, http.StatusOK)
	methods, requests := manager.requests()
	if len(requests) != 3 {
		t.Fatalf("%d requests reached the manager, want the three within their own limits", len(requests))
	}
	for i, size := range []int{limit, limit, limit + 1} {
		req := requests[i]
		if methods[i] != assistantwire.MethodHTTPStreamed || req.BodyStream == nil ||
			!bytes.Equal(req.Body, body[:size]) {
			t.Fatalf("request %s reached the manager over %s with %d bytes", req.Path, methods[i], len(req.Body))
		}
	}
}

func TestStarredImportRelaysToTheSelectedManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	connectFakeManager(t, h, srv, assistantwire.AssistantID)
	const named = "m-0a0b0c"
	connectFakeManager(t, h, srv, named)
	header := map[string]string{ManagerHeader: named}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, assistantwire.StarredImportPath},
		{http.MethodPost, assistantwire.StarredImportPreviewPath},
		{http.MethodPost, assistantwire.StarredImportPath},
	} {
		code, out := ownershipGet(t, h, srv, tc.method, tc.path, header)
		if code != http.StatusOK || out["who"] != named {
			t.Fatalf("%s %s reached the wrong manager: %d %v", tc.method, tc.path, code, out)
		}
	}
}

func TestStarredImportPreservesLegacyManagerResponses(t *testing.T) {
	h, srv, _ := newBusHub(t)
	const id = assistantwire.AssistantID
	legacy := http.NewServeMux()
	// On an older manager this is the only pattern matching /starred/import.
	legacy.HandleFunc("DELETE /api/v1/assistant/starred/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := dialRawBusWith(t, srv, busToken(t, h, id, id),
		func(_ context.Context, method string, raw json.RawMessage) (any, error) {
			if method != assistantwire.MethodHTTP {
				return nil, nil
			}
			var p assistantwire.HTTPRequest
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			w := httptest.NewRecorder()
			legacy.ServeHTTP(w, httptest.NewRequest(p.Method, p.Path, bytes.NewReader(p.Body)))
			return assistantwire.HTTPResponse{Status: w.Code, Header: w.Header(), Body: w.Body.Bytes()}, nil
		})
	if err := conn.call("manager.hello", map[string]any{"id": id}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, allow string
		status              int
	}{
		{http.MethodGet, assistantwire.StarredImportPath, http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPost, assistantwire.StarredImportPath, http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPost, assistantwire.StarredImportPreviewPath, "", http.StatusNotFound},
	} {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status || res.Header.Get("Allow") != tc.allow {
			t.Fatalf("%s %s: got %d Allow %q, want %d Allow %q", tc.method, tc.path, res.StatusCode, res.Header.Get("Allow"), tc.status, tc.allow)
		}
	}
}

func TestStarredBroadcastKeepsManager(t *testing.T) {
	h := newTestHub(t)
	app := wsApp(t, h)
	err := h.relayBroadcastFrom("m-example", json.RawMessage(`{"type":"assistant_starred","manager":"forged"}`))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-app.out:
		var event map[string]json.RawMessage
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if string(event["type"]) != `"assistant_starred"` || string(event["manager"]) != `"m-example"` {
			t.Fatalf("wrong event type or provenance: %s", raw)
		}
		if event["star"] != nil || event["text"] != nil {
			t.Fatal("snapshot left the manager on the event")
		}
	case <-time.After(time.Second):
		t.Fatal("the star's notification was not relayed")
	}
}
