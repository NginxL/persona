package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
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
	quoteLatest    = "latest"           // quote: the one owner request in the current turn
	quoteTurns     = 10                 // turns of the sender's transcript read for it
	relayQuotesKey = "assistant.quotes" // settings key of the verified quotes
	quoteMemory    = 14 * 24 * time.Hour
	quotesKept     = 64
)

// relayRequest carries the brief fields and the identifier the receiver can pass on. A part's reference
// covers its complete source text, even when the quote kept or queued for delivery is clipped.
type relayRequest struct {
	BriefRequest
	PartRef string `json:"part_ref,omitempty"`
}

func (q *relayRequest) reference() string {
	if q.Part {
		return q.PartRef
	}
	return q.MessageID
}

// relayQuote is one quote the hub verified: what the receiver's brief takes, and when it was sent.
type relayQuote struct {
	Request       BriefRequest `json:"request"`
	At            time.Time    `json:"at"`
	SourceChecked bool         `json:"source_checked,omitempty"` // the original owner input passed the meeting privacy check
}

type quoteBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]relayQuote // by sender and the id shown in the quote block
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
		// Older entries did not record whether their original input was shareable. Keep only checked
		// records across the upgrade, including when the first operation after restarting is a read.
		before := len(m)
		maps.DeleteFunc(m, func(_ string, q relayQuote) bool { return !q.SourceChecked })
		if len(m) != before {
			if err := st.PutSetting(ctx, relayQuotesKey, m); err != nil {
				s.logf("remove unverified relay quotes: %v", err)
				return false
			}
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
	req.limitText()
	return req
}

func (req *BriefRequest) limitText() {
	if utf8.RuneCountInString(req.Text) > briefRequestMax {
		req.Text, req.Truncated = string([]rune(req.Text)[:briefRequestMax]), true
	}
}

// verifiedQuote reads only what the manager kept for this sender and block reference.
func (s *Service) verifiedQuote(ctx context.Context, from, id string) *relayRequest {
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
	req := &relayRequest{BriefRequest: q.Request}
	req.Via = slices.Clone(req.Via)
	if req.Part {
		req.PartRef = id
	}
	return req
}

// keepQuote saves a quote conversation from is sending, before it goes: a receiver that plans at once finds it.
func (s *Service) keepQuote(ctx context.Context, from string, req relayRequest) {
	b := &s.quotes
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(ctx, s) {
		return
	}
	now := time.Now().UTC()
	b.m[quoteRef(from, req.reference())] = relayQuote{Request: req.BriefRequest, At: now, SourceChecked: true}
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
func (s *Service) receivedQuote(ctx context.Context, it proto.Item, src *proto.MessageSource) *relayRequest {
	if src == nil || src.Kind != proto.SourceConversation {
		return nil
	}
	return s.verifiedQuote(ctx, src.ConversationID, prompts.ParseRelayQuoteID(it.Text))
}

// resolveQuote verifies a turn-relative choice, a message id or the owner's exact words before relay admission.
func (s *Service) resolveQuote(ctx context.Context, self, quote string) (*relayRequest, error) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return nil, nil
	}
	machine := s.settings(ctx).MachineID
	if s.justMeNow(machine, self) {
		return nil, errPrivateQuote
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if quote == quoteLatest {
		// Codex pages give a steered message the same shape as an opening message. An explicit
		// selection works even when the start of the turn is outside the page.
		row, known := s.session(cctx, machine, self)
		if !known || row.Agent == "codex" {
			return nil, errors.New("not sent: this transcript does not identify the start of the current turn. Use the message id or the user's exact words in quote, or send without quote")
		}
	}
	if err := s.call(cctx, machine, DeviceSessionItems,
		map[string]any{"session_id": self, "turns": quoteTurns, "lazy_images": true}, &res); err != nil {
		return nil, fmt.Errorf("not sent: this conversation could not be read to find the user's message to quote (%v). Send it again without quote", err)
	}
	// The node read may have been in flight when the current turn became private.
	if s.justMeNow(machine, self) {
		return nil, errPrivateQuote
	}
	var req *relayRequest
	var err error
	if quote == quoteLatest {
		req, err = s.quoteInTurn(ctx, self, res.Items)
	} else {
		req, err = s.quoteByIDOrWords(ctx, self, res.Items, quote)
	}
	if err != nil {
		return nil, err
	}
	req.limitText()
	if !slices.Contains(req.Via, self) {
		req.Via = append(req.Via, self)
	}
	return req, nil
}

var errPrivateQuote = errors.New("not sent: the user asked privately during a meeting. Their words stay in this conversation. Do not copy them into text either")

func (s *Service) quoteCandidate(ctx context.Context, self string, it proto.Item) *relayRequest {
	if it.Role != "user" || it.Kind != proto.KindText || it.Task != nil || it.Wake {
		return nil
	}
	if words := ownerMessageText(it); words != "" {
		// Keep the full text available for selection. Only the selected words are clipped for the brief.
		return &relayRequest{BriefRequest: BriefRequest{Text: words, At: it.TS.UTC(), Conversation: self, MessageID: it.ID}}
	}
	return s.receivedQuote(ctx, it, prompts.ItemSource(it))
}

