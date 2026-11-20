package proto

import (
	"strings"
	"testing"
)

func TestUnrecoverableTurnErrorCode(t *testing.T) {
	sealed := "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account"
	for _, text := range []string{sealed, strings.ToUpper(sealed), "API Error: 503 " + sealed} {
		if code := UnrecoverableTurnErrorCode(text); code != TurnErrorCompactionSealed {
			t.Errorf("%q: code = %q", text, code)
		}
	}
	for _, text := range []string{
		"",
		"API Error: 503 encrypted connection failed",
		"compaction failed",
		"compacted natively but not sealed",
		"API Error: 400 ToolSearch is not supported on this endpoint; resend with all tools loaded",
	} {
		if code := UnrecoverableTurnErrorCode(text); code != "" {
			t.Errorf("recoverable error %q classified as %q", text, code)
		}
	}
}

func TestUnrecoverableTurnErrorCodeDoesNotAllocate(t *testing.T) {
	text := "API Error: 503 Service Unavailable: " + strings.Repeat("Upstream connection failed. ", 30)
	if allocs := testing.AllocsPerRun(100, func() { UnrecoverableTurnErrorCode(text) }); allocs != 0 {
		t.Fatalf("session-list error classification allocated %v times", allocs)
	}
}
