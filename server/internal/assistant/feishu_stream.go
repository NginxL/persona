package assistant

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
)

// The progress card as a CardKit entity (feishubot/cardkit.go): made and sent by its id on the run's first tick, so what
// the conversation is writing is streamed into it and comes in letter by letter. What else the card shows (the steps,
// the counts, the Stop button) is written into the entity whole when it changes: CardKit takes that as often as it
// comes while the card streams, where a message's update is held back after five a second (cardBackoffFirst).
// CardKit turns streaming off ten minutes after it was turned on; it is turned on again for the next write. Anything
// else that fails sends the run back to updating its card as a message, which a card sent as an entity still takes
// (the answer always goes that way): nothing waits on CardKit. The entity's id is in the run's Ext ("card_id"), so an
// assistant started again ends its streaming too. CardKit goes on taking writes into an entity whose message was
// withdrawn or deleted, so the message itself is read back every kitCheckEvery (kitGone): one gone is forgotten as a
// message update that finds it gone forgets it, and the next tick posts a new card.

const (
	// progressLiveID is the element of the progress card the conversation's words are streamed into.
	progressLiveID = "live"
	// progressLiveMost is how much of the words the card shows (their end): text that has outgrown it is written over
	// rather than streamed on, as it no longer starts the same.
	progressLiveMost = 2000
	// kitDownFor is how long progress cards go as messages after CardKit would not make one (the app lacks its
	// permission, an older hub): it is not asked on every run.
	kitDownFor = 10 * time.Minute
	// kitCheckEvery is how often an entity's message is read back to see it is still there (kitGone).
	kitCheckEvery = 30 * time.Second
	// codeStreamClosed is CardKit's answer to a write into a card that is not streaming (any more).
	codeStreamClosed = 300309
)

// kitEpoch is where the entities' sequences count from.
var kitEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// cardKit is a run's progress card as an entity, under its chanLive's mu.
type cardKit struct {
	id       string    // the entity
	seq      int64     // its last write's sequence
	streamed string    // the words it has
	shape    [32]byte  // the rest of the card as last written (kitShape)
	off      bool      // CardKit failed for this run: the card is updated as a message from now on
	checked  time.Time // its message was last read back (kitGone); zero: never, as after a restart
}

// next is the sequence of the entity's next write: tenths of a second since kitEpoch, so it goes on growing for an
// assistant started again, and always past the last.
func (k *cardKit) next(now time.Time) int64 {
	n := int64(now.Sub(kitEpoch) / (100 * time.Millisecond))
	if n <= k.seq {
		n = k.seq + 1
	}
	k.seq = n
	return n
}

// progressLive is the conversation's words as the progress card shows them: escaped, and only their end when long.
func progressLive(live string) string {
	live = strings.TrimSpace(live)
	if r := []rune(live); len(r) > progressLiveMost {
		live = "…" + string(r[len(r)-progressLiveMost:])
	}
	return feishubot.EscapeMarkdown(live)
}

// liveElement is the card's element the words go in, nil when it has none.
func liveElement(c *feishubot.Card) map[string]any {
	for _, el := range c.Body.Elements {
		if m, ok := el.(map[string]any); ok && m["element_id"] == progressLiveID {
			return m
		}
	}
	return nil
}

// kitShape is what the card shows besides its words: when it changes, the card is written whole.
func kitShape(c *feishubot.Card) [32]byte {
	el := liveElement(c)
	if el == nil {
		return sha256.Sum256([]byte(c.JSON()))
	}
	words := el["content"]
	el["content"] = ""
	sum := sha256.Sum256([]byte(c.JSON()))
	el["content"] = words
	return sum
}

// kitOf is the run's entity: the one prog keeps, or the one its Ext names (an assistant started again; what it streamed
// is not known, so the next write is the whole card). nil when the card is not an entity, or CardKit failed for it.
func (s *Service) kitOf(r *channel.Run, prog *chanLive) *cardKit {
	if prog.kit == nil {
		if id := s.runExt(r, "card_id"); id != "" {
			prog.kit = &cardKit{id: id}
		}
	}
	if prog.kit == nil || prog.kit.off {
		return nil
	}
	return prog.kit
}

// kitPost makes the run's first progress card an entity streaming its words and sends it by its id. ok is false when
// CardKit did not make one: the caller sends the card as a message.
func (f feishuSink) kitPost(ctx context.Context, r *channel.Run, prog *chanLive, c *feishubot.Card, to feishuTarget) (mid string, err error, ok bool) {
	s := f.s
	now := s.chansNow()
	s.chans.mu.Lock()
	down := now.Before(s.chans.kitDown)
	s.chans.mu.Unlock()
	if down {
		return "", nil, false
	}
	c = s.workbenchStamp(ctx, c.Clone()).Streaming()
	var made struct {
		CardID string `json:"card_id"`
	}
	if err := s.appCall(ctx, AppFeishu, FeishuCardCreate, map[string]any{"card": c.JSON()}, &made); err != nil || made.CardID == "" {
		s.logf("bots: progress card as a card entity: %v; sending it as a message for %s", err, kitDownFor)
		s.chans.mu.Lock()
		s.chans.kitDown = now.Add(kitDownFor)
		s.chans.mu.Unlock()
		return "", nil, false
	}
	mid, err = s.botPostContent(ctx, to, r.ReplyTo, feishubot.CardEntityContent(made.CardID), "", "")
	if err != nil {
		return "", err, true
	}
	words := ""
	if el := liveElement(c); el != nil {
		words, _ = el["content"].(string)
	}
	prog.kit = &cardKit{id: made.CardID, streamed: words, shape: kitShape(c), checked: now}
	s.setRunExt(r, "card_id", made.CardID)
	return mid, nil, true
}

