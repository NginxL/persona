package assistant

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The briefing's quick-capture lines (#13502, docs/assistant/health.md "Quick capture"): how many captures wait to be
// sorted, then those filed since the window began, each by its title and track, at most captureBriefRows of them.
// Sensitive ones (a category, or not tagged) are only counted: "1 capture". None of it names a sensitive capture's
// content, track or title.

// captureBriefRows is the most filed captures a briefing names.
const captureBriefRows = 5

// captureBriefLines are the briefing's quick-capture lines in l, for a run whose window began at since; none when
// there is nothing to say (or no store keeps captures).
func (s *Service) captureBriefLines(ctx context.Context, l i18n.Lang, since time.Time) []string {
	st, ok := s.store().(captureStore)
	if !ok {
		return nil
	}
	waiting, err := st.Captures(ctx, db.CaptureFilter{Statuses: []string{assistantwire.CaptureNew, assistantwire.CaptureSorted,
		assistantwire.CaptureFailed}, Limit: captureListMax})
	if err != nil {
		s.logf("captures: briefing: %v", err)
		return nil
	}
	filed, err := st.Captures(ctx, db.CaptureFilter{Statuses: []string{assistantwire.CaptureFiled}, UpdatedSince: since, Limit: captureListMax})
	if err != nil {
		s.logf("captures: briefing: %v", err)
		return nil
	}
	var lines []string
	if len(waiting) > 0 {
		lines = append(lines, i18n.T(l, "assistant-captures.waiting", i18n.Args{"count": len(waiting)}))
	}
	cats := s.captureCategories(ctx, filed)
	sensitive, shown, more := 0, 0, 0
	for _, c := range filed {
		cc := cats[c.ID]
		if cc.sensitive() {
			sensitive++
			continue
		}
		if shown >= captureBriefRows {
			more++
			continue
		}
		shown++
		var sorted assistantwire.CaptureSort
		_ = json.Unmarshal(c.Sort, &sorted)
		var to assistantwire.CaptureFiledTo
		_ = json.Unmarshal(c.FiledTo, &to)
		e, ok := s.trackEntryOf(ctx, to.Track)
		if !ok {
			e = trackEntry{Slug: to.Track}
		}
		lines = append(lines, captureLine(l, c, sorted, e, cc))
	}
	if more > 0 {
		lines = append(lines, i18n.T(l, "assistant-captures.more", i18n.Args{"count": more}))
	}
	if sensitive > 0 {
		lines = append(lines, i18n.T(l, "assistant-captures.count", i18n.Args{"count": sensitive}))
	}
	return lines
}
