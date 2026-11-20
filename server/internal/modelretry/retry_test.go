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
