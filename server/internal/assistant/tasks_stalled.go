package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// stallCloseAfter is how long a stalled terminal Agent may go with no supervisor action or real progress before the
// manager stops it. Its task and retained session stay available for an explicit continuation.
func stallCloseAfter(expect int) time.Duration {
	return max(time.Hour, time.Duration(expect)*3*time.Minute)
}

// timeoutOwnedTerminal: a working session Persona may stop without touching an assistant, another tool or a runtime.
// PaneID is checked again at the stop boundary: ownership alone does not prove there is a terminal Agent to stop.
func timeoutOwnedTerminal(c proto.Session) bool {
	return c.SourceKind == "" && c.SourceInstanceID == "" && !c.Assistant && c.StartedByPersona && !c.Starting
}

func stalledSessionMoved(c proto.Session) time.Time {
	at := c.UpdatedAt
	if c.ActivityAt != nil && c.ActivityAt.After(at) {
		at = *c.ActivityAt
	}
	return at
}

// stalledSince is the later of supervisor action and the relevant activity. A spinning Agent's transcript movement is
// not progress: its no-progress stretch, not its latest sleep or poll, supplies the activity side of the clock.
func (s *Service) stalledSince(t Todo, c proto.Session, spinning bool) time.Time {
	at := time.Time{}
	if t.Supervision != nil {
		at = t.Supervision.UpdatedAt
	}
	if !spinning {
		if moved := stalledSessionMoved(c); moved.After(at) {
			at = moved
		}
	}
	b := &s.progress
	b.mu.Lock()
	if w := b.m[[2]string{c.MachineID, c.SessionID}]; w != nil {
		if spinning && w.since.After(at) {
			at = w.since
		}
		if w.progressAt.After(at) {
			at = w.progressAt
		}
	}
	b.mu.Unlock()
	return at
}

// stalledCloseDue says whether c is both stalled and past the closure threshold. The existing silent-stall and
// no-progress rules decide what is stalled; merely moving a transcript never resets a spinning Agent's clock.
func (s *Service) stalledCloseDue(t Todo, c proto.Session, now time.Time) (time.Time, bool) {
	if t.Supervision == nil || c.State != proto.StateWorking || c.PendingInteractions > 0 {
		return time.Time{}, false
	}
	_, spinning := s.noProgressSignal(t, c, now)
	moved := stalledSessionMoved(c)
	silent := !moved.IsZero() && now.Sub(moved) >= stallAfter(t.Supervision.ExpectMinutes)
	if !spinning && !silent {
		return time.Time{}, false
	}
	since := s.stalledSince(t, c, spinning)
	return since, !since.IsZero() && now.Sub(since) >= stallCloseAfter(t.Supervision.ExpectMinutes)
}

func (s *Service) progressAfter(machine, session string, at time.Time) bool {
	b := &s.progress
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.m[[2]string{machine, session}]
	return w != nil && w.progressAt.After(at)
}

// timeoutActivityAfter reports activity that should cancel a recovered close. Polling during no_progress is still not
// progress and must not restart the wait after a transient stop failure; silent-stall activity and real progress count.
func (s *Service) timeoutActivityAfter(t Todo, c proto.Session, intent, now time.Time) bool {
	if s.progressAfter(c.MachineID, c.SessionID, intent) {
		return true
	}
	_, spinning := s.noProgressSignal(t, c, now)
	return !spinning && stalledSessionMoved(c).After(intent)
}

