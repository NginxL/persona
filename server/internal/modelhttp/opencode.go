package modelhttp

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// OpenCodeSessionHeader is the session routing header OpenCode Go requires on every gateway request.
const OpenCodeSessionHeader = "x-opencode-session"

// OpenCodeGo reports whether rawURL targets an OpenCode Go inference gateway
// (opencode.ai or *.opencode.ai under /inference/go or /zen/go).
func OpenCodeGo(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Opaque != "" || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "opencode.ai" && !strings.HasSuffix(host, ".opencode.ai") {
		return false
	}
	p := strings.ToLower(u.Path)
	if strings.Contains(p, "..") {
		return false
	}
	return p == "/inference/go" || strings.HasPrefix(p, "/inference/go/") ||
		p == "/zen/go" || strings.HasPrefix(p, "/zen/go/")
}

// OpenCodeSessionID returns a lowercase UUIDv4-shaped session identifier: deterministic when seed is non-empty,
// random otherwise.
func OpenCodeSessionID(seed string) string {
	var b [16]byte
	if s := strings.TrimSpace(seed); s != "" {
		sum := sha256.Sum256([]byte("persona:opencode-session:" + s))
		copy(b[:], sum[:16])
	} else {
		_, _ = rand.Read(b[:])
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// OpenCodeSessionHeaderValue returns the first non-empty x-opencode-session value in newline-delimited "Name: value" headers.
func OpenCodeSessionHeaderValue(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), OpenCodeSessionHeader) {
			if v := strings.TrimSpace(value); v != "" {
				return v
			}
		}
	}
	return ""
}

// HasOpenCodeSessionHeader reports whether newline-delimited "Name: value" headers already supply x-opencode-session.
func HasOpenCodeSessionHeader(raw string) bool {
	return OpenCodeSessionHeaderValue(raw) != ""
}

func firstNonEmptyHeader(values []string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// SetOpenCodeSession canonicalizes any existing x-opencode-session entries and attaches sessionID (or a new UUIDv4)
// when rawURL is an OpenCode Go gateway and no non-empty caller value is present.
func SetOpenCodeSession(h http.Header, rawURL, sessionID string) {
	if h == nil || !OpenCodeGo(rawURL) {
		return
	}
	canonical := http.CanonicalHeaderKey(OpenCodeSessionHeader)
	chosen := firstNonEmptyHeader(h[canonical])
	var matched []string
	for name := range h {
		if strings.EqualFold(name, OpenCodeSessionHeader) {
			matched = append(matched, name)
		}
	}
	slices.Sort(matched)
	for _, name := range matched {
		if chosen == "" {
			chosen = firstNonEmptyHeader(h[name])
		}
		delete(h, name)
	}
	if chosen == "" {
		chosen = strings.TrimSpace(sessionID)
	}
	if chosen == "" {
		chosen = OpenCodeSessionID("")
	}
	h.Set(OpenCodeSessionHeader, chosen)
}

// ClearOpenCodeSession removes any x-opencode-session header in any casing (for cross-origin redirects).
func ClearOpenCodeSession(h http.Header) {
	for name := range h {
		if strings.EqualFold(name, OpenCodeSessionHeader) {
			delete(h, name)
		}
	}
}
