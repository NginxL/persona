package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// relayLaterSend is the quiet context reserved before the first node write under a once_id. An empty reservation
// matters too: a retry of a send that originally carried nothing must not consume newly arrived quiet messages.
type relayLaterSend struct {
	At    time.Time  `json:"at"`
	Text  string     `json:"text,omitempty"`
	Items []relayMsg `json:"items,omitempty"`
}

// conversation_send with wake: false (#11614). Every turn of a supervising conversation re-reads its whole context, so
// a message that only acknowledges or informs should not start one. Such a message is kept for its receiver
// (relayState.Later) and goes in, as a context block in front of it, with the next message the manager delivers there
// for any other reason: a supervision message, another conversation's message, a scheduled follow-up, a notice. It
// never starts a turn of its own, costs no hop and no place in the relay rate, and is never guessed from its words.
// A turn the owner starts from an app goes to the node without this manager: what is kept waits for the next one the
// manager delivers, and shows above the composer meanwhile (queuedRelays).

const (
	// relayLaterMax bounds what one conversation keeps; past it the oldest goes.
	relayLaterMax = 20
	// relayLaterKeep is how long a kept message waits for a delivery to go with; a conversation that got nothing in
	// that long (deleted, abandoned) lets it go.
	relayLaterKeep = 7 * 24 * time.Hour
)

// keepRelayLater keeps msg for conversation to, answering how many wait there now; false when the relay state could not
// be read (nothing was kept).
func (s *Service) keepRelayLater(to string, msg relayMsg) (int, bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0, false
	}
	if b.st.Later == nil {
		b.st.Later = map[string][]relayMsg{}
	}
	msg.QueuedAt = time.Now().UTC()
	q := append(b.st.Later[to], msg)
	if len(q) > relayLaterMax {
		q = slices.Clone(q[len(q)-relayLaterMax:])
	}
	b.st.Later[to] = q
	if err := s.saveRelayLocked(); err != nil {
		b.loaded = false // reload durable state before another send or retry
		return 0, false
	}
	return len(q), true
}

// relayLaterFor renders what is kept for conversation to ("" when nothing), and a done that drops exactly those once
// the message carrying them went in (taken). What came meanwhile stays for the next delivery; a send that failed or is
// in doubt keeps them all, so one is at worst read twice, never lost.
func (s *Service) relayLaterFor(ctx context.Context, to string) (string, func(taken bool)) {
	text, done, _ := s.relayLaterSendFor(ctx, to, "")
	return text, done
}

func (s *Service) relayLaterSendFor(ctx context.Context, to, once string) (string, func(bool), error) {
	loc := s.location(ctx)
	language := answerLanguage(s.hello(ctx).Locale)
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(ctx) {
		b.mu.Unlock()
		return "", func(bool) {}, errors.New("cannot read quiet-message delivery state")
	}
	s.pruneRelayLaterLocked(time.Now())
	identity, _ := json.Marshal([2]string{to, once})
	key := string(identity) // JSON strings are safe as Postgres jsonb keys, unlike a NUL separator.
	carry, reserved := b.st.LaterSends[key]
	if once == "" || !reserved {
		carry = relayLaterSend{At: time.Now().UTC(), Items: slices.Clone(b.st.Later[to])}
		items := make([]prompts.RelayLaterItem, 0, len(carry.Items))
		for _, m := range carry.Items {
			items = append(items, prompts.RelayLaterItem{FromID: m.From, FromTitle: m.FromTitle, At: m.At.In(loc).Format("01-02 15:04"), Text: m.Text})
		}
		if len(items) > 0 {
			carry.Text = prompts.RenderRelayLater(prompts.RelayLater{Language: language, Items: items})
		}
		if once != "" {
			if b.st.LaterSends == nil {
				b.st.LaterSends = map[string]relayLaterSend{}
			}
			b.st.LaterSends[key] = carry
			if err := s.saveRelayLocked(); err != nil {
				b.loaded = false
				b.mu.Unlock()
				return "", func(bool) {}, err // never send a payload whose carry reservation was not saved
			}
		}
	}
	b.mu.Unlock()
	return carry.Text, func(taken bool) {
		if !taken {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if !s.relayLocked(ctx) {
			return
		}
		q := b.st.Later[to]
		rest := slices.DeleteFunc(slices.Clone(q), func(m relayMsg) bool { return slices.Contains(carry.Items, m) })
		if len(rest) == len(q) {
			return
		}
		if len(rest) == 0 {
			delete(b.st.Later, to)
		} else {
			b.st.Later[to] = rest
		}
		if err := s.saveRelayLocked(); err != nil {
			b.loaded = false
		}
	}, nil
}

// withRelayLater puts what is kept for the receiver of an assistant.send in front of its text (params p), answering
// what to call with whether the send was taken. A meeting's batch and a command (a text starting with "/") carry
// nothing: neither is a turn that reads messages.
func (s *Service) withRelayLater(ctx context.Context, p map[string]any) (func(taken bool), error) {
	id, _ := p["session_id"].(string)
	text, ok := p["text"].(string)
	if id == "" || !ok || meetingSource(p) || strings.HasPrefix(strings.TrimSpace(text), "/") {
		return func(bool) {}, nil
	}
	once, _ := p["once_id"].(string)
	later, done, err := s.relayLaterSendFor(ctx, id, once)
	if err != nil || later == "" {
		return done, err
	}
	p["text"] = later + "\n" + text
	return done, nil
}

// pruneRelayLaterLocked lets go of kept messages older than relayLaterKeep; b.mu is held.
func (s *Service) pruneRelayLaterLocked(now time.Time) {
	for key, sent := range s.relays.st.LaterSends {
		if now.Sub(sent.At) > relayLaterKeep {
			delete(s.relays.st.LaterSends, key)
		}
	}
	for id, q := range s.relays.st.Later {
		q = slices.DeleteFunc(q, func(m relayMsg) bool { return now.Sub(m.QueuedAt) > relayLaterKeep })
		if len(q) == 0 {
			delete(s.relays.st.Later, id)
		} else {
			s.relays.st.Later[id] = q
		}
	}
}
