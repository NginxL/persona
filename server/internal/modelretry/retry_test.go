package modelretry

import (
	"testing"
	"time"
)

func TestTemporary(t *testing.T) {
	for _, text := range []string{"408 request timeout", "429 too many requests", "500 internal server error", "502 bad gateway", "503 unavailable", "504 gateway timeout", "529 overloaded", "connection lost", "ECONNRESET", "deadline exceeded", "stream disconnected before completion"} {
		if !Temporary(text) {
			t.Errorf("temporary failure rejected: %s", text)
		}
	}
	for _, text := range []string{"401 upstream 503", "403 timeout", "missing api key", "OAuth token has expired", "permission_error: overloaded", "501 not implemented", "model_not_found", "usage limit", "interrupted by user: 503", "repeated tool loop: timeout", "user decision required", "unknown error"} {
		if Temporary(text) {
			t.Errorf("permanent failure retried: %s", text)
		}
	}
}

func TestAccountExhausted(t *testing.T) {
	for _, text := range []string{
		"API Error: 503 no other eligible account",
		"API Error: 503 no eligible upstream account is currently available",
		"API Error: 503 the upstream request failed and no other eligible upstream account",
		"the upstream request failed and no other eligible upstream account",
		"API Error: 503 NO ELIGIBLE UPSTREAM",
		"unexpected status 503: upstream attempt budget is exhausted",
		"503 upstream attempt budget exhausted",
	} {
		if !AccountExhausted(text) {
			t.Errorf("account exhaustion not recognized: %s", text)
		}
		if !Temporary(text) {
			t.Errorf("account exhaustion must retain its transient classification: %s", text)
		}
	}
	for _, text := range []string{
		"API Error: 503 Service Unavailable",
		"API Error: 529 overloaded",
		"API Error: 429 Too Many Requests",
		"API Error: Connection lost mid-response",
		"API Error: upstream request timeout",
		"API Error: 503 account quota is exhausted",
	} {
		if AccountExhausted(text) {
			t.Errorf("ordinary transient failure misclassified as account exhaustion: %s", text)
		}
	}
}

func TestDelay(t *testing.T) {
	for _, base := range []time.Duration{0, 15 * time.Second, time.Duration(1<<63 - 1)} {
		for _, attempt := range []int{-1, 0, 1, 8, 1000000} {
			for _, limited := range []bool{false, true} {
				ceiling := Ceiling(base, attempt, limited)
				values := map[time.Duration]bool{}
				for i := 0; i < 20; i++ {
					d := Delay(base, attempt, limited)
					if d < ceiling/2 || d > ceiling || d > MaxDelay || d <= 0 {
						t.Fatalf("delay %v outside ceiling %v", d, ceiling)
					}
					values[d] = true
				}
				if len(values) < 2 {
					t.Fatal("jitter did not vary")
				}
			}
		}
	}
	if Ceiling(15*time.Second, 1, false) != 30*time.Second || Ceiling(15*time.Second, 0, true) != time.Minute {
		t.Fatal("exponential or rate-limit multiplier changed")
	}
}

func TestUpstreamServerError(t *testing.T) {
	for _, text := range []string{
		"API Error: 500 Internal Server Error",
		"API Error: 502 Bad Gateway",
		"API Error: 503 Service Unavailable",
		"API Error: 504 Gateway Timeout",
		"API Error: 529 Overloaded",
		"API Error: 503 no eligible upstream account is currently available",
		"API Error: 503 no eligible upstream account for model claude-sonnet-4-5 (model not found on pool)",
		"API Error: 503 model claude-x not available, service unavailable",
		"unexpected status 503 Service Unavailable",
		"server_error: upstream connect error or disconnect/reset before headers",
	} {
		if !UpstreamServerError(text) {
			t.Errorf("upstream server error rejected: %s", text)
		}
	}
	for _, text := range []string{
		"API Error: 400 invalid_request_error",
		`API Error: 400 {"error":{"message":"no account match for model speech_o50"}}`,
		"API Error: 401 invalid x-api-key",
		"Not logged in · Please run /login",
		"API Error: 429 rate limit exceeded",
		"API Error: Request rejected (429) · upstream account capacity is temporarily unavailable",
		"You've hit your session limit · resets 4:20pm (UTC)",
		"API Error: Connection error.",
		"request timed out",
		"501 Not Implemented",
	} {
		if UpstreamServerError(text) {
			t.Errorf("non-upstream-5xx error misclassified: %s", text)
		}
	}
	const sample = "API Error: 503 no eligible upstream account is currently available"
	if allocs := testing.AllocsPerRun(100, func() { _ = AccountExhausted(sample) }); allocs != 0 {
		t.Fatalf("AccountExhausted allocated %v per call, want 0", allocs)
	}
	// Warm lazy-compiled regexes once, then verify steady-state calls allocate zero bytes.
	_ = UpstreamServerError("API Error: 503 Service Unavailable")
	if allocs := testing.AllocsPerRun(100, func() { _ = UpstreamServerError("API Error: 503 Service Unavailable") }); allocs != 0 {
		t.Fatalf("UpstreamServerError allocated %v per call, want 0", allocs)
	}
}
