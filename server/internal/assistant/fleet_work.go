package assistant

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Where each agent stands in its work as the session lists show it (docs/assistant/agent-work.md "Model"): what the Agents
// business reported for its latest turn while it is still stopped on it, what it shows while it works or asks.

// decorateWork puts on sess (decorated but for its work) where it stands in its work, and the state that follows.
// asking is the machine's pending interactions by session (an agent's own before the manager's card); a done turn
// nobody has seen that ended after finishedSince is Just finished (#9751).
func (s *Service) decorateWork(mid string, sess *proto.Session, asking map[string]proto.Interaction, finishedSince time.Time) {
	r := &s.fleet.rep
	r.mu.RLock()
	on := r.workOn
	reported, ok := r.work[[2]string{mid, sess.SessionID}]
	r.mu.RUnlock()
	if !on && !(ok && transientPending(reported) && sess.Status != proto.StatusWorking && sameTurn(*sess, reported.TurnAt)) {
		return // the triage off: only a "Continue" on its way after a transient error is told (transient_retry, #3208)
	}
	var e *proto.WorkState
	if ok {
		e = &reported
	}
	sess.Work = workOf(s.lang(context.Background()), *sess, e, asking)
	unread := sess.State == proto.StateUnread
	sess.State = workState(sess.State, sess.Work)
	if unread && justFinished(*sess, finishedSince) {
		sess.Work.JustFinished = true // workOf's copy: the report keeps none
	}
}

// justFinished: the triage found sess's turn done while nobody had seen it (workState took it out of Unread), and the
// turn ended after since. The apps list it as Just finished till the window passes (docs/protocol/agent-states.md
// "Just finished", #9751). One a /loop wakes again is Waiting instead.
func justFinished(sess proto.Session, since time.Time) bool {
	w := sess.Work
	return sess.State == proto.StateIdle && w != nil && w.Phase == proto.WorkDone && sess.Loop == nil && endedAt(sess).After(since)
}

// endedAt is when the turn sess's work is about ended: the triage's own record of it, else the session's update time
// (when the turn finished, agentState).
func endedAt(sess proto.Session) time.Time {
	if sess.Work != nil && !sess.Work.TurnAt.IsZero() {
		return sess.Work.TurnAt
	}
	return sess.UpdatedAt
}

// finishedDue is when the first of list's Just finished rows runs out of its window, zero when none is.
func finishedDue(list []proto.Session, window time.Duration) time.Time {
	var due time.Time
	for _, sess := range list {
		if sess.Work == nil || !sess.Work.JustFinished {
			continue
		}
		if at := endedAt(sess).Add(window); due.IsZero() || at.Before(due) {
			due = at
		}
	}
	return due
}

// noteFinishedDue keeps when machine's list, as decorated to be told, has its first Just finished row run out (zero:
// none has one).
func (s *Service) noteFinishedDue(machine string, due time.Time) {
	f := &s.fleet
	f.shownMu.Lock()
	defer f.shownMu.Unlock()
	if due.IsZero() {
		delete(f.finishedDue, machine)
		return
	}
	if f.finishedDue == nil {
		f.finishedDue = map[string]time.Time{}
	}
	f.finishedDue[machine] = due
}

// republishFinished tells the hub again each machine's list whose first Just finished row ran out of its window by
// now: nothing else tells a list again when only time passes, and the apps would keep listing the row as Just
// finished (#9751). The list told notes the machine's next one; a machine gone notes none.
func (s *Service) republishFinished(now time.Time) {
	f := &s.fleet
	f.shownMu.Lock()
	var due []string
	for m, at := range f.finishedDue {
		if !now.Before(at) {
			due = append(due, m)
			delete(f.finishedDue, m)
		}
	}
	f.shownMu.Unlock()
	for _, m := range due {
		s.publishSessions(m)
	}
}

// workOf is sess's work state for the lists: reported is what the Agents business reported for its latest turn (nil:
// nothing), asking the pending interactions of its machine, by session.
func workOf(l i18n.Lang, sess proto.Session, reported *proto.WorkState, asking map[string]proto.Interaction) *proto.WorkState {
	if sess.Assistant || sess.Status == proto.StatusOffline {
		return nil
	}
	if sess.Status == proto.StatusWorking {
		return &proto.WorkState{Phase: proto.WorkWorking}
	}
	ix, asks := asking[sess.SessionID]
	if asks && ix.ToolName != assistantwire.WorkCardTool { // the agent asks itself: that is what it waits for
		return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: interactionLine(l, ix)}
	}
	if sess.Status == proto.StatusBlocked && !asks {
		return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: i18n.T(l, "hub-agents_link.screen")}
	}
	if reported == nil || !workShown(reported) || !sameTurn(sess, reported.TurnAt) {
		return nil
	}
	w := *reported
	if w.InteractionID != "" && (!asks || ix.ID != w.InteractionID) {
		w.InteractionID = "" // answered, withdrawn, or not shown yet
	}
	// Normalize old records too: the guard on opening a card did not run for
	// read-only sessions, so their unsupported waits could reach storage.
	clearUnsupportedUserWait(&w, l)
	if !workShown(&w) {
		return nil
	}
	if w.WakeAt != nil && time.Since(*w.WakeAt) > wakeGrace {
		w.WakeAt = nil // long overdue: nobody is waking it, it is no wait that ends by itself
	}
	if w.ResetsAt != nil && w.On == proto.WaitExternal && w.WakeAt == nil && !sess.TurnErrorResumes && w.Wait == "" {
		// A usage-limit wait nothing ends (no continue set: auto_continue off, or dropped; a CLI that does not go on by
		// itself): it names what it waits for, so the lists keep it for the user, not as a wait that ends by itself.
		w.Wait = w.Line
	}
	if sess.Status == proto.StatusInterrupted && selfWakes(&w) {
		// Its process is gone (a node restart is behind it, node/interrupted.go): a wait only that process ends (its
		// background tasks, a /loop, its CLI going on after a limit) never ends, and the task would read "waiting"
		// forever instead of stopped (#5124).
		return nil
	}
	return &w
}

