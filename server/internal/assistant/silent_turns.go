package assistant

import (
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a task Agent's turn with no output, on its row (#10187) ----
//
// A turn in which a task's Agent neither replied nor called a tool (silentTurn, #2349) is no end of the work: its
// task gets an interrupted receipt (tasks_follow.go). The node has no error for it, so the Agent's row said "done" all
// the same: the task read unread ("done, not seen") on every screen and in the assistant's snapshot. The verdict is
// kept here by session and put on the row as a turn error of the hub's own kind (decorateInventory): the row, the task
// and the apps then read it as they read a turn the model service cut off, interrupted with a "Continue".
//
// A turn Persona's own Stop interrupted before it had any output (stopReporting: the owner stopped the conversation
// that handed the task out) is stopped, not a failure of the model service: stopReporting notes each agent it
// interrupts with the task's stretch of work it ran in (the turn the Stop was sent to), and the hub notes when it next
// hears that agent's turn end. A silent end of that same stretch, heard after the Stop, reads TurnErrorStopped, with a
// receipt that says it was stopped. A turn that ended before the Stop was sent, or a later one, does not.

// quietSlack is how much later than the verdict's a row's time may be and still be the same turn, where the node gives
// no Rev: its lists can carry a later UpdatedAt for a transcript that did not change (lookOf, task_turns.go), as
// agentState allows for.
const quietSlack = 2 * time.Second

// stopCounts is how long after Persona's Stop the interrupted turn's end may be heard and still be taken as the Stop's.
// The interrupt ends the turn at once; the end is heard within seconds of it.
const stopCounts = 10 * time.Minute

// stopKeep is how long a Stop is kept for the turn end it is waiting for to be taken up (its settle, a retry).
const stopKeep = time.Hour

// quietMarkKeep is how long a verdict is kept for an agent that never works again.
const quietMarkKeep = 14 * 24 * time.Hour

// quietTurns are the verdicts and the stops. mu is a leaf: nothing else is locked, and no I/O done, while it is held.
type quietTurns struct {
	mu    sync.Mutex
	marks map[SessionRef]quietMark
	stops map[SessionRef]stopMark // the agents stopReporting interrupted
}

// stopMark: Persona's Stop was sent to an agent at at, while it worked on its task's stretch of work that began at
// stretch (the task's working time, or its creation when no start was heard: settleTurn's since). ended: when the hub
// next heard a turn of it end (zero: not yet).
type stopMark struct {
	at, stretch, ended time.Time
}

// rowAt is which turn an agent's row was at, as far as the row tells: its Rev, its time (seen) and its last words
// (text, where known). It is the one identity the verdict (quietMark.holds) and "Continue"'s guard (continueTakenUp)
// both go by: a row the verdict still holds for is one Continue has not been taken up at, and the other way round.
type rowAt struct {
	rev       int64
	seen      time.Time
	text      string
	textKnown bool // the ledger keeps no words: a verdict it restored has none to go by
}

// rowOf is the turn row c is at.
func rowOf(c proto.Session) rowAt {
	return rowAt{rev: c.Rev, seen: c.UpdatedAt, text: c.LastText, textKnown: true}
}

// holds: c is still at row r: nothing written to its transcript since. Its Rev says so where both have one; else its
// time, no earlier and at most quietSlack later, with the same last words.
func (r rowAt) holds(c proto.Session) bool {
	if r.rev != 0 && c.Rev != 0 {
		return c.Rev == r.rev
	}
	if c.UpdatedAt.Before(r.seen) || c.UpdatedAt.After(r.seen.Add(quietSlack)) {
		return false
	}
	return !r.textKnown || c.LastText == r.text
}

// quietMark: the turn of an agent that ended at its row had no output; kind is proto.TurnErrorNoOutput or
// proto.TurnErrorStopped; at: when the verdict was made.
type quietMark struct {
	rowAt
	kind string
	at   time.Time
}

// quietTurnText is the turn error a row with verdict kind shows, in l.
func quietTurnText(l i18n.Lang, kind string) string {
	if kind == proto.TurnErrorStopped {
		return i18n.T(l, "assistant-tasks.stopped_turn")
	}
	return silentTurnErrorIn(l)
}

// markQuietTurn keeps that agent c's turn, ending at this row, had no output (kind), and has the apps told the row.
func (s *Service) markQuietTurn(c proto.Session, kind string) {
	s.putQuietMarks(map[SessionRef]quietMark{{MachineID: c.MachineID, SessionID: c.SessionID}: {rowAt: rowOf(c), kind: kind,
		at: time.Now()}})
	s.publishSessions(c.MachineID)
}

// putQuietMarks keeps marks, a later verdict over an earlier one.
func (s *Service) putQuietMarks(marks map[SessionRef]quietMark) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.marks == nil {
		q.marks = map[SessionRef]quietMark{}
	}
	now := time.Now()
	for ref, m := range q.marks {
		if now.Sub(m.at) > quietMarkKeep {
			delete(q.marks, ref)
		}
	}
	for ref, m := range marks {
		if cur, ok := q.marks[ref]; !ok || !m.seen.Before(cur.seen) {
			q.marks[ref] = m
		}
	}
}

