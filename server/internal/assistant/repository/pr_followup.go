package repository

import "time"

// PRFollowup is a lightweight subscription that keeps watching a to-do's own delivery PR after the to-do was
// accepted. It never reopens the to-do: the PR sweep polls it independently of the to-do's DoneAt and notifies the
// original supervisor conversation once per event, then removes the subscription on a terminal PR state. All fields
// persist in Postgres (todo_pr_followups), never in a JSON setting.
type PRFollowup struct {
	// ID is the subscription id (stable, derived from the to-do id and PR url).
	ID string `json:"id"`
	// TodoID is the accepted (closed) to-do the delivery PR belonged to.
	TodoID string `json:"todo_id"`
	// PRURL is the delivery PR the subscription watches.
	PRURL string `json:"pr_url"`
	// Supervisor is the conversation that reviewed the to-do; notifications go there.
	Supervisor string `json:"supervisor"`
	// Origin is the to-do's own dispatch conversation, kept for the notification's lineage.
	Origin string `json:"origin"`
	// Last observed forge facts. State is "" until the first poll.
	HeadOID            string     `json:"head_oid,omitempty"`
	State              string     `json:"state,omitempty"`
	Review             string     `json:"review,omitempty"`
	ChangesRequestedAt *time.Time `json:"changes_requested_at,omitempty"`
	LastObservedAt     *time.Time `json:"last_observed_at,omitempty"`
	// CreatedAt records the to-do's closure time for new subscriptions.
	CreatedAt time.Time `json:"created_at"`
	// Deduplication keys: a conflict or a current-head changes-requested review wakes the supervisor only once per head.
	NotifiedConflictHead string `json:"notified_conflict_head,omitempty"`
	NotifiedReviewHead   string `json:"notified_review_head,omitempty"`
}
