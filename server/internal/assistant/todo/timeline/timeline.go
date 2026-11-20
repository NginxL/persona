// Package timeline groups a to-do's history into the rounds of its conversation (#10465, proposal 3.6 of
// docs/design/proposals/todos-redesign.md): each instruction or answer of the owner's heads a round, and what followed
// folds under it (relays, one line per run, asks, decisions, results, scope changes); supervision nobody asked for
// forms one Background round a day. It also picks the to-do's results and what changed since the owner last looked
// (the paper's Results and Since you looked). The rules are pinned by the shared cases in spec/fixtures/todo-rounds,
// whose README says what each entry is read from, every rule, line key and judgement call.
//
// Stage 2 reads the timeline from today's stores: the conversation feed's items about the to-do, its decisions, its
// ledger, its brief and its runs' links and sessions. Stage 3 stores the entries themselves (todo_activity).
//
// It is pure, as todo/projection is: no IO and no clock, and it imports only the standard library, proto (the
// sessions as the apps get them) and assistantwire (the wire types). The manager's adapter reads the stores.
package timeline

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Derived is a to-do's conversation, its results and what changed since the owner looked.
type Derived struct {
	// Rounds are the conversation's rounds, oldest first; never nil.
	Rounds []assistantwire.TodoRound
	// Results are what its work produced, newest first, at most maxResults; nil when nothing was.
	Results []assistantwire.TodoResult
	// Recap is what changed since the owner last looked; nil when they never did, or nothing but their own did.
	Recap *assistantwire.TodoRecap
}

// Derive works out one to-do's timeline from its stores.
func Derive(in Input) Derived {
	r := &reader{in: &in, changes: map[string]change{}}
	r.read()
	slices.SortFunc(r.items, byTime)
	r.pair()
	rounds := r.group()
	out := Derived{Rounds: make([]assistantwire.TodoRound, 0, len(rounds)), Results: r.results()}
	roundOf := map[*item]string{}
	for _, rd := range rounds {
		for _, it := range rd.items {
			roundOf[it] = rd.out.ID
		}
		out.Rounds = append(out.Rounds, rd.out)
	}
	out.Recap = r.recap(roundOf)
	return out
}

// byTime orders items by time, then id.
func byTime(a, b *item) int {
	return cmp.Or(a.e.At.Compare(b.e.At), strings.Compare(a.e.ID, b.e.ID))
}

// pair applies R1: one act of the owner's that left two rows makes one header, and a run an instruction started folds
// under it.
//   - a decision recorded with the instruction that carried it (its source message is the instruction's feed item)
//     folds under the instruction;
//   - the card decisions of one card (one per question, under the card's id as their source message) are one answer:
//     the earliest stands for the card, and the others fold with it;
//   - a card's decisions fold under the owner's answer of the card nearest the earliest, within pairWindow (both are
//     written when the card is answered, in no fixed order, and the answer does not say its run);
//   - the hub's relay of the owner's choice folds under the owner's decision it relays: the one nearest it within
//     pairWindow when the choice was relayed, then recorded; else the latest before it within relayWindow, recorded
//     before it was relayed (the owner's reply to its card in Lark). A relay with another decision beside it (the hub's
//     default) relays that one;
//   - the run an instruction started when every run had stopped folds under that instruction.
func (r *reader) pair() {
	instructions := map[string]*item{}
	for _, it := range r.items {
		if it.from == fromInstruction {
			instructions[it.key] = it
		}
	}
	for _, it := range r.items {
		if it.e.Decision == "" || it.key == "" {
			continue
		}
		if in, ok := instructions[it.key]; ok {
			it.join, it.head = in.e.ID, false
		}
	}
	cards := map[string]*item{}
	for _, it := range r.items { // in time order: the earliest of a card's decisions comes first
		if it.from != fromCard || !it.head || it.key == "" {
			continue
		}
		if first, ok := cards[it.key]; ok {
			it.join, it.head = first.e.ID, false
		} else {
			cards[it.key] = it
		}
	}
	for _, it := range r.items {
		var with *item
		switch it.from {
		case fromCard:
			if it.head {
				with = r.nearest(it.e.At, pairWindow, func(c *item) bool { return c.from == fromAnswered })
			}
		case fromRelayAnswer:
			with = r.relayed(it)
		}
		if with != nil {
			it.join, it.head = with.e.ID, false
		}
	}
	for _, in := range r.items {
		if in.started == "" {
			continue
		}
		for _, it := range r.items {
			if it.from == fromLink && it.key == in.started {
				it.join = in.e.ID
			}
		}
	}
	byID := map[string]*item{}
	for _, it := range r.items {
		byID[it.e.ID] = it
	}
	for _, it := range r.items { // a card's later decisions go where its first went
		if to := byID[it.join]; to != nil && to.join != "" {
			it.join = to.join
		}
	}
}