// quietMarks are machine's verdicts by session (nil: none), for one pass over its list.
func (s *Service) quietMarks(machine string) map[string]quietMark {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	var out map[string]quietMark
	now := time.Now()
	for ref, m := range q.marks {
		if now.Sub(m.at) > quietMarkKeep {
			delete(q.marks, ref) // kept past its time (pruneQuiet does the rest)
			continue
		}
		if ref.MachineID == machine {
			if out == nil {
				out = map[string]quietMark{}
			}
			out[ref.SessionID] = m
		}
	}
	return out
}

// quietTurnOver: the agent started a turn: the verdict on its last one is over.
func (s *Service) quietTurnOver(machine, sid string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.marks, SessionRef{MachineID: machine, SessionID: sid})
}

// putQuietTurn is decorateInventory's: row c with no turn error of the node's carries the verdict on its turn while it
// is stopped (unread or idle, as turnError) and still at the turn the verdict was made at.
func putQuietTurn(c *proto.Session, marks map[string]quietMark, l i18n.Lang) {
	m, ok := marks[c.SessionID]
	if !ok || c.TurnError != "" || c.State != proto.StateUnread && c.State != proto.StateIdle || !m.holds(*c) {
		return
	}
	c.TurnError, c.TurnErrorKind = quietTurnText(l, m.kind), m.kind
}

// noteStopped: Persona's Stop is being sent to agent sid on machine, which works on its task's stretch of work that
// began at stretch (stopReporting). The answer is the attempt, for forgetStopAttempt when the interrupt failed.
func (s *Service) noteStopped(machine, sid string, stretch time.Time) time.Time {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stops == nil {
		q.stops = map[SessionRef]stopMark{}
	}
	now := time.Now()
	for ref, m := range q.stops {
		if now.Sub(m.at) > stopKeep {
			delete(q.stops, ref)
		}
	}
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if prev, ok := q.stops[ref]; ok && !now.After(prev.at) {
		now = prev.at.Add(time.Nanosecond) // a Stop's time is its attempt: never one the attempt before had (a coarse clock)
	}
	q.stops[ref] = stopMark{at: now, stretch: stretch}
	return now
}

// forgetStopAttempt: the Stop sent at at to agent sid on machine did not go out: it is taken back, unless a later one
// took its place.
func (s *Service) forgetStopAttempt(machine, sid string, at time.Time) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if m, ok := q.stops[ref]; ok && m.at.Equal(at) {
		delete(q.stops, ref)
	}
}

// stopTurnEnded: the hub heard a turn of agent sid on machine end (turnEnded): the first end after a Stop is the
// interrupted turn's, as far as the hub can tell.
func (s *Service) stopTurnEnded(machine, sid string, now time.Time) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if m, ok := q.stops[ref]; ok && m.ended.IsZero() && !now.Before(m.at) {
		m.ended = now
		q.stops[ref] = m
	}
}

// stopFor is the Stop that interrupted agent sid on machine's turn in the task's stretch of work that began at
// stretch, by its attempt (ok): one Stop, read under one lock, sent in that stretch (stopStretch), whose turn end the
// hub heard after it was sent, within stopCounts of it. A Stop whose end is still to come (a newer one, for a turn going on), one of
// another stretch, one whose turn ended before it was sent, and one taken up since (forgetStop) are none. That same
// attempt classifies the turn (quietTurnKind) and is the one its receipt spends (forgetStop). One kept past stopKeep
// is let go here.
func (s *Service) stopFor(machine, sid string, stretch time.Time) (attempt time.Time, ok bool) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	m, found := q.stops[ref]
	if !found {
		return time.Time{}, false
	}
	if time.Since(m.at) > stopKeep {
		delete(q.stops, ref)
		return time.Time{}, false
	}
	if m.ended.IsZero() || m.ended.Sub(m.at) > stopCounts || !stopStretch(m, stretch) {
		return time.Time{}, false
	}
	return m.at, true
}

