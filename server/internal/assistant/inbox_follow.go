package assistant

import "time"

// Following a matter up (docs/assistant/inbox.md "Following up"): an item stays one matter for as long as it goes on.
// It says where it happens (InboxItem.Where), who wrote its title (TitleBy), what it waits on and when to look at it
// again (Follow), and, as listed, its state as a key (Now) that every app puts in words from the same table
// (spec/fixtures/inbox/now.json).

// Who wrote an item's title and summary (InboxItem.TitleBy).
const (
	TitleByModel     = "model"     // the grouping model
	TitleByRules     = "rules"     // the connector's fallback grouping
	TitleByAssistant = "assistant" // the processing conversation
	TitleByUser      = "user"      // the user
)

// InboxFollow is what a matter waits on: who or what (Who, in the user's words), the next step (Next), when to look
// at it again (CheckAt), how many times in a row it was looked at again with nothing new (Checks), and whether that
// time came with nobody to look (Due: the AI twin was off, so it went back to the user).
type InboxFollow struct {
	Who     string     `json:"who,omitempty"`
	Next    string     `json:"next,omitempty"`
	CheckAt *time.Time `json:"check_at,omitempty"`
	Checks  int        `json:"checks,omitempty"`
	Due     bool       `json:"due,omitempty"`
}

// State keys (InboxItem.Now). The apps put each in words; "" (an older hub) leaves them on their own state words.
const (
	NowConfirmReply   = "confirm_reply"   // a reply suggestion waits for the user
	NowConfirmAgent   = "confirm_agent"   // an Agent suggestion waits for the user to start it
	NowDecide         = "decide"          // a suggestion handed back: the user decides
	NowQueued         = "queued"          // queued for the AI
	NowLooking        = "looking"         // the AI is looking
	NowAgentWorking   = "agent_working"   // an Agent it started is working
	NowWaiting        = "waiting"         // waiting on the other side (Follow says on whom, when to look again, the next step)
	NowFollowUpAsk    = "follow_up_ask"   // looked at again three times with nothing new: back to the user
	NowCheckDue       = "check_due"       // the time to look again came while the AI twin was off: back to the user
	NowFailed         = "failed"          // the AI could not finish (Handling.Error says why)
	NowNeedsYou       = "needs_you"       // needs the user, nothing prepared
	NowFYI            = "fyi"             // worth knowing
	NowFolded         = "folded"          // folded
	NowDoneReplied    = "done_replied"    // done: the user replied
	NowDoneManager    = "done_manager"    // done: the AI looked, nothing to do
	NowDoneUser       = "done_user"       // done: the user marked it done
	NowDoneRead       = "done_read"       // done: read elsewhere
	NowDoneDismissed  = "done_dismissed"  // done: the user dismissed it
	NowDoneMuted      = "done_muted"      // done: muted (done_by read; the newest done entry has By "muted")
	NowDoneSuperseded = "done_superseded" // done: a newer item took its place
)

// nowKey is the item's state key, from the item alone (no clock, no settings: the list memo holds,
// inbox_list_memo.go). Empty until the keys are worked out from the item's state.
func (it *InboxItem) nowKey() string { return "" }
