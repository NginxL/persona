package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A direction's notes (#10086; docs/assistant/unified-entry.md "Direction notes"): what the owner writes for one
// direction (its rules, the readings that prove progress, its terms), kept on the direction's record.
//
// They reach two places, and nothing else:
//   - the leader conversation itself, appended to its role policy (supervision_instructions, which the node passes as
//     --append-system-prompt or a Codex thread's developer instructions). The node keeps that policy with the
//     conversation and compares it on every send: a change restarts that one conversation's process, and a resume or
//     a compaction keeps it. The shared CLAUDE.md and every other conversation's prompt cache are untouched. With the
//     notes come the filed project's name and goal, which change rarely; the project's one-line status, which a
//     summary rewrite changes, goes into the per-message context instead (context.go), so it never restarts a leader.
//     A change reaches the policy only on a send that finds the leader idle (notesInEffect, #11392): the node refuses
//     to restart a process at work, and the owner's message was lost with that refusal.
//   - a new Agent started for the direction (a task the leader supervises, an agent_start from the leader, or an
//     Agent working in the direction's project): a block beside the project background before its first prompt.

// directionNotesMost is how long a direction's notes may be, in characters: the system prompt of every turn of the
// leader carries them.
const directionNotesMost = 4000

// directionNotesClose is a closing tag of the block the notes sit in, which the notes may not write.
var directionNotesClose = lazyre.New(`(?i)<\s*/\s*direction-notes\s*>`)

// cleanDirectionNotes trims the notes, keeps their lines (a list stays a list), drops what would close their block,
// and clips them to directionNotesMost.
func cleanDirectionNotes(notes string) string {
	notes = strings.ReplaceAll(notes, "\r\n", "\n")
	notes = directionNotesClose.ReplaceAllString(notes, "")
	return strings.TrimSpace(clip(strings.TrimSpace(notes), directionNotesMost-1)) // clip adds "…"
}

// notesFrom is the notes_at a save started from (At nil: there were no notes).
type notesFrom struct{ At *time.Time }

// notesChanged refuses a save that started from notes the direction no longer has (#11392): Notes and At are what it
// has now, for the editor to show beside its own.
type notesChanged struct {
	Notes string
	At    *time.Time
}

func (e *notesChanged) Error() string { return "the direction's notes changed since they were read" }