// selfWakes: w is a wait the agent ends by itself, with nothing the manager sends: managerWaits without a wake.
func selfWakes(w *proto.WorkState) bool {
	return managerWaits(w) && w.WakeAt == nil
}

// transientPending: w is a transient model error whose "Continue" the manager sends by itself (wake_at).
func transientPending(w proto.WorkState) bool {
	return w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil
}

// wakeGrace is how late a reported wake may be and still count: the Agents business sends what is due within its
// tick, and tries again a minute later when the machine was away.
var wakeGrace = 5 * time.Minute

// workState is the unified state once the work state is known: a turn that is done, or blocked on something the
// manager waits out for it, no longer counts as "Unread" (docs/assistant/agent-work.md "Model"). A wait nobody will end (the wakes
// off, or dropped because the user went near it) stays "Unread".
func workState(state string, w *proto.WorkState) string {
	if state != proto.StateUnread || w == nil {
		return state
	}
	if w.Phase == proto.WorkDone || managerWaits(w) {
		return proto.StateIdle
	}
	return state
}

// managerWaits: w is a wait that ends without the user — the manager wakes it (wake_at), or it wakes itself (a
// background task, a /loop: on external with nothing named to wait for).
func managerWaits(w *proto.WorkState) bool {
	if w.Phase != proto.WorkBlocked || w.On == proto.WaitUser {
		return false
	}
	return w.WakeAt != nil || w.On == proto.WaitExternal && w.Wait == "" && w.Agent == nil
}

// interactionLineMost is how many runes of a pending interaction's work line are kept (the ellipsis after them).
const interactionLineMost = 60

// interactionLine is what a pending interaction waits for, in a line, in l.
func interactionLine(l i18n.Lang, ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxQuestion:
		if len(ix.Questions) > 1 { // the count after the first question is kept whole; the question is what gets cut
			more := func(q string) string {
				return i18n.T(l, "hub-agents_link.more_questions", i18n.Args{"question": q, "count": len(ix.Questions) - 1})
			}
			q := oneLine(ix.Questions[0].Question)
			if q == "" {
				q = i18n.T(l, "hub-agents_link.question")
			}
			return more(clip(q, max(interactionLineMost-utf8.RuneCountInString(more("")), 1)))
		}
		if len(ix.Questions) > 0 {
			return clip(oneLine(ix.Questions[0].Question), interactionLineMost)
		}
		return i18n.T(l, "hub-agents_link.question")
	case proto.IxPlan:
		if p := planTitle(ix.Plan); p != "" { // which plan: two agents' plans read apart
			return clip(oneLine(i18n.T(l, "hub-agents_link.plan_for", i18n.Args{"plan": p})), interactionLineMost)
		}
		return i18n.T(l, "hub-agents_link.plan")
	case proto.IxPermission:
		if ix.Summary != "" {
			return clip(oneLine(i18n.T(l, "hub-agents_link.permission_for", i18n.Args{"summary": ix.Summary})), interactionLineMost)
		}
		return i18n.T(l, "hub-agents_link.permission")
	case proto.IxElicitation:
		return i18n.T(l, "hub-agents_link.elicitation")
	}
	return i18n.T(l, "hub-agents_link.screen")
}

// decorateAwaits says whether sess (decorated, its work on it) stopped on a question to the user nobody answered yet
// (Session.AwaitsReply, docs/protocol/agent-states.md "Awaiting your reply", #5096), and puts on it what it asks when the node could not
// say (an older node, a host whose words come another way: no AsksRead): the list's last words, when they are the whole
// reply. A node that read the reply and found no question (or found it answered) is taken at its word. asks reads them
// (fleet_asks.go: again only when they changed since the machine's last list).
func decorateAwaits(sess *proto.Session, asks askReader) {
	if !sess.AsksRead && sess.Asks == "" && sess.LastText != "" && !strings.HasSuffix(sess.LastText, "…") {
		sess.Asks = asks.of(sess.SessionID, sess.LastText)
	}
	sess.AwaitsReply = awaitsReply(*sess)
}

// awaitsReply: sess is stopped (Unread or Seen) on a question to the user — the triage says it waits on the user, or,
// when the triage has no word on the turn (off, not yet, unsure), its closing words ask — with nothing else to answer
// it: no card (that is Needs You), no model error (the triage or Retry handles it), no background work or /loop (it wakes
// itself). A turn the triage found done asks nothing: its"shall I also…"is an offer, not a wait; nor one whose card the
// user answered "Skip for now".
func awaitsReply(sess proto.Session) bool {
	if sess.Assistant || sess.State != proto.StateUnread && sess.State != proto.StateIdle || sess.PendingInteractions > 0 ||
		sess.TurnError != "" || sess.Loop != nil || backgroundRunning(sess) {
		return false
	}
	if w := sess.Work; w != nil {
		switch w.Phase {
		case proto.WorkDone:
			return false
		case proto.WorkBlocked:
			return w.On == proto.WaitUser && !w.Deferred // put off on its card: the user chose to leave it
		}
	}
	return sess.Asks != ""
}
