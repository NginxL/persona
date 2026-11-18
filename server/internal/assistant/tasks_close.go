package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Stopping the agents of finished tasks (docs/assistant/todos.md "Stop Agents of completed tasks"). Once a task opened from an
// assistant conversation is done — by the user or by the assistant's judge (done.go) — its agents that have stayed
// still for a while are stopped: the node quits the agent process and keeps its terminal, the shell it ran in (node
// retire.go, session.stop; #4754; the node closes that terminal itself only after its grace period, #10111). The todo keeps the agents
// (machine, session id, directory) and the node keeps how each was started (launcher, model, modes), so reading one
// goes on from its transcript and its terminal stays open on its computer.
//
// Never stopped: an agent that works, waits for the user (a question, a plan, a permission, any dialog) or has
// background shells, subagents, a /loop or a server running; one linked to a task that is still open; an assistant
// conversation; a session another tool owns (the node refuses: session.stop with retire). Agents the user started by
// hand and never linked to a task are no task's, so nothing here touches them.

var (
	// taskCloseIdle is how long a done task's agent must have been still (and the task done) before it is closed.
	taskCloseIdle = 3 * time.Minute
	// taskCloseEvery is how often the assistant looks for agents to close (tests shorten them).
	taskCloseEvery = time.Minute
	// taskCloseRetry is how long a node's refusal stands for an agent that did not change: a node updated meanwhile
	// may close it now (a deploy restarts the hub first and the resident node once it is idle: the old one is asked,
	// which does not know session.retire).
	taskCloseRetry = 10 * time.Minute
)

// taskCloseRefusal is a node's refusal to close an agent: the session's updated_at then, when, and why.
type taskCloseRefusal struct {
	updated, at time.Time
	why         string
}

// taskCloseRefused: (machine, session) → its node's last refusal, so it is not asked every minute while nothing
// changed.
type taskCloseRefused map[[2]string]taskCloseRefusal

// doneSeen: (machine, session) → what the pass saw of a done task's agent since the task was done (#10311). Kept in
// memory across passes, as idleSeen is: a manager restart forgets it, and the first sight of a running agent then
// only holds off its stop.
type doneSeen map[[2]string]doneSeenAgent

// doneSeenAgent is what the pass saw of one agent of a task done at done.
type doneSeenAgent struct {
	done time.Time // the task's done time it is about: a task done again later starts over
	// running is when the pass first saw it running in a pane, zero while it is stopped: its stillness counts from
	// no earlier, so an agent resumed with no message (its last step is old) is not stopped on the next pass.
	running time.Time
	stopped bool // seen stopped since the task was done, or stopped by this pass
	// resumed: running again after that. Someone brought it back on purpose (the owner's Resume, a message that
	// resumed it): this pass leaves it to the idle rule (tasks_idle.go), which covers resumed agents.
	resumed bool
}

func (s *Service) taskCloseLoop(ctx context.Context) {
	t := time.NewTicker(taskCloseEvery)
	defer t.Stop()
	refused := taskCloseRefused{}
	doneSeen := doneSeen{}
	idleSeen := idleSeen{}
	endedSeen := endedSeen{}
	var archived time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.closeDoneTaskAgents(ctx, refused, doneSeen)
		s.closeIdleAgents(ctx, refused, idleSeen)
		s.closeEndedAgentTerminals(ctx, endedSeen) // Agents that quit, with close_ended_terminals on (agent_terminal_close.go)
		s.orphanWatch(ctx, time.Now())             // open to-dos whose agents are all gone (tasks_orphan.go)
		if now := time.Now(); now.Sub(archived) >= autoArchiveEvery {
			archived = now
			s.archiveClosedSessions(ctx, now) // closed sessions idle for days (agent_archive_auto.go)
		}
	}
}

// doneTaskAgent is an agent of a done task, with when the task was done.
type doneTaskAgent struct {
	todo, machine, session string
	doneAt                 time.Time
}

