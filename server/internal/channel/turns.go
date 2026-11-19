package channel

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A message from a channel starts a run: the hub follows the conversation until the turn is over, then sends its
// answer (the assistant's last words, after its last tool call) back. The rules (docs/extensions/channels.md "Message flow"):
// the message is found by its client_msg_id (or a piece of its text, from older nodes); a message queued behind a
// running turn waits for its own turn; a turn that leaves background work answers now and again when that work
// reports back; a message sent while one waits on the same conversation is answered with it.

// Run is one turn a channel waits on, kept by the hub so a restart still answers it.
type Run struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	// ReplyTo is the chat app's message it answers ("" for a channel that does not reply to one).
	ReplyTo   string `json:"reply_to,omitempty"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	// TargetKind is what the conversation is to the channel: conversation, or agent (the Feishu bot's /agents).
	TargetKind string `json:"target_kind,omitempty"`
	Label      string `json:"label,omitempty"`
	// Base is the conversation's last line before the message went in: the answer comes after it.
	Base int `json:"base"`
	// MsgIDs are the client_msg_ids of the messages it answers; Probe a piece of the first one's text, to find it
	// when the node does not tie items to their send.
	MsgIDs []string `json:"msg_ids,omitempty"`
	Probe  string   `json:"probe,omitempty"`
	// Head: before its MsgIDs it answers a message the conversation has no id for, the first thing after Base (the
	// message that started the conversation, folded in: node assistant.send ties no client_msg_id to it).
	Head    bool      `json:"head,omitempty"`
	Started time.Time `json:"started"`
	// Seen: the conversation was seen working since (until then a quiet one may just not have started).
	Seen bool `json:"seen,omitempty"`
	// Held: answered once; its background work goes on, and the turn that work starts is answered too.
	Held bool `json:"held,omitempty"`
	// Folded are earlier messages to the same conversation this answer covers too (sent while it worked).
	Folded []Folded `json:"folded,omitempty"`
	// Ext is the channel's own: the progress card, the reaction on the message.
	Ext map[string]string `json:"ext,omitempty"`
	// Again: the turn ended without an answer and the conversation was asked for one then (AskAgain); the waits for
	// a turn to start count from it.
	Again time.Time `json:"again,omitempty"`
	// Recalled: the owner recalled the one message it answers (#5248); it ends as EndRecalled, with no answer.
	Recalled bool `json:"recalled,omitempty"`
}

// waitFrom is when the turn being waited on was asked for: the message, or the ask for an answer.
func (r *Run) waitFrom() time.Time {
	if r.Again.After(r.Started) {
		return r.Again
	}
	return r.Started
}

// AskAgain makes r wait for the answer to msgID (probe: a piece of its text), a message asking the conversation for
// the answer its turn did not give, after line last.
func (r *Run) AskAgain(last int, msgID, probe string, now time.Time) {
	if last > r.Base {
		r.Base = last
	}
	r.MsgIDs, r.Probe, r.Head, r.Seen, r.Again = []string{msgID}, probe, false, false, now
}

// Queued: the run's message is not in the conversation yet, but may still come (it waits behind another turn).
func (r *Run) Queued(now time.Time) bool { return now.Sub(r.waitFrom()) < QueuedMost }

// Asked: the conversation was asked again for this turn's answer (once is all).
func (r *Run) Asked() bool { return !r.Again.IsZero() }

// Placeholder: text stands in for an answer the turn did not give.
func Placeholder(text string) bool {
	return transcript.ReplyPlaceholder(text)
}

// Folded is an earlier message a run answers too.
type Folded struct {
	ReplyTo string            `json:"reply_to,omitempty"`
	Ext     map[string]string `json:"ext,omitempty"`
}

// How long a run waits, and on what.
const (
	RunMost    = 4 * time.Hour
	HeldMost   = 2 * time.Hour
	QuietGrace = 20 * time.Second // not seen working: the turn may not have started
	EmptyGrace = 2 * time.Minute  // no words yet and not seen working
	QueuedMost = 10 * time.Minute // the message is not in the conversation yet: it waits behind another turn
	// AnswerSettle is how long a turn read as over without words at its end is read again before it counts as one
	// without an answer: the node says a turn is over a moment before its last words are in the transcript.
	AnswerSettle  = 10 * time.Second
	MissingAfter  = 3 * time.Minute // the conversation is not listed (closed, deleted)
	OfflineGrace  = time.Minute
	HeldIdleMost  = 10 * time.Minute // a held run's background work ended without a word
	ProbeLen      = 24
	ProgressAfter = 5 * time.Second
	ProgressEvery = 3 * time.Second
	ProgressIdle  = 12 * time.Second // the elapsed time moves on even without new steps
	TypingEvery   = 5 * time.Second
)

// Fold adds an earlier run on the same conversation to r: one answer covers both messages.
func (r *Run) Fold(o *Run) {
	if o.Base < r.Base || r.Base == 0 {
		r.Base = o.Base
	}
	if o.Started.Before(r.Started) {
		r.Started = o.Started
	}
	r.Folded = append(append(r.Folded, o.Folded...), Folded{ReplyTo: o.ReplyTo, Ext: o.Ext})
	r.MsgIDs = append(slices.Clone(o.MsgIDs), r.MsgIDs...)
	// An earlier run that names no message (it started the conversation, read from its base on) stays one of the
	// messages answered: without this the fold names only the later one and the first answer is lost (#4746). Voice
	// names that one by its probe alone, from base 0 (it is the conversation's first line). A held run names none
	// either, but it was answered already.
	if o.Head || len(o.MsgIDs) == 0 && (o.Probe == "" || o.Base == 0) && !o.Held {
		r.Head = true
	}
	if r.Probe == "" {
		r.Probe = o.Probe
	}
}

// Unfold takes message ref (msgID: its client_msg_id) out of a run that answers others too, as when the owner
// recalled it: the others are still answered. When ref is the run's own message, the latest folded one takes its
// place (the answer replies to it, its reaction is the run's). It returns what ref had (its reply ref and reaction);
// false when the run answers only ref, or does not answer it.
func (r *Run) Unfold(ref, msgID string) (Folded, bool) {
	if len(r.Folded) == 0 || ref == "" {
		return Folded{}, false
	}
	var out Folded
	if r.ReplyTo == ref {
		out = Folded{ReplyTo: ref, Ext: map[string]string{}}
		if rx := r.Ext["reaction"]; rx != "" {
			out.Ext["reaction"] = rx
		}
		last := r.Folded[len(r.Folded)-1]
		r.Folded = slices.Clone(r.Folded[:len(r.Folded)-1])
		r.ReplyTo = last.ReplyTo
		if r.Ext == nil {
			r.Ext = map[string]string{}
		}
		delete(r.Ext, "reaction")
		if rx := last.Ext["reaction"]; rx != "" {
			r.Ext["reaction"] = rx
		}
	} else {
		i := slices.IndexFunc(r.Folded, func(f Folded) bool { return f.ReplyTo == ref })
		if i < 0 {
			return Folded{}, false
		}
		out = r.Folded[i]
		r.Folded = slices.Delete(slices.Clone(r.Folded), i, i+1)
	}
	r.MsgIDs = slices.DeleteFunc(slices.Clone(r.MsgIDs), func(id string) bool { return id == msgID })
	return out, true
}

// Look is the conversation as it is now, for Check.
type Look struct {
	Now time.Time
	// Exists: the conversation is listed; Online: its machine is connected.
	Exists, Online bool
	Working        bool      // working, or starting
	Waiting        bool      // blocked on the owner, or a card waits for an answer
	Offline        bool      // the conversation's own status is offline
	Background     int       // background work still running (commands, Monitors, subagents)
	Failed         bool      // its last turn ended in a model API error
	Lang           i18n.Lang // what the notes are written in (the user's language; zh-Hans when unknown)
}

// Action is what Check says to do.
type Action int

const (
	Wait   Action = iota
	Read          // read the answer now (then Decide)
	Finish        // stop waiting: Step.Note says why
)

// End is how a run ended, for a sink that shows it (the Feishu card's last line).
type End string

const (
	EndDone     End = ""
	EndStopped  End = "stopped"  // interrupted (/stop, the app, the terminal)
	EndError    End = "error"    // the turn ended in a model API error
	EndGaveUp   End = "gave_up"  // not done in RunMost (HeldMost for a held run)
	EndGone     End = "gone"     // the conversation was closed or deleted
	EndOffline  End = "offline"  // its machine is offline
	EndRecalled End = "recalled" // the owner recalled the message it answers
)

// Step is Check's verdict.
type Step struct {
	Do   Action
	Note string
	End  End // with Finish
}

// Check looks at a run against the conversation now: wait, read the answer, or give up. It marks the run Seen when
// the conversation works.
func (r *Run) Check(l Look) Step {
	age := l.Now.Sub(r.Started)
	gaveUp := EndGaveUp
	if l.Offline || !l.Exists && !l.Online {
		gaveUp = EndOffline
	}
	// Both limits end with a word. A held run has no progress card left (it gave the card back with its message), so an
	// ending without a note would be a quiet one: nothing would be said anywhere, and the owner would never hear that
	// the wait is over.
	if (r.Held && age > HeldMost) || age > RunMost {
		return Step{Do: Finish, End: gaveUp, Note: i18n.T(l.Lang, "channel-turns.gave_up", i18n.Args{"took": Duration(l.Lang, age)})}
	}
	if !l.Exists {
		if age > MissingAfter && l.Online {
			return Step{Do: Finish, End: EndGone, Note: i18n.T(l.Lang, "channel-turns.gone")}
		}
		return Step{Do: Wait}
	}
	if l.Working {
		r.Seen = true
		return Step{Do: Wait}
	}
	if l.Waiting {
		return Step{Do: Wait} // waiting on the owner: what it asks goes out as an ask
	}
	if !r.Seen && l.Now.Sub(r.waitFrom()) < QuietGrace {
		return Step{Do: Wait}
	}
	if l.Offline && age < OfflineGrace {
		return Step{Do: Wait}
	}
	return Step{Do: Read}
}

// Reading is what reading the conversation found.
type Reading struct {
	Answer string // the turn's answer ("" when it has no words)
	Found  bool   // the run's message is in the conversation
	Last   int    // the last line read
	// Stopped is why a turn without words ended, when the conversation says (Stopped): an API error, an interruption.
	Stopped string
	// Words: the turn's items end in the assistant's words (else its last words may not be written yet).
	Words bool
}

// Result is what to send when a run is over (or held: answered once, waiting on its background work).
type Result struct {
	Answer string
	Note   string
	Held   bool
	End    End // how a final one ended
	Last   int
	// Quiet: nothing to send (a held run whose background work ended without a word).
	Quiet bool
	// Stopped is why the turn ended without words, when known (Reading.Stopped).
	Stopped string
}

// Decide is, after reading, whether the run is answered: nil to keep waiting. A message not found past QueuedMost
// never gets here: the caller gives up and asks for it again (reading the whole tail would answer with the previous
// turn's words).
func (r *Run) Decide(rd Reading, l Look) *Result {
	age := l.Now.Sub(r.Started)
	held := l.Background > 0
	if strings.TrimSpace(rd.Answer) == "" {
		switch {
		case held:
			return nil
		case r.Held && age < HeldIdleMost:
			return nil
		case r.Held:
			// Its background work ended without more to say: a quiet end, which still says how that turn ended (it may
			// have been interrupted or failed, or its computer gone offline), so a bad ending is not reported as done
			// (#4228).
			return &Result{Quiet: true, Last: rd.Last, Stopped: rd.Stopped, End: endOf(rd, l)}
		case !r.Seen && l.Now.Sub(r.waitFrom()) < EmptyGrace:
			return nil // not started yet (queued behind another message, a slow start)
		}
	}
	res := &Result{Answer: rd.Answer, Held: held, Last: rd.Last}
	if strings.TrimSpace(rd.Answer) == "" {
		res.Stopped = rd.Stopped
	}
	if held {
		res.Note = i18n.T(l.Lang, "channel-turns.background", i18n.Args{"count": l.Background})
	} else {
		res.End = endOf(rd, l)
	}
	return res
}

// endOf is how a run ended, from the signals both an end without words and a quiet one have: the turn's last notice
// (an interruption, a model API error), the conversation's failed turn, and its machine going offline.
func endOf(rd Reading, l Look) End {
	switch {
	case rd.Stopped == transcript.InterruptedNotice:
		return EndStopped
	case strings.HasPrefix(rd.Stopped, transcript.APIErrorPrefix) || l.Failed:
		return EndError
	case l.Offline:
		return EndOffline // cut off by its machine going offline, words or not
	}
	return EndDone
}

// Hold makes r wait for the turn its background work starts, from last on: answered once already.
func (r *Run) Hold(last int, now time.Time) {
	if last > r.Base {
		r.Base = last
	}
	r.MsgIDs, r.Probe, r.Head, r.Seen, r.Held, r.Started, r.Folded, r.Again = nil, "", false, false, true, now, nil, time.Time{}
}

// Find is where the run's message is in items: by its client_msg_id, else by its probe (holds says whether a user
// item holds it, for one cut for pages); 0, true for a run that has neither (everything after Base is new).
func (r *Run) Find(items []proto.Item, holds func(proto.Item) bool) (int, bool) {
	if r.Head || len(r.MsgIDs) == 0 && r.Probe == "" {
		return 0, true
	}
	for i, it := range items {
		if it.ClientMsgID != "" && slices.Contains(r.MsgIDs, it.ClientMsgID) {
			return i, true
		}
	}
	if r.Probe == "" {
		return 0, false
	}
	if holds == nil {
		holds = func(it proto.Item) bool { return strings.Contains(it.Text, r.Probe) }
	}
	for i, it := range items {
		if it.Role == "user" && it.Kind == proto.KindText && holds(it) {
			return i, true
		}
	}
	return 0, false
}

// Answer picks a turn's answer out of its items: every text the assistant wrote after the last message it was given
// (a message taken mid-turn) or notice, in order, but the passing lines said right before a tool call
// (transcript.AnswerParts, the rule the push body follows); without any after those, its last text. A report written
// before a tool call is part of the answer, not only the words after the last call.
func Answer(items []proto.Item) []proto.Item {
	var texts []proto.Item
	var parts []transcript.AnswerPart
	var last *proto.Item
	for i, it := range items {
		switch {
		case it.Role == "assistant" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "":
			last = &items[i]
			texts = append(texts, it)
			parts = append(parts, transcript.AnswerPart{Text: it.Text})
		case it.Kind == proto.KindToolUse:
			if n := len(parts); n > 0 {
				parts[n-1].BeforeTool = true
			}
		case it.Kind == proto.KindNotice || it.Role == "user" && it.Kind == proto.KindText:
			texts, parts = nil, nil
		}
	}
	if len(texts) == 0 {
		if last != nil {
			return []proto.Item{*last}
		}
		return nil
	}
	keep := transcript.AnswerParts(parts)
	out := make([]proto.Item, 0, len(keep))
	for _, i := range keep {
		out = append(out, texts[i])
	}
	return out
}

// StoppedMost bounds the reason Stopped gives.
const StoppedMost = 300

// Stopped is why a turn ended when its items end on a model API error or "Interrupted" (the notices the transcript parser
// writes for them, transcript.APIErrorPrefix / InterruptedNotice) rather than on words: StopReason of its text.
// Compactions after it (an idle conversation compacted, Persona's /compact) are passed over; any other ending gives
// "", other notices too (goals, a task reporting back, Codex events, command output).
func Stopped(items []proto.Item) string {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch {
		case it.Kind == proto.KindThinking, it.TurnDuration != nil, it.Kind == proto.KindText && strings.TrimSpace(it.Text) == "":
			continue
		case it.Kind != proto.KindNotice || it.Task != nil:
			return ""
		case compaction(it):
			continue
		}
		text := strings.TrimSpace(it.Text)
		if text == transcript.InterruptedNotice || strings.HasPrefix(text, transcript.APIErrorPrefix) {
			return StopReason(text)
		}
		return ""
	}
	return ""
}

// compaction: a notice that the context was compacted, which says nothing of how the turn before it ended.
func compaction(it proto.Item) bool {
	if it.Compact != nil {
		return true
	}
	switch strings.TrimSpace(it.Text) {
	case transcript.CompactedNotice, transcript.CompactSummaryNotice, transcript.CompactNotice:
		return true
	}
	return false
}

// StopReason is text as the reason a turn stopped: on one line, cut to StoppedMost runes.
func StopReason(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > StoppedMost {
		text = string(r[:StoppedMost]) + "…"
	}
	return text
}

// Background is how much background work a conversation still runs (commands, Monitors, subagents).
func Background(s proto.Session) int {
	n := len(s.Background)
	for _, a := range s.Subagents {
		if a.Running {
			n++
		}
	}
	return n
}

// Probe is a piece of what the owner wrote, to find the message in the transcript: its first line; else something
// only it has (fallbacks, in order).
func Probe(text string, fallbacks ...string) string {
	for _, s := range append([]string{text}, fallbacks...) {
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				r := []rune(l)
				return string(r[:min(len(r), ProbeLen)])
			}
		}
	}
	return ""
}

// Duration is d for people in l: "12 s", "3 min 4 s", "2 h 5 min" (in l's words).
func Duration(l i18n.Lang, d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return i18n.T(l, "channel-turns.seconds", i18n.Args{"s": s})
	case s < 3600:
		return i18n.T(l, "channel-turns.minutes", i18n.Args{"m": s / 60, "s": s % 60})
	}
	return i18n.T(l, "channel-turns.hours", i18n.Args{"h": s / 3600, "m": s % 3600 / 60})
}
