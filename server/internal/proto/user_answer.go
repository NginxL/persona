package proto

// UserAllowed and UserDenied read the resolution of a permission card only the user may answer (a persona_settings
// change): the user's own allow or deny. Neither holds for a card that expired, was dismissed, withdrawn or cancelled,
// or that someone else answered for the user (AnsweredBy: the assistant, a supervising conversation), nor for an answer
// the history keeps from after the card expired (Late). The tool waiting on it then says nobody answered, never that
// the user declined (#3722).
func (r Resolution) UserAllowed() bool { return r.answeredByUser() && *r.Allow }

// UserDenied: see UserAllowed.
func (r Resolution) UserDenied() bool { return r.answeredByUser() && !*r.Allow }

func (r Resolution) answeredByUser() bool {
	return r.Allow != nil && r.AnsweredBy == "" && !r.Dismiss && !r.Cancel && !r.Late
}

// How a card ended (Resolution.Outcome). Only OutcomeAllowed and OutcomeDeclined are the user's own allow or deny on a
// device; OutcomeAnswered is the user's own answer to a question. Every other outcome is "nobody answered" as far as
// the user is concerned: a tool waiting on such a card must not say the user declined or allowed it, and nothing
// records it as the user's decision.
const (
	OutcomeAllowed  = "allowed"  // the user allowed (or approved) it on a device
	OutcomeDeclined = "declined" // the user declined (or sent back) it on a device
	OutcomeAnswered = "answered" // the user answered a question, picked an option or typed something
	// OutcomeExpired: its time ran out with nobody answering (DismissedExpired), or the answer recorded on it came
	// after it had (Late).
	OutcomeExpired   = "expired"
	OutcomeWithdrawn = "withdrawn" // the asker went away first: its call ended, its hook disconnected (DismissedWithdrawn)
	OutcomeCleared   = "cleared"   // a newer card replaced it, or it was cleared with the others (DismissedCleared)
	OutcomeCancelled = "cancelled" // the question was discarded (Cancel)
	OutcomeInChat    = "replied_in_chat"
	// OutcomeDismissed: closed without a choice (a bare Dismiss: the app's "close", or an end nothing said more of).
	OutcomeDismissed  = "dismissed"
	OutcomeUnanswered = "unanswered" // an empty resolution: no answer at all
	// OutcomeByAssistant, OutcomeByDefault and OutcomeByTerminal: someone or something other than the user on a device
	// gave the answer (AnsweredBy).
	OutcomeByAssistant = "answered_by_" + AnsweredByAssistant
	OutcomeByDefault   = "answered_by_" + AnsweredByDefault
	OutcomeByTerminal  = "answered_by_" + AnsweredByTerminal
)

// Outcome is how the card r answered ended, one of the Outcome* above: allowed or declined only when the user
// themselves allowed or declined it on a device (UserAllowed, UserDenied); expired, withdrawn, cleared, dismissed,
// cancelled, or answered by someone else (AnsweredBy) otherwise. A plan's approval counts as an allow, sending it back
// as a decline.
func (r Resolution) Outcome() string {
	switch {
	case r.AnsweredBy != "":
		return "answered_by_" + r.AnsweredBy
	case r.Late:
		return OutcomeExpired
	case r.Cancel:
		return OutcomeCancelled
	case r.Dismiss:
		switch r.Reason {
		case DismissedExpired:
			return OutcomeExpired
		case DismissedWithdrawn:
			return OutcomeWithdrawn
		case DismissedCleared, "replaced":
			return OutcomeCleared
		case DismissedInChat:
			return OutcomeInChat
		}
		return OutcomeDismissed
	case r.Allow != nil:
		if *r.Allow {
			return OutcomeAllowed
		}
		return OutcomeDeclined
	case r.Approve != nil:
		if *r.Approve {
			return OutcomeAllowed
		}
		return OutcomeDeclined
	case len(r.Answers) > 0 || r.Option != "" || len(r.Options) > 0 || r.Text != "" || len(r.Keys) > 0 || r.Action != "" ||
		len(r.Values) > 0 || len(r.Content) > 0 || r.Mode != "":
		return OutcomeAnswered
	}
	return OutcomeUnanswered
}

// UserDecided says whether outcome (Resolution.Outcome) is the user's own answer on a device: their allow, their
// decline or their answer to a question. Any other is nobody answering, as far as the user is concerned.
func UserDecided(outcome string) bool {
	return outcome == OutcomeAllowed || outcome == OutcomeDeclined || outcome == OutcomeAnswered
}
