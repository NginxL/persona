package assistant

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// GET /api/v1/interactions/history (docs/protocol/protocol.md, docs/extensions/extensions.md "Question history"): the
// cards that were raised and how each ended, plus decisions recorded directly as resolved (reversible-setting
// auto-approval), newest first. Without since, before or kind it is the newest ≤500 as before; with any of them it pages
// back by time: a full page carries next_before / next_before_id, the cursor of the next older page (pass them as before
// and before_id).
//
// An extension's token (agents.read) is never shown a private card (a Just me turn's), a card of a space hidden from
// extensions, or one whose space cannot be told; the values filled into a card's fields (a password a permission asked for) are left
// out of its resolution.

// historyRecord is a record as the history answers it: Reanswerable says the user may still change the answer (one
// the assistant gave on their behalf: POST /api/v1/interactions/{id}/reanswer).
type historyRecord struct {
	db.InteractionRecord
	Reanswerable bool `json:"reanswerable,omitempty"`
}

func (s *Service) serveInteractionHistory(w http.ResponseWriter, r *http.Request) {
	st := s.fleetStore()
	if st == nil {
		apierr.WriteJSON(w, map[string]any{"interactions": []any{}})
		return
	}
	q := r.URL.Query()
	mid := q.Get("machine_id")
	if m, ok := s.fleetMachine(mid); ok {
		mid = m.ID
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out := map[string]any{}
	var recs []db.InteractionRecord
	if q.Has("since") || q.Has("before") || q.Has("kind") {
		query := db.InteractionQuery{Machine: mid, Session: q.Get("session_id"), Kind: q.Get("kind"), BeforeID: q.Get("before_id"), Limit: limit}
		var err error
		if query.Since, err = historyTime(q.Get("since")); err != nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("since must be an RFC 3339 time"))
			return
		}
		if query.Before, err = historyTime(q.Get("before")); err != nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("before must be an RFC 3339 time"))
			return
		}
		if query.BeforeID != "" && query.Before.IsZero() {
			apierr.Write(w, http.StatusBadRequest, errors.New("before_id goes with before"))
			return
		}
		pager, ok := st.(interactionPager) // guardedStore: an error from a store that does not page
		if !ok {
			apierr.Write(w, http.StatusInternalServerError, errNoTable)
			return
		}
		page, err := pager.InteractionsPage(r.Context(), query)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		recs = page.Records
		if page.NextBefore != nil { // from the rows read, before any is left out below: the next page starts after them
			out["next_before"], out["next_before_id"] = page.NextBefore.Format(time.RFC3339Nano), page.NextBeforeID
		}
	} else {
		var err error
		if recs, err = st.Interactions(r.Context(), mid, q.Get("session_id"), limit); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
	}
	ext := r.Header.Get(RoleHeader) == roleExtension
	elicitation := r.Header.Get(assistantwire.ElicitationHeader) == "1"
	hidden := s.extensionHiddenCards(r.Context(), ext, recs)
	list := make([]historyRecord, 0, len(recs))
	for i, rec := range recs {
		if rec.Interaction.Kind == proto.IxElicitation && !elicitation || hidden[i] {
			continue
		}
		if ext && rec.Resolution != nil && rec.Resolution.Values != nil {
			res := *rec.Resolution
			res.Values = nil
			rec.Resolution = &res
		}
		list = append(list, historyRecord{InteractionRecord: rec, Reanswerable: reanswerable(rec)})
	}
	out["interactions"] = list
	apierr.WriteJSON(w, out)
}

// historyTime reads an RFC 3339 time ("" is none).
func historyTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

// reanswerable: the assistant answered rec for the user, who may still give their own answer (reanswer).
func reanswerable(rec db.InteractionRecord) bool {
	return rec.Status == "resolved" && rec.Resolution != nil && rec.Resolution.AnsweredBy == proto.AnsweredByAssistant &&
		len(rec.Interaction.Questions) > 0
}

// extensionHiddenCards says which of recs an extension's token is not shown (none for anyone else): a private card, a
// card of a space hidden from extensions by its directory or its conversation's space, and a card without a directory
// whose conversation cannot be found (its machine's list unreadable, or the conversation not in it).
func (s *Service) extensionHiddenCards(ctx context.Context, ext bool, recs []db.InteractionRecord) []bool {
	hidden := make([]bool, len(recs))
	if !ext {
		return hidden
	}
	type listed struct {
		hide map[string]bool // session id → in a space hidden from extensions
		ok   bool
	}
	machines := map[string]*listed{}
	for i, rec := range recs {
		ix := rec.Interaction
		if ix.Private || s.extensionHides(ctx, identity.SpaceAt(ix.Cwd)) {
			hidden[i] = true
			continue
		}
		l := machines[ix.MachineID]
		if l == nil {
			l = &listed{hide: map[string]bool{}}
			if list, err := s.sessions(ctx, ix.MachineID); err == nil {
				l.ok = true
				for _, c := range list {
					l.hide[c.SessionID] = s.extensionHides(ctx, spaceOf(c)) ||
						s.extensionHides(ctx, identity.SpaceAt(c.Cwd))
				}
			}
			machines[ix.MachineID] = l
		}
		hide, known := l.hide[ix.SessionID]
		hidden[i] = hide || ix.Cwd == "" && (!l.ok || !known)
	}
	return hidden
}
