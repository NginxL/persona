package assistantwire

import "time"

// Outside-decision node requests. A local script reaches these through its
// node's same-user socket; the manager authenticates the connected machine.
const (
	MethodDecisionRaise    = "decision.raise"
	MethodDecisionGet      = "decision.get"
	MethodDecisionWithdraw = "decision.withdraw"
)

// The states and answer provenance of an outside decision.
const (
	OutsideDecisionPending   = "pending"
	OutsideDecisionAnswered  = "answered"
	OutsideDecisionWithdrawn = "withdrawn"
	OutsideAnswerOwner       = "owner"
	OutsideAnswerDefault     = "default"
)

// OutsideDecisionOption is one choice and what choosing it leads to.
type OutsideDecisionOption struct {
	Label   string `json:"label"`
	LeadsTo string `json:"leads_to"`
}

// OutsideDecisionRequest raises a rich owner decision without waiting for its
// answer. AllowDefault is nil for the normal reversible 24-hour default; false
// disables it for this decision. Reversible is required and describes the action
// itself, not whether the caller wants that default.
type OutsideDecisionRequest struct {
	TodoID        string                  `json:"todo_id,omitempty"`
	Ref           string                  `json:"ref,omitempty"`
	Title         string                  `json:"title"`
	Background    string                  `json:"background"`
	WhyOwner      string                  `json:"why_owner"`
	Options       []OutsideDecisionOption `json:"options"`
	Recommend     string                  `json:"recommend"`
	CostOfWaiting string                  `json:"cost_of_waiting"`
	Reversible    *bool                   `json:"reversible"`
	AllowDefault  *bool                   `json:"allow_default,omitempty"`
}

// OutsideDecisionSource is the authenticated kind and display name of the
// caller, never a source asserted by its JSON body.
type OutsideDecisionSource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// OutsideDecisionAnswer is what the owner (or the permitted deadline default)
// supplied. ExplainFirst can be present while the decision remains pending.
type OutsideDecisionAnswer struct {
	Option     string    `json:"option,omitempty"`
	Response   string    `json:"response,omitempty"`
	Text       string    `json:"text,omitempty"`
	AnsweredBy string    `json:"answered_by"`
	AnsweredAt time.Time `json:"answered_at"`
}

// OutsideDecision is the caller's durable polling view. TodoID is the caller's
// association; AskTodoID is the backing row the existing clients answer.
type OutsideDecision struct {
	ID            string                  `json:"id"`
	TodoID        string                  `json:"todo_id,omitempty"`
	AskTodoID     string                  `json:"ask_todo_id"`
	State         string                  `json:"state"`
	Source        OutsideDecisionSource   `json:"source"`
	Title         string                  `json:"title"`
	Background    string                  `json:"background"`
	WhyOwner      string                  `json:"why_owner"`
	Options       []OutsideDecisionOption `json:"options"`
	Recommend     string                  `json:"recommend"`
	CostOfWaiting string                  `json:"cost_of_waiting"`
	Reversible    bool                    `json:"reversible"`
	Default       string                  `json:"default"`
	Deadline      *time.Time              `json:"deadline,omitempty"`
	CreatedAt     time.Time               `json:"created_at"`
	WithdrawnAt   *time.Time              `json:"withdrawn_at,omitempty"`
	Answer        *OutsideDecisionAnswer  `json:"answer,omitempty"`
}

// OutsideDecisionReply is a node's answer to a local decision command. Error
// and Code describe a refused request; transport failures stay Go errors.
type OutsideDecisionReply struct {
	Decision *OutsideDecision `json:"decision,omitempty"`
	Inserted bool             `json:"inserted,omitempty"`
	Code     string           `json:"code,omitempty"`
	Error    string           `json:"error,omitempty"`
}
