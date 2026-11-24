package proto

import "testing"

func TestUserAllowedAndDenied(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name          string
		r             Resolution
		allow, denied bool
	}{
		{"the user allows", Resolution{Allow: &yes}, true, false},
		{"an allow kept from always", Resolution{Allow: &yes, Remembered: true}, true, false},
		{"the user denies", Resolution{Allow: &no}, false, true},
		{"expired", Resolution{Dismiss: true, Reason: DismissedExpired}, false, false},
		{"dismissed", Resolution{Dismiss: true}, false, false},
		{"cancelled", Resolution{Cancel: true}, false, false},
		{"nothing", Resolution{}, false, false},
		{"allowed by the assistant", Resolution{Allow: &yes, AnsweredBy: AnsweredByAssistant}, false, false},
		{"denied by the assistant", Resolution{Allow: &no, AnsweredBy: AnsweredByAssistant}, false, false},
		{"an allow with a dismiss", Resolution{Allow: &yes, Dismiss: true}, false, false},
	} {
		if c.r.UserAllowed() != c.allow || c.r.UserDenied() != c.denied {
			t.Errorf("%s: allowed %v denied %v, want %v %v", c.name, c.r.UserAllowed(), c.r.UserDenied(), c.allow, c.denied)
		}
	}
}

// A card ends in one of three ways as far as the user is concerned: they allowed it, they declined it, or nobody
// answered it. Only the user's own allow or deny on a device is allowed or declined; a card that expired, was withdrawn,
// cleared or cancelled, that someone else answered (the assistant, a default, the terminal), or whose answer came late
// is never either.
func TestResolutionOutcome(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name string
		r    Resolution
		want string
	}{
		{"the user allows", Resolution{Allow: &yes}, OutcomeAllowed},
		{"the user allows always", Resolution{Allow: &yes, Always: true}, OutcomeAllowed},
		{"the user denies", Resolution{Allow: &no}, OutcomeDeclined},
		{"the user approves a plan", Resolution{Approve: &yes}, OutcomeAllowed},
		{"the user sends a plan back", Resolution{Approve: &no, Feedback: "smaller"}, OutcomeDeclined},
		{"the user answers a question", Resolution{Answers: map[string]string{"a": "1"}}, OutcomeAnswered},
		{"expired", Resolution{Dismiss: true, Reason: DismissedExpired}, OutcomeExpired},
		{"withdrawn", Resolution{Dismiss: true, Reason: DismissedWithdrawn}, OutcomeWithdrawn},
		{"cleared", Resolution{Dismiss: true, Reason: DismissedCleared}, OutcomeCleared},
		{"replaced", Resolution{Dismiss: true, Reason: "replaced"}, OutcomeCleared},
		{"replied in the chat", Resolution{Dismiss: true, Reason: DismissedInChat}, OutcomeInChat},
		{"closed", Resolution{Dismiss: true}, OutcomeDismissed},
		{"an allow with a dismiss", Resolution{Allow: &yes, Dismiss: true}, OutcomeDismissed},
		{"cancelled", Resolution{Cancel: true}, OutcomeCancelled},
		{"nothing", Resolution{}, OutcomeUnanswered},
		{"allowed by the assistant", Resolution{Allow: &yes, AnsweredBy: AnsweredByAssistant}, OutcomeByAssistant},
		{"denied by the assistant", Resolution{Allow: &no, AnsweredBy: AnsweredByAssistant}, OutcomeByAssistant},
		{"taken by default", Resolution{Answers: map[string]string{"a": "1"}, AnsweredBy: AnsweredByDefault, Late: true}, OutcomeByDefault},
		{"closed in the terminal", Resolution{Dismiss: true, AnsweredBy: AnsweredByTerminal}, OutcomeByTerminal},
		{"answered after it expired", Resolution{Answers: map[string]string{"a": "1"}, Late: true}, OutcomeExpired},
		{"expired, then a late allow", Resolution{Allow: &yes, Late: true}, OutcomeExpired},
	} {
		got := c.r.Outcome()
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		users := c.r.AnsweredBy == "" && !c.r.Late && !c.r.Dismiss && !c.r.Cancel && (c.r.Allow != nil || c.r.Approve != nil || len(c.r.Answers) > 0)
		if UserDecided(got) != users {
			t.Errorf("%s: UserDecided(%q) = %v, want %v", c.name, got, UserDecided(got), users)
		}
		if (got == OutcomeAllowed) != (c.r.UserAllowed() || c.r.Approve != nil && *c.r.Approve && users) {
			t.Errorf("%s: allowed %q but UserAllowed %v", c.name, got, c.r.UserAllowed())
		}
		if (got == OutcomeDeclined) != (c.r.UserDenied() || c.r.Approve != nil && !*c.r.Approve && users) {
			t.Errorf("%s: declined %q but UserDenied %v", c.name, got, c.r.UserDenied())
		}
	}
}
