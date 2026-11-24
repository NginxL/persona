package extmgr

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Only the user's own decline on a device is told as a decline (browser_run, computer_run, iphone_do): a card that
// expired, was withdrawn or cleared, or that someone else answered is nobody answering, and the model is told the user
// did not decline it.
func TestApproveExpiredIsNotDeclined(t *testing.T) {
	if res := notApproved(proto.OutcomeDeclined); !res.IsError || res.Content[0].Text != declinedText {
		t.Fatalf("the user's decline: %+v", res)
	}
	if w := refusalWord(proto.OutcomeDeclined); w != "declined on the phone" {
		t.Fatalf("the user's decline logs %q", w)
	}
	for _, outcome := range []string{proto.OutcomeExpired, proto.OutcomeWithdrawn, proto.OutcomeCleared, proto.OutcomeDismissed,
		proto.OutcomeUnanswered, proto.OutcomeByAssistant, proto.OutcomeByTerminal} {
		res := notApproved(outcome)
		text := res.Content[0].Text
		if !res.IsError || strings.Contains(text, "declined") || !strings.Contains(text, "did NOT decline") {
			t.Errorf("%s: %+v", outcome, res)
		}
		if w := refusalWord(outcome); strings.Contains(w, "declined") {
			t.Errorf("%s logs %q", outcome, w)
		}
	}
}
