package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/db"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func browserCall(t *testing.T, mux http.Handler, action, token string, body map[string]any, want int) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/auth/browser-login/"+action, strings.NewReader(string(b)))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	status := response.Code
	if status != want {
		t.Fatalf("%s: status=%d want=%d result=%v", action, status, want, result)
	}
	return result
}

func TestBrowserLoginApproval(t *testing.T) {
	h := newTestHub(t)
	s := newPairStore()
	h.store = s
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	token := phone["token"].(string)
	c := browserCall(t, mux, "start", "", map[string]any{"name": "Test browser"}, 200)
	if strings.Contains(c["url"].(string), c["secret"].(string)) {
		t.Fatal("QR contains polling credential")
	}
	poll := map[string]any{"id": c["id"], "secret": c["secret"]}
	if browserCall(t, mux, "poll", "", poll, 200)["state"] != "pending" {
		t.Fatal("not pending")
	}
	approval := map[string]any{"id": c["id"], "hub": h.hubID()}
	browserCall(t, mux, "approve", "", approval, 401)
	browserCall(t, mux, "approve", h.cfg.Token, approval, 403)
	if browserCall(t, mux, "approve", token, approval, 200)["state"] != "scanned" {
		t.Fatal("not scanned")
	}
	approval["allow"] = true
	browserCall(t, mux, "approve", token, approval, 200)
	browserCall(t, mux, "poll", "", map[string]any{"id": c["id"], "secret": "wrong"}, 410)
	result := browserCall(t, mux, "poll", "", poll, 200)
	again := browserCall(t, mux, "poll", "", poll, 200)
	if result["token"] != again["token"] || result["id"] != again["id"] {
		t.Fatal("retry minted another device")
	}
	browserCall(t, mux, "approve", token, approval, 409)
	if code, _ := call(t, mux, "GET", "/api/v1/machines", result["token"].(string), ""); code != 200 {
		t.Fatalf("browser credential unusable: %d", code)
	}
}

func TestBrowserLoginDenialExpiryAndRevocation(t *testing.T) {
	for _, which := range []string{"denial", "expiry", "revocation", "other hub"} {
		t.Run(which, func(t *testing.T) {
			h := newTestHub(t)
			h.store = newPairStore()
			mux := h.routes()
			_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
			token := phone["token"].(string)
			c := browserCall(t, mux, "start", "", map[string]any{"name": "Test browser"}, 200)
			approval := map[string]any{"id": c["id"], "hub": h.hubID(), "allow": true}
			poll := map[string]any{"id": c["id"], "secret": c["secret"]}
			switch which {
			case "denial":
				approval["allow"] = false
				browserCall(t, mux, "approve", token, approval, 200)
				if browserCall(t, mux, "poll", "", poll, 200)["state"] != "denied" {
					t.Fatal("not denied")
				}
			case "expiry":
				h.browserLogin.flows[c["id"].(string)].Until = time.Now().Add(-time.Second)
				browserCall(t, mux, "approve", token, approval, 410)
				browserCall(t, mux, "poll", "", poll, 410)
			case "revocation":
				browserCall(t, mux, "approve", token, approval, 200)
				call(t, mux, "DELETE", "/api/v1/auth/tokens/"+phone["id"].(string), h.cfg.Token, "")
				browserCall(t, mux, "poll", "", poll, 403)
			case "other hub":
				approval["hub"] = "other"
				browserCall(t, mux, "approve", token, approval, 400)
			}
		})
	}
}

func TestBrowserLoginRejectsCrossOriginAndPlaintext(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	r := securityRequest(mux, "POST", "/api/v1/auth/browser-login/start", "", `{"name":"Browser"}`, "203.0.113.1:1", false)
	if r.Code != 403 {
		t.Fatalf("plaintext accepted: %d", r.Code)
	}
	req := httptest.NewRequest("POST", "/api/v1/auth/browser-login/start", strings.NewReader(`{"name":"Browser"}`))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatalf("cross-origin request accepted: %d", response.Code)
	}

}

type ambiguousBrowserStore struct {
	*pairStore
	failNext bool
}

func (s *ambiguousBrowserStore) CreateDeviceToken(ctx context.Context, token db.DeviceToken, hash string) error {
	if err := s.pairStore.CreateDeviceToken(ctx, token, hash); err != nil {
		return err
	}
	if s.failNext {
		s.failNext = false
		return errors.New("fixture: acknowledgement lost after commit")
	}
	return nil
}
func TestBrowserLoginAmbiguousCommit(t *testing.T) {
	h := newTestHub(t)
	s := &ambiguousBrowserStore{pairStore: newPairStore()}
	h.store = s
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	c := browserCall(t, mux, "start", "", map[string]any{"name": "Browser"}, 200)
	browserCall(t, mux, "approve", phone["token"].(string), map[string]any{"id": c["id"], "hub": h.hubID(), "allow": true}, 200)
	poll := map[string]any{"id": c["id"], "secret": c["secret"]}
	s.failNext = true
	browserCall(t, mux, "poll", "", poll, 503)
	result := browserCall(t, mux, "poll", "", poll, 200)
	if len(s.rows) != 2 {
		t.Fatalf("created duplicate credentials: %d", len(s.rows))
	}
	if result["token"] != h.browserLogin.flows[c["id"].(string)].Token {
		t.Fatal("credential identity changed")
	}
}
func TestBrowserLoginTrustedRequestMetadata(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	for _, name := range []string{"Browser\nVerified", "Browser\tVerified", "Browser\u202eVerified"} {
		browserCall(t, mux, "start", "", map[string]any{"name": name}, 400)
	}
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	c := browserCall(t, mux, "start", "", map[string]any{"name": "Browser"}, 200)
	data := browserCall(t, mux, "approve", phone["token"].(string), map[string]any{"id": c["id"], "hub": h.hubID()}, 200)
	if data["source"] != "127.0.0.1" || data["age_seconds"].(float64) < 0 {
		t.Fatal(data)
	}
}

