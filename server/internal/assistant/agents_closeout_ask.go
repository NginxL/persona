package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The status question (docs/assistant/agent-work.md "Closeout", #3616): an agent on no supervised task has nobody to
// follow it up. When its turn did work, ended without a closeout and the triage could not call it finished, the
// manager asks it once where the work stands, in the closeout form. It asks only for a report on work already done:
// the message starts nothing new. The question is scheduled on the turn's work entry (wake_at, with CloseoutAsk
// "pending") and sent by the tick after every recheck a wake gets (workGuard); "sent" stays on the entry, so the turn
// is asked once across a manager restart and the turn that answers is never asked again.

// What proto.WorkState.CloseoutAsk says.
const (
	closeoutAskPending = "pending"
	closeoutAskSent    = "sent"
)

// The work events of a status question: it went, and what the turn after it was.
const (
	closeoutAskSentEvent     = "closeout_ask_sent"
	closeoutAskAnsweredEvent = "closeout_ask_answered" // the next turn carried a closeout
	closeoutAskIgnoredEvent  = "closeout_ask_ignored"  // it did not
)

// planCloseoutAsk schedules the status question for e's turn when it is due one. It sends nothing: the tick does,
// after checking again (workWake).
func (s *Service) planCloseoutAsk(ctx context.Context, machine string, c proto.Session, e *db.SessionWork) {
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.CloseoutAsk {
		return
	}
	w := &e.Work
	if e.WakeAt != nil || e.InteractionID != "" || w.Closeout != nil || !closeoutAskable(machine, c, s) {
		return
	}
	// Unsure, or done on the model's word alone with nothing to show for it. A wait or a decision has its own path.
	if w.Phase != "" && !(w.Phase == proto.WorkDone && w.By == "model" && len(w.Artifacts) == 0) {
		return
	}
	items, read := s.lastTurnItems(ctx, machine, c.SessionID)
	if !read || !turnDidWork(items) {
		return // a question and its answer, or a turn that cannot be read
	}
	for _, it := range items {
		if it.Kind == proto.KindNotice && it.Text == userStoppedNotice {
			return // the user stopped it: theirs to go on with
		}
	}
	if _, reply := turnExchange(items); closeout.Has(reply) {
		return
	}
	at := time.Now().UTC()
	e.WakeAt, w.CloseoutAsk = &at, closeoutAskPending // not Work.WakeAt: the apps are told of no wait
}

// closeoutAskable: c is a session the status question may go to. Only one Persona started; not an assistant conversation, a review's reviewer,
// a session of a source or an archived or read-only one, and not an agent on a supervised open todo (its supervisor
// follows it).
func closeoutAskable(machine string, c proto.Session, s *Service) bool {
	if c.Assistant || c.SourceKind != "" || c.Archived || c.ReadOnlyReason != "" || s.isReviewer(machine, c.SessionID) {
		return false
	}
	if !c.StartedByPersona {
		return false // a session the user started by hand in a terminal is theirs: nothing is typed into it unasked
	}
	t, ok := s.openTodoOfAgent(machine, c.SessionID)
	return !ok || t.Supervision == nil || t.Supervision.Supervisor == ""
}

// sendCloseoutAsk sends e's pending status question. c is the session as workGuard read it right before: still
// stopped on the same turn, nothing pending or queued, not looked at on the computer, its machine up, on no completed
// todo. The entry says "sent" before the message goes, so a restart in between cannot send it twice.
func (s *Service) sendCloseoutAsk(ctx context.Context, e db.SessionWork, c proto.Session, now time.Time) {
	k := [2]string{e.MachineID, e.SessionID}
	if !closeoutAskable(e.MachineID, c, s) || now.Sub(e.TurnAt) > workBackfill {
		s.logf("work %s/%s: not asked for its status: supervised, not an agent's own session, or too old", k[0], short(k[1]))
		e.WakeAt, e.Work.CloseoutAsk = nil, ""
		s.putWork(ctx, e)
		return
	}
	e.WakeAt, e.Work.CloseoutAsk = nil, closeoutAskSent
	s.putWork(ctx, e)
	language := answerLanguage(s.hello(ctx).Locale)
	if err := s.sendToSession(ctx, k[0], c.SessionID, renderCloseoutAsk(language, s.closeoutForm(k[0], language))); err != nil {
		s.logf("work %s/%s: %s: %v", k[0], short(k[1]), closeoutAskSentEvent, err)
		at := now.Add(2 * time.Minute).UTC()
		e.WakeAt, e.Work.CloseoutAsk = &at, closeoutAskPending
		s.putWork(ctx, e)
		return
	}
	s.logf("work %s/%s: asked for its status (no closeout, %s)", k[0], short(k[1]), cmpOr(e.Work.Phase, "unsure"))
	s.workEvent(k[0], k[1], closeoutAskSentEvent, map[string]any{"after": now.Sub(e.TurnAt).Seconds(), "phase": e.Work.Phase})
}

// closeoutAskOutcome is the event for the turn after a status question, w being what the triage found of it.
func closeoutAskOutcome(w proto.WorkState) string {
	if w.Closeout != nil {
		return closeoutAskAnsweredEvent
	}
	return closeoutAskIgnoredEvent
}

func renderCloseoutAsk(language, form string) string {
	return prompts.RenderAgentCloseoutAsk(prompts.AgentCloseoutAsk{Form: form, Language: language})
}