// stopStretch: m is a Stop of the turn whose stretch of work began at stretch: sent in it, or sent while the agent was
// still starting, before its turn's start was heard (the stretch then begins after the Stop's, but no later than the
// turn end the hub heard after the Stop). A later turn's stretch begins after that end.
func stopStretch(m stopMark, stretch time.Time) bool {
	return m.stretch.Equal(stretch) || stretch.After(m.stretch) && !stretch.After(m.ended)
}

// quietTasksLeft: the agents left their open task (it closed or was cancelled, they were let go, or another task has
// them now: fleetTakeSnapshot): what was said of their last turn for that task goes, verdict and Stop.
func (s *Service) quietTasksLeft(refs []SessionRef) {
	if len(refs) == 0 {
		return
	}
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ref := range refs {
		delete(q.marks, ref)
		delete(q.stops, ref)
	}
}

// forgetStop: a turn end of agent sid on machine was taken up (a receipt): the Stop attempt that interrupted it
// (stopFor; zero: none) is spent on that turn. A newer Stop, sent since, stays for the turn it interrupts.
func (s *Service) forgetStop(machine, sid string, attempt time.Time) {
	if !attempt.IsZero() {
		s.forgetStopAttempt(machine, sid, attempt)
	}
}

// pruneQuiet lets go of the verdicts and Stops kept past their time (the supervision sweep, pruneTurns), and with
// gone, of those of machine's sessions its node no longer lists (pruneGoneQuiet). It says which machines lost a
// verdict, whose lists the apps have with it.
func (s *Service) pruneQuiet(now time.Time, machine string, gone func(sid string) bool) (changed map[string]bool) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	for ref, m := range q.marks {
		if now.Sub(m.at) > quietMarkKeep || gone != nil && ref.MachineID == machine && gone(ref.SessionID) {
			delete(q.marks, ref)
			if changed == nil {
				changed = map[string]bool{}
			}
			changed[ref.MachineID] = true
		}
	}
	for ref, m := range q.stops {
		if now.Sub(m.at) > stopKeep || gone != nil && ref.MachineID == machine && gone(ref.SessionID) {
			delete(q.stops, ref)
		}
	}
	return changed
}

// pruneGoneQuiet lets go of the verdicts and Stops of machine's sessions its node no longer lists (setSessions, on
// the node's own news of its list). It asks the devices' list as it is now, not a copy read earlier: a reader's old
// copy, from before an agent was listed, never takes a fresh verdict or Stop away.
func (s *Service) pruneGoneQuiet(machine string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	has := false
	for ref := range q.marks {
		has = has || ref.MachineID == machine
	}
	for ref := range q.stops {
		has = has || ref.MachineID == machine
	}
	q.mu.Unlock()
	if !has || s.devs == nil {
		return
	}
	listed := map[string]bool{}
	if _, ok := s.devs.SessionsWhere(machine, func(c *proto.Session) bool { listed[c.SessionID] = true; return false }); !ok {
		return // the devices do not have the machine (now): nothing to go by
	}
	s.pruneQuiet(time.Now(), machine, func(sid string) bool { return !listed[sid] })
}

// taskStretch is when task t's current stretch of work began, as settleTurn counts it (its since): when one of its
// agents last started a turn, or its creation when no start was heard (a hub restarted since).
func (s *Service) taskStretch(t Todo) time.Time {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if since, ok := s.tasks.working[t.ID]; ok {
		return since
	}
	return t.CreatedAt
}

// restoreQuietTurns puts the verdicts the ledger kept back on the rows (turnsLocked, once it is read): a manager
// started again shows a turn with no output as it did before. The apps are told the rows of their machines.
func (s *Service) restoreQuietTurns(turns map[string]taskTurn) {
	marks := map[SessionRef]quietMark{}
	machines := map[string]bool{}
	for _, e := range turns {
		if e.Silent == "" || e.State != turnInterrupted {
			continue
		}
		if time.Since(e.At) > quietMarkKeep {
			continue // kept past its time: not put back
		}
		marks[SessionRef{MachineID: e.Machine, SessionID: e.Session}] = quietMark{rowAt: rowAt{rev: e.Rev, seen: e.Seen},
			kind: e.Silent, at: e.At}
		machines[e.Machine] = true
	}
	if len(marks) == 0 {
		return
	}
	s.putQuietMarks(marks)
	go func() { // the ledger's lock is held: its readers never wait for a list to be decorated
		for m := range machines {
			s.publishSessions(m)
		}
	}()
}