func TestBrowserLoginHasItsOwnLimits(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	const remote = "198.51.100.7:1"
	jsonReq := func(action, body, from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/auth/"+action, strings.NewReader(body))
		r.RemoteAddr = from
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	// Password sign-in's limit spent: opening the sign-in page still gets a code.
	for i := 0; i < h.cfg.Security.failures(); i++ {
		jsonReq("password/start", `{}`, remote)
	}
	if w := jsonReq("password/start", `{}`, remote); w.Code != 429 {
		t.Fatalf("password limit not reached: %d", w.Code)
	}
	var c map[string]any
	if w := jsonReq("browser-login/start", `{"name":"Browser"}`, remote); w.Code != 200 {
		t.Fatalf("password limit blocked a QR start: %d %s", w.Code, w.Body)
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &c)
	}

	// Starts: their own per-source limit, which leaves password sign-in from elsewhere alone.
	h2 := newTestHub(t)
	h2.store = newPairStore()
	mux = h2.routes()
	for i := 0; i < browserStartLimits.failures(); i++ {
		if w := jsonReq("browser-login/start", `{"name":"Browser"}`, remote); w.Code != 200 {
			t.Fatalf("start %d refused: %d", i, w.Code)
		}
	}
	w := jsonReq("browser-login/start", `{"name":"Browser"}`, remote)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("start limit not applied: %d", w.Code)
	}
	if w := jsonReq("password/start", `{}`, remote); w.Code == 429 {
		t.Fatal("QR starts blocked password sign-in")
	}
	if w := jsonReq("browser-login/start", `{"name":"Browser"}`, "203.0.113.9:1"); w.Code != 200 {
		t.Fatalf("one source's starts blocked another: %d", w.Code)
	}

	// Polls: only misses count, per source.
	good := jsonReq("browser-login/start", `{"name":"Browser"}`, "192.0.2.50:1")
	_ = json.Unmarshal(good.Body.Bytes(), &c)
	poll := `{"id":"` + c["id"].(string) + `","secret":"` + c["secret"].(string) + `"}`
	for i := 0; i < 40; i++ {
		if w := jsonReq("browser-login/poll", poll, "192.0.2.50:1"); w.Code != 200 {
			t.Fatalf("a browser polling its own code was limited at %d: %d", i, w.Code)
		}
	}
	for i := 0; i < browserPollMisses.failures(); i++ {
		if w := jsonReq("browser-login/poll", `{"id":"x","secret":"y"}`, "192.0.2.60:1"); w.Code != 410 {
			t.Fatalf("miss %d: %d", i, w.Code)
		}
	}
	if w := jsonReq("browser-login/poll", `{"id":"x","secret":"y"}`, "192.0.2.60:1"); w.Code != 429 {
		t.Fatalf("poll misses not limited: %d", w.Code)
	}
	if w := jsonReq("browser-login/poll", poll, "192.0.2.50:1"); w.Code != 200 {
		t.Fatalf("another source's misses blocked a browser: %d", w.Code)
	}
}

func TestBrowserLoginAuditLog(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	var logs bytes.Buffer
	h.logger = log.New(&logs, "", 0)
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	token, phoneID := phone["token"].(string), phone["id"].(string)
	for _, allow := range []bool{false, true} {
		c := browserCall(t, mux, "start", "", map[string]any{"name": "Test browser"}, 200)
		browserCall(t, mux, "approve", token, map[string]any{"id": c["id"], "hub": h.hubID(), "allow": allow}, 200)
		result := browserCall(t, mux, "poll", "", map[string]any{"id": c["id"], "secret": c["secret"]}, 200)
		if allow {
			want := `device token ` + result["id"].(string) + ` for "Test browser" (web, full) by browser sign-in, approved by device ` + phoneID + ` "Phone"`
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("issuance not logged with its approver:\n%s", logs.String())
			}
		}
	}
	for _, want := range []string{
		`browser sign-in for "Test browser" (requested from 127.0.0.0/24) denied by device ` + phoneID + ` "Phone"`,
		`browser sign-in for "Test browser" (requested from 127.0.0.0/24) approved by device ` + phoneID + ` "Phone"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, logs.String())
		}
	}
}

func TestBrowserLoginReplacesThePreviousWebCredential(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	_, old := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Old browser","kind":"web"}`)
	signIn := func(replaces string) map[string]any {
		c := browserCall(t, mux, "start", "", map[string]any{"name": "Browser", "replaces": replaces}, 200)
		browserCall(t, mux, "approve", phone["token"].(string), map[string]any{"id": c["id"], "hub": h.hubID(), "allow": true}, 200)
		return browserCall(t, mux, "poll", "", map[string]any{"id": c["id"], "secret": c["secret"]}, 200)
	}
	// Only a web credential: a phone's id named by a browser stays.
	if r := signIn(phone["id"].(string)); r["replaced"] != nil {
		t.Fatalf("replaced a phone: %v", r)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", phone["token"].(string), ""); code != 200 {
		t.Fatalf("phone revoked: %d", code)
	}
	r := signIn(old["id"].(string))
	if r["replaced"] != old["id"] {
		t.Fatalf("previous web credential not replaced: %v", r)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", old["token"].(string), ""); code != 401 {
		t.Fatalf("previous web credential still works: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", r["token"].(string), ""); code != 200 {
		t.Fatalf("new credential unusable: %d", code)
	}
	browserCall(t, mux, "start", "", map[string]any{"name": "Browser", "replaces": strings.Repeat("x", 65)}, 400)
}
