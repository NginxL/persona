package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Hand-overs of the owner's words to and from a direction's lead (#11699, docs/assistant/unified-entry.md "Across a
// relay"). A conversation_send that carries a verified quote (relay_quote.go) and has a direction's lead conversation
// (isLeader, not a task leader Agent) on either end is one hand-over, recorded once the send is accepted: sent at once
// or queued. A send refused at once (its model has the error), one without a quote and one between two conversations
// neither of which is a lead have none. Its state follows what the relay does with the message, written where the relay
// decides it and only once it is true, with one log line per change:
//   - queued: the message waits in the receiver's queue (queueRelay);
//   - delivered: it went in (relayDelivered: sent at once, from the queue, or a try in doubt found in the transcript);
//   - answered: the receiver's first conversation_send back to the sender since then went into the sender
//     (relayMsg.Answers, handoverToAnswer); one sent while the hand-over's own try was in doubt answers it once that try
//     is found to have gone in (AnswerAt);
//   - not_delivered: it will not be tried again (Code: the refusal's code, or handoverGone).
//
// The entries are a lazy settings record shaped like the verified quotes, kept as long and as many. Besides the log,
// only the start rule of a lead a hand-over was sent into reads them (relay_lead_hold.go: one past its 14 days marks no
// lead).
const (
	handoversKey   = "assistant.handovers" // settings key of the hand-overs
	handoverMemory = quoteMemory           // an entry is kept this long …
	handoversKept  = quotesKept            // … and at most this many, the oldest dropped first
)

// The states of a hand-over.
const (
	handoverQueued       = "queued"
	handoverDelivered    = "delivered"
	handoverAnswered     = "answered"
	handoverNotDelivered = "not_delivered"
	// handoverGone is not_delivered's code when the receiver is gone.
	handoverGone = "gone"
)

// handoverMoves are the states a hand-over comes to each state from ("": a send not accepted yet). Nothing else moves
// it, so a state is never undone, and an answer needs a hand-over that went in.
var handoverMoves = map[string][]string{
	handoverQueued:       {""},
	handoverDelivered:    {"", handoverQueued},
	handoverAnswered:     {handoverDelivered},
	handoverNotDelivered: {handoverQueued},
}

// handover is one hand-over of the owner's words.
type handover struct {
	ID      string `json:"id"`
	From    string `json:"from"`     // the sending conversation
	To      string `json:"to"`       // the receiving conversation …
	ToTitle string `json:"to_title"` // … by its name when it was sent
	// Conversation and MessageID: where the owner wrote the words (the quote's; for a part of a message, that message).
	Conversation string    `json:"conversation"`
	MessageID    string    `json:"message_id"`
	State        string    `json:"state"`
	Code         string    `json:"code,omitempty"` // not_delivered: why
	At           time.Time `json:"at"`             // when it was made
	ChangedAt    time.Time `json:"changed_at"`     // when it last changed
	// AnswerAt: when an answer to it went into the sender while its own message was in doubt (queued after a try that
	// may have gone in); it is answered once that try is found to have gone in, and it is cleared when the message is
	// sent again instead.
	AnswerAt time.Time `json:"answer_at,omitzero"`
	// ResentAt: when its message was last sent again after the looks for an earlier try missed, or moved to another
	// receiver: an answer named before then (gone in, queued or on its way) does not count.
	ResentAt time.Time `json:"resent_at,omitzero"`
}

// handoverBook holds the hand-overs. Its lock is taken before the relay lock (handoverToAnswer, backgroundLost), never
// after it.
type handoverBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]handover // by id
	// pending: by id, the hand-over of a send not accepted yet, in memory. It becomes an entry when its message is
	// queued or goes in (handoverSet), and is forgotten when the send is refused at once.
	pending map[string]handover
	// replying: by hand-over, when a reply being sent now named it (handoverToAnswer), in memory.
	replying map[string]time.Time
}

