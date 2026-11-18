package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// FeatureMessageSendState is informational: extension features can appear in
// older hubs' feature arrays. Trust only the authenticated hello's top-level
// message_send_state version, written by the hub, not an extension report.
const (
	FeatureMessageSendState = "message_send_state"
	MessageSendStateVersion = 1
)

const (
	MessageSendProcessing = "processing"
	MessageSendDelivered  = "delivered"
	MessageSendFailed     = "failed"
	MessageSendUnknown    = "unknown"
	// MessageSendKept: nothing went in, and the assistant conversation's manager keeps the message and sends it again
	// by itself (the HTTP answer is 202 {"delivered":"later"}; the row's undelivered list shows it, #10194).
	MessageSendKept = "kept"
)

// SendTraceID is a bounded diagnostic correlation, never the original message ID.
// Empty IDs carry no cross-process correlation; timings remain useful locally.
func SendTraceID(id string) string {
	if id == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

// MessageSendState reports an HTTP send's progress to session subscribers.
// Processing only acknowledges hub receipt, not persistence or runtime delivery.
// It never supersedes the session's state or another executing turn. Clients keep
// it per client message ID, independently of session snapshots, and reconcile a
// lost event with the final HTTP response or a retry using the same ID.
// Delivered acknowledges input delivery, not completion of the model's answer.
// Clients consume early events only when their current authenticated hello has
// message_send_state equal to version 1; a feature-array entry alone is not proof.
type MessageSendState struct {
	Type        string    `json:"type"`
	MachineID   string    `json:"machine_id"`
	SessionID   string    `json:"session_id"`
	ClientMsgID string    `json:"client_msg_id"`
	State       string    `json:"state"`
	At          time.Time `json:"at"`
}
