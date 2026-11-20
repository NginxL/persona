package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func errorResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("non-JSON error: %v", w.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil || legacy.Error == "" {
		t.Fatalf("legacy error: %+v (%v)", legacy, err)
	}
	if code, ok := body["code"].(string); !ok || code == "" {
		t.Fatalf("missing code: %v", body)
	}
	if v, ok := body["retryable"]; ok {
		if _, valid := v.(bool); !valid {
			t.Fatalf("invalid retryable: %v", body)
		}
	}
	if v, ok := body["delivered"]; ok && v != "none" && v != "unknown" && v != "yes" {
		t.Fatalf("invalid delivered: %v", body)
	}
	return body
}

func TestErrorCodes(t *testing.T) {
	for _, tt := range []struct {
		status int
		code   string
	}{
		{400, "bad_request"}, {401, "invalid_token"}, {403, "forbidden"}, {404, "not_found"}, {405, "method_not_allowed"},
		{408, "timeout"}, {409, "conflict"}, {410, "gone"}, {413, "too_large"}, {429, "rate_limited"},
		{500, "internal"}, {502, "internal"}, {503, "internal"}, {504, "timeout"},
	} {
		t.Run(tt.code+fmt.Sprint(tt.status), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeErr(w, tt.status, errors.New("示例错误"))
			body := errorResponse(t, w)
			if w.Code != tt.status || body["code"] != tt.code || body["error"] != "示例错误" {
				t.Fatalf("%d %v", w.Code, body)
			}
			if tt.status == 500 && (body["retryable"] != nil || body["delivered"] != nil) {
				t.Fatalf("invented delivery or retryability: %v", body)
			}
		})
	}
	w := httptest.NewRecorder()
	writeErrCode(w, 403, "tls_required", errors.New("需要加密连接"))
	if body := errorResponse(t, w); body["code"] != "tls_required" {
		t.Fatal(body)
	}
}

func TestCallErrorDelivery(t *testing.T) {
	for _, tt := range []struct {
		name            string
		err             error
		status          int
		code, delivered string
		retryable       any
	}{
		{"offline", fmt.Errorf("node: %w", errOffline), 503, "node_unreachable", "none", true},
		{"closed", fmt.Errorf("send: %w", rpc.ErrClosed), 502, "node_unreachable", "", nil},
		{"eof", io.EOF, 502, "node_unreachable", "", nil},
		{"timeout", fmt.Errorf("send: %w", context.DeadlineExceeded), 502, "timeout", "", nil},
		{"network timeout", &net.DNSError{IsTimeout: true}, 502, "timeout", "", nil},
		{"cancelled", context.Canceled, 502, "internal", "", nil},
		{"plain", errors.New("node failed"), 502, "internal", "", nil},
		{"remote timeout", &rpc.Error{Message: context.DeadlineExceeded.Error()}, 502, "timeout", "", nil},
		{"coded timeout", &rpc.Error{Code: "timeout", Message: "node deadline"}, 502, "timeout", "", nil},
		// a session's history its machine has no transcript for: the client's, not the hub's (#9551)
		{"no transcript", fmt.Errorf("session.items: %w", &rpc.Error{Code: "not_found", Message: "transcript for session 11111111-2222-3333-4444-555555555555 not found"}), 404, "not_found", "", false},
		{"invalid optional fields", &rpc.Error{Message: "failed", Data: json.RawMessage(`{"delivered":{},"retryable":"yes"}`)}, 502, "internal", "", nil},
		{"remote unknown", &rpc.Error{Message: "partly typed", Data: json.RawMessage(`{"delivered":"unknown"}`)}, 502, "internal", "unknown", nil},
		{"remote yes", &rpc.Error{Message: "acknowledgement failed", Data: json.RawMessage(`{"delivered":"yes","retryable":false}`)}, 502, "internal", "yes", false},
		{"null data", &rpc.Error{Message: "failed", Data: json.RawMessage(`null`)}, 502, "internal", "", nil},
		{"malformed data", &rpc.Error{Message: "failed", Data: json.RawMessage(`[]`)}, 502, "internal", "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			callErr(w, tt.err)
			body := errorResponse(t, w)
			if delivered, _ := body["delivered"].(string); w.Code != tt.status || body["code"] != tt.code || delivered != tt.delivered || body["retryable"] != tt.retryable {
				t.Fatalf("%d %v", w.Code, body)
			}
		})
	}
}

