package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's words across a relay (#3822, docs/assistant/unified-entry.md "Briefs"). A brief records the owner's
// message verbatim only when they spoke in the conversation that hands the work out. When the main conversation passes
// their request on to a leader, the leader's brief would only say a conversation started it. conversation_send and
// conversation_new can therefore carry one of the owner's messages (quote): the hub reads it from the sending
// conversation's own transcript, checks the owner wrote it there (or that the sender received it as a verified quote
// itself, so it passes on), and keeps what it verified. The receiver gets the text in a block after the relay context
// (prompts.RelayQuote); when it hands out work right after, briefOrigin takes the request from what the hub kept, by
// the sender and the id in that block, never from the text a model wrote.
const (
	quoteLatest    = "latest"           // quote: the owner's most recent message the sender can quote
	quoteTurns     = 10                 // turns of the sender's transcript read for it
	relayQuotesKey = "assistant.quotes" // settings key of the verified quotes
	quoteMemory    = 14 * 24 * time.Hour
	quotesKept     = 64
)

// relayQuote is one quote the hub verified: what the receiver's brief takes, and when it was sent.
type relayQuote struct {
	Request BriefRequest `json:"request"`
	At      time.Time    `json:"at"`
}

type quoteBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]relayQuote // by quoteRef(sender, message id)
}

func quoteRef(from, id string) string { return from + "/" + id }

func (b *quoteBook) loadLocked(ctx context.Context, s *Service) bool {
	if b.loaded {
		return true
	}
	m := map[string]relayQuote{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, relayQuotesKey, &m); err != nil {
			s.logf("load relay quotes: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]relayQuote{}
	}
	b.m, b.loaded = m, true
	return true
}

// ownerRequest is the owner's message as a brief keeps it: the text verbatim, cut at briefRequestMax.
func ownerRequest(text string, at time.Time, conversation, id string) *BriefRequest {
	req := &BriefRequest{Text: text, At: at.UTC(), Conversation: conversation, MessageID: id}
	if utf8.RuneCountInString(text) > briefRequestMax {
		req.Text, req.Truncated = string([]rune(text)[:briefRequestMax]), true
	}
	return req
}

// verifiedQuote is the quote sender from sent under the owner's message id, as the hub verified it; nil when it
// keeps none.
func (s *Service) verifiedQuote(ctx context.Context, from, id string) *BriefRequest {
	if from == "" || id == "" {
		return nil
	}
	b := &s.quotes
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(ctx, s) {
		return nil
	}
	q, ok := b.m[quoteRef(from, id)]
	if !ok {
		return nil
	}
	req := q.Request
	req.Via = slices.Clone(req.Via)
	return &req
}

// keepQuote saves a quote conversation from is sending, before it goes: a receiver that plans at once finds it.
func (s *Service) keepQuote(ctx context.Context, from string, req BriefRequest) {
	b := &s.quotes
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(ctx, s) {
		return
	}
	now := time.Now().UTC()
	b.m[quoteRef(from, req.MessageID)] = relayQuote{Request: req, At: now}
	keys := make([]string, 0, len(b.m))
	for k, q := range b.m {
		if now.Sub(q.At) > quoteMemory {
			delete(b.m, k)
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) > quotesKept { // the oldest go first
		slices.SortFunc(keys, func(x, y string) int { return b.m[x].At.Compare(b.m[y].At) })
		for _, k := range keys[:len(keys)-quotesKept] {
			delete(b.m, k)
		}
	}
	st := s.store()
	if st == nil {
		return
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(sctx, relayQuotesKey, b.m); err != nil {
		s.logf("save relay quotes: %v", err)
	}
}

// receivedQuote is the verified quote a relay item in a conversation carries: the sender (its source) and the id in
// its quote block (prompts.ParseRelayQuoteID) name what the hub kept. nil for any other item.
func (s *Service) receivedQuote(ctx context.Context, it proto.Item, src *proto.MessageSource) *BriefRequest {
	if src == nil || src.Kind != proto.SourceConversation {
		return nil
	}
	return s.verifiedQuote(ctx, src.ConversationID, prompts.ParseRelayQuoteID(it.Text))
}

// resolveQuote is the owner's message conversation self quotes in what it sends: quote is a message id or "latest".
// It is either one the owner wrote in self (by their own client: ownerMessageText) or a verified quote self
// received; "latest" takes whichever of the two came last in its transcript. self is added to the ones it was passed
// on through. An error says why the send does not go, so the model can send without it.
func (s *Service) resolveQuote(ctx context.Context, self, quote string) (*BriefRequest, error) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return nil, nil
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.call(cctx, s.settings(ctx).MachineID, DeviceSessionItems,
		map[string]any{"session_id": self, "turns": quoteTurns, "lazy_images": true}, &res); err != nil {
		return nil, fmt.Errorf("not sent: this conversation could not be read to find the user's message to quote (%v). Send it again without quote", err)
	}
	for i := len(res.Items) - 1; i >= 0; i-- {
		it := res.Items[i]
		if it.Role != "user" || it.Kind != proto.KindText {
			continue
		}
		if text := ownerMessageText(it); text != "" {
			if quote == quoteLatest || it.ID == quote {
				req := ownerRequest(text, it.TS, self, it.ID)
				req.Via = []string{self}
				return req, nil
			}
			continue
		}
		if q := s.receivedQuote(ctx, it, prompts.ItemSource(it)); q != nil && (quote == quoteLatest || q.MessageID == quote) {
			if !slices.Contains(q.Via, self) {
				q.Via = append(q.Via, self)
			}
			return q, nil
		}
		if it.ID == quote {
			return nil, fmt.Errorf("not sent: message %s is not one the user wrote in this conversation, nor a quote of theirs it received. Quote only the user's own words, or send without quote", quote)
		}
	}
	if quote == quoteLatest {
		return nil, errors.New("not sent: this conversation has no message of the user's own (nor a quote of theirs it received) in its recent turns. Send without quote")
	}
	return nil, fmt.Errorf("not sent: no message %s in this conversation's recent turns. Use quote=\"latest\" for the user's latest message, or send without quote", quote)
}

// relayQuoteOf is the quote as the receiver reads it (assistant/relay.md).
func relayQuoteOf(req *BriefRequest) *prompts.RelayQuote {
	if req == nil {
		return nil
	}
	q := &prompts.RelayQuote{ID: req.MessageID, At: req.At.UTC().Format("2006-01-02 15:04 UTC"), Conversation: req.Conversation, Text: req.Text}
	if len(req.Via) > 1 { // the first passed it on from where the owner wrote it: already named
		q.Via = slices.Clone(req.Via[1:])
	}
	return q
}

// quoteNote is what a send's result says about the quote it carried.
func quoteNote(req *BriefRequest) string {
	if req == nil {
		return ""
	}
	return fmt.Sprintf(" The user's message %s went with it verbatim; work handed out on it records their words.", req.MessageID)
}
