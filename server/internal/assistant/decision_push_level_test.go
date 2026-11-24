package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Only choosing what comes first is pushed below P0: the owner's call, but no emergency (a2eb5451, 2026-11-23). Every
// other reason a decision is the owner's still interrupts. A reason added to whyOwners must be listed here.
func TestDecisionPushLevelByWhyOwner(t *testing.T) {
	want := map[string]string{
		whyPriority:      assistantwire.LevelP1,
		whyIrreversible:  assistantwire.LevelP0,
		whyVisible:       assistantwire.LevelP0,
		whyAcceptance:    assistantwire.LevelP0,
		whyOnlyOwnerKnow: assistantwire.LevelP0,
		"":               assistantwire.LevelP0, // a decision from before why_owner was kept
	}
	for _, why := range whyOwners {
		if _, ok := want[why]; !ok {
			t.Errorf("why_owner %q has no push level here", why)
		}
	}
	for why, level := range want {
		if got := decisionPushLevel(why); got != level {
			t.Errorf("decisionPushLevel(%q) = %q, want %q", why, got, level)
		}
	}
}
