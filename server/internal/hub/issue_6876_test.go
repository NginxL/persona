package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Optional node-call bodies have one contract: no body and JSON objects are accepted,
// while null, arrays, and malformed JSON are refused before the node is called.
func TestIssue6876OptionalJSONObjectBodies(t *testing.T) {
	routes := []struct {
		name   string
		path   string
		method string
		handle func(*Hub) http.HandlerFunc
	}{
		{"attach", "/api/v1/sessions/m1/s1/attach", "session.attach", func(h *Hub) http.HandlerFunc {
			return h.proxyBody("session.attach")
		}},
		{"interrupt", "/api/v1/sessions/m1/s1/interrupt", "session.interrupt", func(h *Hub) http.HandlerFunc {
			return h.withMachine(h.interruptSession)
		}},
		{"modes", "/api/v1/sessions/m1/s1/modes", "session.modes", func(h *Hub) http.HandlerFunc {
			return h.withMachine(h.sessionModes)
		}},
	}
	bodies := []struct {
		name      string
		body      string
		want400   bool
		wantCalls int
	}{
		{"empty", "", false, 1},
		{"empty object", "{}", false, 1},
		{"object", `{"effort":"high"}`, false, 1},
		{"null", "null", true, 0},
		{"array", "[]", true, 0},
		{"malformed", `{"effort":`, true, 0},
	}
	for _, route := range routes {
		for _, tc := range bodies {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				tn := newTermNode(t)
				r := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(tc.body))
				r.SetPathValue("mid", "m1")
				r.SetPathValue("sid", "s1")
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull, ID: "dev"}))
				w := httptest.NewRecorder()
				route.handle(tn.hub)(w, r)

				wantStatus := http.StatusOK
				if tc.want400 {
					wantStatus = http.StatusBadRequest
				}
				if w.Code != wantStatus {
					t.Errorf("body %q: status = %d, want %d; response: %s", tc.body, w.Code, wantStatus, w.Body.String())
				}
				if tc.want400 {
					var out map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
						t.Fatalf("bad-request response is not JSON: %q: %v", w.Body.String(), err)
					}
					if out["code"] != "bad_request" {
						t.Errorf("body %q: error code = %v, want bad_request", tc.body, out["code"])
					}
				}
				wantCalls := tc.wantCalls
				tn.mu.Lock()
				calls := append([]string(nil), tn.log...)
				tn.mu.Unlock()
				if len(calls) != wantCalls {
					t.Errorf("body %q: node calls = %q, want %d call(s) to %s", tc.body, calls, wantCalls, route.method)
				}
			})
		}
	}
}

// An oversized preview start is a body-limit error, not a malformed request, and
// is rejected before preview.start reaches the node.
func TestIssue6876PreviewStartBodyTooLarge(t *testing.T) {
	h := newTestHub(t)
	_, calls := previewNode(t, h, nil, nil)
	body := `{"cwd":"/work","name":"` + strings.Repeat("x", 64<<10) + `","workspace":"w1"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/machines/m1/preview/start", strings.NewReader(body))
	r.SetPathValue("mid", "m1")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull, ID: "dev"}))
	w := httptest.NewRecorder()
	h.withMachine(h.previewStart)(w, r)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %q: %v", w.Body.String(), err)
	}
	if w.Code != http.StatusRequestEntityTooLarge || out["code"] != proto.CodeTooLarge {
		t.Errorf("oversized body: status = %d, code = %v, want 413 %q; response: %s", w.Code, out["code"], proto.CodeTooLarge, w.Body.String())
	}
	if len(*calls) != 0 {
		t.Errorf("oversized body reached the node: %q", *calls)
	}
}

func issue6876RewindRequest(rn *rewindNode, lang i18n.Lang) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/m1/s1/rewind", strings.NewReader(rewindBody))
	r.SetPathValue("mid", "m1")
	r.SetPathValue("sid", "s1")
	r.Header.Set("Accept-Language", string(lang))
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull, ID: "dev"}))
	w := httptest.NewRecorder()
	logRequests(rn.hub, rn.hub.withMachine(rn.hub.rewindSession)).ServeHTTP(w, r)
	return w
}

// A node from before session.rewind gets a localized outdated-node error in
// each supported request language.
func TestIssue6876RewindOldNodeErrorIsLocalized(t *testing.T) {
	wantErrors := map[i18n.Lang]string{
		i18n.En:     "personad on this computer is too old to rewind a session. Update it",
		i18n.ZhHans: "这台机器上的 personad 太旧，还不会回退会话，需要重新部署",
		i18n.Ja:     "このコンピュータの personad は古いため、セッションを巻き戻せません。更新してください",
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			rn := newRewindNode(t)
			rn.err = &rpc.Error{Message: "unknown method session.rewind"}
			w := issue6876RewindRequest(rn, lang)
			var out struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("response is not JSON: %q: %v", w.Body.String(), err)
			}
			wantError := wantErrors[lang]
			if w.Code != http.StatusConflict || out.Code != proto.CodeNodeOutdated || out.Error != wantError {
				t.Errorf("status = %d, code = %q, error = %q; want 409, %q, %q", w.Code, out.Code, out.Error, proto.CodeNodeOutdated, wantError)
			}
		})
	}
}

// Localizing the old-node case must not change an ordinary rewind refusal or
// discard the refusal details returned by the node.
func TestIssue6876RewindRefusalBodyUnchanged(t *testing.T) {
	rn := newRewindNode(t)
	rn.err = &rpc.Error{
		Code:    "rewind_busy",
		Message: "node refusal detail",
		Data:    json.RawMessage(`{"delivered":"none","retryable":false,"draft":"keep me"}`),
	}
	w := issue6876RewindRequest(rn, i18n.En)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %q: %v", w.Body.String(), err)
	}
	want := map[string]any{
		"code":      "rewind_busy",
		"error":     i18n.T(i18n.En, "rpc-errors.rewind_busy"),
		"detail":    "node refusal detail",
		"delivered": "none",
		"retryable": false,
		"draft":     "keep me",
	}
	if w.Code != http.StatusConflict || !reflect.DeepEqual(got, want) {
		t.Errorf("refusal response: status = %d, body = %#v; want 409, %#v", w.Code, got, want)
	}
}
