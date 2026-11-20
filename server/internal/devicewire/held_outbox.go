package devicewire

import "time"

// HeldOutboxPage is the owner's per-computer view of the held Agent message queue.
type HeldOutboxPage struct {
	MachineID  string              `json:"machine_id"`
	Computer   string              `json:"computer"`
	Messages   []HeldOutboxMessage `json:"messages"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// HeldOutboxMessage describes delivery without carrying the message, attachments or caller credentials.
type HeldOutboxMessage struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	SentAt     time.Time `json:"sent_at"`
	HeldAt     time.Time `json:"held_at"`
	State      string    `json:"state"`
	Failure    string    `json:"failure,omitempty"`
	Detail     string    `json:"detail"`
	CanRetry   bool      `json:"can_retry"`
	CanDiscard bool      `json:"can_discard"`
}

// HeldOutboxRoute is relayed to the manager of the named computer.
const HeldOutboxRoute = "GET /api/v1/machines/{mid}/held-messages"

const (
	HeldOutboxWaiting  = "waiting"
	HeldOutboxSending  = "sending"
	HeldOutboxChecking = "checking"
	HeldOutboxFailed   = "failed"
	HeldOutboxPageSize = 50
	HeldOutboxMaxPage  = 100
)

// HeldOutboxStateValid recognizes the optional filter shared by the CLI and the manager.
func HeldOutboxStateValid(state string) bool {
	switch state {
	case "", HeldOutboxWaiting, HeldOutboxSending, HeldOutboxChecking, HeldOutboxFailed:
		return true
	default:
		return false
	}
}
