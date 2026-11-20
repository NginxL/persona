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

// Held-message reconciliation methods are used between the devices manager and the node.
const (
	MethodSessionEchoStatus = "session.echo_status"
	MethodSessionSendUnseen = "session.send_unseen"
	CodeEchoUnconfirmed     = "echo_unconfirmed"
)

// SessionEchoParams identifies one send in one session.
type SessionEchoParams struct {
	SessionID   string `json:"session_id"`
	ClientMsgID string `json:"client_msg_id"`
}

// SessionEchoState describes the evidence the node has for a send.
type SessionEchoState string

const (
	EchoSeen        SessionEchoState = "seen"
	EchoKnownUnseen SessionEchoState = "known_unseen"
	EchoUnknown     SessionEchoState = "unknown"
)

// SessionEchoStatus is the node's answer to a session.echo_status request.
type SessionEchoStatus struct {
	State SessionEchoState `json:"state"`
}

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

// FeatureTodoSendState is informational as FeatureMessageSendState is: trust only the authenticated hello's top-level
// todo_send_state version. An app declares the same name in client_hello to get the events.
const (
	FeatureTodoSendState = "todo_send_state"
	TodoSendStateVersion = 1
)

// TodoSendState is MessageSendState for a to-do's writes (#10465): a message on its conversation's route or an
// answer to one of its asks, sent with X-Persona-Op-Id. Processing once the hub has claimed the op id, before the
// manager has the write (which may wait for a node, or for a new Agent to start); then, once the receipt is stored,
// delivered (the route answered 2xx), failed (nothing was taken: the answer says delivered "none", no manager had it,
// or it was refused before anything ran; a new op id may send again) or unknown (it may have been taken, a refusal
// that says nothing of delivery included: ask again with the same op id, never send it again). Only to the full
// devices that declared todo_send_state, outside the event log. The HTTP answer, or the op id's receipt, stays
// authoritative.
type TodoSendState struct {
	Type       string    `json:"type"`
	TodoID     string    `json:"todo_id"`
	ClientOpID string    `json:"client_op_id"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
}
