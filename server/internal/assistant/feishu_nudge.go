package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The nudge under a card nobody answered (#6084, docs/extensions/bots.md "Nudging a card nobody answered"): a card the Lark bot
// sent (a question, a plan, a permission) that has waited nudge_after without an answer gets one short reply under it,
// "Still waiting for you: …", so it reaches the user where they answer. Like the reply reminder (agents_reply.go) it is
// a sweep once a minute over what waits, rules only, costing no tokens; unlike it, it follows the card into Lark and
// does not need the card to have been seen.
//
// One nudge per card, ever: the card's record (botCardRef) keeps when it was nudged, so a restart does not nudge again.
// None for a card answered, dismissed or expired (its record is gone), or whose agent is gone. The hub's quiet hours
// (#5806) hold a nudge back: it goes out in the first sweep after them, if the card still waits.

// nudgeEvery is how often the cards are looked at, the reply reminder's.
const nudgeEvery = replyEvery

// nudgeOldest: a card still unnudged this long after it was due is let go (the user left it), as replyOldest.
const nudgeOldest = replyOldest

// A send that fails for a reason that may pass is tried again, at most nudgeMaxTries times in all and only within
// nudgeRetryWindow of the first: Feishu drops a repeated key only for about an hour, so a later try could land twice.
const (
	nudgeMaxTries    = 3
	nudgeRetryWindow = time.Hour
)

// A deadline nudge is independent of the ordinary nudge_after nudge. Half the
// remaining card lifetime usually gives the owner more time than the fixed
// margin; very short cards use the margin but never nudge in their first two
// minutes. A lifetime too short for that lower bound gets no deadline nudge.
const (
	deadlineNudgeMargin = 5 * time.Minute
	deadlineNudgeMinAge = 2 * time.Minute
)

func deadlineNudgeAt(created, expires time.Time) (time.Time, bool) {
	if created.IsZero() || expires.IsZero() {
		return time.Time{}, false
	}
	half := created.Add(expires.Sub(created) / 2)
	at := half
	if margin := expires.Add(-deadlineNudgeMargin); margin.Before(at) {
		at = margin
	}
	if youngest := created.Add(deadlineNudgeMinAge); youngest.After(at) {
		at = youngest
	}
	return at, at.Before(expires)
}

type nudgeKind int

const (
	ordinaryNudge nudgeKind = iota
	deadlineNudge
)

type dueNudge struct {
	id   string
	kind nudgeKind
}

func (c botCard) progress(kind nudgeKind) nudgeProgress {
	if kind == deadlineNudge {
		return c.DeadlineNudge
	}
	return nudgeProgress{NudgedAt: c.NudgedAt, SkippedAt: c.SkippedAt, Tries: c.Tries, TriedAt: c.TriedAt}
}

func (c *botCard) setProgress(kind nudgeKind, p nudgeProgress) {
	if kind == deadlineNudge {
		c.DeadlineNudge = p
		return
	}
	c.NudgedAt, c.SkippedAt, c.Tries, c.TriedAt = p.NudgedAt, p.SkippedAt, p.Tries, p.TriedAt
}

func deadlineNudgeDue(c botCard, now time.Time) bool {
	if c.DeadlineNudge.settled() {
		return false
	}
	at, ok := deadlineNudgeAt(c.At, c.Ix.ExpiresAt)
	// Do not exclude an expired stored deadline here. A paused prepared card
	// can resume with a later effective deadline; nudgeCard rechecks it live.
	return ok && !now.Before(at)
}

// nudgeAfterOf is the setting in the status, in minutes: the default when the hub did not say, 0 off.
func nudgeAfterOf(st FeishuStatus) time.Duration {
	n := assistantwire.NudgeAfterDefault
	if st.NudgeAfter != nil {
		n = *st.NudgeAfter
	}
	return time.Duration(max(n, 0)) * time.Minute
}

func (s *Service) nudgeLoop(ctx context.Context) {
	t := time.NewTicker(nudgeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			s.nudgeTick(cctx)
			cancel()
		}
	}
}