func (b *handoverBook) loadLocked(ctx context.Context, s *Service) bool {
	if b.loaded {
		return true
	}
	m := map[string]handover{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, handoversKey, &m); err != nil {
			s.logf("load hand-overs: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]handover{}
	}
	b.m, b.loaded = m, true
	return true
}

// saveLocked forgets the entries older than handoverMemory and, past handoversKept, the oldest, then saves the rest.
func (b *handoverBook) saveLocked(s *Service, now time.Time) {
	ids := make([]string, 0, len(b.m))
	for id, e := range b.m {
		if now.Sub(e.At) > handoverMemory {
			delete(b.m, id)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > handoversKept {
		slices.SortFunc(ids, func(x, y string) int { return b.m[x].At.Compare(b.m[y].At) })
		for _, id := range ids[:len(ids)-handoversKept] {
			delete(b.m, id)
		}
	}
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, handoversKey, b.m); err != nil {
		s.logf("save hand-overs: %v", err)
	}
}

// handoverFor is the hand-over a conversation_send from → to (toTitle) is when it carries the owner's verified words
// (quote): its id goes on the message (relayMsg.Handover). "" when it is none: no quote, or neither end is a
// direction's lead. It becomes an entry only once the send is accepted; done forgets it if it was not.
func (s *Service) handoverFor(ctx context.Context, from, to, toTitle string, quote *relayRequest) (string, func()) {
	if quote == nil || !s.isLeader(ctx, from) && !s.isLeader(ctx, to) {
		return "", func() {}
	}
	now := time.Now().UTC()
	e := handover{ID: "handover_" + randomHex(8), From: from, To: to, ToTitle: toTitle, Conversation: quote.Conversation,
		MessageID: quote.MessageID, At: now, ChangedAt: now}
	b := &s.handovers
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]handover{}
	}
	b.pending[e.ID] = e
	b.mu.Unlock()
	return e.ID, func() {
		b.mu.Lock()
		delete(b.pending, e.ID)
		b.mu.Unlock()
	}
}

// handoverChange runs change on hand-over id under the book's lock and saves the book when change says it changed
// something. A pending hand-over is offered with state ""; one past handoverMemory is not offered: it is as good as
// forgotten. False when nothing changed.
func (s *Service) handoverChange(id string, change func(e *handover, now time.Time) bool) bool {
	if id == "" {
		return false
	}
	b := &s.handovers
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(context.Background(), s) {
		return false
	}
	now := time.Now().UTC()
	if !b.changeLocked(s, id, now, change) {
		return false
	}
	b.saveLocked(s, now)
	return true
}

// changeLocked is handoverChange's step, without the save; b.mu is held and the book loaded.
func (b *handoverBook) changeLocked(s *Service, id string, now time.Time, change func(e *handover, now time.Time) bool) bool {
	e, ok := b.m[id]
	if !ok {
		if e, ok = b.pending[id]; !ok {
			return false // never accepted, or forgotten since
		}
	}
	if now.Sub(e.At) > handoverMemory || !change(&e, now) {
		return false
	}
	b.m[id] = e
	delete(b.pending, id)
	return true
}

// handoverMove moves e to state (code: why it was not delivered) when it may come there from where it is
// (handoverMoves), and logs the change. False when it may not.
func (s *Service) handoverMove(e *handover, state, code string, now time.Time) bool {
	if !slices.Contains(handoverMoves[state], e.State) {
		return false
	}
	e.State, e.Code, e.ChangedAt = state, code, now
	what := state
	if code != "" {
		what += " (" + code + ")"
	}
	s.logf("hand-over %s from %s to %s: %s", e.ID, short(e.From), short(e.To), what)
	return true
}

// handoverSet moves hand-over id to state (code: why it was not delivered) when it may come there from where it is
// (handoverMoves) and check, if any, holds of it, and saves it. A pending one becomes an entry here. False when
// nothing changed.
func (s *Service) handoverSet(id, state, code string, check func(handover) bool) bool {
	return s.handoverChange(id, func(e *handover, now time.Time) bool {
		return (check == nil || check(*e)) && s.handoverMove(e, state, code, now)
	})
}