// A node's not_found for a session's history keeps its reason through the devices manager that carries the computer,
// here played on the real bus as in send_held_test.go: the app gets the reason as the node gave it, the sentence for it
// in the request's language and the node's words as the detail (#11388).
func TestSessionItemsNotFoundReasonThroughItsManager(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Studio Mac"})
	m.managed, m.peer = true, &busNode{h: h, id: "box", done: make(chan struct{})}
	registerMachine(h, m)
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
	if err != nil {
		t.Fatal(err)
	}
	const words = "transcript for session s1 not found"
	connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, token, func(method string, raw json.RawMessage) (any, error) {
		var c devicewire.NodeCall
		if method != devicewire.MethodCall || json.Unmarshal(raw, &c) != nil || c.Method != "session.items" {
			return map[string]any{}, nil
		}
		return nil, &rpc.Error{Code: proto.CodeNotFound, Message: words, Data: json.RawMessage(`{"reason":"no_transcript"}`)}
	})
	h.devices.mu.Lock()
	h.devices.upOpen = map[string]bool{devicesOwner: true}
	h.devices.mu.Unlock()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/box/s1/items?turns=2", nil)
		r.SetPathValue("sid", "s1")
		rec := httptest.NewRecorder()
		h.sessionItems(langRecorder{rec, lang}, r, m)
		body := errorResponse(t, rec)
		if rec.Code != http.StatusNotFound || body["code"] != proto.CodeNotFound || body["reason"] != proto.NotFoundNoTranscript ||
			body["error"] != i18n.T(lang, "apierr-apierr.session_no_transcript") || body["detail"] != words {
			t.Errorf("%s: %d %v", lang, rec.Code, body)
		}
	}
}

func TestCallErrorRefusalFields(t *testing.T) {
	for _, code := range []string{"input_draft", "dialog_open", "prompt_changed", "attach_busy", "node_outdated"} {
		t.Run(code, func(t *testing.T) {
			w := httptest.NewRecorder()
			callErr(w, fmt.Errorf("send: %w", &rpc.Error{Code: code, Message: "请先处理当前输入", Data: json.RawMessage(`{"delivered":"none","draft":"示例草稿","clearable":"示例草稿","code":"ignored","error":"ignored"}`)}))
			body := errorResponse(t, w)
			if w.Code != 409 || body["code"] != code || body["error"] != "请先处理当前输入" || body["draft"] != "示例草稿" || body["clearable"] != "示例草稿" || body["delivered"] != "none" {
				t.Fatalf("%d %v", w.Code, body)
			}
		})
	}
}

// Only the API answers with the error body: everything outside /api is the web app (webApp), which serves its own 404.
func TestRouteErrorsJSON(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	for _, tt := range []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{"GET", "/api/v1/missing", 404, "not_found", ""},
		{"POST", "/api/v1/machines", 405, "method_not_allowed", "GET, HEAD"},
		{"DELETE", "/api/v1/sessions", 405, "method_not_allowed", "GET, HEAD, POST"},
		{"GET", "/api/v1/machines/example/dirs", 404, "not_found", ""},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			r := localRequest(tt.method, tt.path)
			r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			body := errorResponse(t, w)
			if w.Code != tt.status || body["code"] != tt.code || w.Header().Get("Allow") != tt.allow {
				t.Fatalf("%d %v %v", w.Code, body, w.Header())
			}
		})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, localRequest("GET", "/api/v1/machines"))
	if body := errorResponse(t, w); w.Code != 401 || body["code"] != "invalid_token" || body["delivered"] != "none" {
		t.Fatalf("%d %v", w.Code, body)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := localRequest(method, "/api/v1/machines")
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("matched route changed: %d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, localRequest("GET", "/api/v1/health"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"error_codes"`) {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}
}

func TestCallErrorKeepsWrappedLegacyMessage(t *testing.T) {
	w := httptest.NewRecorder()
	err := fmt.Errorf("send: %w", &rpc.Error{Message: "failed"})
	callErr(w, err)
	if body := errorResponse(t, w); body["error"] != err.Error() {
		t.Fatalf("legacy error text changed: %v", body)
	}
}

// localRequest comes from the LAN: httptest's default remote address is a public documentation one, which the
// gate would refuse as plaintext from the internet (gate.go).
func localRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	return r
}

// Undeclared paths bypass matched route middleware; their 404/405 still use the first request language.
func TestFallbackRouteLanguage(t *testing.T) {
	h := newTestHub(t)
	handler := h.routes()
	for _, tc := range []struct{ header, notFound, notAllowed string }{
		{"en", "The requested resource was not found.", "This operation is not supported."},
		{"zh-Hans", "找不到请求的内容。", "当前操作不受支持。"},
		{"ja-JP, en;q=0.8", "要求された内容が見つかりません。", "この操作はサポートされていません。"},
		{"fr-FR, ja;q=0.8", "The requested resource was not found.", "This operation is not supported."},
		{"", "Not Found", "Method Not Allowed"},
	} {
		for _, route := range []struct {
			method, path, want string
			status             int
		}{
			{"GET", "/api/v1/i18n-missing", tc.notFound, 404},
			{"POST", "/api/v1/machines", tc.notAllowed, 405},
		} {
			req := localRequest(route.method, route.path)
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			req.Header.Set("Accept-Language", tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body := errorResponse(t, rec)
			if rec.Code != route.status || body["error"] != route.want {
				t.Fatalf("%s %s %s: %d %v", tc.header, route.method, route.path, rec.Code, body)
			}
			if route.status == 405 && rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("Allow header lost")
			}
		}
	}
}
