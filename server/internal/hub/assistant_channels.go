package hub

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The channels are the assistant's (internal/assistant, channels*.go and feishu*.go; docs/extensions/channels.md): which
// conversation a channel talks to, following the turn and sending the answer back, what is asked there, the commands,
// the pushes and the sync mirror. This file is the hub's end of them on the bus:
//
//   - in: the owner's messages (channels.receive, feishu.message) and card presses (feishu.card_action) go to the
//     assistant (bots_chat.go, channels_extension.go); new and ended cards are told (interaction.announced,
//     interaction.ended); a conversation it follows has its items and the text it writes sent on (session.items,
//     session.delta) until it unfollows or disconnects;
//   - out: plain transport methods (app.call feishu.*, channels.*, interactions.resolve; device.call session.follow,
//     session.unfollow, session.send_once) the assistant asks for.
//
// The session lists' channels and the finished push a channel's turn skips are no longer drawn here: the manager's
// fleet reads them from the assistant's snapshot (ChannelsReport; internal/assistant/fleet_snapshot.go, fleet_push.go).

// channelNoted tells the assistant a channel's message went into a conversation through the apps' routes (a voice
// conversation's errand, POST …/messages with client_msg_id voice-…): the conversation shows where it came from.
func (h *Hub) channelNoted(id, machine, session string) {
	_ = h.notifyBus(h.sessionOwner(machine, session), assistantwire.EventChannelNoted, assistantwire.ChannelNoted{Channel: id, MachineID: machine, SessionID: session})
}

// ---- following a conversation for the assistant ----

// channelsDeviceCall answers the device methods of the channels' turns; false for the others.
func (h *Hub) channelsDeviceCall(ctx context.Context, m *Machine, p assistantwire.DeviceCall) (json.RawMessage, bool, error) {
	switch p.Method {
	case assistantwire.DeviceSessionSendOnce, assistantwire.DeviceAssistantAnswerAgain:
		var a map[string]any
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
		if id, _ := a["client_msg_id"].(string); id == "" {
			return nil, true, errors.New("session.send_once needs client_msg_id")
		}
		manager, _ := callerOf(ctx)
		method := "session.send"
		if p.Method == assistantwire.DeviceAssistantAnswerAgain {
			method = p.Method
			if after, _ := a["answer_again_after"].(string); after == "" {
				return nil, true, errors.New("assistant.answer_again needs the transcript tail")
			}
		}
		return json.RawMessage(`{}`), true, h.sendOnce(ctx, m, method, a, manager)
	}
	return nil, false, nil
}

// sendOnce sends with the same once-only guard as the app (send.go): a message id is sent at most once.
// A clarification proof is consumed only after that guard agrees to send, and is
// valid for its exact card, manager, destination, text and client_msg_id.
func (h *Hub) sendOnce(ctx context.Context, m *Machine, method string, p map[string]any, manager string) error {
	id, _ := p["client_msg_id"].(string)
	sid, _ := p["session_id"].(string)
	token, _ := p[assistantwire.ClarificationProofParam].(string)
	interactionID, _ := p[assistantwire.ClarificationInteractionParam].(string)
	delete(p, assistantwire.ClarificationProofParam)
	delete(p, assistantwire.ClarificationInteractionParam)
	if h.store != nil {
		state, err := h.store.ClaimSend(ctx, id, m.cfg.ID, sid)
		switch {
		case err != nil:
			return err
		case state == sendDelivered:
			return nil
		case state == sendSending || state == sendUnknown:
			return i18n.Err("hub-assistant_channels.send_in_doubt")
		}
	}
	if token != "" {
		proof, err := h.takeClarificationProof(ctx, manager, token)
		if err == nil {
			text, _ := p["text"].(string)
			if !clarificationProofMatches(proof, interactionID, m.cfg.ID, sid, id, text) {
				err = errors.New("clarification proof does not match this message")
			}
		}
		if err != nil {
			if h.store != nil {
				// Validation happened before any node call: this is a definite failed
				// claim, never an uncertain delivery.
				if serr := h.store.SettleSend(context.WithoutCancel(ctx), id, ""); serr != nil {
					h.logf("channels: settle unverified clarification: %v", serr)
				}
			}
			return err
		}
		ctx = withVerifiedClarification(ctx, proof.Text)
	}
	if method == assistantwire.DeviceAssistantAnswerAgain {
		p["once_id"] = id
		delete(p, "client_msg_id") // the persisted follow-up is Persona's message
	}
	err := m.call(ctx, method, p, nil)
	if h.store != nil {
		if serr := h.store.SettleSend(context.WithoutCancel(ctx), id, sendOutcome(err)); serr != nil {
			h.logf("channels: settle send: %v", serr)
		}
	}
	return err
}