// relayed is the owner's decision relay it relays (pair); nil when it relays none of theirs.
func (r *reader) relayed(relay *item) *item {
	if d := r.nearest(relay.e.At, pairWindow, func(c *item) bool { return c.e.Decision != "" }); d != nil {
		if d.from == fromManual && d.head {
			return d
		}
		return nil
	}
	var out *item
	for _, c := range r.items {
		if c.from == fromManual && c.head && !c.e.At.After(relay.e.At) && relay.e.At.Sub(c.e.At) <= relayWindow &&
			(out == nil || byTime(c, out) > 0) {
			out = c
		}
	}
	return out
}

// round is a round being put together: the wire round, and the items it holds (its header's included).
type round struct {
	out   assistantwire.TodoRound
	items []*item
}

// group puts the items into rounds and orders them.
//   - R1: every header opens an owner round, r:<header id>.
//   - R2: an item belongs to the round of the latest header at or before it; one before every header to the start
//     round, start, headed by the owner's request when the owner started the work. An item R1 paired goes with its
//     header whatever its time.
//   - R3: supervision nobody asked for goes to its day's Background round in the owner's zone, bg:<YYYY-MM-DD>.
//   - R4: the turns of one run in one round make one line (collapse); the latest round's line of a run that works
//     now is live.
//
// The start round comes first; the others by when they began, then id.
func (r *reader) group() []*round {
	headers := map[string]*round{}
	start := &round{out: assistantwire.TodoRound{ID: "start", Kind: assistantwire.TodoRoundKindStart, Header: r.request}}
	days := map[string]*round{}
	var rounds []*round
	for _, it := range r.items {
		if it.head {
			rd := &round{out: assistantwire.TodoRound{ID: "r:" + it.e.ID, Kind: assistantwire.TodoRoundKindOwner,
				At: it.e.At, Header: &it.e}}
			headers[it.e.ID] = rd
			rounds = append(rounds, rd)
		}
	}
	cur := start
	for _, it := range r.items {
		rd := cur
		switch {
		case it.head:
			rd = headers[it.e.ID]
			cur = rd
		case it.join != "" && headers[it.join] != nil:
			rd = headers[it.join]
		case it.bg:
			day := it.e.At.In(r.zone()).Format("2006-01-02")
			if rd = days[day]; rd == nil {
				rd = &round{out: assistantwire.TodoRound{ID: "bg:" + day, Kind: assistantwire.TodoRoundKindBackground,
					At: it.e.At}}
				days[day] = rd
				rounds = append(rounds, rd)
			}
		}
		rd.items = append(rd.items, it)
	}
	for _, rd := range rounds {
		rd.collapse()
	}
	slices.SortFunc(rounds, func(a, b *round) int {
		return cmp.Or(a.out.At.Compare(b.out.At), strings.Compare(a.out.ID, b.out.ID))
	})
	start.collapse()
	if start.out.Header != nil {
		start.out.At = start.out.Header.At
	} else if len(start.out.Entries) > 0 {
		start.out.At = start.out.Entries[0].At
	}
	if start.out.Header != nil || len(start.out.Entries) > 0 {
		rounds = append([]*round{start}, rounds...)
	}
	if latest := latestRound(rounds); latest != nil {
		r.live(latest)
	}
	return rounds
}

func (r *reader) zone() *time.Location {
	if r.in.Loc != nil {
		return r.in.Loc
	}
	return time.UTC
}

