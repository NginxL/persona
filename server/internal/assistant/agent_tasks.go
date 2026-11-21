package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Every agent Persona starts belongs to a task (#3617, docs/assistant/unified-entry.md "Agent tasks"). While
// assistant.agent_tasks is on (off by default: a supervised task costs model calls, the judge's when its agent stops
// and the supervisor's turns), an agent Persona started that names no task gets a quiet, supervised todo, made of the
// pieces the main conversation's own tasks are made of (quietAgentTask, adoptAgent, the todo_supervision ledger):
//
//   - agent_start from any conversation makes the todo at once (quietAgentTask);
//   - an agent an app started (New Agent, POST /api/v1/sessions without todo_id) gets one when a turn of it ends having
//     done work (agentTaskAtTurnEnd): a session that only answers a question leaves no todo behind;
//   - a todo the user made by hand gets a supervisor when an agent is linked to it (superviseLinkedTodo), and any open
//     todo's agent that reports gets one then (superviseOpenTodoOf), so report_to_supervisor has a receiver.
//
// The supervisor is the conversation that started the agent when it supervises (the main conversation, a leader), else
// the main conversation. Agents the user starts by hand in a terminal are not Persona's and are left alone. With the
// setting off none of this runs.

// agentTasks: assistant.agent_tasks is on (off by default).
func (s *Service) agentTasks(ctx context.Context) bool {
	v := s.settings(ctx).AgentTasks
	return v != nil && *v
}

// agentTaskSupervisor is the conversation that supervises the task of an agent conversation started ("" for an app's
// start or the user's own todo): conversation itself when it supervises, else the main conversation. false while
// assistant.agent_tasks is off or there is no main conversation to fall back to.
func (s *Service) agentTaskSupervisor(ctx context.Context, conversation string) (string, bool) {
	if !s.agentTasks(ctx) {
		return "", false
	}
	if s.supervises(ctx, conversation) {
		return conversation, true
	}
	main := s.settings(ctx).Main
	return main, main != ""
}

// ensureSupervisor gives open todo id a supervisor when it has none (agentTaskSupervisor of the conversation that
// opened it), with no done_when: the todo with its ledger, false when it stays unsupervised. A reporting
// conversation's task stays unsupervised so its ordinary quiet notices are unchanged; superviseOpenTodoOf routes its
// Agent's report through the reporter's own delivery path instead.
func (s *Service) ensureSupervisor(ctx context.Context, id string) (Todo, bool) {
	t, ok := s.todoNow(id)
	if !ok || t.DoneAt != nil {
		return Todo{}, false
	}
	if t.Supervision != nil && t.Supervision.Supervisor != "" {
		return t, true
	}
	supervisor, ok := s.agentTaskSupervisor(ctx, t.Origin)
	if !ok {
		return Todo{}, false
	}
	if _, reports := s.reporterOf(ctx, t.Origin); reports {
		return Todo{}, false
	}
	v, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if v.Supervisor != "" {
			return false
		}
		v.Supervisor = supervisor
		return true
	})
	if err != nil {
		s.logf("task %s: supervision: %v", id, err)
		return Todo{}, false
	}
	t.Supervision = &v
	s.logf("task %s: supervised by %s (agent tasks)", id, short(supervisor))
	return t, true
}

// superviseLinkedTodo: the user linked an agent to todo id. One they made by hand gets a supervisor (ensureSupervisor);
// a conversation's task keeps what it has.
func (s *Service) superviseLinkedTodo(ctx context.Context, id string) {
	if !s.agentTasks(ctx) {
		return
	}
	if t, ok := s.todoNow(id); ok && t.Origin == "" {
		s.ensureSupervisor(ctx, id)
	}
}

// superviseOpenTodoOf is the open todo agent machine/session works for, whoever opened it, with a supervisor for its
// report. An active reporting conversation's todo keeps its stored state unsupervised: only the returned copy names
// that conversation, and reportNow delivers through the reporter's existing notice path. Other unsupervised todos get
// a ledger (ensureSupervisor). false: it is on no open todo, or that todo stays unsupervised.
func (s *Service) superviseOpenTodoOf(ctx context.Context, machine, session string) (Todo, bool) {
	if !s.agentTasks(ctx) {
		return Todo{}, false
	}
	t, ok := s.openTodoOfAgent(machine, session)
	if !ok {
		return Todo{}, false
	}
	if _, reports := s.reporterOf(ctx, t.Origin); reports && !s.reporterStopped(ctx, t.Origin) {
		t.Supervision = &Supervision{Supervisor: t.Origin}
		return t, true
	}
	return s.ensureSupervisor(ctx, t.ID)
}

