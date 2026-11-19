package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"time"
)

// A write whose row failed must not leave memory ahead of the database (#4366): a restart would read the old row,
// and until then the apps and the AI twin would act on a state that was never kept. inboxWrite snapshots an item
// before a change and, when its row could not be written, gives it back exactly what the row still holds, but only
// while nothing else moved the item since the change (its stored form and its mutation stamp are still the ones the
// change left). A concurrent move wins: its own save writes the row.

// inboxWrite is one item's change on its way to the database.
type inboxWrite struct {
	it      *InboxItem
	prev    []byte // the item's stored form before the change; nil for an item the change created
	told    int    // the agent's in-memory note count before the change
	applied []byte // the stored form the change left (settle); nil while a round's step is changing it (roundWrite)
	gen     uint64 // the mutation stamp settle took
	undone  bool   // its row failed and the change was undone (saveWrites)
}

// itemState is an item's stored form, leaving out the arrival stamp the save itself sets (arrived). Under b.mu.
func itemState(it *InboxItem) []byte {
	c := *it
	c.ArrivedOf, c.ArrivedAt = "", time.Time{}
	raw, _ := json.Marshal(&c)
	return raw
}

// beginWrite snapshots an item before a change; newWrite stands for an item the change creates. Under b.mu.
func beginWrite(it *InboxItem) *inboxWrite {
	w := &inboxWrite{it: it, prev: itemState(it)}
	if it.Agent != nil {
		w.told = it.Agent.told
	}
	return w
}

func newWrite(it *InboxItem) *inboxWrite { return &inboxWrite{it: it} }

// settle records what the change left and stamps the item. Under b.mu, right after the change.
func (b *inboxBook) settle(w *inboxWrite) {
	w.applied = itemState(w.it)
	w.gen = b.touched(w.it.ID)
}

// holds says the item is still the one this change left. Under b.mu.
func (b *inboxBook) holds(w *inboxWrite) bool {
	return b.items[w.it.ID] == w.it && b.mark[w.it.ID] == w.gen && bytes.Equal(itemState(w.it), w.applied)
}

// restore gives the item back what it held before the change; an item the change created is forgotten. Pointers whose value did not change are kept, as are the agent and handling records
// themselves (copied into), so code holding them (deliverNotes compares it.Agent) still finds them. Under b.mu.
func (b *inboxBook) restore(w *inboxWrite) {
	if w.prev == nil {
		delete(b.items, w.it.ID)
		return
	}
	var p InboxItem
	if err := json.Unmarshal(w.prev, &p); err != nil {
		return
	}
	cur, agent, handling := w.it, w.it.Agent, w.it.Handling
	cv, pv := reflect.ValueOf(cur).Elem(), reflect.ValueOf(&p).Elem()
	for i := range cv.NumField() {
		switch name := cv.Type().Field(i).Name; name {
		case "ArrivedOf", "ArrivedAt": // the save's own stamp, not the change's
		case "SuggestionAttempts":
			cur.SuggestionAttempts = restoreSuggestionAttempts(p.SuggestionAttempts, cur.SuggestionAttempts)
		default:
			if !reflect.DeepEqual(cv.Field(i).Interface(), pv.Field(i).Interface()) {
				cv.Field(i).Set(pv.Field(i))
			}
		}
	}
	if agent != nil && p.Agent != nil {
		*agent = *p.Agent
		cur.Agent = agent
	}
	if cur.Agent != nil {
		cur.Agent.told = w.told
	}
	if handling != nil && p.Handling != nil {
		*handling = *p.Handling
		cur.Handling = handling
	}
}

// A collection round (InboxCollected) is written the same way (#91). It changes items in steps, and some steps let
// b.mu go before they change anything (a mail's text and a connector's settling are asked for meanwhile), so each
// step takes the round's write of an item before it changes the item (roundWrite, roundNew) and settles those
// writes before it lets b.mu go (settleRound). A write that still holds at a later step is taken up again, its
// snapshot staying what the row held before the round; an item something else moved in between gets a new write
// from there, the other move saving what the round had done before. Undoing is safe because the sources hand the
// same things in again: a connector's Collect answers from what it holds, a mail round reads each account's newest
// page, a settling connector is asked every round. What is undone comes back with the next round.

// roundWrite is called before the running round changes an item, under b.mu; outside a round it does nothing.
func (b *inboxBook) roundWrite(it *InboxItem) {
	r := b.batch
	if r == nil {
		return
	}
	if w := r.writes[it.ID]; w != nil && w.it == it {
		if w.applied == nil { // this step changed it already
			return
		}
		if b.roundHolds(w) {
			w.applied = nil
			return
		}
	}
	r.writes[it.ID] = beginWrite(it)
}

// roundNew stands for an item the running round creates, under b.mu: undone, it is forgotten.
func (b *inboxBook) roundNew(it *InboxItem) {
	if r := b.batch; r != nil {
		r.writes[it.ID] = newWrite(it)
	}
}

// settleRound settles what the running round's step changed, under b.mu, before the step lets it go.
func (b *inboxBook) settleRound() {
	if r := b.batch; r != nil {
		for _, w := range r.writes {
			if w.applied == nil {
				b.settle(w)
			}
		}
	}
}

// roundHolds is holds for a write of the running round, whose new plugin items wait in its batch. Under b.mu.
func (b *inboxBook) roundHolds(w *inboxWrite) bool {
	if r := b.batch; r != nil && r.items[w.it.ID] == w.it {
		return b.mark[w.it.ID] == w.gen && bytes.Equal(itemState(w.it), w.applied)
	}
	return b.holds(w)
}

// roundMuted takes the filings of the round's mutes (applyMutes) into its writes, under b.mu, for its last step: an
// item the round changed already keeps the round's write, whose snapshot is from before both.
func (b *inboxBook) roundMuted(ws []*inboxWrite) []*InboxItem {
	r := b.batch
	out := make([]*InboxItem, 0, len(ws))
	for _, w := range ws {
		if cur := r.writes[w.it.ID]; cur == nil || cur.it != w.it || cur.applied != nil {
			w.applied = nil
			r.writes[w.it.ID] = w
		}
		out = append(out, w.it)
	}
	return out
}

// roundSaved is the running round's writes of the items it saves now, once settled; under b.mu. The new plugin
// items that wait for the model are admission's to write (admit).
func (b *inboxBook) roundSaved(changed []*InboxItem) []*inboxWrite {
	r := b.batch
	var ws []*inboxWrite
	taken := map[*inboxWrite]bool{}
	for _, it := range changed {
		if w := r.writes[it.ID]; w != nil && w.it == it && !taken[w] {
			taken[w] = true
			ws = append(ws, w)
		}
	}
	return ws
}

// saveWrites persists the items of settled writes and restores, inside the saving window (saveInboxUndo), each one
// whose row failed while it still holds what its change left; what names the change in the log. It returns the
// failed ids.
func (s *Service) saveWrites(ctx context.Context, ws []*inboxWrite, what string) map[string]bool {
	if len(ws) == 0 {
		return nil
	}
	return s.saveInboxUndo(ctx, itemsOf(ws), s.undoWrites(ws, what))
}

// undoWrites is saveInboxUndo's undo for writes saved along with other items.
func (s *Service) undoWrites(ws []*inboxWrite, what string) func(failed map[string]bool) {
	b := &s.inbox
	return func(failed map[string]bool) {
		for _, w := range ws {
			if failed[w.it.ID] && b.holds(w) {
				b.restore(w)
				w.undone = true
				s.logf("inbox: %s: %s could not be saved; it is undone and comes again on the next pass", w.it.ID, what)
			}
		}
	}
}
