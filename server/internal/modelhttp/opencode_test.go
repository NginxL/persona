package modelhttp

import (
	"net/http"
	"regexp"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestOpenCodeGo(t *testing.T) {
	cases := map[string]bool{
		"https://opencode.ai/inference/go/openai/v1":                  true,
		"https://opencode.ai/inference/go/openai/v1/chat/completions": true,
		"https://opencode.ai/inference/go/anthropic":                  true,
		"https://opencode.ai/inference/go/anthropic/v1/messages":      true,
		"https://opencode.ai/inference/go":                            true,
		"https://opencode.ai/zen/go":                                  true,
		"https://opencode.ai/zen/go/v1":                               true,
		"https://opencode.ai/zen/go/anthropic":                        true,
		"https://API.OpenCode.AI:443/Inference/Go/openai/v1":          true,
		"http://sub.opencode.ai:8080/zen/go/v1/chat/completions":      true,
		// Strict domain and path-segment boundaries:
		"https://notopencode.ai/inference/go/openai/v1":   false,
		"https://opencode.ai.example.com/inference/go/v1": false,
		"https://opencode.ai/inference/good/v1":           false,
		"https://opencode.ai/zen/golang/v1":               false,
		"https://opencode.ai/zen/v1":                      false,
		"https://opencode.ai/v1":                          false,
		"https://opencode.ai/inference/go/../v1":          false,
		"ftp://opencode.ai/inference/go/openai/v1":        false,
		"opencode.ai/inference/go/openai/v1":              false,
		"https://api.openai.com/v1":                       false,
		"https://api.anthropic.com":                       false,
		"":                                                false,
	}
	for raw, want := range cases {
		if got := OpenCodeGo(raw); got != want {
			t.Errorf("OpenCodeGo(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestOpenCodeSessionID(t *testing.T) {
	seededA := OpenCodeSessionID("conv-123")
	seededB := OpenCodeSessionID("conv-123")
	seededOther := OpenCodeSessionID("conv-456")
	if !uuidV4Pattern.MatchString(seededA) {
		t.Fatalf("seeded id %q is not UUIDv4", seededA)
	}
	if seededA != seededB || seededA == seededOther {
		t.Fatalf("seeded ids: %q %q %q", seededA, seededB, seededOther)
	}
	randA := OpenCodeSessionID("")
	randB := OpenCodeSessionID("")
	if !uuidV4Pattern.MatchString(randA) || !uuidV4Pattern.MatchString(randB) || randA == randB {
		t.Fatalf("random ids: %q %q", randA, randB)
	}
}

func TestSetAndClearOpenCodeSession(t *testing.T) {
	h := make(http.Header)
	SetOpenCodeSession(h, "https://api.openai.com/v1/chat/completions", "ses-ignored")
	if got := h.Get(OpenCodeSessionHeader); got != "" {
		t.Fatalf("unexpected header on non-opencode host: %q", got)
	}

	SetOpenCodeSession(h, "https://opencode.ai/inference/go/openai/v1/chat/completions", "ses-1")
	if got := h.Get(OpenCodeSessionHeader); got != "ses-1" {
		t.Fatalf("got %q, want ses-1", got)
	}

	// Existing caller header (any casing) is canonicalized and wins without leaving duplicate keys.
	canonicalKey := http.CanonicalHeaderKey(OpenCodeSessionHeader)
	custom := http.Header{"x-opencode-session": {"caller-supplied"}}
	SetOpenCodeSession(custom, "https://opencode.ai/zen/go/v1/chat/completions", "ses-2")
	if len(custom) != 1 || len(custom[canonicalKey]) != 1 || custom.Get(OpenCodeSessionHeader) != "caller-supplied" {
		t.Fatalf("caller header not canonicalized cleanly: %v", custom)
	}

	// A forwarded non-canonical empty value is removed instead of remaining alongside the generated value.
	emptyLower := http.Header{"x-opencode-session": {"", "   "}}
	SetOpenCodeSession(emptyLower, "https://opencode.ai/zen/go/v1/chat/completions", "ses-generated")
	if len(emptyLower) != 1 || len(emptyLower[canonicalKey]) != 1 || emptyLower.Get(OpenCodeSessionHeader) != "ses-generated" {
		t.Fatalf("empty non-canonical header left duplicates: %v", emptyLower)
	}

	// Mixed-case entries combining empty and non-empty values collapse to the single non-empty caller value.
	mixed := http.Header{
		"X-Opencode-Session": {""},
		"x-opencode-session": {"caller-non-empty"},
		"X-OpenCode-Session": {""},
	}
	SetOpenCodeSession(mixed, "https://opencode.ai/inference/go/openai/v1/chat/completions", "ses-fallback")
	if len(mixed) != 1 || len(mixed[canonicalKey]) != 1 || mixed.Get(OpenCodeSessionHeader) != "caller-non-empty" {
		t.Fatalf("mixed-case headers not collapsed cleanly: %v", mixed)
	}

	ClearOpenCodeSession(custom)
	ClearOpenCodeSession(emptyLower)
	ClearOpenCodeSession(mixed)
	ClearOpenCodeSession(h)
	if len(custom) != 0 || len(emptyLower) != 0 || len(mixed) != 0 || len(h) != 0 {
		t.Fatalf("ClearOpenCodeSession left headers: %v %v %v %v", custom, emptyLower, mixed, h)
	}
}
