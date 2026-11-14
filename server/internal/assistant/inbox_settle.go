package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Handled elsewhere (docs/assistant/inbox.md "Handled elsewhere"): an open item the owner already dealt with in the
// source, or that the other side closed, is filed without the owner pressing Done. Two places find it:
//
//   - grouping: the model relating a batch says an open event is settled (group.md "settled"), from the batch's own
//     lines (the other side's "done, thanks", the owner's own lines);
//   - settleInbox below: the owner's own reply makes no batch (a connector hands only other people's messages), so a
//     pass reads the conversation after each open "Needs you" event through the connector's group tool and files the
//     event when the owner answered there (a direct chat: any message of theirs; a group: one quoting or in the
//     thread of the event's messages), else asks the triage model about what came after.
//
// Either way the chain says why (self_replied, or done with the reason), the item can be reopened as usual, and
// SettledOf keeps the evidence so a reopened item is not filed again on it.

// How an item was settled: the group answer's "settled", the settle model's "state".
const (
	SettledReplied = "replied" // the owner answered in the source
	SettledOver    = "over"    // the other side closed it, or someone else answered
)

const (
	settleEvery   = 20 * time.Minute // an item is looked at again at most this often
	settleSpacing = time.Minute      // passes start at most this often (rounds come every few seconds on chat events)
	settlePerPass = 8                // items one pass looks at
	settleAround  = 5                // messages either side of the event's newest the pass reads (the group tool cuts at 8 KB)
	settleTimeout = time.Minute      // one item: the read and the model
)

// settleItem files an open item handled elsewhere: how is SettledReplied (done_by replied, a self_replied entry at
// position) or SettledOver (done_by manager, a done entry saying so); why is the model's short reason, evidence the
// newest position it looked at ("": the item's own newest). Under b.mu.
func (b *inboxBook) settleItem(it *InboxItem, how, why, position, evidence string, now time.Time) {
	l := b.userLang()
	switch how {
	case SettledReplied:
		link := &InboxChainLink{Type: LinkSource, Position: position}
		b.chainAdd(it, InboxChainEntry{Kind: ChainSelfReplied, At: now.UTC(), Via: it.Source, Detail: why, Link: link,
			Text: i18n.T(l, "assistant-inbox_chain.self_replied", i18n.Args{"source": b.sourceName(it.Source)})})
		it.done(now, "replied")
	case SettledOver:
		it.done(now, doneByManager)
		b.chainAdd(it, InboxChainEntry{Kind: ChainDone, At: now.UTC(), By: doneByManager, Detail: why,
			Text: i18n.T(l, "assistant-inbox_chain.done_over")})
	default:
		return
	}
	it.SettledOf = cmp.Or(evidence, position, it.Last)
}

// settleMsg is one message the connector's group tool answers (the Lark plugin's: newest last).
type settleMsg struct {
	ID      string `json:"id"`
	Pos     string `json:"pos"`
	At      string `json:"at"`
	Sender  string `json:"sender"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to"`
	Thread  string `json:"thread"`
	Mine    bool   `json:"mine"`
}

// settleMessages reads the messages of a group tool's answer, as many as are whole: the hub cuts an answer past 8 KB.
func settleMessages(result string) []settleMsg {
	dec := json.NewDecoder(strings.NewReader(result))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []settleMsg
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return out
		}
		if key != "messages" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return out
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return out
		}
		for dec.More() {
			var m settleMsg
			if dec.Decode(&m) != nil {
				return out
			}
			out = append(out, m)
		}
		return out
	}
	return out
}

// positionAfter: position a comes after b, both a conversation's message positions (decimal numbers); false when
// either is not one, so an unknown order never proves anything.
func positionAfter(a, b string) bool {
	x, ok1 := new(big.Int).SetString(a, 10)
	y, ok2 := new(big.Int).SetString(b, 10)
	return ok1 && ok2 && x.Cmp(y) > 0
}

// settleCandidate: an open "Needs you" event of a grouping connector, not handled by its conversation, with no
// suggestion waiting, whose newest message from someone else is known (the group tool's anchor). Under b.mu.
func settleCandidate(it *InboxItem) bool {
	return it.open() && it.Tier == TierAction && it.Group != nil && it.Lark != nil && it.Lark.ReplyTo != "" && it.Last != "" &&
		!it.handled() && currentSuggestion(it) == nil
}

