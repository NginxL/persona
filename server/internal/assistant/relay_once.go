package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A relayed message (drainRelays) goes in once. Its send waits a bounded time; a node slow to answer (one building a
// long session list) may have written the line already when the time runs out. Taken as failed, the message stayed
// queued and went in again on the next drain, and again after that while the node stayed slow: the conversation read
// it a dozen times. Two guards:
//   - every try of a message carries one once_id (relayMsgID), and the node sends an id once: a try while the first
//     still runs waits for it, one after it is answered by it (node: sendsafe.go sentLog). It is not the message's
//     client_msg_id: a line tied to one reads as the owner's own message (its source, briefs, user inputs);
//   - a try with no answer in time looks for its line in the conversation's latest turns (relayArrived) before the
//     message stays queued: the node may have been restarted in between, and its memory of ids with it. An older
//     node ignores once_id; this guard still holds there.

// relayCheckWait bounds the look at a conversation after a send whose outcome is in doubt.
var relayCheckWait = 15 * time.Second

// relayMsgID is the once_id every try of message msg to conversation to goes under: made from the message, so
// the same after the manager restarts and for messages queued before ids existed, and different for any other one.
func relayMsgID(to string, msg relayMsg) string {
	h := sha256.New()
	for _, part := range []string{to, msg.From, msg.Tool, msg.State, msg.At.UTC().Format(time.RFC3339Nano), msg.Text} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "relay-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// relayInDoubt: the send may have gone in though no answer said so — the time ran out, or the link dropped while it
// went. A refusal (the conversation is gone, its launcher cannot start) is no doubt: nothing went in.
func relayInDoubt(err error) bool {
	return replyInDoubt(err) || errors.Is(err, rpc.ErrClosed)
}

// relayClockSlack is how much earlier than a try a line may say it was written and still be the try's: the node's
// clock against the manager's.
const relayClockSlack = 30 * time.Second

// relayContextRe is any context block a line can carry before or around a relay's words: the live snapshot, the
// message source the manager puts in front (prompts.MessageSourceText), the note on a message that came mid-turn.
var relayContextRe = lazyre.New(`(?s)<` + compat.ContextPattern() + `(?:\s[^>]*)?>.*?</` + compat.ContextPattern() + `>\s*`)

// relayWords is a line's or a relay's own words: its context blocks left out.
func relayWords(s string) string {
	return strings.TrimSpace(relayContextRe.ReplaceAllString(s, ""))
}

// relayArrived says whether message msg is in conversation to's latest turns: a line of the user's with its words,
// written no earlier than the try that began at tried (an older one with the same words is another message), and for
// a message from another conversation, from that conversation. A conversation that cannot be read says no: the
// message stays queued, as before.
func (s *Service) relayArrived(machine, to string, msg relayMsg, tried time.Time) bool {
	want := relayWords(relayDeliveryText(msg))
	if want == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayCheckWait)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": to, "turns": 3, "lazy_images": true}, &res); err != nil {
		return false
	}
	fromConversation := msg.Tool == "conversation_send" || msg.Tool == "conversation_new"
	for _, it := range res.Items {
		if it.Role != "user" || it.Kind != proto.KindText {
			continue
		}
		if !it.TS.IsZero() && it.TS.Before(tried.Add(-relayClockSlack)) {
			continue
		}
		if relayWords(it.Text) != want {
			continue
		}
		if fromConversation {
			if src := prompts.ParseRelaySource(it.Text); src == nil || src.ConversationID != msg.From {
				continue // the same words from another conversation, or the owner's own
			}
		}
		return true
	}
	return false
}

// relaySettled decides a send of msg that began at tried and ended with err. In doubt and found in the conversation,
// it went in: noted as delivered, and true. Otherwise false: still in doubt, the try is kept so the next one looks
// before sending (relayLanded), and a supervision message lets the conversation's pushes go (deliverRelay held them
// while in doubt).
func (s *Service) relaySettled(machine, to string, msg relayMsg, tried time.Time, err error) bool {
	if !relayInDoubt(err) {
		return false
	}
	if s.relayArrived(machine, to, msg, tried) {
		s.logf("relay to %s: %v; it is in the conversation, taken as delivered", short(to), err)
		s.relayDelivered(context.Background(), to, msg)
		s.forgetDoubt(to, msg)
		return true
	}
	b := &s.relays
	b.mu.Lock()
	if b.doubted == nil {
		b.doubted = map[string]time.Time{}
	}
	if _, ok := b.doubted[relayMsgID(to, msg)]; !ok {
		b.doubted[relayMsgID(to, msg)] = tried
	}
	b.mu.Unlock()
	if msg.Tool == superviseTool {
		s.releasePushes(context.Background(), to) // it did not go in, as far as can be seen: no turn of its to hold
	}
	return false
}

// relayLanded says whether an earlier try of msg that ended in doubt has gone in since: its line may come after the
// look (relaySettled), and a node restarted in between no longer knows the once_id. Found, it is noted as delivered.
func (s *Service) relayLanded(machine, to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	tried, ok := b.doubted[relayMsgID(to, msg)]
	b.mu.Unlock()
	if !ok || !s.relayArrived(machine, to, msg, tried) {
		return false
	}
	s.logf("relay to %s: an earlier try in doubt is in the conversation, taken as delivered", short(to))
	s.relayDelivered(context.Background(), to, msg)
	s.forgetDoubt(to, msg)
	return true
}

func (s *Service) forgetDoubt(to string, msg relayMsg) {
	b := &s.relays
	b.mu.Lock()
	delete(b.doubted, relayMsgID(to, msg))
	b.mu.Unlock()
}