// handoverWentIn: message msg went into conversation to (relayDelivered). The hand-over it is has been delivered, and
// answered too when its answer went in while it was in doubt (AnswerAt); the one it answers has been answered, or, while
// that one is still in doubt itself, is answered once it is found delivered.
func (s *Service) handoverWentIn(to string, msg relayMsg) {
	s.handoverChange(msg.Handover, func(e *handover, now time.Time) bool {
		if e.From != msg.From || e.To != to || !s.handoverMove(e, handoverDelivered, "", now) {
			return false
		}
		if !e.AnswerAt.IsZero() {
			s.handoverMove(e, handoverAnswered, "", now)
		}
		return true
	})
	s.handoverChange(msg.Answers, func(e *handover, now time.Time) bool {
		if e.From != to || e.To != msg.From {
			return false
		}
		if !claimHolds(*e, msg.At) {
			s.logf("hand-over %s from %s to %s: an answer named before it was sent again does not count", e.ID, short(e.From), short(e.To))
			return false
		}
		if e.State == handoverQueued && e.AnswerAt.IsZero() {
			e.AnswerAt = now
			s.logf("hand-over %s from %s to %s: an answer went in while it is in doubt", e.ID, short(e.From), short(e.To))
			return true
		}
		return s.handoverMove(e, handoverAnswered, "", now)
	})
}

// handoverRetried: hand-over id's message is sent again, its earlier try taken as not gone in after the looks for it
// (tryQueuedRelay), so an answer named before then answered something else: one that went in meanwhile (AnswerAt) and
// one still queued or on its way (ResentAt).
func (s *Service) handoverRetried(id string) {
	s.handoverChange(id, func(e *handover, now time.Time) bool {
		if e.State != handoverQueued {
			return false
		}
		e.AnswerAt, e.ResentAt = time.Time{}, now
		s.logf("hand-over %s from %s to %s: sent again; an answer named before does not count", e.ID, short(e.From), short(e.To))
		return true
	})
}

// handoverEnded: hand-over id will not be tried again (code: why).
func (s *Service) handoverEnded(id, code string) {
	s.handoverSet(id, handoverNotDelivered, code, nil)
}

// handoversMovedLocked: the hand-overs ids, queued for a background supervisor that is gone, now wait for conversation
// to (title) instead; with to "" they are dropped and will not be tried again (backgroundLost). One whose queueRelay
// has not written queued yet is queued here first. An answer named for one that moves was the old receiver's: it does
// not count (AnswerAt, ResentAt). The caller holds s.handovers.mu, taken before the relay lock, so the hand-overs change
// before the queue that moves them is saved.
func (s *Service) handoversMovedLocked(ids []string, to, title string) {
	b := &s.handovers
	if len(ids) == 0 || !b.loadLocked(context.Background(), s) {
		return
	}
	now := time.Now().UTC()
	changed := false
	for _, id := range ids {
		changed = b.changeLocked(s, id, now, func(e *handover, now time.Time) bool {
			queued := false
			if e.State == "" { // its message is in the queue, its queued not written yet (queueRelay)
				queued = s.handoverMove(e, handoverQueued, "", now)
			}
			if to == "" {
				return s.handoverMove(e, handoverNotDelivered, handoverGone, now) || queued
			}
			if e.State != handoverQueued {
				return queued
			}
			s.logf("hand-over %s from %s: its message moved from %s to %s", e.ID, short(e.From), short(e.To), short(to))
			e.To, e.ToTitle, e.ChangedAt = to, title, now
			e.AnswerAt, e.ResentAt = time.Time{}, now
			return true
		}) || changed
	}
	if changed {
		b.saveLocked(s, now)
	}
}