// doneTaskAgents are the agents still linked to tasks that are done, except those that count for an open todo now.
func (s *Service) doneTaskAgents() []doneTaskAgent {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var out []doneTaskAgent
	for _, t := range s.todos.todos {
		if t.Origin == "" || t.DoneAt == nil {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			if _, open := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; open {
				continue
			}
			out = append(out, doneTaskAgent{todo: t.ID, machine: a.MachineID, session: a.SessionID, doneAt: *t.DoneAt})
		}
	}
	return out
}

// retirable says whether s, an agent of a task done at doneAt, may be closed now: running in a pane of the engine,
// still since both the task was done and its last step at least taskCloseIdle ago, and with nothing going on.
func retirable(s proto.Session, doneAt, now time.Time) bool {
	return retirableAfter(s, doneAt, now, taskCloseIdle)
}

// retirableAfter is retirable with the time the agent must have been still.
func retirableAfter(s proto.Session, doneAt, now time.Time, idle time.Duration) bool {
	switch {
	case s.Assistant, s.SourceKind != "", s.Starting, !s.StartedByPersona:
		return false
	case s.Status == proto.StatusOffline || s.PaneID == "":
		return false // not running: nothing to close
	case s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked || s.Status == proto.StatusUnknown:
		return false // working, waiting, or the engine cannot tell
	case s.State == proto.StateWorking || s.State == proto.StateNeedsYou || s.PendingInteractions > 0:
		return false
	case len(s.Queued) > 0 || len(s.Background) > 0 || s.Loop != nil || len(s.Ports) > 0:
		return false // more to come, or something it runs that the user may be using
	}
	for _, sa := range s.Subagents {
		if sa.Running {
			return false
		}
	}
	for _, w := range s.Workflows { // closing it would kill the run (#2935)
		if w.Running {
			return false
		}
	}
	if s.Agent != "" && s.Agent != "claude" && s.Agent != "codex" && s.Agent != "traex" {
		return false
	}
	last := doneAt
	if s.UpdatedAt.After(last) {
		last = s.UpdatedAt
	}
	return now.Sub(last) >= idle
}

// closeDoneTaskAgents retires the agents of done tasks that may be closed now (retirable); refused remembers the
// sessions a node would not close, seen what each agent did since its task was done.
func (s *Service) closeDoneTaskAgents(ctx context.Context, refused taskCloseRefused, seen doneSeen) {
	if !s.todosLoaded() {
		return
	}
	agents := s.doneTaskAgents()
	if len(agents) == 0 {
		clear(seen)
		return
	}
	now := time.Now()
	reachable := map[string]bool{}
	for _, d := range s.devices(ctx) {
		reachable[d.ID] = d.Reachable
	}
	lists := map[string]map[string]proto.Session{}
	done := make(map[[2]string]bool, len(agents))
	for _, a := range agents {
		key := [2]string{a.machine, a.session}
		done[key] = true // an unreachable machine keeps what was seen
		if !reachable[a.machine] {
			continue
		}
		c, ok := s.doneTaskAgentRow(ctx, lists, a)
		if !ok {
			continue
		}
		w := seen.see(key, a.doneAt, c, now)
		if w.resumed || w.running.IsZero() {
			continue
		}
		since := a.doneAt
		if w.running.After(since) {
			since = w.running
		}
		if !retirable(c, since, now) {
			continue
		}
		if r, ok := refused[key]; ok && r.updated.Equal(c.UpdatedAt) && now.Sub(r.at) < taskCloseRetry {
			continue
		}
		if s.retireAgent(ctx, refused, "task "+a.todo, a.machine, c, now) {
			// Stopped by this pass: running again before a pass sees it stopped is a resume too.
			w.stopped, w.running = true, time.Time{}
			seen[key] = w
		}
	}
	for k := range seen {
		if !done[k] { // its task reopened, or the agent left it
			delete(seen, k)
		}
	}
}

