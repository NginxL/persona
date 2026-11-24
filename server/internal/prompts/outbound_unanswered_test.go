package prompts

import (
	"strings"
	"testing"
)

// A confirmation card that ended without the user's own allow or decline is reported as nobody answering, for
// every way it can end, and never as the user declining it.
func TestOutboundUnansweredGolden(t *testing.T) {
	const tail = ": nothing was sent to that agent. The user did NOT decline it, so do not say they did. Tell them it is " +
		"waiting for their confirmation, or ask again when they are around."
	for outcome, how := range map[string]string{
		"expired":               "it expired unanswered",
		"withdrawn":             "it was withdrawn before anyone answered it",
		"cleared":               "it was cleared before anyone answered it",
		"answered_by_assistant": "someone else answered it, not the user",
		"answered_by_default":   "someone else answered it, not the user",
		"answered_by_terminal":  "someone else answered it, not the user",
		"dismissed":             "it was closed without a choice",
		"unanswered":            "it was closed without a choice",
	} {
		got := RenderOutboundUnanswered(OutboundUnanswered{Outcome: outcome, Nothing: "nothing was sent to that agent"})
		want := "Nobody answered the confirmation card on the phone (" + how + ")" + tail
		if got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", outcome, got, want)
		}
		if strings.Contains(got, "declined") || strings.Contains(got, "user declined") {
			t.Errorf("%s says the user declined: %s", outcome, got)
		}
	}
}