// closeStalledTask records timeout_closing before stopping every current terminal Agent of task. If a response or the
// final ledger write is lost, that durable intent completes only after the retained session is observed stopped.
// Runtime-owned sessions never enter this path and keep their ordinary stalled / no_progress reminders.
func (s *Service) closeStalledTask(ctx context.Context, task Todo, agents []proto.Session, now time.Time) {
	if task.Supervision == nil || task.DoneAt != nil {
		return
	}
	state := task.Supervision.state(task)
	switch state {
	case reviewPending, reviewNeedsUser, reviewUserCheck, reviewParked, reviewTimedOut:
		return
	}
	recovering := state == reviewClosing
	superseded := supersededAgents(task, agents)
	currentAgents := currentTaskAgents(agents, superseded)
	expected := 0
	for _, a := range task.Agents {
		if a.active() && !superseded[[2]string{a.MachineID, a.SessionID}] {
			expected++
		}
	}
	if expected == 0 || len(currentAgents) != expected {
		return // an unreachable or unlisted current Agent cannot be confirmed stopped
	}
	for _, c := range currentAgents {
		if !timeoutOwnedTerminal(c) || c.PendingInteractions > 0 {
			return
		}
		if recovering && stoppedAgent(c) {
			continue
		}
		if c.PaneID == "" || c.State != proto.StateWorking {
			return
		}
		if !recovering {
			if _, due := s.stalledCloseDue(task, c, now); !due {
				return
			}
		}
	}

	// A supervisor action or a link move must win over a stop already being recovered. Keep their writers out while
	// the current ledger and live rows are rechecked and the durable close intent is written.
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	s.supervise.writeMu.Lock()
	defer s.supervise.writeMu.Unlock()
	current, ok := s.todoNow(task.ID)
	if !ok || current.DoneAt != nil || current.Supervision == nil ||
		!current.Supervision.UpdatedAt.Equal(task.Supervision.UpdatedAt) || current.Supervision.state(current) != state {
		return
	}
	s.todos.mu.RLock()
	owned := true
	for _, a := range current.Agents {
		if a.active() && s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}] != task.ID {
			owned = false
		}
	}
	s.todos.mu.RUnlock()
	if !owned {
		return
	}
	cancelClosing := func() {
		_, _, _ = s.updateSupervisionLockedAt(ctx, task.ID, now, func(v *Supervision) bool {
			if v.State != reviewClosing {
				return false
			}
			v.State = ""
			if v.Round > 0 {
				v.State = reviewRework
			}
			return true
		})
	}
	if !recovering {
		updated, changed, err := s.updateSupervisionLockedAt(ctx, task.ID, now, func(v *Supervision) bool {
			if v.State != task.Supervision.State || !v.UpdatedAt.Equal(task.Supervision.UpdatedAt) {
				return false
			}
			v.State = reviewClosing
			return true
		})
		if err != nil {
			s.logf("task %s: record timeout intent: %v", task.ID, err)
			return
		}
		if !changed {
			return
		}
		task.Supervision = &updated
	}
	for _, c := range currentAgents {
		live, ok := s.session(ctx, c.MachineID, c.SessionID)
		if !ok {
			return
		}
		if stoppedAgent(live) {
			continue
		}
		if !timeoutOwnedTerminal(live) || live.PaneID == "" || live.State != proto.StateWorking || live.PendingInteractions > 0 ||
			recovering && s.timeoutActivityAfter(current, live, task.Supervision.UpdatedAt, now) {
			cancelClosing()
			return
		}
		if !recovering {
			if _, due := s.stalledCloseDue(current, live, now); !due {
				cancelClosing()
				return
			}
		}
		var result struct {
			Stopped bool `json:"stopped"`
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		// A working Agent cannot use retire=true: that form is deliberately for still Agents. This is the same
		// terminal-retaining stop as the user's Stop, so only an explicit user Resume can continue it.
		err := s.call(cctx, c.MachineID, deviceSessionStop, map[string]any{"session_id": c.SessionID}, &result)
		cancel()
		if err != nil {
			s.logf("task %s: stalled stop: %v", task.ID, err)
			return // the call may have stopped the Agent: recover the durable intent before claiming anything
		}
		if !result.Stopped {
			latest, ok := s.session(ctx, c.MachineID, c.SessionID)
			if !ok || !stoppedAgent(latest) {
				return
			}
		}
	}
	if _, _, err := s.updateSupervisionLockedAt(ctx, task.ID, now, func(v *Supervision) bool {
		if v.State != reviewClosing || !v.UpdatedAt.Equal(task.Supervision.UpdatedAt) {
			return false
		}
		v.State = reviewTimedOut
		v.Reviews = append(v.Reviews, TaskReview{At: now.UTC(), Verdict: reviewTimedOut, By: "system"})
		return true
	}); err != nil {
		s.logf("task %s: record timeout: %v", task.ID, err)
	}
}

// continueTimedOut resumes the retained session through the same node call as the user's Resume. A plain send cannot
// restart a stopped Agent, and resume_automatic would wrongly apply the assistant-only stop-cause restriction to the
// user's explicit Continue. The node fills in the session's original launcher, transcript and workspace.
func (s *Service) continueTimedOut(ctx context.Context, task Todo) (json.RawMessage, error) {
	current, ok := s.todoNow(task.ID)
	if !ok || current.Supervision == nil || current.Supervision.state(current) != reviewTimedOut {
		return s.todoView(ctx, task.ID)
	}
	agents := s.taskAgents(ctx, current)
	byID := map[[2]string]proto.Session{}
	for _, c := range agents {
		byID[[2]string{c.MachineID, c.SessionID}] = c
	}
	st, claimed := s.claimTaskStart(task.ID)
	if !claimed {
		return s.todoView(ctx, task.ID)
	}
	defer func() { s.endTaskStart(task.ID, st, proto.Session{}) }()
	for _, a := range current.Agents {
		if !a.active() {
			continue
		}
		if c, found := byID[[2]string{a.MachineID, a.SessionID}]; found && !stoppedAgent(c) {
			continue // already resumed through another path; its next turn also clears the timeout
		}
		m, ok := s.device(ctx, a.MachineID)
		if !ok || !m.Reachable {
			return nil, machineOffline(cmp.Or(m.Name, a.MachineID))
		}
		var result struct {
			Session proto.Session `json:"session"`
		}
		sctx, cancel := context.WithTimeout(ctx, agentStartTimeout)
		err := s.call(sctx, a.MachineID, DeviceSessionStart, map[string]any{
			"resume_session_id": a.SessionID,
			"prompt":            continueText,
		}, &result)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("continue timed-out task in its original session: %w", err)
		}
	}
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		if v.State != reviewTimedOut {
			return false
		}
		v.State = ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	}); err != nil {
		return nil, err
	}
	return s.todoView(ctx, task.ID)
}