// nudgeTick nudges the cards that waited nudge_after, outside quiet hours. With no card kept it does nothing, not even
// asking the hub for the bot's status.
func (s *Service) nudgeTick(ctx context.Context) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	idle := !b.loaded || len(b.cards) == 0
	b.mu.Unlock()
	if idle {
		return
	}
	st, err := s.feishuStatusKept(ctx)
	if err != nil || !st.Ready || st.Owner == "" {
		return
	}
	after := nudgeAfterOf(st)
	b.mu.Lock()
	if !b.loaded {
		b.mu.Unlock()
		return
	}
	now := b.clock()
	if after <= 0 {
		if b.st.NudgeOffAt.IsZero() {
			b.st.NudgeOffAt = now
			s.saveBotStateLocked()
		}
		b.mu.Unlock()
		return
	}
	turnedOn := !b.st.NudgeOffAt.IsZero() // off before: what is already due is not a burst of nudges, it is let go
	if turnedOn {
		b.st.NudgeOffAt = time.Time{}
		s.saveBotStateLocked()
	}
	var due []dueNudge
	for id, c := range b.cards {
		if c.MessageID == "" {
			continue
		}
		if deadlineNudgeDue(c, now) {
			due = append(due, dueNudge{id: id, kind: deadlineNudge})
		}
		// A card left more than a day past its ordinary wait is let go (the user left it).
		if !c.nudgeSettled() && now.Sub(c.At) >= after && now.Sub(c.At) <= after+nudgeOldest {
			due = append(due, dueNudge{id: id, kind: ordinaryNudge})
		}
	}
	slices.SortFunc(due, func(x, y dueNudge) int {
		if order := b.cards[x.id].At.Compare(b.cards[y.id].At); order != 0 {
			return order
		}
		return int(x.kind - y.kind)
	})
	if turnedOn {
		for _, d := range due {
			c := b.cards[d.id]
			p := c.progress(d.kind)
			p.SkippedAt = now
			c.setProgress(d.kind, p)
			s.keepBotCardLocked(d.id, &c)
		}
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if len(due) == 0 {
		return
	}
	quiet, err := s.nudgeQuiet(ctx)
	if err != nil {
		s.logf("bots: nudge: quiet hours: %v", err)
		return
	}
	if quiet {
		return // still due after them: the first sweep outside them nudges
	}
	for _, d := range due {
		s.nudgeCard(ctx, d.id, st.Owner, d.kind)
	}
}

// nudgeQuiet says whether the user is in their quiet hours now (the hub's, notify.quiet).
func (s *Service) nudgeQuiet(ctx context.Context) (bool, error) {
	var res struct {
		Quiet bool `json:"quiet"`
	}
	if err := s.appCall(ctx, AppNotify, NotifyQuietGet, map[string]any{}, &res); err != nil {
		return false, err
	}
	return res.Quiet, nil
}

// nudgeCard nudges under the card of interaction id if it still waits and this
// kind of nudge has not settled. Every send re-reads the live card, so an answer,
// withdrawal, pause, replacement or extended deadline during the sweep wins.
func (s *Service) nudgeCard(ctx context.Context, id, owner string, kind nudgeKind) {
	ix, end := s.botCardLook(ctx, id)
	if end != cardWaiting || ix.Private || s.agentGone(ix) { // ended, private, or its agent went away
		return
	}
	expires := ix.ExpiresAt
	if kind == deadlineNudge {
		var waiting bool
		expires, waiting = s.approvalNudgeDeadline(ix)
		if !waiting {
			return
		}
	}
	b := &s.fbot
	b.mu.Lock()
	c, ok := b.cards[id] // read again: it may have been answered while the hub was asked
	if !ok || c.MessageID == "" || c.progress(kind).settled() {
		b.mu.Unlock()
		return
	}
	now := b.clock()
	if kind == deadlineNudge {
		at, ok := deadlineNudgeAt(c.At, expires)
		if !ok || now.Before(at) || !now.Before(expires) {
			b.mu.Unlock()
			return
		}
	}
	p := c.progress(kind)
	if !p.TriedAt.IsZero() && now.Sub(p.TriedAt) > nudgeRetryWindow {
		// Feishu drops a repeated key only for about an hour: a send tried again later could land twice.
		p.SkippedAt = now
		c.setProgress(kind, p)
		s.keepBotCardLocked(id, &c)
		b.mu.Unlock()
		return
	}
	if p.TriedAt.IsZero() {
		p.TriedAt = now
	}
	p.Tries++
	p.NudgedAt = now
	c.setProgress(kind, p)
	s.keepBotCardLocked(id, &c) // marked before it is sent: a restart in between loses a nudge, never doubles one
	b.mu.Unlock()
	err := s.botNudge(ctx, c, ix, owner, kind)
	if err == nil {
		s.boardChanged() // its line on the waiting-for-you board says it was nudged
		return
	}
	s.logf("bots: nudge under card %s: %v", id, err)
	// Refused (the card or its chat is gone): nothing to nudge under. Otherwise it may not have gone out, and the next
	// sweep tries again under the same key, so one that did go out lands once, up to nudgeMaxTries and nudgeRetryWindow.
	giveUp := chatRefused(err) || botcard.MessageGone(err) || p.Tries >= nudgeMaxTries
	b.mu.Lock()
	if cur, still := b.cards[id]; still && cur.MessageID == c.MessageID {
		p := cur.progress(kind)
		p.NudgedAt = time.Time{}
		if giveUp {
			p.SkippedAt = b.clock()
		}
		cur.setProgress(kind, p)
		s.keepBotCardLocked(id, &cur)
	}
	b.mu.Unlock()
}