// sameNotesAt: a and b (nil: no notes) are the same instant to the millisecond, so a value a client parsed and wrote
// again at millisecond precision matches the one it read.
func sameNotesAt(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

// nextNotesAt is when notes saved at now are written: now at millisecond precision, and a millisecond after prev (the
// notes' time before) when that is not later, so two saves never share a value.
func nextNotesAt(prev *time.Time, now time.Time) time.Time {
	at := now.UTC().Truncate(time.Millisecond)
	if prev != nil && !at.After(prev.Truncate(time.Millisecond)) {
		at = prev.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	return at
}

// setDirectionNotes keeps notes on direction id (a leader now or before); "" clears them, and answers them as kept with
// their time. from, when the editor said what it started from, must still be the notes' time, else nothing changes and
// *notesChanged says what is there now. Only that leader's policy changes, on the first message that finds it idle
// (notesInEffect), no other conversation's.
func (s *Service) setDirectionNotes(ctx context.Context, id, notes string, from *notesFrom) (string, *time.Time, error) {
	return s.setDirectionNotesMode(ctx, id, notes, from, false)
}

// setCurrentDirectionNotes is the assistant-approved form: unlike the app route, it also refuses a direction that is
// no longer current. Membership, notes_at and the write are checked under the same direction lock.
func (s *Service) setCurrentDirectionNotes(ctx context.Context, id, notes string, from *notesFrom) (string, *time.Time, error) {
	return s.setDirectionNotesMode(ctx, id, notes, from, true)
}

func (s *Service) setDirectionNotesMode(ctx context.Context, id, notes string, from *notesFrom, requireCurrent bool) (string, *time.Time, error) {
	if requireCurrent {
		// Every Leaders update goes through updateSettings (prefs.change). Hold it across the membership check and the
		// notes write so ending or remaking the direction cannot slip between them and produce a false success.
		s.prefs.change.Lock()
		defer s.prefs.change.Unlock()
	}
	notes = cleanDirectionNotes(notes)
	// What a current leader was told of its direction before this save: the baseline notesInEffect keeps while it
	// works when no send has recorded one yet (a leader from before #11392, or one never sent to since). Read before
	// the lock, since directionPrompt reads the record itself, and used only if the notes are still the ones it read.
	var before *prompts.DirectionNotes
	if slices.Contains(s.settings(ctx).Leaders, id) {
		p := s.directionPrompt(ctx, id)
		before = &p
	}
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return "", nil, errors.New("the directions could not be read")
	}
	d := b.m[id]
	if d == nil {
		b.mu.Unlock()
		return "", nil, errNoDirection
	}
	if requireCurrent && !slices.Contains(s.settings(ctx).Leaders, id) {
		b.mu.Unlock()
		return "", nil, errNoDirection
	}
	if from != nil && !sameNotesAt(d.NotesAt, from.At) {
		changed := &notesChanged{Notes: d.Notes, At: d.NotesAt}
		b.mu.Unlock()
		return "", nil, changed
	}
	if d.Notes == notes {
		at := d.NotesAt
		b.mu.Unlock()
		return notes, at, nil
	}
	if d.NotesInEffect == nil && before != nil && before.Notes == d.Notes {
		// Until this save every send rendered the notes as they were, so that is what the leader runs on.
		block := prompts.RenderDirectionNotes(*before)
		d.NotesInEffect = &block
	}
	d.Notes = notes
	if d.NotesLastAt == nil {
		d.NotesLastAt = d.NotesAt // an older record
	}
	if notes == "" {
		d.NotesAt = nil // NotesLastAt stays: the next save comes after it
	} else {
		at := nextNotesAt(d.NotesLastAt, b.clock())
		d.NotesAt, d.NotesLastAt = &at, &at
	}
	at := d.NotesAt
	s.saveDirectionsLocked(ctx)
	b.mu.Unlock()
	s.directionsSoon()
	return notes, at, nil
}

// directionNotesRoutes: the owner writes a direction's notes. GET /api/v1/assistant/leaders reads them.
func (s *Service) directionNotesRoutes() {
	s.handle("PUT /api/v1/assistant/directions/{id}/notes", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
			return
		}
		var in struct {
			Notes string `json:"notes"`
			// NotesAt: the notes_at the editor started from, as GET /api/v1/assistant/leaders gave it (null: there
			// were none); left out, the save is not checked.
			NotesAt json.RawMessage `json:"notes_at"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var from *notesFrom
		if len(in.NotesAt) > 0 {
			from = &notesFrom{}
			if err := json.Unmarshal(in.NotesAt, &from.At); err != nil {
				apierr.Write(w, http.StatusBadRequest, errors.New("notes_at must be a time (RFC 3339) or null"))
				return
			}
		}
		notes, at, err := s.setDirectionNotes(r.Context(), r.PathValue("id"), in.Notes, from)
		var changed *notesChanged
		switch {
		case errors.Is(err, errNoDirection):
			apierr.Write(w, http.StatusNotFound, err)
			return
		case errors.As(err, &changed):
			body := apierr.LocalizedBody(http.StatusConflict, codeNotesChanged, i18n.Err("assistant-direction_notes.changed"), i18n.OfWriter(w))
			body["notes"], body["notes_at"] = changed.Notes, changed.At
			apierr.WriteBody(w, http.StatusConflict, body)
			return
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"notes": notes, "notes_at": at})
	})
}

// codeNotesChanged is the 409 of a notes save that started from notes the direction no longer has.
const codeNotesChanged = "notes_changed"

// directionPrompt is what direction id is told of itself: its name, notes and filed project's name and goal (the
// knowledge base unread: no project line; the notes still go).
func (s *Service) directionPrompt(ctx context.Context, id string) prompts.DirectionNotes {
	out := prompts.DirectionNotes{Lang: directionLang(s.lang(ctx))}
	d := s.directionOf(id)
	if d == nil {
		return out
	}
	out.Notes = d.Notes
	out.Name = s.leaderNames(ctx, []string{id})[id]
	if d.Project != "" {
		if e, err := s.kbGet(ctx); err == nil && e != nil {
			for _, p := range e.Store.Projects() {
				if p.Slug == d.Project {
					out.ProjectName, out.ProjectGoal = oneLine(p.Name), clip(oneLine(p.Goal), 300)
					break
				}
			}
		}
	}
	if out.Notes == "" {
		// Without the owner's notes a project's goal alone is not added: a leader without notes keeps its policy as
		// it was before notes existed, and a project being rewritten never restarts it.
		out.ProjectGoal = ""
	}
	return out
}

// directionLang is the template's language: "" for Chinese (the prompts' original language), else the code.
func directionLang(l i18n.Lang) string {
	switch l {
	case i18n.En, i18n.Ja:
		return string(l)
	}
	return ""
}

// leaderPolicy is the role policy of leader id: the supervision instructions and, when the owner wrote notes, the
// direction's block after them, as notesInEffect has it for a send that finds the leader idle or not. Not a leader, or
// no notes: the policy as it is.
func (s *Service) leaderPolicy(ctx context.Context, id, policy string, idle bool) string {
	if policy == "" || id == "" {
		return policy
	}
	if !slices.Contains(s.settings(ctx).Leaders, id) {
		// No leader now (an ended direction that still supervises a task: one handed out since it ended, or one its
		// hand-back to main could not move, handBackDirectionTasks): no notes block. A send that finds it idle records
		// that, so a direction made again starts from what the conversation runs on, not from the block it had before
		// it ended (#11392).
		s.notesInEffect(ctx, id, "", idle)
		return policy
	}
	if block := s.notesInEffect(ctx, id, prompts.RenderDirectionNotes(s.directionPrompt(ctx, id)), idle); block != "" {
		return policy + "\n\n" + block
	}
	return policy
}

// notesInEffect is the notes block conversation id's policy carries on this send (#11392). A send that finds it idle
// takes block (a leader's notes as they are now; "" for a conversation that is no leader) and keeps it with the
// direction's record as the block in effect; any other send takes the block in effect, so notes saved while the
// leader works wait for its first idle send instead of changing the policy of a process at work (the node refuses
// that send, and the owner's message was not kept). Kept in the record, it outlives a manager restart. A record
// without one yet takes block: no send recorded one and no notes were saved since (setDirectionNotes sets the
// baseline), so block is what every send gave it.
//
// Only a send whose outcome its caller reports moves the block: one whose ctx holds a notesCarry (withNotesCarry),
// which this fills in. Any other send takes the last confirmed block, not one a send still in flight moved, which may
// yet be put back while this send's process runs it: the node's own request for what its message runs on
// (assistant.model), and the parameters the manager makes before it sends them through call, which takes them again
// with a carry of its own.
//
// A carry whose policy step already ran (the hub's retry of the owner's message while the first attempt still waits
// for its notice, notesOwnerCarry) moves nothing. While that attempt's move is in flight it carries the newest block in
// flight, the block in effect: that move's own, so the one notice settles the block that went, unless a later message
// moved the block again, whose block the lead runs if that message went in; else the last confirmed block, as a send
// no one reports.
func (s *Service) notesInEffect(ctx context.Context, id, block string, idle bool) string {
	carry, _ := ctx.Value(notesCarryKey{}).(*notesCarry)
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.directionsLocked(ctx) {
		return block
	}
	d := b.m[id]
	if d == nil {
		return block
	}
	s.notesOwnerExpireLocked(ctx, id)
	if carry != nil && carry.id != "" {
		if f := b.inflight[id]; carry.id == id && carry.move != nil && f != nil && slices.Contains(f.moved, carry.move) {
			return f.moved[len(f.moved)-1].block
		}
		carry = nil
	}
	switch {
	case carry == nil:
		if given := b.inflight[id].confirmed(); given != nil {
			return *given
		}
		if d.NotesInEffect != nil {
			return *d.NotesInEffect
		}
		return block
	case !idle:
		if d.NotesInEffect != nil {
			block = *d.NotesInEffect
		}
	case d.NotesInEffect == nil || *d.NotesInEffect != block:
		f := b.flight(id)
		f.moved = append(f.moved, &notesMove{prev: d.NotesInEffect, block: block})
		if len(f.moved) > notesInFlightMost {
			f.moved = f.moved[1:] // its outcome no longer counts: its block is kept
		}
		given := block
		d.NotesInEffect = &given
		s.saveDirectionsLocked(ctx)
	}
	carry.id, carry.move = id, b.inflight[id].carrying(block)
	return block
}

// notesFlight is what is in flight for one conversation's notes block: the moves of the block in effect whose sends'
// outcomes are not all known, and the owner's messages the hub has yet to report, each oldest first.
type notesFlight struct {
	moved []*notesMove
	owner []notesOwnerSend
}

// notesMove is a send's move of the block in effect from prev (nil: none recorded) to block. carriers are the sends in
// flight that carry block: the one that moved it, and any later one that took the same block.
type notesMove struct {
	prev     *string
	block    string
	carriers int
}

// notesCarry is one send's part in its conversation's notes block (withNotesCarry): the conversation its policy step
// ran for ("" until it ran for one with a direction record), the move in flight whose block it carries (nil: none,
// or settled), and whether it is the owner's message waiting for the hub's notice (notesOwnerSent).
type notesCarry struct {
	id    string
	move  *notesMove
	owner bool
}

// notesOwnerSend is the owner's message msg (its client_msg_id; "" none) the hub was given parameters for at at, until
// its notice comes.
type notesOwnerSend struct {
	carry *notesCarry
	msg   string
	at    time.Time
}

type notesCarryKey struct{}

// withNotesCarry is ctx for one send whose outcome its caller reports with notesSendOutcome (the manager's own sends,
// call) or has the hub report (the owner's messages, notesOwnerSent): its policy step may move the block in effect.
func withNotesCarry(ctx context.Context) (context.Context, *notesCarry) {
	c := &notesCarry{}
	return context.WithValue(ctx, notesCarryKey{}, c), c
}

// notesInFlightMost is how many moves, and how many of the owner's messages, one conversation keeps in flight; the
// oldest beyond it is dropped as if its outcome were never learned.
const notesInFlightMost = 8

// notesSeenMost is how many of the owner's latest message ids the manager keeps per conversation, to know a retry of
// one it already gave parameters for (notesOwnerCarry).
const notesSeenMost = 64

// notesOwnerWait is how long the owner's message waits for the hub's notice: longer than the hub waits for any send to
// go in (its sendWait). One whose notice has not come by then gets none (the hub did not send it after all, it stopped
// while sending it, or the notice was lost while the manager was away), and counts as an outcome never learned.
const notesOwnerWait = 10 * time.Minute

// flight is conversation id's flight, made if it has none; b.mu is held.
func (b *directionBook) flight(id string) *notesFlight {
	if b.inflight == nil {
		b.inflight = map[string]*notesFlight{}
	}
	f := b.inflight[id]
	if f == nil {
		f = &notesFlight{}
		b.inflight[id] = f
	}
	return f
}

// tidyFlight drops conversation id's flight once nothing is in it; b.mu is held.
func (b *directionBook) tidyFlight(id string) {
	if f := b.inflight[id]; f != nil && len(f.moved) == 0 && len(f.owner) == 0 {
		delete(b.inflight, id)
	}
}

// carrying is the newest move in flight to block, now carried by one more send; nil when none is.
func (f *notesFlight) carrying(block string) *notesMove {
	if f == nil {
		return nil
	}
	for i := len(f.moved) - 1; i >= 0; i-- {
		if m := f.moved[i]; m.block == block {
			m.carriers++
			return m
		}
	}
	return nil
}

// confirmed is the last confirmed block: the one before the oldest move in flight (nil: none in flight). With none
// recorded before that move, it is the move's own block, which every send before it gave the conversation.
func (f *notesFlight) confirmed() *string {
	if f == nil || len(f.moved) == 0 {
		return nil
	}
	if m := f.moved[0]; m.prev != nil {
		return m.prev
	}
	return &f.moved[0].block
}

// notesSendOutcome settles carry's send once its outcome is known (#11392); a send that never left counts as not
// taken. Taken by the node, the block it carried is what the process runs: its move, and every move before it, stay.
// Not taken (the node refused it, as it refuses a new policy for a process it already sees at work while the
// manager's session row still read idle, or it failed before reaching the conversation), its move counts for nothing
// once no other send in flight carries the block: when no later move is in flight the block before it is put back, so
// the next messages of the same turn are not refused too; else the later move counts from that block.
func (s *Service) notesSendOutcome(ctx context.Context, carry *notesCarry, taken bool) {
	if carry == nil || carry.move == nil {
		return
	}
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	s.notesSettleLocked(ctx, carry, taken)
}

// notesSettleLocked is notesSendOutcome with b.mu held.
func (s *Service) notesSettleLocked(ctx context.Context, carry *notesCarry, taken bool) {
	if carry == nil || carry.move == nil {
		return
	}
	b := &s.directions
	m, id := carry.move, carry.id
	carry.move = nil
	f := b.inflight[id]
	i := -1
	if f != nil {
		i = slices.Index(f.moved, m)
	}
	switch {
	case i < 0: // a later move was taken, or it was dropped
		return
	case taken:
		f.moved = f.moved[i+1:]
	case m.carriers > 1:
		m.carriers--
		return
	default:
		f.moved = slices.Delete(f.moved, i, i+1)
		if i < len(f.moved) {
			f.moved[i].prev = m.prev
		} else if d := b.m[id]; b.loaded && d != nil && d.NotesInEffect != nil && *d.NotesInEffect == m.block {
			d.NotesInEffect = m.prev
			s.saveDirectionsLocked(ctx)
		}
	}
	b.tidyFlight(id)
}

// notesOwnerCarry is ctx for the hub's request for the parameters of the owner's message msg (its client_msg_id; ""
// none) to conversation id (assistant.send_params, bus.go), and the carry its policy step fills in (nil: none). A
// message the manager already gave parameters for is the hub's retry, which moves nothing. While the first attempt
// still waits for its notice, the retry is tied to it: it carries the block that attempt carried while that move is in
// flight (notesInEffect), and the one notice settles it, for the hub sends the retry only when the first attempt
// reached nothing, and else answers it from its record without a notice. Once nothing waits for it (settled, or past
// notesOwnerWait), the retry has no carry: it takes the last confirmed block.
func (s *Service) notesOwnerCarry(ctx context.Context, id, msg string) (context.Context, *notesCarry) {
	if msg != "" {
		b := &s.directions
		b.mu.Lock()
		s.notesOwnerExpireLocked(ctx, id)
		var waiting *notesCarry
		if f := b.inflight[id]; f != nil {
			if i := slices.IndexFunc(f.owner, func(o notesOwnerSend) bool { return o.msg == msg }); i >= 0 {
				waiting = f.owner[i].carry
			}
		}
		seen := slices.Contains(b.ownerSeen[id], msg)
		b.mu.Unlock()
		switch {
		case waiting != nil:
			return context.WithValue(ctx, notesCarryKey{}, waiting), waiting
		case seen:
			return ctx, nil
		}
	}
	return withNotesCarry(ctx)
}

// notesOwnerRefused: the parameters made with carry (notesOwnerCarry) were refused, and the hub sends nothing. A new
// message's move counts as not taken; a retry's leaves its first attempt waiting for that attempt's notice.
func (s *Service) notesOwnerRefused(ctx context.Context, carry *notesCarry) {
	if carry == nil {
		return
	}
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	if !carry.owner {
		s.notesSettleLocked(ctx, carry, false)
	}
}

// notesOwnerSent: the hub sends the owner's message msg (its client_msg_id; "" none) whose parameters were made with
// carry (notesOwnerCarry, SendParams), and tells how it went by its notice (ConversationSent, notesOwnerOutcome). A
// retry tied to its first attempt waits for that notice from now, as the newest message: the hub waits for the retry
// as long as for any send.
func (s *Service) notesOwnerSent(ctx context.Context, carry *notesCarry, msg string) {
	if carry == nil || carry.id == "" {
		return
	}
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	s.notesOwnerExpireLocked(ctx, carry.id)
	f := b.flight(carry.id)
	if carry.owner {
		if i := slices.IndexFunc(f.owner, func(o notesOwnerSend) bool { return o.carry == carry }); i >= 0 {
			o := f.owner[i]
			o.at = b.clock()
			f.owner = append(slices.Delete(f.owner, i, i+1), o) // oldest first, as notesOwnerExpireLocked reads them
		}
		b.tidyFlight(carry.id)
		return
	}
	carry.owner = true
	f.owner = append(f.owner, notesOwnerSend{carry: carry, msg: msg, at: b.clock()})
	if len(f.owner) > notesInFlightMost {
		s.notesSettleLocked(ctx, f.owner[0].carry, true) // as if its outcome were never learned: its block is kept
		f.owner = f.owner[1:]
	}
	if msg != "" {
		if b.ownerSeen == nil {
			b.ownerSeen = map[string][]string{}
		}
		seen := append(b.ownerSeen[carry.id], msg)
		if len(seen) > notesSeenMost {
			seen = seen[len(seen)-notesSeenMost:]
		}
		b.ownerSeen[carry.id] = seen
	}
}

// notesOwnerOutcome settles, by the hub's notice for conversation id, the owner's message msg (its client_msg_id) it
// names, and only that one. A notice that names none settles the oldest message without an id still waiting for its
// notice. Only while the hub has not shown, on the connection the notice came on (hubConnOf), that it names messages
// (an older hub names none) does such a notice settle the oldest message waiting when none without an id waits: a hub
// that names messages sends a notice without an id only for a message sent without one, such as one sent just before
// the manager restarted. A new connection may be to another version of the hub, so it shows that anew.
func (s *Service) notesOwnerOutcome(ctx context.Context, id, msg string, taken bool) {
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	conn := hubConnOf(ctx)
	if msg != "" {
		b.namedOn = max(b.namedOn, conn+1) // a notice of an older connection handled late shows nothing of a newer one
	}
	s.notesOwnerExpireLocked(ctx, id)
	f := b.inflight[id]
	if f == nil {
		return
	}
	i := slices.IndexFunc(f.owner, func(o notesOwnerSend) bool { return o.msg == msg })
	if i < 0 && msg == "" && len(f.owner) > 0 && b.namedOn != conn+1 {
		i = 0
	}
	if i < 0 {
		return
	}
	carry := f.owner[i].carry
	f.owner = slices.Delete(f.owner, i, i+1)
	s.notesSettleLocked(ctx, carry, taken)
	b.tidyFlight(id)
}

// notesOwnerExpireLocked settles the owner's messages to conversation id whose notice can no longer come
// (notesOwnerWait) as outcomes never learned: the block each carried is kept. b.mu is held.
func (s *Service) notesOwnerExpireLocked(ctx context.Context, id string) {
	b := &s.directions
	f := b.inflight[id]
	if f == nil {
		return
	}
	now := b.clock()
	i := 0
	for i < len(f.owner) && now.Sub(f.owner[i].at) >= notesOwnerWait {
		s.notesSettleLocked(ctx, f.owner[i].carry, true)
		i++
	}
	f.owner = f.owner[i:]
	b.tidyFlight(id)
}

// leadIdle: a send now finds leader conversation c idle, as its computer lists it: not starting, working, waiting on
// the owner, compacting or running background work. One its computer does not list (known false) has no process the
// manager knows of, and a send starts one on the policy it is given. The node can see work a moment before this does;
// it still refuses a new role for a process at work (node/assistant.go), the safety net for that moment.
func leadIdle(c proto.Session, known bool) bool {
	if !known {
		return true
	}
	return !c.Starting && c.Status != proto.StatusWorking && c.Status != proto.StatusBlocked && c.PendingInteractions == 0 &&
		(c.Compaction == nil || c.Compaction.State != proto.CompactionRunning) && !backgroundRunning(c)
}

// directionContext is the block a new Agent gets for the directions its work belongs to: the direction of lead (the
// leader conversation supervising or starting it; "" none) and those filed under one of the projects slugs. Only
// directions with notes, current ones, each once, the lead's first; at most projectContextMost of them.
func (s *Service) directionContext(ctx context.Context, lead string, slugs []string) string {
	cfg := s.settings(ctx)
	var ids []string
	if lead != "" && slices.Contains(cfg.Leaders, lead) {
		ids = append(ids, lead)
	}
	if len(slugs) > 0 {
		b := &s.directions
		b.mu.Lock()
		if s.directionsLocked(ctx) {
			var more []string
			for id, d := range b.m {
				if d.EndedAt == nil && d.Notes != "" && d.Project != "" && slices.Contains(slugs, d.Project) && !slices.Contains(ids, id) {
					more = append(more, id)
				}
			}
			slices.Sort(more)
			ids = append(ids, more...)
		}
		b.mu.Unlock()
	}
	var blocks []string
	for _, id := range ids {
		if !slices.Contains(cfg.Leaders, id) {
			continue
		}
		if block := prompts.RenderTodoDirection(s.directionPrompt(ctx, id)); block != "" {
			blocks = append(blocks, block)
		}
		if len(blocks) == projectContextMost {
			break
		}
	}
	return strings.Join(blocks, "\n\n")
}

// directionStatus is the one-line status of the project leader id's direction is filed under, for its per-message
// context; "" when the direction has no notes (a leader without them sees its context as before), no project, or the
// knowledge base cannot be read.
func (s *Service) directionStatus(ctx context.Context, id string) string {
	d := s.directionOf(id)
	if d == nil || d.Notes == "" || d.Project == "" {
		return ""
	}
	e, err := s.kbGet(ctx)
	if err != nil || e == nil {
		return ""
	}
	for _, p := range e.Store.Projects() {
		if p.Slug == d.Project {
			return clip(oneLine(p.OneLine), 300)
		}
	}
	return ""
}