// see notes c, the row of an agent of a task done at done, and returns what has been seen of it by now.
func (seen doneSeen) see(key [2]string, done time.Time, c proto.Session, now time.Time) doneSeenAgent {
	w := seen[key]
	if !w.done.Equal(done) {
		w = doneSeenAgent{done: done}
	}
	switch {
	case c.Starting: // still launching (the task was done before it started, #2954): neither stopped nor running yet
	case c.Status == proto.StatusOffline || c.PaneID == "":
		w.stopped, w.running = true, time.Time{}
	default:
		if w.stopped {
			w.resumed = true
		}
		if w.running.IsZero() {
			w.running = now
		}
	}
	seen[key] = w
	return w
}

// doneTaskAgentRow is a's session as the apps get it (the state and the cards waiting count). With the fleet in this
// process it is the row a's node lists, decorated alone (listedSession): one only the archive keeps is offline, never
// retirable, so a task done long ago whose agent's session is gone costs a lookup each pass, not its machine's whole
// list (#3197). Otherwise each machine is listed once a pass, into lists.
func (s *Service) doneTaskAgentRow(ctx context.Context, lists map[string]map[string]proto.Session, a doneTaskAgent) (proto.Session, bool) {
	if s.fleetOn() && a.machine != DeviceAll {
		m, ok := s.fleetMachine(a.machine)
		if !ok {
			return proto.Session{}, false // sessions.list: no such machine, no sessions
		}
		return s.listedSession(m.ID, a.session)
	}
	list, ok := lists[a.machine]
	if !ok {
		list = map[string]proto.Session{}
		got, _ := s.sessions(ctx, a.machine)
		for _, c := range got {
			list[c.SessionID] = c
		}
		lists[a.machine] = list
	}
	c, ok := list[a.session]
	return c, ok
}

// retireAgent asks c's node to stop its agent and keep its terminal (session.stop with retire) and remembers a
// refusal; who names the reason in the log. True when the node answered: the agent is not running any more.
func (s *Service) retireAgent(ctx context.Context, refused taskCloseRefused, who, machine string, c proto.Session, now time.Time) bool {
	key := [2]string{machine, c.SessionID}
	var res struct {
		Stopped bool `json:"stopped"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	cause := proto.StopTaskCompleted
	if who == "idle" {
		cause = proto.StopIdle
	}
	// A done task ends its Agent's session: with the owner's close-on-end setting its terminal goes too (#10355,
	// agent_terminal_close.go). An idle stop is no end: that Agent keeps its terminal either way.
	closeIt := cause == proto.StopTaskCompleted && !agentCloseBusy(c) && s.closeEndedTerminals(ctx)
	var err error
	if closeIt {
		res.Stopped, err = s.closeEndedTerminal(cctx, machine, c.SessionID, cause)
	} else {
		err = s.call(cctx, machine, deviceSessionStop, map[string]any{"session_id": c.SessionID, "retire": true, "stop_cause": cause}, &res)
	}
	cancel()
	var re *rpc.Error
	switch {
	case err == nil:
		delete(refused, key)
		switch {
		case res.Stopped && closeIt:
			s.logf("%s: closed agent %s/%s and its terminal (close_ended_terminals)", who, machine, c.SessionID)
		case res.Stopped:
			s.logf("%s: stopped agent %s/%s (its terminal stays)", who, machine, c.SessionID)
		}
		return true
	case errors.As(err, &re): // the node refused (or is too old to know session.stop: nothing closed): not again for a while
		if prev, ok := refused[key]; !ok || prev.why != err.Error() {
			s.logf("%s: agent %s/%s left open: %v", who, machine, c.SessionID, err)
		}
		refused[key] = taskCloseRefusal{updated: c.UpdatedAt, at: now, why: err.Error()}
	default:
		s.logf("%s: close agent %s/%s: %v", who, machine, c.SessionID, err)
	}
	return false
}
