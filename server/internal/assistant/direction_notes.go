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

// setDirectionNotes keeps notes on direction id (a leader now or before); "" clears them. Only that leader's policy
// changes: its process restarts on its next message (the node compares the policy), no other conversation's.
func (s *Service) setDirectionNotes(ctx context.Context, id, notes string) (string, error) {
	notes = cleanDirectionNotes(notes)
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return "", errors.New("the directions could not be read")
	}
	d := b.m[id]
	if d == nil {
		b.mu.Unlock()
		return "", errNoDirection
	}
	if d.Notes == notes {
		b.mu.Unlock()
		return notes, nil
	}
	d.Notes = notes
	if notes == "" {
		d.NotesAt = nil
	} else {
		now := time.Now().UTC()
		d.NotesAt = &now
	}
	s.saveDirectionsLocked(ctx)
	b.mu.Unlock()
	s.directionsSoon()
	return notes, nil
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
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		notes, err := s.setDirectionNotes(r.Context(), r.PathValue("id"), in.Notes)
		switch {
		case errors.Is(err, errNoDirection):
			apierr.Write(w, http.StatusNotFound, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"notes": notes})
	})
}

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
// direction's block after them. Not a leader, or no notes: the policy as it is.
func (s *Service) leaderPolicy(ctx context.Context, id, policy string) string {
	if policy == "" || id == "" || !slices.Contains(s.settings(ctx).Leaders, id) {
		return policy
	}
	if block := prompts.RenderDirectionNotes(s.directionPrompt(ctx, id)); block != "" {
		return policy + "\n\n" + block
	}
	return policy
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