// latestRound is the round a run line is live in: the latest round that is not a Background one.
func latestRound(rounds []*round) *round {
	for i := len(rounds) - 1; i >= 0; i-- {
		if rounds[i].out.Kind != assistantwire.TodoRoundKindBackground {
			return rounds[i]
		}
	}
	return nil
}

// PaperRounds is how many rounds the paper shows (proposal 3.5 row 10).
const PaperRounds = 2

// Latest are the n rounds the paper shows of a timeline's rounds (oldest first): the latest n that are not Background
// ones, where the work and the owner's asks are; with fewer, the latest Background ones make up the rest. Oldest
// first, as in the timeline. A Background round of today can begin after the round that holds the open ask, which the
// paper must not lose.
func Latest(rounds []assistantwire.TodoRound, n int) []assistantwire.TodoRound {
	take := make([]bool, len(rounds))
	left := n
	for _, background := range []bool{false, true} {
		for i := len(rounds) - 1; i >= 0 && left > 0; i-- {
			if (rounds[i].Kind == assistantwire.TodoRoundKindBackground) == background {
				take[i], left = true, left-1
			}
		}
	}
	var out []assistantwire.TodoRound
	for i, rd := range rounds {
		if take[i] {
			out = append(out, rd)
		}
	}
	return out
}

// runLineID is the id of run key's line in round (R4); an unnamed run's key is "?".
func runLineID(key, round string) string { return "run:" + cmp.Or(key, "?") + "@" + round }

// collapse writes the round's entries (R4): its items but the header, oldest first, each run's turns as one line at
// its latest turn: that turn's summary, or interrupted when it was cut off, and how many turns there were.
func (rd *round) collapse() {
	out := make([]assistantwire.TodoTimelineEntry, 0, len(rd.items))
	lines := map[string]int{}
	for _, it := range rd.items {
		switch {
		case rd.out.Header == &it.e:
			continue
		case it.turn == nil:
			out = append(out, it.e)
			continue
		}
		e := it.e
		e.ID, e.Turns = runLineID(it.turn.key, rd.out.ID), 1
		if it.turn.interrupted {
			e.Line = line(lineInterrupted, "run", title(it.turn.run))
		}
		if i, ok := lines[it.turn.key]; ok {
			e.Turns += out[i].Turns
			out[i] = e
			continue
		}
		lines[it.turn.key] = len(out)
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b assistantwire.TodoTimelineEntry) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID))
	})
	rd.out.Entries = out
}

// live marks, in round rd, the line of every run that works now (its session's state is working): live, saying what
// it does now when its session says (its activity, else the first line of its latest text), and no longer interrupted.
// A run that has had no turn in the round yet gets a line of its own, from when it has worked (not before the round
// began).
func (r *reader) live(rd *round) {
	for _, a := range r.in.Todo.Agents {
		if a.ReleasedAt != nil || a.StartError != "" {
			continue
		}
		s := r.session(a.MachineID, a.SessionID)
		if s == nil || s.State != proto.StateWorking {
			continue
		}
		key, run := runKey(a.MachineID, a.SessionID), r.ref(a.MachineID, a.SessionID)
		text := s.Activity
		if text == "" {
			text = firstLine(s.LastText)
		}
		id := runLineID(key, rd.out.ID)
		if i := slices.IndexFunc(rd.out.Entries, func(e assistantwire.TodoTimelineEntry) bool { return e.ID == id }); i >= 0 {
			e := &rd.out.Entries[i]
			e.Type, e.Line, e.Live, e.Text = assistantwire.TodoEntryTypeRun, nil, true, cmp.Or(text, e.Text)
			continue
		}
		since := s.StateSince
		if since.IsZero() {
			since = s.UpdatedAt
		}
		if since.Before(rd.out.At) {
			since = rd.out.At
		}
		rd.out.Entries = append(rd.out.Entries, assistantwire.TodoTimelineEntry{ID: id, Type: assistantwire.TodoEntryTypeRun,
			At: since.UTC(), Actor: assistantwire.TodoActor{Kind: assistantwire.TodoActorKindRun, Run: run}, Run: run,
			Text: text, Live: true})
	}
	slices.SortFunc(rd.out.Entries, func(a, b assistantwire.TodoTimelineEntry) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID))
	})
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
