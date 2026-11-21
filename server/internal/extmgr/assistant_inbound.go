package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What comes in from the channels for the assistant (assistant_channels.go): the owner's messages and card presses.
// The transport took them, so none is dropped without a word: one waits for the assistant (the first seconds of a hub
// start, an assistant restarting) and is asked again when a call was cut off or timed out — the assistant takes a
// message once by its id, so asking again is safe — and only when the assistant stays away is the owner told it can't
// be reached. What the owner did is handed over with a single-use owner action: answering a card in a channel needs
// one (interactions.resolve), so the assistant can't approve a plan, allow a tool or type into a terminal by itself.

// inboundWait is how long an owner's message waits for the assistant (tests shorten it): longer than a hub start
// or an assistant restart takes.
var inboundWait = 60 * time.Second

// inboundMost is how many messages may wait at once (a plugin delivering its backlog right as the hub starts);
// beyond it the owner is told at once.
const inboundMost = 500

// inbound is what waits for the assistant, and the owner actions handed over.
type inbound struct {
	waiting atomic.Int32
	mu      sync.Mutex
	actions map[string]ownerAction
}

// ownerAction is an owner's press or message the hub handed the assistant: what it may answer with it. It answers
// only in the channel it came from: a card's press that card alone, of any kind; a message (no card) only a card of
// one of kinds, those the channel takes a typed answer for.
type ownerAction struct {
	channel     string
	interaction string   // a card's press: that card only ("" for a message)
	kinds       []string // a message: the kinds of card it may answer (none: no card at all)
	approval    *assistantwire.OwnerApprovalProof
	expires     time.Time
}

const ownerActionFor = 10 * time.Minute

// issueOwnerAction records the owner's press on interaction's card in channel and returns its id for the assistant.
func (h *Manager) issueOwnerAction(channel, interaction string) string {
	return h.recordOwnerAction(ownerAction{channel: channel, interaction: interaction})
}

// issueMessageAction records one of the owner's messages in channel and returns its id for the assistant: it answers
// a card of one of kinds, the channel's typed answers (it can't know which card the message answers).
func (h *Manager) issueMessageAction(channel string, kinds ...string) string {
	return h.recordOwnerAction(ownerAction{channel: channel, kinds: kinds})
}

// issueOwnerReplyAction retains the ordinary question-answer capability and
// independently vouches for this exact owner message's text and direct-parent
// location. It does not name an interaction: the assistant must find the
// pending proposal whose posted card is ParentID, then present that target here.
func (h *Manager) issueOwnerReplyAction(channel string, proof assistantwire.OwnerApprovalProof) string {
	return h.recordOwnerAction(ownerAction{channel: channel, kinds: []string{proto.IxQuestion}, approval: &proof})
}

func (h *Manager) recordOwnerAction(a ownerAction) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := "oa_" + hex.EncodeToString(b[:])
	in := &h.chans.in
	in.mu.Lock()
	defer in.mu.Unlock()
	now := time.Now()
	for k, a := range in.actions {
		if now.After(a.expires) {
			delete(in.actions, k)
		}
	}
	if in.actions == nil {
		in.actions = map[string]ownerAction{}
	}
	a.expires = now.Add(ownerActionFor)
	in.actions[id] = a
	return id
}

// errNoOwnerAction: an answer to a card from a channel without the owner's action behind it.
var errNoOwnerAction = i18n.Err("hub-assistant_inbound.no_owner_action")

// takeOwnerAction uses up the owner action id for an answer in channel to interaction, a card of kind (once). The
// action must be one issued in that channel, and be that card's press or a message that answers its kind; anything
// else is refused, and the action stays unused.
func (h *Manager) takeOwnerAction(id, channel, interaction, kind string) error {
	in := &h.chans.in
	in.mu.Lock()
	defer in.mu.Unlock()
	a, ok := in.actions[id]
	if !ok || id == "" || time.Now().After(a.expires) || !a.answers(channel, interaction, kind) {
		return errNoOwnerAction
	}
	delete(in.actions, id)
	return nil
}

// takeOwnerApproval is the strict permission-reply counterpart of
// takeOwnerAction. The expected proof comes from the event and the assistant's
// direct-parent card lookup; every field must equal the hub-issued original.
func (h *Manager) takeOwnerApproval(id, channel, interaction, kind string, expected assistantwire.OwnerApprovalProof) (assistantwire.OwnerApprovalProof, error) {
	in := &h.chans.in
	in.mu.Lock()
	defer in.mu.Unlock()
	a, ok := in.actions[id]
	if !ok || id == "" || time.Now().After(a.expires) || a.channel == "" || a.channel != channel ||
		interaction == "" || kind != proto.IxPermission || a.approval == nil || *a.approval != expected ||
		expected.MessageID == "" || expected.ParentID == "" || expected.ChatID == "" {
		return assistantwire.OwnerApprovalProof{}, errNoOwnerAction
	}
	delete(in.actions, id)
	return *a.approval, nil
}

// answers: the action may answer interaction, a card of kind, in channel.
func (a ownerAction) answers(channel, interaction, kind string) bool {
	switch {
	case channel == "" || a.channel != channel || interaction == "":
		return false
	case a.interaction != "":
		return a.interaction == interaction
	}
	return kind != "" && slices.Contains(a.kinds, kind)
}

// toAssistant hands one of the owner's messages to the assistant (method, params): it waits for the assistant up to
// inboundWait and asks again after a cut-off or timed-out call. An error is the assistant's own refusal, or
// errAssistantOffline when it could not be reached in time.
func (h *Manager) toAssistant(method string, params any) error {
	return h.toAssistantSince(method, params, time.Now())
}

// toAssistantSince is toAssistant for a message that came at since: it waits for the assistant until inboundWait
// after that (a message that waited its turn behind others does not wait the whole time again).
func (h *Manager) toAssistantSince(method string, params any, since time.Time) error {
	in := &h.chans.in
	wait := inboundWait
	if in.waiting.Add(1) > inboundMost {
		wait = 0 // too many waiting already: say so at once
	}
	defer in.waiting.Add(-1)
	return h.callAssistantWaiting(method, params, nil, since.Add(wait), 20*time.Second)
}

// callAssistantWaiting calls the assistant until deadline: while it is not connected it waits, a call cut off or out
// of time (each one at most per) goes again.
func (h *Manager) callAssistantWaiting(method string, params, out any, deadline time.Time, per time.Duration) error {
	for {
		if !h.assistantUp() {
			if time.Now().After(deadline) {
				return errAssistantOffline
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		left := time.Until(deadline)
		ctx, cancel := context.WithTimeout(context.Background(), max(min(per, left), time.Second))
		err := h.callAssistant(ctx, method, params, out)
		cancel()
		var re *rpc.Error
		switch {
		case err == nil:
			return nil
		case errors.As(err, &re) && re.Code != bus.CodeStaleConnection && re.Code != assistantwire.CodeTryAgain:
			return err // the assistant's answer: it refused this one
		case time.Now().After(deadline):
			return errAssistantOffline
		}
		time.Sleep(200 * time.Millisecond)
	}
}
