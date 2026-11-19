package timeline

import (
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Input is everything one to-do's timeline is read from. The JSON fields are the stores' own, under the same names, so
// the manager's rows decode straight into them; what a type does not read is left out.
type Input struct {
	// Todo is the to-do as GET /api/v1/todos/{id} lists it: its runs' links (the ones the owner took off the to-do are
	// not listed), its ledger, its brief and lineage, its open ask.
	Todo Todo `json:"todo"`
	// Feed are the conversation feed's items whose ref is the to-do, in any order; kinds the timeline does not read are
	// skipped.
	Feed []FeedItem `json:"feed"`
	// Decisions are the to-do's decisions, superseded ones included, in any order.
	Decisions []Decision `json:"decisions"`
	// Sessions are its runs' sessions as the hub sends them to the apps (state worked out).
	Sessions []proto.Session `json:"sessions"`
	// Machines are the computers' names by machine id: a receipt written before #10465 names its run's computer only by
	// name.
	Machines map[string]string `json:"machines"`
	// Loc is the owner's time zone, which a Background round's day is in; nil is UTC.
	Loc *time.Location `json:"-"`
}

// Todo is the part of a listed to-do the timeline reads.
type Todo struct {
	ID          string     `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	DoneAt      *time.Time `json:"done_at"`
	StartError  string     `json:"start_error,omitempty"`
	DoneBy      string     `json:"done_by,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	SeenAt      *time.Time `json:"seen_at,omitempty"`
	Agents      []Agent    `json:"agents"`
	// Supervision is its ledger, whose reviews keep the latest 20; Brief its brief and Lineage the to-dos the brief's
	// work went through, both as withBrief adds them.
	Supervision *Supervision  `json:"supervision,omitempty"`
	Brief       *Brief        `json:"brief,omitempty"`
	Lineage     []LineageStep `json:"lineage,omitempty"`
	// Ask is its own open ask and DecisionReview the decision round's review, set when the owner chose Don't handle.
	Ask            *Ask            `json:"ask,omitempty"`
	DecisionReview *DecisionReview `json:"decision_review,omitempty"`
	// What its work produced: the PR its runs named (pr_url), how the hub last saw it and its earlier ones, and how far
	// the finished work went out.
	PRURL    string     `json:"pr_url,omitempty"`
	PRStatus *PRStatus  `json:"pr_status,omitempty"`
	OtherPRs []PRStatus `json:"other_prs,omitempty"`
	Delivery *Delivery  `json:"delivery,omitempty"`
}

// Agent is one of the to-do's runs as its link has it (agents[]).
type Agent struct {
	MachineID  string     `json:"machine_id"`
	SessionID  string     `json:"session_id"`
	LinkedAt   time.Time  `json:"linked_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	Title      string     `json:"title"`
	StartError string     `json:"start_error,omitempty"`
}

// Supervision is a supervised to-do's ledger: where it stands (a cancelled to-do's says cancelled, which the list's
// done_by does not) and its reviews, the latest last.
type Supervision struct {
	State   string   `json:"state"`
	Reviews []Review `json:"reviews"`
}

// Review is one review of the ledger: when, its verdict, who gave it (judge, the done judge; reviewer, the independent
// reviewer; "" the supervisor), the rework round it started, its note, a needs_user review's question or what a park
// waits for (reason), until when a park waits, and the fixed answer the owner gave a needs_user review's decision and
// when (decide_yourself, not_what_i_asked, explain_first).
type Review struct {
	At              time.Time `json:"at"`
	Verdict         string    `json:"verdict"`
	By              string    `json:"by,omitempty"`
	Round           int       `json:"round,omitempty"`
	Note            string    `json:"note,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Until           time.Time `json:"until,omitzero"`
	OwnerResponse   string    `json:"owner_response,omitempty"`
	OwnerResponseAt time.Time `json:"owner_response_at,omitzero"`
}

// Brief is the to-do's brief: who started the work, the owner's request when they did, and its log.
type Brief struct {
	ID        string        `json:"id"`
	StartedBy string        `json:"started_by"`
	Request   *BriefRequest `json:"request,omitempty"`
	Log       []BriefEvent  `json:"log"`
}

// BriefRequest is the owner's message that started the work, as they wrote it.
type BriefRequest struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// BriefEvent is one entry of a brief's log: a scope change (what changed and why) or the overdue mark, and the to-do it
// was made on (a brief is shared by every to-do of one piece of work).
type BriefEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Todo   string    `json:"todo_id,omitempty"`
	Change string    `json:"change,omitempty"`
	Why    string    `json:"why,omitempty"`
}

