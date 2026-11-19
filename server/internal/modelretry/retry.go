// Package modelretry classifies temporary model failures and bounds retry delays.
package modelretry

import (
	"math/rand/v2"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var permanentStatus = lazyre.New(`\b(?:400|401|402|403|404|405|409|410|413|422|501|505)\b`)
var temporaryStatus = lazyre.New(`\b(?:408|429|500|502|503|504|529)\b`)

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
		"rate limit", "rate_limit", "too many requests", "overloaded", "at capacity", "no eligible upstream",
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

// Permanent recognizes explicit evidence that continuation cannot fix.
func Permanent(text string) bool {
	t := strings.ToLower(text)
	if permanentStatus.MatchString(t) || assistantwire.AuthErrorText(t) {
		return true
	}
	for _, word := range []string{
		"unauthorized", "authentication", "permission denied", "access denied", "forbidden", "invalid api key",
		"api key missing", "missing api key", "not logged in", "sign in", "login required", "token expired",
		"invalid_request", "invalid request", "model_not_found", "model not found", "model does not exist",
		"context length", "context window", "context_length", "prompt is too long", "too many tokens",
		"usage limit", "usage_limit", "quota_exhausted", "insufficient_quota",
		"interrupted by user", "aborted by user", "user aborted", "request was aborted", "cancelled", "canceled",
		"tool loop", "repeated tool", "user decision required",
	} {
		if strings.Contains(t, word) {
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
