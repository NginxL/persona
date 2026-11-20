package proto

import (
	"slices"
	"testing"
)

// The start-speed vocabulary is shared by clients, the hub and nodes. Codex's
// native tier names stay at this boundary instead of leaking into session.start.
func TestSessionStartSpeedVocabularyAndCodexMapping(t *testing.T) {
	if !slices.Equal(Speeds, []string{"standard", "fast", "ultrafast"}) {
		t.Fatalf("shared speeds = %v, want standard, fast, ultrafast", Speeds)
	}
	for _, tc := range []struct {
		speed, tier string
	}{
		{speed: "fast", tier: "priority"},
		{speed: "ultrafast", tier: "ultrafast"},
		{speed: "standard", tier: ""},
		{speed: "", tier: ""},
		{speed: "FAST", tier: ""},
		{speed: "ludicrous", tier: ""},
	} {
		if got := CodexServiceTier(tc.speed); got != tc.tier {
			t.Errorf("CodexServiceTier(%q) = %q, want %q", tc.speed, got, tc.tier)
		}
	}
}