// settleInbox looks, in the background, at up to settlePerPass open "Needs you" events not looked at for settleEvery,
// the longest unasked first, and files those handled elsewhere. One pass at a time.
func (s *Service) settleInbox(ctx context.Context) {
	b := &s.inbox
	if !b.settling.TryLock() {
		return
	}
	now := time.Now()
	b.mu.Lock()
	if b.settleAsked == nil {
		b.settleAsked, b.settleSeen = map[string]time.Time{}, map[string]string{}
	}
	if now.Sub(b.settlePass) < settleSpacing {
		b.mu.Unlock()
		b.settling.Unlock()
		return
	}
	b.settlePass = now
	for id := range b.settleAsked { // what is no longer a candidate is forgotten (asked again if it becomes one)
		if it := b.items[id]; it == nil || !settleCandidate(it) {
			delete(b.settleAsked, id)
			delete(b.settleSeen, id)
		}
	}
	var todo []*InboxItem
	for _, it := range b.items {
		if settleCandidate(it) && now.Sub(b.settleAsked[it.ID]) >= settleEvery {
			todo = append(todo, it)
		}
	}
	slices.SortFunc(todo, func(x, y *InboxItem) int {
		if c := b.settleAsked[x.ID].Compare(b.settleAsked[y.ID]); c != 0 {
			return c
		}
		return y.UpdatedAt.Compare(x.UpdatedAt)
	})
	todo = todo[:min(len(todo), settlePerPass)]
	for _, it := range todo {
		b.settleAsked[it.ID] = now
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		b.settling.Unlock()
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer b.settling.Unlock()
		var filed []*inboxWrite
		for _, it := range todo {
			if w := s.settleOne(ctx, it); w != nil {
				filed = append(filed, w)
			}
		}
		// Undone when its row can't be written: the next pass finds it again.
		s.saveWrites(ctx, filed, "its filing as handled elsewhere")
	}()
}

// settleSnapshot is what settleOne reads of an item under b.mu.
type settleSnapshot struct {
	connector, thread, anchor, last, start, settledOf string
	group                                             bool
	title, summary, context                           string
	// others are the newest positions of the conversation's other open events: in a direct chat, the owner's message
	// after one of those answers that one, not necessarily this.
	others []string
}

// settleOne looks at one item: the filing when it filed it, else nil.
func (s *Service) settleOne(ctx context.Context, it *InboxItem) *inboxWrite {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()
	b := &s.inbox
	b.mu.Lock()
	if !settleCandidate(it) {
		b.mu.Unlock()
		return nil
	}
	snap := settleSnapshot{connector: it.Group.Connector, thread: it.Group.Thread, anchor: it.Lark.ReplyTo, last: it.Last,
		start: it.Lark.StartPosition, settledOf: it.SettledOf, group: it.Lark.IsGroup, title: it.Title, summary: it.Summary,
		context: it.Context}
	for _, o := range b.items {
		if o != it && o.open() && o.Group != nil && o.Group.Connector == snap.connector && o.Group.Thread == snap.thread {
			snap.others = append(snap.others, o.Last)
		}
	}
	asked := b.settleSeen[it.ID]
	b.mu.Unlock()

	var res struct {
		Result string `json:"result"`
	}
	args, _ := json.Marshal(map[string]any{"message": snap.anchor, "n": settleAround})
	err := s.appCall(ctx, AppInbox, inboxGroupTool, map[string]any{"source": snap.connector,
		"call": connector.GroupToolCall{Thread: snap.thread, Name: connector.GroupMessagesAround, Args: args}}, &res)
	if err != nil {
		if !unsupported(err) {
			s.logf("inbox: looking for %s handled elsewhere: %v", it.ID, err)
		}
		return nil
	}
	msgs := settleMessages(res.Result)
	how, at, newest := settleVerdict(snap, msgs)
	why := ""
	if how == "" && newest != "" && newest != asked { // the model already said "open" to exactly these
		var answered bool
		how, why, at, answered = s.askSettled(ctx, snap, msgs)
		if answered { // a failed call is asked again next time
			b.mu.Lock()
			b.settleSeen[it.ID] = newest
			b.mu.Unlock()
		}
	}
	if how == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !settleCandidate(it) || it.Last != snap.last { // moved meanwhile: the next pass looks again
		return nil
	}
	w := beginWrite(it)
	b.settleItem(it, how, why, at, newest, time.Now())
	b.settle(w)
	return w
}

// contextIDs are the message ids an event's context names, as the Lark plugin writes its lines ("… (id=<id>, 回复 …)").
var contextIDs = lazyre.New(`\(id=([^,()\s]+)`)

