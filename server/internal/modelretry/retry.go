// Package modelretry classifies temporary model failures and bounds retry delays.
package modelretry

import (
	"math/rand/v2"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var permanentStatusCodes = []string{"400", "401", "402", "403", "404", "405", "409", "410", "413", "422", "501", "505"}
var temporaryStatus = lazyre.New(`\b(?:408|429|500|502|503|504|529)\b`)
var upstream5xxStatusCodes = []string{"500", "502", "503", "504", "520", "521", "522", "523", "524", "529"}
var rateLimitStatusCodes = []string{"429"}

func isASCIIWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
}

func hasStatusCode(text string, codes []string) bool {
	for i := 0; i+3 <= len(text); i++ {
		if text[i] < '4' || text[i] > '5' {
			continue
		}
		if (i > 0 && isASCIIWordByte(text[i-1])) || (i+3 < len(text) && isASCIIWordByte(text[i+3])) {
			continue
		}
		tok := text[i : i+3]
		for _, c := range codes {
			if tok == c {
				return true
			}
		}
	}
	return false
}

// containsFoldASCII matches an ASCII lowercase marker case-insensitively without allocating a lowercased copy.
func containsFoldASCII(text, lowerMarker string) bool {
	for i := 0; i+len(lowerMarker) <= len(text); i++ {
		c := text[i]
		if c == lowerMarker[0] || (c >= 'A' && c <= 'Z' && c+('a'-'A') == lowerMarker[0]) {
			if strings.EqualFold(text[i:i+len(lowerMarker)], lowerMarker) {
				return true
			}
		}
	}
	return false
}

// Temporary requires positive evidence of a recoverable failure. Permanent
// evidence wins even when an error also quotes an upstream temporary failure.
func Temporary(text string) bool {
	t := strings.ToLower(text)
	if Permanent(t) {
		return false
	}
	if temporaryStatus.MatchString(t) {
		return true
	}
	for _, word := range []string{
		"rate limit", "rate_limit", "too many requests", "overloaded", "at capacity", "no eligible upstream", "no other eligible upstream",
		"timed out", "timeout", "deadline exceeded", "socket hang up", "econnreset", "econnrefused", "eai_again",
		"connection reset", "connection refused", "connection closed", "connection error", "connection lost", "network error",
		"upstream connect error", "upstream request timeout", "network is unreachable", "fetch failed", "stream disconnected", "stream error",
		"internal server error", "service unavailable", "bad gateway", "temporarily unavailable",
		"can't reach the api server", "unable to connect to api", "no response from api",
	} {
		if strings.Contains(t, word) {
			return true
		}
	}
	return false
}

var accountExhaustedWords = []string{
	"no other eligible account",
	"no eligible upstream",
	"no other eligible upstream",
	"upstream attempt budget is exhausted",
	"upstream attempt budget exhausted",
}

var rateLimitWords = []string{"rate limit", "rate_limit", "too many requests"}

var upstreamServerWords = []string{
	"internal server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"overloaded",
	"at capacity",
	"temporarily unavailable",
	"upstream connect error",
	"upstream request timeout",
	"upstream unavailable",
}

var permanentWords = []string{
	"unauthorized", "authentication", "permission denied", "access denied", "forbidden", "invalid api key",
	"api key missing", "missing api key", "not logged in", "sign in", "login required", "token expired",
	"invalid_request", "invalid request", "model_not_found", "model not found", "model does not exist",
	"context length", "context window", "context_length", "prompt is too long", "too many tokens",
	"usage limit", "usage_limit", "quota_exhausted", "insufficient_quota",
	"interrupted by user", "aborted by user", "user aborted", "request was aborted", "cancelled", "canceled",
	"tool loop", "repeated tool", "user decision required",
}

// AccountExhausted recognizes a provider response saying that its upstream
// account pool has no account available for another attempt. It remains a
// transient classification; the caller's policy decides whether to retry it.
func AccountExhausted(text string) bool {
	for _, word := range accountExhaustedWords {
		if containsFoldASCII(text, word) {
			return true
		}
	}
	return false
}

// UpstreamServerError recognizes an upstream 5xx or gateway/capacity outage
// (including explicit account-pool exhaustion), distinct from 4xx client/auth
// failures, 429 rate limits, subscription usage limits, and local socket timeouts.
func UpstreamServerError(text string) bool {
	if AccountExhausted(text) {
		return true
	}
	if Permanent(text) || hasStatusCode(text, rateLimitStatusCodes) {
		return false
	}
	for _, rl := range rateLimitWords {
		if containsFoldASCII(text, rl) {
			return false
		}
	}
	if hasStatusCode(text, upstream5xxStatusCodes) {
		return true
	}
	for _, word := range upstreamServerWords {
		if containsFoldASCII(text, word) {
			return true
		}
	}
	return false
}

// Permanent recognizes explicit evidence that continuation cannot fix.
func Permanent(text string) bool {
	if hasStatusCode(text, permanentStatusCodes) {
		return true
	}
	for _, word := range assistantwire.AuthErrorWords {
		if containsFoldASCII(text, word) {
			return true
		}
	}
	for _, word := range permanentWords {
		if containsFoldASCII(text, word) {
			return true
		}
	}
	return false
}

const MaxDelay = 30 * time.Minute

// Delay returns exponential equal jitter in [ceiling/2, ceiling]. Saturating
// before multiplication keeps arbitrarily late attempts and large bases safe.
func Delay(base time.Duration, attempt int, limited bool) time.Duration {
	ceiling := Ceiling(base, attempt, limited)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling-ceiling/2)+1))
}

func Ceiling(base time.Duration, attempt int, limited bool) time.Duration {
	if base <= 0 {
		base = 15 * time.Second
	}
	d := min(base, MaxDelay)
	if limited {
		d = min(d, MaxDelay/4) * 4
	}
	for i := 0; i < max(attempt, 0) && d < MaxDelay; i++ {
		d = min(d, MaxDelay/2) * 2
	}
	return min(d, MaxDelay)
}