// kitProgress writes the progress card into its entity: whole when more than its words changed (with the words it
// has), then its words streamed. false when the card is not an entity, or CardKit failed now: the caller updates it
// as a message.
func (f feishuSink) kitProgress(ctx context.Context, r *channel.Run, prog *chanLive, c *feishubot.Card) bool {
	s := f.s
	k := s.kitOf(r, prog)
	if k == nil {
		return false
	}
	c = s.workbenchStamp(ctx, c.Clone()).Streaming()
	el := liveElement(c)
	if el == nil {
		return false
	}
	words, _ := el["content"].(string)
	if shape := kitShape(c); shape != k.shape {
		el["content"] = k.streamed // the rest is streamed below
		err := f.kitWrite(ctx, k, FeishuCardUpdate, func(seq int64) map[string]any {
			return map[string]any{"card_id": k.id, "card": c.JSON(), "sequence": seq}
		})
		el["content"] = words
		if err != nil {
			return f.kitFailed(k, err)
		}
		k.shape = shape
	}
	if words != k.streamed {
		err := f.kitWrite(ctx, k, FeishuCardStream, func(seq int64) map[string]any {
			return map[string]any{"card_id": k.id, "element": progressLiveID, "text": words, "sequence": seq}
		})
		if err != nil {
			return f.kitFailed(k, err)
		}
		k.streamed = words
	}
	return true
}

// kitGone reports whether the message of the run's entity was withdrawn or deleted, reading it back at most every
// kitCheckEvery. A read that fails for another reason (a rate limit, the hub away) says nothing: the card goes on.
func (f feishuSink) kitGone(ctx context.Context, k *cardKit, messageID string) bool {
	s := f.s
	now := s.chansNow()
	if now.Sub(k.checked) < kitCheckEvery {
		return false
	}
	k.checked = now
	items, err := s.botMessages(ctx, messageID)
	if err != nil {
		return feishubot.MessageGone(err)
	}
	for _, it := range items {
		if it.MessageID == messageID {
			return it.Deleted
		}
	}
	return false
}

// forgetProgressCard drops a run's progress card that is gone from the chat (withdrawn or deleted): the next tick
// posts a new one, and the answer does not go into this one. Its entity goes with it, so no later write streams into
// a card nobody sees, nor does an assistant started again take the entity up from the run's Ext.
func (s *Service) forgetProgressCard(r *channel.Run, prog *chanLive) {
	s.setRunExt(r, "card", "")
	s.setRunExt(r, "card_id", "")
	prog.kit = nil
	prog.sentCard = [32]byte{}
}

// kitWrite makes one write into the entity; when the card stopped streaming it is turned on again and the write
// tried once more.
func (f feishuSink) kitWrite(ctx context.Context, k *cardKit, method string, params func(seq int64) map[string]any) error {
	s := f.s
	err := s.appCall(ctx, AppFeishu, method, params(k.next(s.chansNow())), nil)
	if feishuCode(err) != codeStreamClosed {
		return err
	}
	if err := s.appCall(ctx, AppFeishu, FeishuCardSettings, map[string]any{"card_id": k.id, "streaming": true, "sequence": k.next(s.chansNow())}, nil); err != nil {
		return err
	}
	return s.appCall(ctx, AppFeishu, method, params(k.next(s.chansNow())), nil)
}

// kitFailed leaves CardKit for the run's card: it is updated as a message from now on.
func (f feishuSink) kitFailed(k *cardKit, err error) bool {
	k.off = true
	f.s.logf("bots: progress card entity %s: %v; updating it as a message", k.id, err)
	return false
}

// kitEnd turns the streaming of a run's entity off before its card is written for the last time (the answer, a fold,
// a recall): written as a message, it then shows as any other card. k is the run's entity as its chanLive keeps it
// (nil when it keeps none: the run was folded, or the assistant started again), ext the run's values. Best effort:
// CardKit ends streaming by itself.
func (s *Service) kitEnd(ctx context.Context, k *cardKit, ext map[string]string) {
	switch {
	case k != nil && k.off:
		return
	case k == nil && ext["card_id"] == "":
		return
	case k == nil:
		k = &cardKit{id: ext["card_id"]}
	}
	if err := s.appCall(ctx, AppFeishu, FeishuCardSettings, map[string]any{"card_id": k.id, "streaming": false, "sequence": k.next(s.chansNow())}, nil); err != nil {
		s.logf("bots: end the progress card's streaming: %v", err)
	}
}