// LineageStep is one to-do of the brief's lineage: the current one names the to-do it took the work over from.
type LineageStep struct {
	TodoID    string    `json:"todo_id"`
	Title     string    `json:"title,omitempty"`
	Started   time.Time `json:"started"`
	Continues string    `json:"continues,omitempty"`
	Current   bool      `json:"current,omitempty"`
}

// Ask is the to-do's own open ask (the list's ask): what the conclusion came to and when the round was asked.
type Ask struct {
	Outcome  string    `json:"outcome,omitempty"`
	AskedAt  time.Time `json:"asked_at"`
	Question string    `json:"question,omitempty"`
}

// DecisionReview is the decision round under review; DismissalID is set when the owner chose Don't handle on it.
type DecisionReview struct {
	DismissalID string `json:"dismissal_id,omitempty"`
}

// PRStatus is how the hub last saw one of the to-do's PRs.
type PRStatus struct {
	URL       string    `json:"url"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	CheckedAt time.Time `json:"checked_at"`
}

// Delivery is how far the finished work went out (live: deployed) and when that was judged.
type Delivery struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// FeedItem is one item of a conversation's feed whose ref is the to-do; its body is read by its kind.
type FeedItem struct {
	ID   string          `json:"id"`
	At   time.Time       `json:"at"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Decision is one of the to-do's decisions (a todo_decisions row): what was asked and chosen, why, where it came from
// (manual, card, outcome, agent, thread, reported) and the decision that superseded it.
type Decision struct {
	ID            string    `json:"id"`
	Question      string    `json:"question"`
	Choice        string    `json:"choice"`
	Reason        string    `json:"reason"`
	Source        string    `json:"source"`
	SourceMachine string    `json:"source_machine"`
	SourceSession string    `json:"source_session"`
	SourceMessage string    `json:"source_message"`
	SupersededBy  string    `json:"superseded_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// The feed items' bodies, by kind, as the manager writes them (only what the timeline reads).
type (
	// routedBody: words that went to a run of the task. from assistant: Persona's; relay answer: the hub's wording of
	// the owner's answer; neither: the owner's own. new_agent: the run the words started when every run had stopped.
	// machine_id and session_id name the run they reached (#10465).
	routedBody struct {
		Text            string `json:"text"`
		From            string `json:"from,omitempty"`
		Relay           string `json:"relay,omitempty"`
		NewAgent        string `json:"new_agent,omitempty"`
		NewAgentMachine string `json:"new_agent_machine,omitempty"`
		MachineID       string `json:"machine_id,omitempty"`
		SessionID       string `json:"session_id,omitempty"`
	}
	// receiptBody: a run's turn ended (done, interrupted) or a start failed (failed, no run). machine is the run's
	// computer by name; machine_id and session_id name the run on receipts written since #10465.
	receiptBody struct {
		State     string `json:"state"`
		Summary   string `json:"summary,omitempty"`
		PR        string `json:"pr,omitempty"`
		Machine   string `json:"machine,omitempty"`
		MachineID string `json:"machine_id,omitempty"`
		SessionID string `json:"session_id,omitempty"`
	}
	// answeredBody: someone dealt with a run's card: the owner, or Persona in their place (by assistant).
	answeredBody struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Items    []struct {
			Answer string `json:"answer,omitempty"`
		} `json:"items,omitempty"`
		By     string `json:"by,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	// taskBody: the task card; wake says how a run the task did not start came to work for it (active, resumed).
	taskBody struct {
		Wake string `json:"wake,omitempty"`
	}
	// reportBody: a supervised task's run reported to its supervisor.
	reportBody struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	// reviewBody: one acceptance review, as the conversation shows it.
	reviewBody struct {
		Verdict string    `json:"verdict"`
		Round   int       `json:"round,omitempty"`
		Note    string    `json:"note,omitempty"`
		Until   time.Time `json:"until,omitzero"`
	}
)
