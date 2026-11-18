package hub

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Browser challenges are short-lived handshakes, like passwordLoginState. Durable
// device credentials are issued only after approval and are stored in Postgres.
type browserLoginFlow struct {
	Source                                             string
	Masked                                             string // the source as auth logs write it (maskedSource)
	CreatedAt                                          time.Time
	Issued                                             bool
	Secret, Name, State, ApproverHash, Token, DeviceID string
	Replaces, Replaced                                 string // this browser's previous web credential, as in POST /auth/tokens
	Until                                              time.Time
}
type browserLoginState struct {
	mu    sync.Mutex
	flows map[string]*browserLoginFlow
	// Its own limits (#10291): opening the sign-in page, refreshing its code or polling it never counts against
	// password sign-in (passwordLimited), and a block here leaves password sign-in open.
	starts, global, misses authLimiter
}

// Protocol values, not measured capacity: a person refreshes a code a few times a minute at most, and a browser
// that polls its own code never misses.
var (
	browserStartLimits = SecurityConfig{AuthFailures: 20, AuthWindowSeconds: 60, AuthBlockSeconds: 60}  // starts per source
	browserStartGlobal = SecurityConfig{AuthFailures: 200, AuthWindowSeconds: 60, AuthBlockSeconds: 60} // starts, all sources
	browserPollMisses  = SecurityConfig{AuthFailures: 20, AuthWindowSeconds: 60, AuthBlockSeconds: 60}  // unknown id or wrong secret, per source
)

// browserLimited answers 429 for a source (or, for starts, everyone) over browser sign-in's own limits. A start is
// counted here; a poll counts only when it misses (browserMissed).
func (h *Hub) browserLimited(w http.ResponseWriter, r *http.Request, start bool) bool {
	now, key, s := time.Now(), authSourceKey(r), &h.browserLogin
	type check struct {
		limiter *authLimiter
		key     string
		cfg     SecurityConfig
	}
	checks := []check{{&s.misses, key, browserPollMisses}}
	if start {
		checks = []check{{&s.starts, key, browserStartLimits}, {&s.global, "all", browserStartGlobal}}
	}
	for _, c := range checks {
		if d := c.limiter.retry(c.key, now, c.cfg); d > 0 {
			retryAfter(w, d)
			writeErrCode(w, http.StatusTooManyRequests, "rate_limited", i18n.Err("hub-password_login.rate_limited"))
			return true
		}
	}
	if start {
		s.starts.fail(key, now, browserStartLimits)
		s.global.fail("all", now, browserStartGlobal)
	}
	return false
}