// handoverToAnswer is the hand-over a conversation_send from → to answers (relayMsg.Answers): of those from to to from,
// kept and that no other reply on its way answers (queued for to, or being sent now), the oldest that was delivered,
// else the oldest whose message is still in doubt (queued after a try that may have gone in). "" for none. done lets go of it once the
// reply is queued, went in or was refused: a reply that does not go in leaves it for the next one to answer.
func (s *Service) handoverToAnswer(from, to string) (string, func()) {
	b := &s.handovers
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(context.Background(), s) {
		return "", func() {}
	}
	now := time.Now().UTC()
	var open []handover
	for _, e := range b.m {
		if e.From == to && e.To == from && !claimHolds(e, b.replying[e.ID]) && now.Sub(e.At) <= handoverMemory &&
			(e.State == handoverDelivered || e.State == handoverQueued && e.AnswerAt.IsZero()) {
			open = append(open, e)
		}
	}
	if len(open) == 0 {
		return "", func() {}
	}
	answered, inDoubt := s.relayHandoverMarks(to, from)
	slices.SortFunc(open, func(x, y handover) int {
		if xd, yd := x.State == handoverDelivered, y.State == handoverDelivered; xd != yd { // in doubt: after every delivered one
			if xd {
				return -1
			}
			return 1
		}
		return cmp.Or(x.At.Compare(y.At), strings.Compare(x.ID, y.ID))
	})
	for _, e := range open {
		claimed := slices.ContainsFunc(answered[e.ID], func(at time.Time) bool { return claimHolds(e, at) })
		if claimed || e.State == handoverQueued && !inDoubt[e.ID] {
			continue
		}
		if b.replying == nil {
			b.replying = map[string]time.Time{}
		}
		b.replying[e.ID] = now
		return e.ID, func() {
			b.mu.Lock()
			delete(b.replying, e.ID)
			b.mu.Unlock()
		}
	}
	return "", func() {}
}

// claimHolds: a reply made at time at that named hand-over e still answers it: one made before e was sent again or
// moved (ResentAt) does not, whether it waits in the asker's queue, is on its way or goes in (handoverWentIn), so it
// does not hold e from a later reply either. A zero at is no claim.
func claimHolds(e handover, at time.Time) bool {
	return !at.IsZero() && !at.Before(e.ResentAt)
}

// relayHandoverMarks are, of the relay queue, when the replies queued for conversation asker that name a hand-over were
// made, by hand-over, and the hand-overs whose message is queued for conversation replier after a try that may have
// gone in (in doubt).
func (s *Service) relayHandoverMarks(asker, replier string) (answered map[string][]time.Time, inDoubt map[string]bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	answered, inDoubt = map[string][]time.Time{}, map[string]bool{}
	if !s.relayLocked(context.Background()) {
		return answered, inDoubt
	}
	for _, m := range b.st.Queue[asker] {
		if m.Answers != "" {
			answered[m.Answers] = append(answered[m.Answers], m.At)
		}
	}
	for _, m := range b.st.Queue[replier] {
		if m.Handover != "" && !m.TriedAt.IsZero() {
			inDoubt[m.Handover] = true
		}
	}
	return answered, inDoubt
}

// queueLeads are the conversations that are a direction's lead (isLeader), for a drain to rank a hand-over in a lead's
// queue ahead of the rest (relayMsg.rankIn). They are read before the relay lock is taken, and only while a queue holds a
// hand-over, the one message whose rank they change: nil otherwise. One queued meanwhile ranks with the rest until the
// next drain.
func (s *Service) queueLeads(ctx context.Context) map[string]bool {
	b := &s.relays
	b.mu.Lock()
	held := false
	if s.relayLocked(ctx) {
		for _, q := range b.st.Queue {
			if slices.ContainsFunc(q, func(m relayMsg) bool { return m.Handover != "" }) {
				held = true
				break
			}
		}
	}
	b.mu.Unlock()
	if !held {
		return nil
	}
	out := map[string]bool{}
	for _, id := range s.settings(ctx).Leaders {
		if s.isLeader(ctx, id) {
			out[id] = true
		}
	}
	return out
}
