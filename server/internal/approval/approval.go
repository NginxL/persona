// Package approval carries a prepared assistant mutation between its owner and
// the interaction coordinator. The executable operation is process-local: it is
// never serialized into an interaction or a clarification journal.
package approval

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/proto"
)

// ErrStale says the current state no longer produces the proposal on the card.
// The operation must not apply; the owner needs a fresh card.
var ErrStale = errors.New("the proposal is stale; ask for a fresh approval")

// Operation is one immutable proposal and its owner-specific apply path. Apply
// must recheck current state immediately before changing anything. Discard
// releases any owner-side handle without applying; it is optional for direct
// in-process operations and used by the hub's retained settings handle.
type Operation struct {
	// Replacement identifies equivalent proposals within a conversation. A new
	// operation replaces an older one only after that older card is Clarifying.
	Replacement string
	Apply       func(context.Context, proto.Resolution) (string, error)
	Discard     func(context.Context, string)
	// Denied and Unanswered preserve each tool's existing non-approval result.
	Denied     string
	Unanswered string
}

// Outcome is what the original tool call returns. Clarifying is non-final: the
// same card and prepared operation remain pending for a later genuine Allow or
// Deny. Text is the tool result in either phase.
type Outcome struct {
	Clarifying bool   `json:"clarifying,omitempty"`
	Text       string `json:"text"`
}

// ClarifyingText explicitly prevents the model from issuing an equivalent card.
const ClarifyingText = "The owner asked a question about this approval. Nothing changed. The original approval card remains valid and is still pending; answer the question, and do not retry the tool or create another approval. A later message will report the original card's final outcome."

// WaitingText is the original tool call's non-final result when the owner has not answered within the card's decision
// window: the card stays pending on the owner's apps (the Persona app and the web), where they can still approve it,
// whether or not a notification reached them.
const WaitingText = "The owner has not answered this approval yet. Nothing changed. The approval card stays pending in the Persona app and on the web, where the owner can still allow or deny it, even if no notification reached their devices; do not retry the tool or create another approval. A later message will report the original card's final outcome."
