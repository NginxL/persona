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

// Every try carries the same once_id. The node keeps it for ten minutes or until restart (sentLog).
// The queue also saves the start of a try before sending and checks the transcript when the outcome is in doubt.
// A line can appear late even after a successful read, so both a miss and a read error get another look.
const (
	relayCheckWait = 15 * time.Second
	relayLookGap   = 10 * time.Second
	relayLookLimit = 3
)

// relayMsgID is the once_id every try of message msg to conversation to goes under: made from the message, so
// the same after the manager restarts and for messages queued before ids existed, and different for any other one.
func relayMsgID(to string, msg relayMsg) string {
	if msg.OnceID != "" {
		return msg.OnceID
	}
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

// relayArrived checks the latest turns for the relay's text and source since the first try that may have gone in.
// An error means the transcript could not be read. A successful read without a match can still miss a late write.
func (s *Service) relayArrived(machine, to string, msg relayMsg) (bool, error) {
	text := strings.TrimSpace(relayDeliveryText(msg))
	want := relayWords(text)
	ctx, cancel := context.WithTimeout(context.Background(), relayCheckWait)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": to, "turns": 3, "lazy_images": true}, &res); err != nil {
		return false, err
	}
	fromConversation := msg.Tool == "conversation_send" || msg.Tool == "conversation_new"
	var onceID string
	if want == "" {
		onceID = relayMsgID(to, msg)
	}
	for _, it := range res.Items {
		if it.Role != "user" || it.Kind != proto.KindText {
			continue
		}
		if !it.TS.IsZero() && it.TS.Before(msg.TriedAt.Add(-relayClockSlack)) {
			continue
		}
		if fromConversation {
			if src := prompts.ItemSource(it); src == nil || src.Kind != proto.SourceConversation || src.ConversationID != msg.From {
				continue
			}
		}
		if want == "" {
			if id := prompts.RelayMessageID(it.Text); id != "" {
				if id == onceID && it.ClientMsgID == "" {
					return true, nil
				}
				continue
			}
			// A relay sent before ids were kept in the transcript must match its rendered context.
			if text == "" || !strings.HasSuffix(strings.TrimSpace(it.Text), text) {
				continue
			}
		}
		if relayWords(it.Text) != want {
			continue
		}
		return true, nil
	}
	return false, nil
}

// lookForRelay notes a match as delivered. Otherwise it advances the relay's look schedule.
func (s *Service) lookForRelay(machine, to string, msg *relayMsg) bool {
	landed, err := s.relayArrived(machine, to, *msg)
	if landed {
		s.logf("relay to %s: a try in doubt is in the conversation, taken as delivered", short(to))
		s.relayDelivered(context.Background(), to, *msg)
		return true
	}
	if err != nil {
		s.logf("relay to %s: transcript check failed: %v", short(to), err)
	}
	msg.Looks = min(msg.Looks+1, relayLookLimit)
	msg.LookAfter = time.Now().UTC().Add(relayLookGap)
	return false
}

// tryDirectRelay carries an immediate try's start into the queue if the send ends in doubt.
func (s *Service) tryDirectRelay(ctx context.Context, machine, to string, msg *relayMsg) error {
	tried := time.Now().UTC()
	err := s.deliverRelay(ctx, machine, to, *msg)
	if relayInDoubt(err) {
		msg.TriedAt = tried
		if msg.Tool == superviseTool {
			if s.lookForRelay(machine, to, msg) {
				return nil
			}
			s.releasePushes(context.Background(), to)
		}
	}
	return err
}

// tryQueuedRelay sends the queue head after reconciliation and updates head to its saved pre-send state.
// A positive wait keeps the returned message queued for another drain.
func (s *Service) tryQueuedRelay(ctx context.Context, machine, to string, head *relayMsg) (relayMsg, time.Duration, error) {
	previous := *head
	msg := previous
	if msg.TriedAt.IsZero() {
		msg.TriedAt = time.Now().UTC()
	} else {
		// A duplicate relay is less harmful than a lost one when there is no owner to ask. Retry under the same id.
		s.logf("relay to %s: no match after %d transcript checks; trying again", short(to), relayLookLimit)
	}
	msg.Looks, msg.LookAfter = 0, time.Time{}

	// The draining head is reserved for this try. A manager stopped during the send must find the mark on restart.
	b := &s.relays
	b.mu.Lock()
	q := b.st.Queue[to]
	q[0] = msg
	err := s.saveRelayLocked()
	if err != nil {
		q[0] = previous
	}
	b.mu.Unlock()
	if err != nil {
		return previous, relayRetryGap, err
	}
	*head = msg
	err = s.deliverRelay(ctx, machine, to, msg)
	if !relayInDoubt(err) {
		// A refusal does not add any doubt. Retain the earlier try's mark, if there was one.
		return previous, 0, err
	}
	if s.lookForRelay(machine, to, &msg) {
		return msg, 0, nil
	}
	if msg.Tool == superviseTool {
		s.releasePushes(context.Background(), to)
	}
	return msg, relayLookGap, nil
}
