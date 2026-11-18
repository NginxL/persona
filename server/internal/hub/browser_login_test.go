package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// browserReq sends a browser-login (or password) call from remote, over TLS when tlsOn is set, with extra headers.
func browserReq(mux http.Handler, action, token, body, remote string, tlsOn bool, header map[string]string) *httptest.ResponseRecorder {
	path := "/api/v1/auth/browser-login/" + action
	if strings.HasPrefix(action, "password/") {
		path = "/api/v1/auth/" + action
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	if tlsOn {
		r.TLS = &tls.ConnectionState{}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func browserCall(t *testing.T, mux http.Handler, action, token string, body map[string]any, want int) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := browserReq(mux, action, token, string(b), "127.0.0.1:1", false, nil)
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != want {
		t.Fatalf("%s: status=%d want=%d result=%v", action, response.Code, want, result)
	}
	return result
}

type browserFixture struct {
	h     *Hub
	mux   http.Handler
	phone map[string]any // a signed-in phone: id, token
}

func newBrowserFixture(t *testing.T) *browserFixture {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Phone","kind":"phone"}`)
	return &browserFixture{h: h, mux: mux, phone: phone}
}

func (f *browserFixture) start(t *testing.T, body map[string]any) (challenge, poll map[string]any) {
	t.Helper()
	if body == nil {
		body = map[string]any{"name": "Test browser"}
	}
	c := browserCall(t, f.mux, "start", "", body, 200)
	return c, map[string]any{"id": c["id"], "secret": c["secret"]}
}

// allow is the phone's Allow; it answers the number the phone shows.
func (f *browserFixture) allow(t *testing.T, c map[string]any) string {
	t.Helper()
	r := browserCall(t, f.mux, "approve", f.phone["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID(), "allow": true, "show_number": true}, 200)
	n, _ := r["number"].(string)
	if r["state"] != "confirm" || !regexp.MustCompile(`^[1-9][0-9]$`).MatchString(n) {
		t.Fatalf("allow: %v", r)
	}
	return n
}

// signIn runs the whole flow: the phone allows, the browser types the number.
func (f *browserFixture) signIn(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	c, poll := f.start(t, body)
	poll["number"] = f.allow(t, c)
	return browserCall(t, f.mux, "confirm", "", poll, 200)
}

func otherNumber(n string) string {
	if n == "42" {
		return "43"
	}
	return "42"
}

func TestBrowserLoginApproval(t *testing.T) {
	f := newBrowserFixture(t)
	mux, token := f.mux, f.phone["token"].(string)
	c, poll := f.start(t, nil)
	if strings.Contains(c["url"].(string), c["secret"].(string)) {
		t.Fatal("QR contains polling credential")
	}
	if browserCall(t, mux, "poll", "", poll, 200)["state"] != "pending" {
		t.Fatal("not pending")
	}
	approval := map[string]any{"id": c["id"], "hub": f.h.hubID()}
	browserCall(t, mux, "approve", "", approval, 401)
	browserCall(t, mux, "approve", f.h.cfg.Token, approval, 403)
	p := newPairCode(t, f.h)
	browserCall(t, mux, "approve", p.Token, approval, 403) // a pairing code is not a signed-in device
	inspect := browserCall(t, mux, "approve", token, approval, 200)
	if inspect["state"] != "scanned" || inspect["number"] != nil || inspect["network"] != "lan" {
		t.Fatalf("inspect: %v", inspect)
	}
	if browserCall(t, mux, "poll", "", poll, 200)["state"] != "scanned" {
		t.Fatal("browser not told it was scanned")
	}

	// An app from before browser_qr_match would close and leave the browser waiting for a number nobody saw.
	approval["allow"] = true
	if r := browserCall(t, mux, "approve", token, approval, 400); r["code"] != "update_required" {
		t.Fatalf("old app's Allow: %v", r)
	}
	number := f.allow(t, c)
	if browserCall(t, mux, "poll", "", poll, 200)["state"] != "confirm" {
		t.Fatal("browser not asked for the number")
	}
	// The device that allowed it sees the number again (its app was closed); another device does not.
	if r := browserCall(t, mux, "approve", token, map[string]any{"id": c["id"], "hub": f.h.hubID()}, 200); r["number"] != number {
		t.Fatalf("approver lost its number: %v", r)
	}
	_, other := call(t, mux, "POST", "/api/v1/auth/tokens", f.h.cfg.Token, `{"name":"Tablet","kind":"phone"}`)
	if r := browserCall(t, mux, "approve", other["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID()}, 200); r["number"] != nil {
		t.Fatalf("another device saw the number: %v", r)
	}
	approval["show_number"] = true
	browserCall(t, mux, "approve", token, approval, 409) // already allowed

	browserCall(t, mux, "poll", "", map[string]any{"id": c["id"], "secret": "wrong"}, 410)
	browserCall(t, mux, "confirm", "", map[string]any{"id": c["id"], "secret": "wrong", "number": number}, 410)
	poll["number"] = number
	result := browserCall(t, mux, "confirm", "", poll, 200)
	delete(poll, "number")
	again := browserCall(t, mux, "poll", "", poll, 200)
	poll["number"] = number
	retried := browserCall(t, mux, "confirm", "", poll, 200)
	if result["token"] != again["token"] || result["id"] != again["id"] || retried["token"] != result["token"] {
		t.Fatal("retry minted another device")
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", result["token"].(string), ""); code != 200 {
		t.Fatalf("browser credential unusable: %d", code)
	}
}

func TestBrowserLoginWrongNumberVoids(t *testing.T) {
	f := newBrowserFixture(t)
	s := f.h.store.(*pairStore)
	before := len(s.rows)
	c, poll := f.start(t, nil)
	number := f.allow(t, c)
	poll["number"] = otherNumber(number)
	if r := browserCall(t, f.mux, "confirm", "", poll, 409); r["code"] != "number_mismatch" || r["state"] != "voided" {
		t.Fatalf("wrong number: %v", r)
	}
	poll["number"] = number // one try only
	if r := browserCall(t, f.mux, "confirm", "", poll, 409); r["code"] != "not_waiting" || r["state"] != "voided" {
		t.Fatalf("second try: %v", r)
	}
	delete(poll, "number")
	if browserCall(t, f.mux, "poll", "", poll, 200)["state"] != "voided" {
		t.Fatal("not voided")
	}
	browserCall(t, f.mux, "approve", f.phone["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID(), "allow": true, "show_number": true}, 409)
	if len(s.rows) != before {
		t.Fatalf("a voided sign-in made a credential: %d → %d", before, len(s.rows))
	}
	// Before the phone allows it there is nothing to type.
	_, poll2 := f.start(t, nil)
	poll2["number"] = "42"
	if r := browserCall(t, f.mux, "confirm", "", poll2, 409); r["code"] != "not_waiting" || r["state"] != "pending" {
		t.Fatalf("confirm before allow: %v", r)
	}
}

func TestBrowserLoginDenialExpiryAndRevocation(t *testing.T) {
	for _, which := range []string{"denial", "denial after allow", "expiry", "revocation", "other hub"} {
		t.Run(which, func(t *testing.T) {
			f := newBrowserFixture(t)
			mux, token := f.mux, f.phone["token"].(string)
			c, poll := f.start(t, nil)
			deny := map[string]any{"id": c["id"], "hub": f.h.hubID(), "allow": false}
			switch which {
			case "denial":
				browserCall(t, mux, "approve", token, deny, 200)
				if browserCall(t, mux, "poll", "", poll, 200)["state"] != "denied" {
					t.Fatal("not denied")
				}
			case "denial after allow":
				number := f.allow(t, c)
				browserCall(t, mux, "approve", token, deny, 200)
				poll["number"] = number
				if r := browserCall(t, mux, "confirm", "", poll, 409); r["state"] != "denied" {
					t.Fatalf("cancelled sign-in confirmed: %v", r)
				}
			case "expiry":
				f.h.browserLogin.flows[c["id"].(string)].Until = time.Now().Add(-time.Second)
				browserCall(t, mux, "approve", token, deny, 410)
				browserCall(t, mux, "poll", "", poll, 410)
			case "revocation":
				poll["number"] = f.allow(t, c)
				call(t, mux, "DELETE", "/api/v1/auth/tokens/"+f.phone["id"].(string), f.h.cfg.Token, "")
				if r := browserCall(t, mux, "confirm", "", poll, 403); r["code"] != "approver_revoked" {
					t.Fatal(r)
				}
			case "other hub":
				deny["hub"] = "other"
				browserCall(t, mux, "approve", token, deny, 400)
			}
		})
	}
}

// Transport as link sign-in has it: plaintext is refused only from the internet.
func TestBrowserLoginTransport(t *testing.T) {
	for _, tc := range []struct {
		name, remote, proto string
		tls                 bool
		trusted             []string
		want                int
	}{
		{"public TLS", "192.0.2.9:1", "", true, nil, 200},
		{"loopback", "127.0.0.1:1", "", false, nil, 200},
		{"LAN plaintext", "[fd00::20]:1", "", false, nil, 200},
		{"LAN HTTPS proxy", "[fd00::20]:1", "https", false, nil, 200},
		{"trusted container gateway", "[fd00::1]:1", "https", false, []string{"fd00::1/128"}, 200},
		{"untrusted container gateway", "[fd00::1]:1", "https", false, nil, 403},
		{"public plaintext", "192.0.2.9:1", "", false, nil, 403},
		{"public spoofed HTTPS", "192.0.2.9:1", "https", false, nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			f.h.admin.gateways = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("fd00::1")} }
			f.h.cfg.Security.TrustedCIDRs = tc.trusted
			hdr := map[string]string{"X-Forwarded-Proto": tc.proto}
			w := browserReq(f.mux, "start", "", `{"name":"Browser"}`, tc.remote, tc.tls, hdr)
			if w.Code != tc.want {
				t.Fatalf("start: %d %s", w.Code, w.Body)
			}
			if tc.want != 200 {
				if !strings.Contains(w.Body.String(), "tls_required") {
					t.Fatal(w.Body)
				}
				return
			}
			var c map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &c)
			poll := `{"id":"` + c["id"].(string) + `","secret":"` + c["secret"].(string) + `"}`
			if w := browserReq(f.mux, "poll", "", poll, tc.remote, tc.tls, hdr); w.Code != 200 {
				t.Fatalf("poll: %d", w.Code)
			}
			approve := `{"id":"` + c["id"].(string) + `","hub":"` + f.h.hubID() + `","allow":true,"show_number":true}`
			if w := browserReq(f.mux, "approve", f.phone["token"].(string), approve, tc.remote, tc.tls, hdr); w.Code != 200 {
				t.Fatalf("approve from the same connection: %d %s", w.Code, w.Body)
			}
		})
	}
	f := newBrowserFixture(t)
	if w := browserReq(f.mux, "start", "", `{"name":"Browser"}`, "127.0.0.1:1", false, map[string]string{"Origin": "https://untrusted.example"}); w.Code != 403 {
		t.Fatalf("cross-origin request accepted: %d", w.Code)
	}
}

// The network the approving device is told about: never a forwarding header's claim.
func TestBrowserLoginNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		header       map[string]string
		tls          bool
		want         string
	}{
		{"internet", "198.51.100.7:1", nil, true, "internet"},
		{"internet claiming a LAN address", "198.51.100.7:1", map[string]string{"X-Forwarded-For": "fd00::5"}, true, "internet"},
		{"LAN", "[fd00::20]:1", nil, false, "lan"},
		{"this machine", "127.0.0.1:1", nil, false, "lan"},
		{"proxy on this machine", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, false, "unknown"},
		{"LAN proxy", "[fd00::2]:1", map[string]string{"X-Forwarded-Proto": "https"}, false, "unknown"},
		{"container gateway", "[fd00::1]:1", map[string]string{}, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			f.h.admin.gateways = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("fd00::1")} }
			f.h.cfg.Security.TrustedCIDRs = []string{"fd00::1/128"}
			w := browserReq(f.mux, "start", "", `{"name":"Browser"}`, tc.remote, tc.tls, tc.header)
			var c map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 {
				t.Fatalf("start: %d %s", w.Code, w.Body)
			}
			r := browserCall(t, f.mux, "approve", f.phone["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID()}, 200)
			if r["network"] != tc.want {
				t.Fatalf("network %v, want %s", r["network"], tc.want)
			}
		})
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
	f := &browserFixture{h: h, mux: mux, phone: phone}
	c, poll := f.start(t, nil)
	poll["number"] = f.allow(t, c)
	s.failNext = true
	browserCall(t, mux, "confirm", "", poll, 503)
	result := browserCall(t, mux, "confirm", "", poll, 200)
	if len(s.rows) != 2 {
		t.Fatalf("created duplicate credentials: %d", len(s.rows))
	}
	if result["token"] != h.browserLogin.flows[c["id"].(string)].Token {
		t.Fatal("credential identity changed")
	}
}

func TestBrowserLoginTrustedRequestMetadata(t *testing.T) {
	f := newBrowserFixture(t)
	for _, name := range []string{"Browser\nVerified", "Browser\tVerified", "Browser‮Verified"} {
		browserCall(t, f.mux, "start", "", map[string]any{"name": name}, 400)
	}
	c, _ := f.start(t, map[string]any{"name": "Browser"})
	data := browserCall(t, f.mux, "approve", f.phone["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID()}, 200)
	if data["source"] != "127.0.0.1" || data["age_seconds"].(float64) < 0 || data["network"] != "lan" {
		t.Fatal(data)
	}
}

func TestBrowserLoginHasItsOwnLimits(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	mux := h.routes()
	const remote = "198.51.100.7:1"
	jsonReq := func(action, body, from string) *httptest.ResponseRecorder {
		return browserReq(mux, action, "", body, from, true, nil)
	}
	// Password sign-in's limit spent: opening the sign-in page still gets a code.
	for i := 0; i < h.cfg.Security.failures(); i++ {
		jsonReq("password/start", `{}`, remote)
	}
	if w := jsonReq("password/start", `{}`, remote); w.Code != 429 {
		t.Fatalf("password limit not reached: %d", w.Code)
	}
	if w := jsonReq("start", `{"name":"Browser"}`, remote); w.Code != 200 {
		t.Fatalf("password limit blocked a QR start: %d %s", w.Code, w.Body)
	}

	// Starts: their own per-source limit, which leaves password sign-in from elsewhere alone.
	h2 := newTestHub(t)
	h2.store = newPairStore()
	mux = h2.routes()
	for i := 0; i < browserStartLimits.failures(); i++ {
		if w := jsonReq("start", `{"name":"Browser"}`, remote); w.Code != 200 {
			t.Fatalf("start %d refused: %d", i, w.Code)
		}
	}
	w := jsonReq("start", `{"name":"Browser"}`, remote)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("start limit not applied: %d", w.Code)
	}
	if w := jsonReq("password/start", `{}`, remote); w.Code == 429 {
		t.Fatal("QR starts blocked password sign-in")
	}
	if w := jsonReq("start", `{"name":"Browser"}`, "203.0.113.9:1"); w.Code != 200 {
		t.Fatalf("one source's starts blocked another: %d", w.Code)
	}

	// Polls and confirms: only misses count, per source.
	var c map[string]any
	good := jsonReq("start", `{"name":"Browser"}`, "192.0.2.50:1")
	_ = json.Unmarshal(good.Body.Bytes(), &c)
	poll := `{"id":"` + c["id"].(string) + `","secret":"` + c["secret"].(string) + `"}`
	for i := 0; i < 40; i++ {
		if w := jsonReq("poll", poll, "192.0.2.50:1"); w.Code != 200 {
			t.Fatalf("a browser polling its own code was limited at %d: %d", i, w.Code)
		}
	}
	for i := 0; i < browserPollMisses.failures(); i++ {
		action := "poll"
		if i%2 == 1 {
			action = "confirm"
		}
		if w := jsonReq(action, `{"id":"x","secret":"y"}`, "192.0.2.60:1"); w.Code != 410 {
			t.Fatalf("miss %d: %d", i, w.Code)
		}
	}
	if w := jsonReq("poll", `{"id":"x","secret":"y"}`, "192.0.2.60:1"); w.Code != 429 {
		t.Fatalf("misses not limited: %d", w.Code)
	}
	if w := jsonReq("poll", poll, "192.0.2.50:1"); w.Code != 200 {
		t.Fatalf("another source's misses blocked a browser: %d", w.Code)
	}
}

func TestBrowserLoginAuditLog(t *testing.T) {
	f := newBrowserFixture(t)
	var logs bytes.Buffer
	f.h.logger = log.New(&logs, "", 0)
	phoneID := f.phone["id"].(string)
	c, _ := f.start(t, nil)
	browserCall(t, f.mux, "approve", f.phone["token"].(string), map[string]any{"id": c["id"], "hub": f.h.hubID(), "allow": false}, 200)
	c, poll := f.start(t, nil)
	poll["number"] = otherNumber(f.allow(t, c))
	browserCall(t, f.mux, "confirm", "", poll, 409)
	result := f.signIn(t, nil)
	for _, want := range []string{
		`browser sign-in for "Test browser" (requested from 127.0.0.0/24, lan) denied by device ` + phoneID + ` "Phone"`,
		`browser sign-in for "Test browser" (requested from 127.0.0.0/24, lan) allowed by device ` + phoneID + ` "Phone"; waiting for the browser's number`,
		`browser sign-in for "Test browser" (requested from 127.0.0.0/24) voided: the number typed did not match`,
		`device token ` + result["id"].(string) + ` for "Test browser" (web, full) by browser sign-in, approved by device ` + phoneID + ` "Phone"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, logs.String())
		}
	}
}

func TestBrowserLoginReplacesThePreviousWebCredential(t *testing.T) {
	f := newBrowserFixture(t)
	mux := f.mux
	_, old := call(t, mux, "POST", "/api/v1/auth/tokens", f.h.cfg.Token, `{"name":"Old browser","kind":"web"}`)
	// Only a web credential: a phone's id named by a browser stays.
	if r := f.signIn(t, map[string]any{"name": "Browser", "replaces": f.phone["id"]}); r["replaced"] != nil {
		t.Fatalf("replaced a phone: %v", r)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", f.phone["token"].(string), ""); code != 200 {
		t.Fatalf("phone revoked: %d", code)
	}
	r := f.signIn(t, map[string]any{"name": "Browser", "replaces": old["id"]})
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