// agentTaskAtTurnEnd makes agent sid on machine a task when its turn ended having done work and it is on no todo: a
// quiet todo of the main conversation, which supervises it (adoptAgent, as agent_supervise makes one, with its card in
// the conversation). Only an agent Persona started, never one of a terminal, an assistant conversation
// (assistantsConversation), a runtime's session, a review's reviewer, or an agent that was on a todo before (a closed
// task's agent stays that task's). The task, false when none was made.
func (s *Service) agentTaskAtTurnEnd(ctx context.Context, machine, sid string) (Todo, bool) {
	main, ok := s.agentTaskSupervisor(ctx, "")
	if !ok {
		return Todo{}, false
	}
	c, ok := s.session(ctx, machine, sid)
	if !ok || !c.StartedByPersona || c.SourceKind != "" || c.Starting || c.Archived ||
		c.Status == proto.StatusWorking || s.isReviewer(machine, sid) || s.assistantsConversation(ctx, machine, c) {
		return Todo{}, false
	}
	if s.ensureTodosLoaded(ctx) != nil || s.everOnTodo(machine, sid) {
		return Todo{}, false
	}
	am, ok := s.device(ctx, machine)
	if !ok {
		return Todo{}, false
	}
	items, read := s.lastTurnItems(ctx, machine, sid)
	if !read || !turnDidWork(items) {
		return Todo{}, false
	}
	ask, _ := turnExchange(items)
	notes := ask
	if notes == "" {
		notes = i18n.T(s.lang(ctx), "assistant-tasks.adopt_notes", i18n.Args{"agent": agentLabel(c), "machine": am.Name, "cwd": c.Cwd})
	}
	todo, made, err := s.adoptAgentAs(ctx, main, am, c, "", ask, notes, nil, 0, false, true)
	if err != nil || !made {
		if err != nil {
			s.logf("agent task %s on %s: %v", short(sid), machine, err)
		}
		return Todo{}, false
	}
	s.taskCard(ctx, main, todo.ID, wakeActive)
	return s.taskOf(ctx, machine, sid)
}

// assistantsConversation: session c on machine is an assistant's conversation, never an agent to make a task of
// (#10801). Its row's Assistant flag alone does not tell: the flag says it runs in this manager's assistant directory
// on machine as the computer's home names it (isAssistantSession), and the node marks every conversation it keeps a
// start record for started_by_persona. So it is one too when
//   - it is a headless row in an assistant or health directory of any manager (identity.IsAssistantDir): another
//     manager's, left on a computer that moved here (docs/assistant/managers.md "Moving a machine or a source"), or
//     this one's, listed before the computer reported its home;
//   - this manager's records say its assistant started it, wherever its row is listed (an older node's row at rest
//     may follow a `cd`): the main conversation, the background supervisor, and every conversation with a noted origin
//     (originSet: the apps' new chats and POST /api/v1/assistant/conversations, a channel's or an extension's /agent,
//     scheduled tasks, guests, meeting rooms).
//
// Session.Assistant is left as it is: it decides the Chat tab and which conversations this assistant's tools reach,
// and another manager's conversations are none of those.
func (s *Service) assistantsConversation(ctx context.Context, machine string, c proto.Session) bool {
	if s.isAssistantRow(c) || c.PaneID == "" && c.Cwd != "" && identity.IsAssistantDir(c.Cwd) {
		return true
	}
	// Each on the machine it was recorded on: a session ID is only unique on its own computer.
	if cfg := s.settings(ctx); c.SessionID != "" &&
		(c.SessionID == cfg.Main && s.sameMachine(machine, cfg.mainRouteMachine()) ||
			c.SessionID == cfg.BackgroundSupervisor && s.sameMachine(machine, cfg.BackgroundSupervisorMachineID)) {
		return true
	}
	return s.originNoted(ctx, machine, c.SessionID)
}

// everOnTodo: agent mid/sid is or was on a todo (open or done, active or released).
func (s *Service) everOnTodo(mid, sid string) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if _, ok := s.todos.activeBySession[[2]string{mid, sid}]; ok {
		return true
	}
	for _, t := range s.todos.todos {
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid {
				return true
			}
		}
	}
	return false
}

// workTools are the tools that edit files, run commands or hand work to subagents, by the names Claude Code and
// Codex write them: a turn that calls one did work (turnDidWork).
var workTools = map[string]bool{
	"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true, "apply_patch": true,
	"Bash": true, "shell": true, "exec_command": true, "local_shell": true, "shell_command": true,
	"Task": true, "Agent": true,
}

// turnDidWork: the turn's items have an edit, a command or a subagent (workTools). A turn that only read, searched
// and answered is a question and its answer.
func turnDidWork(items []proto.Item) bool {
	for _, it := range items {
		if it.Kind == proto.KindToolUse && workTools[it.ToolName] {
			return true
		}
	}
	return false
}