func (s *Service) quoteInTurn(ctx context.Context, self string, items []proto.Item) (*relayRequest, error) {
	// A wake resumes the preceding turn. If its actual start fell outside the page, queued inputs alone prove nothing.
	start := len(items)
	for i, it := range slices.Backward(items) {
		if it.Role == "user" && it.Kind == proto.KindText && !it.Queued && it.Task == nil && !it.Wake {
			start = i
			break
		}
	}
	if start == len(items) {
		return nil, errors.New("not sent: the start of this turn is outside the transcript page. Put the user's exact words in quote, or send without quote")
	}
	var choices []*relayRequest
	var private bool
	for _, it := range items[start:] {
		if strings.HasPrefix(it.ClientMsgID, justMeMsgPrefix) {
			private = true
			continue
		}
		if q := s.quoteCandidate(ctx, self, it); q != nil {
			choices = append(choices, q)
		}
	}
	switch len(choices) {
	case 0:
		if private {
			return nil, errPrivateQuote
		}
		return nil, errors.New("not sent: the user wrote nothing in this turn to quote. To pass on an earlier request, put the user's exact words in quote, or send without quote")
	case 1:
		return choices[0], nil
	default:
		var msg strings.Builder
		msg.WriteString("not sent: more than one user message or received quote in this turn. To select one, copy the id on the left into quote. The text on the right is a preview:")
		for _, q := range choices {
			fmt.Fprintf(&msg, "\n%s: %s", q.reference(), clip(oneLine(q.Text), 60))
		}
		return nil, errors.New(msg.String())
	}
}

func (s *Service) quoteByIDOrWords(ctx context.Context, self string, items []proto.Item, selector string) (*relayRequest, error) {
	// Resolve every kind of id first, even when a newer message contains the id as ordinary words.
	var candidates []*relayRequest
	var private []string
	var passageSource bool
	for _, it := range slices.Backward(items) {
		if strings.HasPrefix(it.ClientMsgID, justMeMsgPrefix) {
			if it.ID == selector {
				return nil, errPrivateQuote
			}
			private = append(private, ownerMessageText(it))
			continue
		}
		q := s.quoteCandidate(ctx, self, it)
		if it.ID == selector && q == nil {
			return nil, errors.New("not sent: that item is neither a message the user wrote nor a verified quote this conversation received. Use the user's exact words, or send without quote")
		}
		if q != nil {
			if q.reference() == selector || (!q.Part && it.ID == selector) {
				return q, nil
			}
			if q.Part && (q.MessageID == selector || it.ID == selector) {
				passageSource = true // keep looking for the whole message, even behind a newer passage
			}
			candidates = append(candidates, q)
		}
	}
	if passageSource {
		return nil, errors.New("not sent: this conversation received a passage. Select its own quote reference or the exact words of the passage, or send without quote")
	}
	words := oneLine(selector)
	if utf8.RuneCountInString(words) >= urgentQuoteMin {
		for _, q := range candidates {
			if excerpt := quoteExcerpt(q.Text, words); excerpt != "" {
				if excerpt != q.Text || q.Truncated {
					q.Part, q.Truncated = true, false
					q.PartRef = fmt.Sprintf("%s.part-%x", q.MessageID, sha256.Sum256([]byte(q.Conversation+"\x00"+excerpt)))
					q.Text = excerpt
				}
				return q, nil
			}
		}
		for _, text := range private {
			if quoteExcerpt(text, words) != "" {
				return nil, errPrivateQuote
			}
		}
	}
	return nil, errors.New("not sent: no matching user message or verified quote in the last ten turns. Use its id or at least four characters of the user's exact words, or send without quote")
}

// quoteExcerpt matches in whitespace-folded text and returns the corresponding original bytes, including any
// spaces, tabs and line breaks between the first and last words. The selector has already been folded.
func quoteExcerpt(text, words string) string {
	fields := strings.Fields(text)
	start := strings.Index(strings.Join(fields, " "), words)
	if start < 0 {
		return ""
	}
	end := start + len(words)
	var raw, folded, from int
	for _, field := range fields {
		raw += strings.Index(text[raw:], field)
		next := folded + len(field)
		if folded <= start && start < next {
			from = raw + start - folded
		}
		if end <= next {
			return text[from : raw+end-folded]
		}
		raw += len(field)
		folded = next + 1
	}
	return ""
}

// relayQuoteOf is the quote as the receiver reads it (assistant/relay.md).
func relayQuoteOf(req *relayRequest) *prompts.RelayQuote {
	if req == nil {
		return nil
	}
	q := &prompts.RelayQuote{ID: req.reference(), Part: req.Part, At: req.At.UTC().Format("2006-01-02 15:04 UTC"), Conversation: req.Conversation, Text: req.Text}
	if len(req.Via) > 1 { // the first passed it on from where the owner wrote it: already named
		q.Via = slices.Clone(req.Via[1:])
	}
	return q
}

// quoteNote is what a send's result says about the quote it carried.
func quoteNote(req *relayRequest) string {
	if req == nil {
		return ""
	}
	if req.Part {
		return fmt.Sprintf(" Part of the user's message %s went with it verbatim. Work handed out on it records those words.", req.MessageID)
	}
	return fmt.Sprintf(" The user's message %s went with it verbatim. Work handed out on it records their words.", req.MessageID)
}