func (h *Hub) browserLoginRoutes(mux *http.ServeMux) {
	routes := http.NewServeMux()
	routes.HandleFunc("POST /api/v1/auth/browser-login/start", func(w http.ResponseWriter, r *http.Request) {
		if h.browserLimited(w, r, true) {
			return
		}
		var in struct {
			Name     string `json:"name"`
			Replaces string `json:"replaces,omitempty"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		in.Name, in.Replaces = strings.TrimSpace(in.Name), strings.TrimSpace(in.Replaces)
		if in.Name == "" || len([]rune(in.Name)) > 80 || strings.IndexFunc(in.Name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			writeErr(w, 400, errors.New("invalid browser name"))
			return
		}
		if len(in.Replaces) > 64 {
			writeErr(w, 400, errors.New("invalid replaced credential"))
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, f := range s.flows {
			if !time.Now().Before(f.Until) {
				delete(s.flows, id)
			}
		}
		if len(s.flows) >= passwordFlowLimit {
			writeErr(w, 429, errors.New("too many pending sign-ins"))
			return
		}
		if s.flows == nil {
			s.flows = map[string]*browserLoginFlow{}
		}
		id, secret := randomToken(), randomToken()
		f := &browserLoginFlow{Secret: hashToken(secret), Name: in.Name, Source: remoteIP(r).String(), Masked: maskedSource(r), Replaces: in.Replaces,
			CreatedAt: time.Now(), State: "pending", Until: time.Now().Add(passwordFlowLifetime)}
		link := "persona://browser-login?" + url.Values{"hub": {h.hubID()}, "request": {id}}.Encode()
		qr, err := newQRImage(link)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		s.flows[id] = f
		writeJSON(w, map[string]any{"id": id, "secret": secret, "expires_at": f.Until, "qr": qr, "url": link})
	})
	routes.HandleFunc("POST /api/v1/auth/browser-login/poll", func(w http.ResponseWriter, r *http.Request) {
		if h.browserLimited(w, r, false) {
			return
		}
		var in struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		f := s.flows[in.ID]
		if f == nil || !time.Now().Before(f.Until) || subtle.ConstantTimeCompare([]byte(f.Secret), []byte(hashToken(in.Secret))) != 1 {
			s.misses.fail(authSourceKey(r), time.Now(), browserPollMisses)
			writeErr(w, 410, errors.New("sign-in expired"))
			return
		}
		if f.State != "approved" {
			writeJSON(w, map[string]any{"state": f.State})
			return
		}
		approver, err := h.store.DeviceTokenByHash(r.Context(), f.ApproverHash)
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		if approver == nil || approver.Role != roleFull {
			writeErr(w, 403, errors.New("approving device is no longer authorized"))
			return
		}
		if f.Token == "" {
			f.Token, f.DeviceID = "jd_"+randomHex(24), "dev_"+randomHex(6)
		}
		if !f.Issued {
			// Reconcile a commit whose acknowledgement was lost using the same identity.
			existing, err := h.store.DeviceTokenByHash(r.Context(), hashToken(f.Token))
			if err != nil {
				writeErr(w, 503, err)
				return
			}
			if existing == nil {
				if err := h.store.CreateDeviceToken(r.Context(), db.DeviceToken{ID: f.DeviceID, Name: f.Name, Kind: "web", Role: roleFull}, hashToken(f.Token)); err != nil {
					writeErr(w, 503, err)
					return
				}
			}
			f.Issued = true
			h.logf("device token %s for %q (web, full) by browser sign-in, approved by device %s %q", f.DeviceID, f.Name, approver.ID, approver.Name)
			// Only a web credential, as in POST /auth/tokens: an id a browser kept cannot take a phone, a watch or a link off.
			if old := f.Replaces; old != "" && old != f.DeviceID {
				if ok, err := h.revokeWebToken(r.Context(), old); err != nil {
					h.logf("device token %s: replacing %s: %v", f.DeviceID, old, err)
				} else if ok {
					f.Replaced = old
					h.revoked(old)
					h.sendRevocations(newlyReachable(nil, h.meshView()))
					h.logf("device token %s replaces %s", f.DeviceID, old)
				}
			}
			h.notifyPairing(f.Name, f.DeviceID)
		}
		// The same browser can retry a lost response without creating another device.
		resp := map[string]any{"state": "approved", "token": f.Token, "id": f.DeviceID}
		if f.Replaced != "" {
			resp["replaced"] = f.Replaced
		}
		writeJSON(w, resp)
	})
	approval := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := principalOf(r.Context())
		if !mayTrust(who) || who.ID == "" {
			writeErr(w, 403, errors.New("a signed-in full device must approve"))
			return
		}
		var in struct {
			ID    string `json:"id"`
			Hub   string `json:"hub"`
			Allow *bool  `json:"allow,omitempty"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		if in.Hub != h.hubID() {
			writeErr(w, 400, errors.New("this code belongs to another hub"))
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		f := s.flows[in.ID]
		if f == nil || !time.Now().Before(f.Until) {
			writeErr(w, 410, errors.New("sign-in expired"))
			return
		}
		if in.Allow != nil {
			if f.State != "pending" && f.State != "scanned" {
				writeErr(w, 409, errors.New("sign-in already decided"))
				return
			}
			f.State = "denied"
			if *in.Allow {
				f.State = "approved"
				f.ApproverHash = hashToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			}
			h.logf("browser sign-in for %q (requested from %s) %s by device %s %q", f.Name, f.Masked, f.State, who.ID, who.Name)
		} else if f.State == "pending" {
			f.State = "scanned"
		}
		writeJSON(w, map[string]any{"name": f.Name, "state": f.State, "expires_at": f.Until, "source": f.Source, "age_seconds": int(time.Since(f.CreatedAt).Seconds())})
	})
	routes.Handle("POST /api/v1/auth/browser-login/approve", h.gate(h.auth(approval)))
	mux.Handle("/api/v1/auth/browser-login/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if h.memberRefuses(w) {
			return
		}
		if r.TLS == nil && !remoteIP(r).IsLoopback() {
			writeErrCode(w, 403, "tls_required", errors.New("HTTPS is required"))
			return
		}
		if code, err := adminWriteCheck(r); err != nil {
			writeErrCode(w, 403, code, err)
			return
		}
		if h.store == nil {
			writeErr(w, 503, errors.New("database unavailable"))
			return
		}
		routes.ServeHTTP(w, r)
	}))
}
