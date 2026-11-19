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
	notes = cleanDirectionNotes(notes)
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
	d.Notes = notes
	if notes == "" {
		d.NotesAt = nil
	} else {
		at := nextNotesAt(d.NotesAt, time.Now())
		d.NotesAt = &at
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
	if policy == "" || id == "" || !slices.Contains(s.settings(ctx).Leaders, id) {
		return policy
	}
	if block := s.notesInEffect(ctx, id, prompts.RenderDirectionNotes(s.directionPrompt(ctx, id)), idle); block != "" {
		return policy + "\n\n" + block
	}
	return policy
}

// notesInEffect is the notes block leader id's policy carries on this send (#11392). A send that finds the leader idle
// takes block, the notes as they are now, and keeps it with the direction's record as the block in effect; any other
// send takes the block in effect, so notes saved while the leader works wait for its first idle send instead of
// changing the policy of a process at work (the node refuses that send, and the owner's message was not kept). Kept
// in the record, it outlives a manager restart. A record without one yet (from before, or none) takes block.
func (s *Service) notesInEffect(ctx context.Context, id, block string, idle bool) string {
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
	if !idle {
		if d.NotesInEffect != nil {
			return *d.NotesInEffect
		}
		return block
	}
	if d.NotesInEffect == nil || *d.NotesInEffect != block {
		d.NotesInEffect = &block
		s.saveDirectionsLocked(ctx)
	}
	return block
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
