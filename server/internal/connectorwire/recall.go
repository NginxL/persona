package connectorwire

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SentMessage is a message one of a connector's writes made, as the sent-as-user ledger keeps it (docs/extensions/connectors.md
// "Sent as the user"): what a connector needs to find the message again.
type SentMessage struct {
	Action string          // the tool that wrote it (Sent.Action)
	Ref    string          // its id on the other side (Sent.MessageRef), never empty
	Target json.RawMessage // where it went, as the ledger row has it (GitHub's {"ref": "owner/name#12"})
	At     time.Time       // when the ledger row was written, after the send: what a connector with a limit of its own counts from
}

// What Recall wraps (errors.Is) to say why it did not take the message back; an error that wraps none of them is the
// other side saying no, and nothing changed.
var (
	// ErrRecallUnknown: the request may have taken effect without the connector being told (a timeout, a lost
	// connection, an answer that does not say): the message may be gone or not.
	ErrRecallUnknown = errors.New("could not confirm whether the message was taken back")
	// ErrCannotRecall: nothing was sent, as this message cannot be taken back: the row does not give what the connector
	// needs to find it (an id or a target it could not have written), or it is a kind the connector cannot recall.
	ErrCannotRecall = errors.New("this message cannot be taken back")
	// ErrRecallUnavailable: nothing was sent, as the connector cannot act now (no token, the computer it reads one
	// from is offline) or its credentials were refused.
	ErrRecallUnavailable = errors.New("the connector is not available to take the message back")
)

// Recaller is an Actions that can also take back a message one of its writes made (docs/extensions/connectors.md "Sent as the
// user"). Like RefActions it is not a capability of a built-in connector: nothing is declared and Check does not look
// for it, the hub finds it with RecallerOf. A program's connector (a proxy, which implements every interface) is one
// only when its manifest declares CapRecall. A connector without it has nothing to recall.
type Recaller interface {
	Actions
	// CanRecall says whether the message that the write action made, age ago, can still be taken back. It goes by the
	// action and the age alone and answers from memory (a page of the ledger asks it for every row): whether the other
	// side agrees is Recall's to find out. The limit a service sets on how late a message can be taken back is its own.
	CanRecall(action string, age time.Duration) bool
	// Recall takes the message back, once the user has agreed. It returns nil only when the other side confirms the
	// message is no longer there, a message that was already gone included, and it may be asked again after an answer
	// was lost: a repeat after success is a success too. An error wrapping ErrRecallUnknown says the request may have
	// taken effect; ErrCannotRecall and ErrRecallUnavailable that nothing was sent, and why; any other error is the
	// other side refusing, and nothing changed. Its words are one sentence for people, never a secret or the
	// message's text.
	Recall(ctx context.Context, m SentMessage) error
}

// RecallerOf is c as a Recaller, when it is one: the one way the hub finds it. A proxy (a program's connector, which
// implements every interface and has only what it declares) is one only when it declares CapRecall.
func RecallerOf(c Connector) (Recaller, bool) {
	r, ok := c.(Recaller)
	if ok && c.Manifest().Proxy && !c.Manifest().Has(CapRecall) {
		return nil, false
	}
	return r, ok
}