// agentGone: the agent ix asks for is no longer in its computer's list (a list that is read, and does not have it).
func (s *Service) agentGone(ix proto.Interaction) bool {
	if ix.SessionID == "" {
		return false
	}
	// Only whether it is listed: no copy of the list, nor of the card (the closure takes the id alone).
	sid := ix.SessionID
	found, listed := s.nodeSessionsWhere(ix.MachineID, func(c *proto.Session) bool { return c.SessionID == sid })
	return listed > 0 && len(found) == 0
}

// botNudge replies under c with the nudge: in a topic group in the topic, @-mentioning the owner, which is what makes
// it reach them there; in the owner's private chat a plain reply, which notifies by itself.
func (s *Service) botNudge(ctx context.Context, c botCard, ix proto.Interaction, owner string, kind nudgeKind) error {
	text := s.nudgeText(ctx, ix)
	to := feishuTarget{NoDetour: true} // under the card or nowhere: a card that was withdrawn is not nudged
	if c.ChatID != "" {
		to.ChatID, to.InThread = c.ChatID, true
		text = feishuMention(owner) + " " + text
	}
	p := targetParams(to, map[string]any{"reply_to": c.MessageID, "text": text, "uuid": nudgeKey(ix.ID, kind)})
	return s.appCall(ctx, AppFeishu, FeishuText, p, nil)
}

// feishuMention is a text message's @ of the user with open id (Feishu's text syntax, which the name inside the tag
// does not matter to: it shows the user's own).
func feishuMention(openID string) string {
	return `<at user_id="` + strings.NewReplacer(`"`, "", "<", "", ">", "").Replace(openID) + `"></at>`
}

// nudgeKey is the key one kind of card nudge goes out under, so a retry after an
// error lands once. Keep the ordinary key compatible with cards sent before the
// deadline nudge existed; the two kinds must not deduplicate each other.
func nudgeKey(id string, kind nudgeKind) string {
	what := "nudge:" + id
	if kind == deadlineNudge {
		what = "deadline-nudge:" + id
	}
	sum := sha256.Sum256([]byte(what))
	return "nudge-" + hex.EncodeToString(sum[:12])
}

// nudgeText is the one line: what the card asks, in the user's language. A permission's is the tool only, never the
// command or the path it would run on.
func (s *Service) nudgeText(ctx context.Context, ix proto.Interaction) string {
	l := s.botLang(ctx)
	what := ""
	switch ix.Kind {
	case proto.IxPermission:
		if ix.ToolName != "" {
			return i18n.T(l, "assistant-feishu_cards.nudge_permission", i18n.Args{"tool": ixToolName(ix)})
		}
	case proto.IxQuestion:
		what = firstQuestion(ix)
	case proto.IxPlan:
		what = ix.Plan
	case proto.IxScreen:
		what = i18n.T(l, "assistant-feishu_cards.screen_summary")
	case proto.IxElicitation:
		if ix.Elicitation != nil {
			what = ix.Elicitation.Message
		}
	}
	what = clip(oneLine(firstLine(what)), 100)
	if what == "" {
		return i18n.T(l, "assistant-feishu_cards.nudge_bare")
	}
	return i18n.T(l, "assistant-feishu_cards.nudge", i18n.Args{"what": what})
}

// cardNudged: the card asking interaction id was nudged under (the waiting-for-you board marks its row).
func (s *Service) cardNudged(id string) bool {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	return !b.cards[id].NudgedAt.IsZero() || !b.cards[id].DeadlineNudge.NudgedAt.IsZero()
}
