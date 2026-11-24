package hub

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
)

type authBucket struct {
	failures int
	until    time.Time
	blocked  bool
}

type authLimiter struct {
	mu         sync.Mutex
	buckets    map[string]authBucket
	nextSweep  time.Time
	nextLog    time.Time
	suppressed uint64
}

func authSourceKey(r *http.Request) string {
	ip := remoteIP(r)
	if !ip.IsValid() {
		return "unknown"
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

func maskedSource(r *http.Request) string {
	ip := remoteIP(r)
	if !ip.IsValid() {
		return "unknown"
	}
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

func (l *authLimiter) sweep(now time.Time) {
	if now.Before(l.nextSweep) {
		return
	}
	for key, b := range l.buckets {
		if !now.Before(b.until) {
			delete(l.buckets, key)
		}
	}
	l.nextSweep = now.Add(time.Minute)
}

// retry is checked before authentication performs any database lookup. Only a blocked source waits: a table filled
// by someone cycling through many prefixes must not lock every other phone out (fail makes room instead), and the
// tokens are far too long to guess one attempt at a time anyway.
func (l *authLimiter) retry(key string, now time.Time, cfg SecurityConfig) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	if b, ok := l.buckets[key]; ok {
		if !now.Before(b.until) {
			delete(l.buckets, key)
		} else if b.blocked {
			return b.until.Sub(now)
		}
	}
	return 0
}

// evict makes room for a new source: the unblocked bucket that ends first, else the blocked one that ends first.
func (l *authLimiter) evict() {
	victim, victimBlocked := "", true
	var until time.Time
	for key, b := range l.buckets {
		better := victim == "" || (victimBlocked && !b.blocked) || (victimBlocked == b.blocked && b.until.Before(until))
		if better {
			victim, victimBlocked, until = key, b.blocked, b.until
		}
	}
	delete(l.buckets, victim)
}

func (l *authLimiter) fail(key string, now time.Time, cfg SecurityConfig) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	if l.buckets == nil {
		l.buckets = map[string]authBucket{}
	}
	b, exists := l.buckets[key]
	if !exists && len(l.buckets) >= cfg.maxSources() {
		l.nextSweep = time.Time{}
		l.sweep(now)
		if len(l.buckets) >= cfg.maxSources() {
			l.evict()
		}
	}
	if !now.Before(b.until) {
		b = authBucket{until: now.Add(cfg.window())}
	}
	b.failures++
	if b.failures >= cfg.failures() && !b.blocked {
		b.blocked, b.until = true, now.Add(cfg.block())
	}
	l.buckets[key] = b
	return b.failures
}

// securityError owns authentication/ingress logs, including failed WS handshakes. The request logger skips
// these responses so public scans cannot produce an unbounded second copy of each failure.
func (h *Hub) securityError(w http.ResponseWriter, r *http.Request, status int, code string, failures int) {
	if sw, ok := w.(*statusWriter); ok {
		sw.securityFailure = true
	}
	l := &h.devtok.failures
	now := time.Now()
	l.mu.Lock()
	log := !now.Before(l.nextLog)
	suppressed := l.suppressed
	if log {
		l.nextLog, l.suppressed = now.Add(h.cfg.Security.logInterval()), 0
	} else {
		l.suppressed++
	}
	l.mu.Unlock()
	if log {
		// Only emit fixed protocol paths: arbitrary paths can contain secrets supplied by the caller.
		path := "/api/"
		if r.URL.Path == "/api/v1/ws" {
			path = r.URL.Path
		}
		h.logf("auth source=%s path=%s status=%d code=%s failures=%d suppressed=%d", maskedSource(r), path, status, code, failures, suppressed)
	}
	message := map[string]string{
		"tls_required":            "TLS is required for public requests",
		codePrivateNetworkOnly:    "this hub answers only its private network: connect through your organization's network or VPN",
		"master_token_local_only": "the master token is restricted to local networks",
		"rate_limited":            "too many authentication failures",
		bus.CodeTokenRevoked:      "this token was revoked or replaced",
		codeClockSkew:             "the manager's clock is more than a minute off the hub's: set its clock right",
		codeProofReplayed:         "this request was sent before",
		codeProofBusy:             "too many requests from this manager's extensions just now",
		codeManagerOffline:        "the manager is not connected to the hub right now: try again shortly",
		codeAuthorizeUnsure:       "a manager did not answer whose this token is: try again shortly",
		codeTokenStoreUnavailable: "the hub could not check this token just now: try again shortly",
	}[code]
	if message == "" {
		message = "invalid token"
	}
	writeErrCode(w, status, code, errors.New(message))
}

func retryAfter(w http.ResponseWriter, retry time.Duration) {
	seconds := max(1, int((retry+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