// settleFloor is the position after which messages are evidence: the event's newest, or what it was filed on once
// (reopened by hand since, so only newer messages count).
func settleFloor(snap settleSnapshot) string {
	if positionAfter(snap.settledOf, snap.last) {
		return snap.settledOf
	}
	return snap.last
}

// recalledText is how the Lark plugin writes a recalled message: it says nothing.
const recalledText = "(撤回了一条消息)" // i18n-ignore: the connector's own words, matched, never shown

// settleVerdict decides from the messages after the event's newest (last) what it can without a model: the owner's
// own message in a direct chat (with no other open event of the chat in between, which it may be answering
// instead), or one in a group quoting or in the thread of the event's messages, is a reply (how SettledReplied, at the
// newest such message's position). newest is the newest later message's position when there is something after the
// event to go on ("" nothing, or nothing newer than what SettledOf already filed it on once); with how "" the model
// reads it. A recalled message of the owner's counts for nothing.
func settleVerdict(snap settleSnapshot, msgs []settleMsg) (how, at, newest string) {
	ours := map[string]bool{snap.anchor: true} // the event's messages: its context names them, else the window's own
	for _, m := range contextIDs.FindAllStringSubmatch(snap.context, -1) {
		ours[m[1]] = true
	}
	named := len(ours) > 1
	floor := settleFloor(snap)
	var later []settleMsg
	for _, m := range msgs {
		switch {
		case positionAfter(m.Pos, floor):
			if !m.Mine || m.Text != recalledText {
				later = append(later, m)
			}
		case positionAfter(m.Pos, snap.last):
		case !named && !m.Mine && (snap.start == "" || !positionAfter(snap.start, m.Pos)):
			ours[m.ID] = true
		}
	}
	if len(later) == 0 {
		return "", "", ""
	}
	newest = later[len(later)-1].Pos
	for _, m := range later {
		if !m.Mine {
			continue
		}
		targeted := ours[m.ReplyTo] || m.Thread != "" && ours[m.Thread]
		between := slices.ContainsFunc(snap.others, func(o string) bool { return positionAfter(o, snap.last) && positionAfter(m.Pos, o) })
		if targeted || !snap.group && !between {
			how, at = SettledReplied, m.Pos
		}
	}
	return how, at, newest
}

// askSettled asks the triage model whether the event is over given what came after it (after settleFloor): how ""
// when it is not; at is the position the verdict rests on (the owner's first later message for a reply, else the
// newest later one); answered is false when the model could not be asked or said nothing usable. A "replied" with no
// message of the owner's after the event is taken as "over": the chain never says the owner replied when they didn't.
func (s *Service) askSettled(ctx context.Context, snap settleSnapshot, msgs []settleMsg) (how, why, at string, answered bool) {
	floor := settleFloor(snap)
	var later []prompts.InboxSettledLine
	mine := ""
	for _, m := range msgs {
		if !positionAfter(m.Pos, floor) || m.Mine && m.Text == recalledText {
			continue
		}
		line := prompts.InboxSettledLine{Sender: m.Sender, Text: m.Text, Mine: m.Mine, ReplyTo: m.ReplyTo, Thread: m.Thread}
		if t, err := time.Parse(time.RFC3339, m.At); err == nil {
			line.At = t.Format("01-02 15:04")
		}
		later = append(later, line)
		at = m.Pos
		if m.Mine && mine == "" {
			mine = m.Pos
		}
	}
	if len(later) == 0 {
		return "", "", "", false
	}
	hello := s.hello(ctx)
	system := prompts.RenderInboxSettled(prompts.InboxSettled{Owner: hello.Owner})
	input := prompts.RenderInboxSettledInput(prompts.InboxSettledInput{Title: snap.title, Summary: snap.summary,
		Context: snap.context, Later: later})
	var out struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := s.inboxModelJSON(ctx, purposeTriage, system, input, 0, &out); err != nil {
		s.logf("inbox: asking whether %s is over: %v", snap.anchor, err)
		return "", "", "", false
	}
	why = clip(oneLine(out.Reason), 30)
	switch {
	case out.State == SettledReplied && mine != "":
		return SettledReplied, why, mine, true
	case out.State == SettledReplied || out.State == SettledOver:
		return SettledOver, why, at, true
	case out.State == "open":
		return "", "", "", true
	}
	return "", "", "", false
}
