package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A supervised task's agent reports in its own words: often in another language than the user's, in Markdown, with
// internal ids. The Progress row shows a one-sentence paraphrase in the user's language instead (#3181,
// docs/assistant/unified-entry.md "Progress tray"): the triage's small model (purpose agent.triage) writes it after the
// report is kept, off the agent's tool call, as the progress's line. A newer report wins: a line is written only while
// the progress is still the report it paraphrases. With no model, an error or an empty answer there is no line and the
// apps show the report's text.

// reportLineTimeout bounds one paraphrase; a report that waits longer is likely replaced by then anyway.
const reportLineTimeout = 45 * time.Second

// reportLineMax is how long the line may be in a language: 30 characters in Chinese and Japanese (as the triage's
// work line), 60 in the others.
func reportLineMax(locale string) int {
	if l := i18n.Lang(locale); i18n.Valid(l) && l != i18n.ZhHans && l != i18n.Ja {
		return 60
	}
	return 30
}

// goReportLine paraphrases task t's report of kind, said at at, in the background (counted in supervise.lines).
// text is the report as kept for the supervisor (at most 2000 characters), not the progress's one clipped line.
func (s *Service) goReportLine(t Todo, at time.Time, kind, text string) {
	s.supervise.lines.Add(1)
	go func() {
		defer s.supervise.lines.Done()
		ctx, cancel := context.WithTimeout(context.Background(), reportLineTimeout)
		defer cancel()
		if line := s.reportLineFromModel(ctx, t.Title, kind, text); line != "" {
			s.setProgressLine(ctx, t.ID, at, line)
		}
	}()
}

// reportLineFromModel is the model's paraphrase of a report; "" when there is no model, it fails or says nothing.
func (s *Service) reportLineFromModel(ctx context.Context, title, kind, text string) string {
	locale := s.hello(ctx).Locale
	max := reportLineMax(locale)
	// Reasoning models think first: a small max_tokens comes back empty (todo_titles.go).
	out, err := s.purposeModel("agent.triage").Complete(ctx,
		prompts.RenderAgentReportLine(prompts.AgentReportLine{Language: answerLanguage(locale), Max: max}),
		prompts.RenderAgentReportLineInput(prompts.AgentReportLineInput{Language: answerLanguage(locale), Task: clip(oneLine(title), 120), Kind: kind,
			Text: clipMiddleRunes(text, 1500)}), 300)
	if err != nil {
		if !ai.IsPaused(err) {
			s.logf("report line: %v", err)
		}
		return ""
	}
	return cleanReportLine(out, max)
}

// cleanReportLine is the model's answer as a line: its first non-empty line without quotes, Markdown marks or a final
// full stop, at most twice max characters (the rows cut it to fit anyway).
func cleanReportLine(out string, max int) string {
	line := ""
	for l := range strings.SplitSeq(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = strings.Trim(line, "「」『』《》\"'“”`*_# ") // i18n-ignore: quotes and marks a model wraps its answer in
	line = strings.TrimRight(line, "。.")          // i18n-ignore: a final full stop in the model's answer
	return clip(oneLine(line), 2*max)
}

// setProgressLine writes line as task id's progress line while the progress is still the report said at at; a newer
// report (or none) drops it.
func (s *Service) setProgressLine(ctx context.Context, id string, at time.Time, line string) {
	s.todos.mu.Lock()
	cur := s.todos.todos[id]
	ok := cur != nil && cur.Supervision != nil && cur.Supervision.Progress != nil && cur.Supervision.Progress.At.Equal(at)
	if ok {
		v := cur.Supervision.clone()
		v.Progress.Line = line
		cur.Supervision = v
	}
	s.todos.mu.Unlock()
	if ok {
		s.todosChanged(ctx)
	}
}
