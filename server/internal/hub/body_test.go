package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// endlessBody is a JSON object whose string value never ends; it counts what the server read of it.
type endlessBody struct {
	head []byte
	read int64
}

func (b *endlessBody) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if b.read < int64(len(b.head)) {
			p[n] = b.head[b.read]
		} else {
			p[n] = 'a'
		}
		n++
		b.read++
	}
	return n, nil
}

func (b *endlessBody) Close() error { return nil }

// postEndless sends an endless body to path with token and returns the status, the error code and how much was read.
func postEndless(t *testing.T, mux http.Handler, method, path, token string) (int, string, int64) {
	t.Helper()
	body := &endlessBody{head: []byte(`{"name":"`)}
	req := httptest.NewRequest(method, path, nil)
	req.Body = body
	req.ContentLength = -1
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Code, body.read
}

// A watch (answer-only) token may answer cards; an oversized answer is refused with 413 after reading about its cap,
// and a normal one still reaches the handler.
func TestResolveBodyLimitForAnswerRole(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`)
	_, watch := call(t, mux, "POST", "/api/v1/auth/tokens", phone["token"].(string), `{"name":"Watch","kind":"watch"}`)
	watchTok := watch["token"].(string)

	code, errCode, read := postEndless(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", watchTok)
	if code != http.StatusRequestEntityTooLarge || errCode != proto.CodeTooLarge {
		t.Fatalf("oversized answer: %d %q, want 413 %q", code, errCode, proto.CodeTooLarge)
	}
	if read > 2<<20 {
		t.Fatalf("read %d bytes of an oversized answer (cap 1 MiB)", read)
	}
	connectAssistant(t, h) // a normal answer goes on to the manager, which has no such card
	if code, _ := call(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", watchTok, `{"allow":true}`); code != http.StatusGone {
		t.Fatalf("a normal answer reaches the handler (410: no such card), got %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", watchTok, `{"allow":`); code != http.StatusBadRequest {
		t.Fatalf("a malformed answer: %d, want 400", code)
	}
}

// A pairing code for one machine's persona-link can only trade itself for a token: an oversized body is refused before
// its role is checked, without reading it all.
func TestAuthTokensBodyLimitForLinkPair(t *testing.T) {
	h := newTestHub(t)
	store := newPairStore()
	h.store = store
	mux := h.routes()
	const pair = "jp_link_pair_code_for_test"
	if err := store.CreatePairToken(context.Background(), hashToken(pair), time.Now().Add(time.Hour), "box"); err != nil {
		t.Fatal(err)
	}

	code, errCode, read := postEndless(t, mux, "POST", "/api/v1/auth/tokens", pair)
	if code != http.StatusRequestEntityTooLarge || errCode != proto.CodeTooLarge {
		t.Fatalf("oversized pairing request: %d %q, want 413 %q", code, errCode, proto.CodeTooLarge)
	}
	if read > 64<<10 {
		t.Fatalf("read %d bytes of an oversized pairing request (cap 4 KiB)", read)
	}
	// A small body gets past decoding to the hub.json check (no machine "box" of kind link here).
	code, out := call(t, mux, "POST", "/api/v1/auth/tokens", pair, `{"name":"box link","kind":"link"}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "kind link") {
		t.Fatalf("small pairing request: %d %v", code, out)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`); code != 200 {
		t.Fatalf("a full device adds a phone: %d", code)
	}
}

// PUT /api/v1/pins and /api/v1/mutes store their arrays as they come: an oversized body is refused and the stored
// setting stays as it was.
func TestPinsBodyLimit(t *testing.T) {
	h := newTestHub(t)
	store := &settingsStore{vals: map[string][]byte{}}
	h.store = store
	mux := h.routes()
	for _, key := range []string{"pins", "mutes"} {
		path := "/api/v1/" + key
		if code, _ := call(t, mux, "PUT", path, h.cfg.Token, `{"spaces":["m/a"],"sessions":["m/s1"]}`); code != 200 {
			t.Fatalf("PUT %s: %d", key, code)
		}
		before := string(store.vals[key])
		puts := store.puts

		big := `{"spaces":["` + strings.Repeat("m/x", 1<<19) + `"],"sessions":[]}` // 1.5 MiB
		code, out := call(t, mux, "PUT", path, h.cfg.Token, big)
		if code != http.StatusRequestEntityTooLarge || out["code"] != proto.CodeTooLarge {
			t.Fatalf("oversized PUT %s: %d %v", key, code, out)
		}
		if got := string(store.vals[key]); got != before || store.puts != puts {
			t.Fatalf("%s changed after a refused PUT: %s", key, got)
		}
		if code, _ := call(t, mux, "PUT", path, h.cfg.Token, `{"spaces":[`); code != http.StatusBadRequest {
			t.Fatalf("malformed PUT %s: %d, want 400", key, code)
		}
	}
}

// decodeJSONBody's cap: at the limit the body decodes, one byte over it is 413, a malformed one 400.
func TestDecodeJSONBodyTooLarge(t *testing.T) {
	const limit = 64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			S string `json:"s"`
		}
		if err := decodeJSONBody(w, r, &v, limit); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		_, _ = io.WriteString(w, v.S)
	})
	fill := func(n int) string { // a body of exactly n bytes
		return `{"s":"` + strings.Repeat("x", n-len(`{"s":""}`)) + `"}`
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{fill(limit), 200},
		{fill(limit + 1), http.StatusRequestEntityTooLarge},
		{`{"s":`, http.StatusBadRequest},
		{``, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(c.body)))
		if w.Code != c.want {
			t.Errorf("%d-byte body: %d, want %d (%s)", len(c.body), w.Code, c.want, w.Body.String())
		}
	}
}

func TestOptionalObject(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		message    string
	}{
		{"empty", "", http.StatusOK, ""},
		{"object", `{"x":1}`, http.StatusOK, ""},
		{"null", "null", http.StatusBadRequest, "request body must be a JSON object"},
		{"array", "[]", http.StatusBadRequest, ""},
		{"malformed", "{", http.StatusBadRequest, ""},
		{"oversized", `{"x":"` + strings.Repeat("x", 64) + `"}`, http.StatusRequestEntityTooLarge, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			p, ok := optionalObject(w, httptest.NewRequest("POST", "/", strings.NewReader(tc.body)), 32)
			if w.Code != tc.status || ok != (tc.status == http.StatusOK) {
				t.Fatalf("got %d, ok=%v, body=%s", w.Code, ok, w.Body.String())
			}
			if ok && p == nil {
				t.Fatal("accepted a nil object")
			}
			if tc.message != "" && !strings.Contains(w.Body.String(), tc.message) {
				t.Fatalf("body %s does not contain %q", w.Body.String(), tc.message)
			}
			if tc.status == http.StatusRequestEntityTooLarge && !strings.Contains(w.Body.String(), `"code":"too_large"`) {
				t.Fatalf("missing too_large: %s", w.Body.String())
			}
		})
	}
}

// The app API handlers that cap their body answer an oversized one with 413 too_large (docs/protocol/protocol.md) after
// reading about the cap, not 400 with Go's "request body too large"; a small malformed body is still 400.
func TestAppHandlersBodyLimitTooLarge(t *testing.T) {
	h := newTestHub(t)
	h.byID["m1"] = newMachine(h, MachineConfig{ID: "m1", Name: "m1"})
	mux := h.routes()
	const opID = "0b6f3c2e-6d1a-4c55-9a43-2f8e6f6b9c10"
	for _, c := range []struct {
		method, path string
		opID         string // X-Persona-Op-Id: withClientOp reads the body itself
		limit        int64
	}{
		{"POST", "/api/v1/sessions/m1/s1/messages", "", 32 << 20},
		{"POST", "/api/v1/sessions/m1/s1/messages", opID, 32 << 20},
		{"POST", "/api/v1/sessions/m1/s1/interrupt", "", 32 << 20},
		{"POST", "/api/v1/sessions/m1/s1/keys", "", 32 << 20},
		{"POST", "/api/v1/sessions/m1/s1/rename", "", 1 << 20},
		{"POST", "/api/v1/sessions/m1/s1/rewind", "", rewindMaxPrompt + 4<<10},
		{"POST", "/api/v1/machines/m1/preview/start", "", 64 << 10},
		{"POST", "/api/v1/sessions", "", 4 << 20},
		{"POST", "/api/v1/notify", "", 16 << 10},
		// (/api/v1/agents/… are the manager's: extmgr TestAgentRoutesBodyLimitTooLarge.)
		{"POST", "/api/v1/machines/m1/panes", "", 4 << 10},
		{"POST", "/api/v1/machines/m1/panes/close", "", 4 << 10},
		{"POST", "/api/v1/phone/runtime-cli", "", 1 << 20},
		{"PUT", "/api/v1/looks/current", "", 4 << 10},
	} {
		name := c.method + " " + c.path
		if c.opID != "" {
			name += " (op id)"
		}
		t.Run(name, func(t *testing.T) {
			body := &endlessBody{head: []byte(`{"name":"`)}
			req := httptest.NewRequest(c.method, c.path, nil)
			req.Body = body
			req.ContentLength = -1
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			if c.opID != "" {
				req.Header.Set("X-Persona-Op-Id", c.opID)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			var out struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if w.Code != http.StatusRequestEntityTooLarge || out.Code != proto.CodeTooLarge {
				t.Fatalf("oversized body: %d %q (%s), want 413 %q", w.Code, out.Code, out.Error, proto.CodeTooLarge)
			}
			if body.read > 2*c.limit+64<<10 {
				t.Fatalf("read %d bytes of an oversized body (cap %d)", body.read, c.limit)
			}
			if code, got := call(t, mux, c.method, c.path, h.cfg.Token, `{"name":`); code != http.StatusBadRequest {
				t.Fatalf("malformed body: %d %v, want 400", code, got)
			}
		})
	}
}

// brokenBody fails its first read the way a client that goes away mid-body does.
type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

// A body forwarded to the assistant is 413 too_large only when it is over busHTTPLimit; a body that cannot be read
// for another reason is 400.
func TestRelayHTTPBodyErrors(t *testing.T) {
	h := newTestHub(t)
	for _, c := range []struct {
		body io.ReadCloser
		want int
		code string
	}{
		{&endlessBody{head: []byte(`{"name":"`)}, http.StatusRequestEntityTooLarge, proto.CodeTooLarge},
		{brokenBody{}, http.StatusBadRequest, ""},
	} {
		req := httptest.NewRequest("POST", "/api/v1/inbox/x/reply", nil)
		req.Body = c.body
		req.ContentLength = -1
		w := httptest.NewRecorder()
		h.relayHTTP(w, req, "mgr", true, false)
		var out struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != c.want || (c.code != "" && out.Code != c.code) {
			t.Errorf("%T: %d %q, want %d %q", c.body, w.Code, out.Code, c.want, c.code)
		}
	}
}
