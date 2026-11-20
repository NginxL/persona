package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the assistant's task_plan tool ----

// planTaskIn is a task as task_plan's arguments have it. Its todo_id is the todo to run on (PlanTask.ReuseTodo); the
// plan's own todo_id, the todo a started task became, is the hub's to set, and the shallower field wins in JSON.
type planTaskIn struct {
	PlanTask
	TodoID string `json:"todo_id"`
}

// toolTaskPlanFrom is task_plan from the conversation sessionID running on machine (where its attachments are).
func (s *Service) toolTaskPlanFrom(ctx context.Context, machine, sessionID string, args json.RawMessage) (string, error) {
	return s.taskPlanFrom(ctx, machine, sessionID, args, nil)
}

// taskPlanFrom is task_plan from sessionID: a conversation (scope nil), or the agent of the task leader's task in scope,
// which plans workers beneath that task only and starts them at once (task_leader.go).
func (s *Service) taskPlanFrom(ctx context.Context, machine, sessionID string, args json.RawMessage, scope *leaderScope) (string, error) {
	wait, err := agentWaitDuration(args)
	if err != nil {
		return "", err
	}
	var a struct{ Tasks []PlanTask }
	lead := "" // the leader conversation the main conversation hands the plan to (task_plan's supervisor, #3608)
	if scope != nil {
		tasks, err := scope.workerTasks(args)
		if err != nil {
			return "", err
		}
		a.Tasks = tasks
	} else {
		var in struct {
			Tasks      []planTaskIn `json:"tasks"`
			Supervisor string       `json:"supervisor"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("tasks: %v", err)
		}
		var err error
		if lead, err = s.planSupervisor(ctx, sessionID, in.Supervisor); err != nil {
			return "", err
		}
		for _, t := range in.Tasks {
			t.PlanTask.ReuseTodo = strings.TrimSpace(t.TodoID)
			t.PlanTask.Parent = "" // the hub's, set only for a task leader's workers
			t.PlanTask.Supervisor = lead
			a.Tasks = append(a.Tasks, t.PlanTask)
		}
	}
	if len(a.Tasks) == 0 {
		return "", fmt.Errorf("tasks is empty")
	}
	if len(a.Tasks) > 8 {
		return "", fmt.Errorf("at most 8 tasks in one plan")
	}
	if scope != nil {
		if err := s.checkLeaderRoom(ctx, scope, len(a.Tasks)); err != nil {
			return "", err
		}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("todos are unavailable: %v", err)
	}
	reused := map[string]bool{}
	modes := map[string]string{} // each computer's Claude Code default mode, read once for this plan
	for i := range a.Tasks {
		t := &a.Tasks[i]
		t.WaitInputID = ""
		t.BackendStartDigest = ""
		t.WorkspaceError = nil
		asked := strings.TrimSpace(t.Permission) // the caller's own, before read-only replaces it
		if t.Readonly {
			asked = ""
		}
		t.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
		t.TodoID, t.Error, t.ErrorCode, t.ErrorArgs, t.RetryWith = "", "", "", nil, nil
		t.WorkspaceResolved, t.ReservedTodoID, t.WorkspaceInferred = nil, "", false
		t.OwnerStarted = false // the hub's: only the owner's Start on the card sets it (startPlan)
		t.RequestGate, t.Queue = "", nil
		t.CloseTabOnAccept, t.TerminalTarget, t.TerminalCleanup = false, taskTerminalTarget{}, ""
		t.TerminalAuthorized = false
		// The hub's too: the scheduled round this plan comes from is the hub's to tell (scheduledRoundOf, #8045).
		t.Schedule, t.ScheduleTitle, t.ScheduleBasis = "", "", ""
		// Reroute is hub-owned: drop any reroute a model passes in task_plan args, so healthyPlace seeds it only from a
		// real health detour and its first-detour guard keeps the true original machine (#2271).
		t.Reroute = nil
		t.Title, t.Prompt, t.Cwd = strings.TrimSpace(t.Title), strings.TrimSpace(t.Prompt), strings.TrimSpace(t.Cwd)
		t.Model = strings.TrimSpace(t.Model)
		t.Speed = strings.TrimSpace(t.Speed)
		t.Permission = strings.TrimSpace(t.Permission)
		if err := t.checkRole(scope != nil); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if m, ok := s.machineByName(ctx, t.Machine); ok && t.Role == roleLeader && s.isWorkMachine(m.ID) {
			return "", fmt.Errorf("task %d: role \"leader\" cannot run on %s: a work-trust computer gives its agents only the "+
				"reporting tools, so it could not supervise workers. Put the task leader on a computer you own", i+1, m.Name)
		}
		if err := t.normalizeMachinePolicy(); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.ExpectMinutes < 0 {
			return "", fmt.Errorf("task %d: expect_minutes must be zero or a positive number of minutes", i+1)
		}
		t.DoneWhen, t.ExpectMinutes = cleanDoneWhen(t.DoneWhen), min(t.ExpectMinutes, 24*60)
		if t.Title == "" || t.Prompt == "" || t.Cwd == "" && !t.backendWorkspace() {
			return "", fmt.Errorf("task %d: title, prompt and cwd are required", i+1)
		}
		t.Brief = nil // the hub's (planBriefs below), never the tool call's
		t.Goal, t.OutOfBounds = clip(oneLine(t.Goal), 300), cleanDoneWhen(t.OutOfBounds)
		t.Continues, t.ContinuesWhy = strings.TrimSpace(t.Continues), clip(oneLine(t.ContinuesWhy), 300)
		t.StartedWhy = clip(oneLine(t.StartedWhy), 300)
		t.EstimateSource = ""
		if t.Continues == "" {
			t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, "")
		}
		if err := briefRequired(t); err != nil && !(s.briefsOptional && t.Continues == "") {
			return "", fmt.Errorf("task %d (%s): %w", i+1, t.Title, err)
		}
		if scope == nil {
			if err := s.takeOver(ctx, sessionID, t); err != nil {
				return "", fmt.Errorf("task %d: %v", i+1, err)
			}
		}
		t.InboxHandoff = ""
		if err := s.planInboxSource(ctx, machine, sessionID, t); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.Model != "" && !proto.ValidModelID(t.Model) { // it goes to the agent as --model (#1644)
			return "", fmt.Errorf("task %d: model %q is not a model id: %s", i+1, t.Model, proto.ModelIDRule)
		}
		if scope == nil { // a task leader has none to give and keeps its task's project
			atts, err := cleanAttachments(t.Attachments)
			if err == nil {
				atts, err = s.checkAttachments(ctx, machine, sessionID, atts)
			}
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			t.Attachments, t.Mentioned, t.AttachFrom = atts, s.mentionedAttachments(ctx, machine, sessionID, t.Prompt+"\n\n"+t.Context, atts), ""
			if t.Project, err = s.todoProject(ctx, t.Project); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
		}
		if len(t.Attachments)+len(t.Mentioned) > 0 {
			t.AttachFrom = machine
		}
		if !s.placeTask(ctx, t) {
			return "", fmt.Errorf("task %d: unknown machine %q (machines: %s)", i+1, t.Machine, s.machineIDs(ctx))
		}
		// Validate the tier now without pinning its model: it is resolved again at dispatch.
		preview := *t
		if err := s.applyTaskTier(ctx, &preview); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		s.healthyPlace(ctx, &preview) // health follows the resolved launcher
		t.Machine, t.Cwd, t.Why = preview.Machine, preview.Cwd, preview.Why
		t.Reroute = preview.Reroute
		// What it runs on the computer it lands on (#2640): a default that computer lacks is swapped now, so the card
		// names the launcher that will run and its why says so; a launcher this plan names that it lacks is refused.
		takesModes := true
		if _, ok := s.machineByName(ctx, t.Machine); ok {
			own := t.launchPick()
			fit, err := s.fitPlanTaskSpeed(ctx, t)
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			if fit.allowAll() {
				t.Permission = "bypassPermissions"
			}
			// traex, omp…: no mode, no effort (launcher_modes.go). One the call names is refused now, rather than by
			// the computer once the task starts; one a tier filled in is dropped. pi, cursor, agy: dropped either way.
			if takesModes = startTakesModes(fit.agent); !takesModes {
				if err := startModesRefused(t.Launcher, fit.agent, asked, own.effort); err != nil {
					return "", fmt.Errorf("task %d: %w", i+1, err)
				}
				t.Effort = ""
				if asked != "" { // a work agent's, not refused: the card shows none, as none will run
					t.Permission = ""
				}
			}
		}
		// Unset: settled on the computer it lands on, so the card shows what will run (tasks_permission.go). An agent
		// that takes no mode gets none: a read-only task keeps its plan, which only says it is read-only.
		if t.Permission == "" && takesModes {
			t.Permission = s.taskPermission(ctx, t.Machine, modes)
		}
		if err := s.resolveTaskWorkspace(ctx, t); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		s.resolveTaskRepo(ctx, t) // the kind planStartsAtOnce looks up
	}
	for i, t := range a.Tasks {
		if t.ReuseTodo == "" {
			continue
		}
		if reused[t.ReuseTodo] {
			return "", fmt.Errorf("task %d: todo_id %s is already the todo of another task in this plan", i+1, t.ReuseTodo)
		}
		reused[t.ReuseTodo] = true
		if t.Continues == "" {
			if _, err := s.reusableTodo(ctx, sessionID, t.ReuseTodo, false); err != nil {
				return "", fmt.Errorf("task %d: %v", i+1, err)
			}
		}
	}
	if scope == nil {
		for i := range a.Tasks {
			if err := s.planPieces(&a.Tasks[i]); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			a.Tasks[i].RequestGate = s.planRequestGate(a.Tasks[i])
		}
	}
	if err := s.planBriefs(ctx, machine, sessionID, a.Tasks); err != nil {
		return "", err
	}
	// A round the hub started for a scheduled task (schedule_grant.go, #8045): its plan names the schedule, so the card
	// can offer to let it start these by itself, and that schedule's grant may start them now. Never in the health
	// space, whose plans always wait for Start.
	if scope == nil && slices.ContainsFunc(a.Tasks, func(t PlanTask) bool { return t.UserRequested }) && s.inScheduledTurn(ctx, machine, sessionID) {
		// A round the hub started has no message of the owner's: "the user asked for this" is only the model's word.
		for i := range a.Tasks {
			a.Tasks[i].UserRequested = false
		}
	}
	var round Task
	var inRound bool
	if scope == nil && !s.inHealth(ctx, sessionID) {
		if round, inRound = s.scheduledRoundOf(ctx, machine, sessionID); inRound && round.grantEligible() && s.claudeConversation(ctx, machine, sessionID) {
			for i := range a.Tasks { // the card offers to let this schedule start these by itself
				a.Tasks[i].Schedule, a.Tasks[i].ScheduleTitle, a.Tasks[i].ScheduleBasis = round.ID, round.labelIn(s.lang(ctx)), round.runGrantBasis
			}
		}
	}
	if wait > 0 && len(a.Tasks) == 1 && scope == nil {
		a.Tasks[0].WaitInputID = "wait_" + randomHex(16)
	}
	dups := s.planDuplicates(ctx, a.Tasks) // before the plan's own todos exist
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: sessionID, Tasks: a.Tasks, State: planOpen, CreatedAt: now, UpdatedAt: now}
	if err := s.savePlan(ctx, p); err != nil {
		return "", err
	}
	// The plan goes in the item's body too: a client that missed the task_plan event (or raced it) still draws the
	// card from the conversation itself.
	if scope == nil {
		if _, err := s.appendFeed(ctx, sessionID, feedPlan, p.ID, map[string]any{"plan": p}); err != nil {
			s.logf("task plan %s: feed: %v", p.ID, err)
		}
	}
	// A plan from the health space hands its prompts to agents outside it: it waits for the user's "Start" on its card,
	// whatever would start it at once, and its push names no task (health.go).
	health := s.inHealth(ctx, sessionID)
	if scope != nil { // no card: a leader's workers start at once, as part of the task the user let start
		started, err := s.startPlan(ctx, p.ID, nil, false)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Queued %d worker(s) beneath your task %s for dispatch:\n", len(started.Tasks), scope.task.ID)
		for _, t := range started.Tasks {
			if t.Error != "" {
				fmt.Fprintf(&b, "- %s on %s: dispatch failed: %s\n", t.Title, t.Machine, t.Error)
			} else {
				fmt.Fprintf(&b, "- %s → todo %s on %s (dispatching)\n", t.Title, t.TodoID, t.Machine)
			}
		}
		b.WriteString("This is not confirmation that any agent started: you are told when one fails to start. A worker that has " +
			"report_to_supervisor reports to you in a message, and they cannot hand work on.")
		b.WriteString(dups)
		return b.String(), nil
	}
	var granted func() func(PlanTask) bool
	if inRound {
		granted = func() func(PlanTask) bool { return s.scheduleGrantCover(ctx, round, machine, sessionID) }
	}
	if health || slices.ContainsFunc(p.Tasks, func(task PlanTask) bool { return task.RequestGate != "" }) || !s.planStartsAtOnceUnder(ctx, p.Tasks, granted) {
		s.event(ctx, map[string]any{"type": "task_plan", "plan": s.planWithSuggestion(ctx, p, s.lang(ctx))})
		// A card below the answer is easy to miss: push it as well.
		titles := make([]string, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			titles = append(titles, t.Title)
		}
		l := s.lang(ctx)
		title := i18n.T(l, "assistant-tasks.plan_waiting", i18n.Args{"n": len(p.Tasks)})
		if len(titles) == 1 {
			title = i18n.T(l, "assistant-tasks.plan_waiting_single")
		}
		here := s.settings(ctx).MachineID
		machineName := "Persona"
		if m, ok := s.device(ctx, here); ok {
			machineName = m.Name
		}
		body := strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep"))
		if health {
			title, body = healthNotice(l)
		}
		s.push(ctx, Notification{Subtitle: machineName, DeviceID: here, SessionID: sessionID, Title: title, Body: body})
		if lead != "" || s.isLeader(ctx, sessionID) {
			s.lreports.plansDue.Store(true)
			s.directionsSoon() // a leader's plan waiting for Start is a decision for the owner (leader_reports.go)
		}
		return fmt.Sprintf("Plan %s is on the user's screen as a card and pushed to their phone; nothing has started. "+
			"Don't wait for it: carry on. When they start it, the tasks show up in the context snapshot.", p.ID) + dups, nil
	}
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	supervisor := ""
	fmt.Fprintf(&b, "Queued %d task(s) for dispatch without asking (read-only, asked for by the user, a kind the user trusts, or one "+
		"the user lets this scheduled task start):\n", len(started.Tasks))
	for _, t := range started.Tasks {
		if t.Error != "" {
			fmt.Fprintf(&b, "- %s on %s: dispatch failed: %s\n", t.Title, t.Machine, t.Error)
		} else {
			fmt.Fprintf(&b, "- %s → todo %s on %s (dispatching)\n", t.Title, t.TodoID, t.Machine)
			if todo, ok := s.todoNow(t.TodoID); ok && todo.Supervision != nil {
				supervisor = todo.Supervision.Supervisor
			}
		}
	}
	b.WriteString("This is not confirmation that any agent started. Startup can still fail; the task cards and context snapshot show the outcome.")
	cfg := s.settings(ctx)
	background := s.backgroundOn(cfg)
	switch {
	case supervisor == "":
	case supervisor != sessionID && supervisor != cfg.Main:
		fmt.Fprintf(&b, " Leader conversation %s supervises them: their supervision messages go there, and it reviews them with task_review.", supervisor)
	case background && s.isBackground(ctx, sessionID):
		b.WriteString(" You follow them up as the background supervisor: a supervision message comes when one needs you (review it with task_review).")
	case background && s.isMain(ctx, sessionID):
		b.WriteString(" The background supervisor follows them up: their supervision messages go to it and it reviews them with " +
			"task_review, so none comes to you for them. You still hear of a start that failed and of what it hands to the user, " +
			"and may review any of them yourself (task_review).")
	case supervisor == sessionID:
		b.WriteString(" You supervise them: a supervision message comes when one needs you (review it with task_review).")
	case background:
		b.WriteString(" The background supervisor follows them up (their supervision messages and task_review); this conversation " +
			"gets a one-line notice with the task's link when each one ends.")
	default:
		b.WriteString(" The main conversation supervises them (its supervision messages and task_review); this conversation " +
			"gets a one-line notice with the task's link when each one ends.")
	}
	b.WriteString(dups)
	if wait > 0 && len(started.Tasks) == 1 && scope == nil && started.Tasks[0].Error == "" {
		t := started.Tasks[0]
		return s.waitAgentReply(ctx, b.String(), t.WaitInputID, wait, func(ctx context.Context) (string, string, error) {
			todo, ok, err := s.todo(ctx, t.TodoID)
			if !ok || err != nil {
				return "", "", nil
			}
			if todo.StartError != "" {
				return "", "", errors.New(todo.StartError)
			}
			if len(todo.Agents) > 0 {
				a := todo.Agents[len(todo.Agents)-1]
				return a.MachineID, a.SessionID, nil
			}
			return "", "", nil
		}), nil
	}
	return b.String(), nil
}

// planSupervisor is task_plan's supervisor from conversation self: "" (self supervises, as before) or a current leader,
// which only the main conversation may hand work to.
func (s *Service) planSupervisor(ctx context.Context, self, lead string) (string, error) {
	lead = strings.TrimSpace(lead)
	switch {
	case lead == "" || lead == self:
		return "", nil
	case !s.isMain(ctx, self):
		return "", errors.New("supervisor: only the main conversation hands work to a leader conversation; leave it out to supervise the tasks yourself")
	case !s.isLeader(ctx, lead):
		return "", fmt.Errorf("supervisor: %s is not a leader conversation (assistant.leaders lists them); leave it out to supervise the tasks yourself", lead)
	}
	return lead, nil
}

// applyPlanEdit takes what the user changed on the card; an empty field keeps the assistant's.
func applyPlanEdit(t *PlanTask, e PlanTask) {
	if e.Workspace != nil {
		t.Workspace, t.WorkspaceResolved, t.WorkspaceInferred = e.Workspace, nil, false
	}
	if s := strings.TrimSpace(e.Title); s != "" {
		t.Title = s
	}
	if s := strings.TrimSpace(e.Prompt); s != "" {
		t.Prompt = s
	}
	if s := strings.TrimSpace(e.Machine); s != "" {
		if s != t.Machine {
			t.MachinePolicy = machineFixed
			// The user's explicit retarget supersedes a plan-time health detour: drop the stale reroute so the receipt
			// names the machine the task will actually run on, not the old landing (#2271).
			t.Reroute = nil
		}
		t.Machine = s
	}
	if e.MachinePolicy != "" {
		t.MachinePolicy = e.MachinePolicy
	}
	if s := strings.TrimSpace(e.Cwd); s != "" {
		t.Cwd = s
	}
	if s := strings.TrimSpace(e.Launcher); s != "" {
		t.Launcher = s
	}
	if s := strings.TrimSpace(e.Model); s != "" {
		t.Model = s
	}
	if s := strings.TrimSpace(e.Tier); s != "" {
		t.Tier = s
	}
	if s := strings.TrimSpace(e.Effort); s != "" {
		t.Effort = s
	}
	if s := strings.TrimSpace(e.Speed); s != "" {
		t.Speed = s
	}
	if s := strings.TrimSpace(e.Permission); s != "" {
		t.Permission = s
	}
	if e.Ultracode != nil {
		t.Ultracode = e.Ultracode
	}
}

func planErr(w http.ResponseWriter, err error) {
	var invalid planInvalid
	switch {
	case errors.As(err, &invalid):
		apierr.Write(w, http.StatusBadRequest, invalid.error)
	case errors.Is(err, errPlanNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errPlanNotOpen):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}

// checkRole refuses a role a plan task may not have, before anything is planned. Any conversation may hand out a
// leader: its own reports reach the conversation that planned it. A task leader's workers are never one.
func (t *PlanTask) checkRole(byLeader bool) error {
	switch {
	case t.Role == "":
	case byLeader:
		return errors.New("a task leader plans workers only: role is refused")
	case t.Role != roleLeader:
		return fmt.Errorf("role %q is not one: leave it out, or \"leader\"", t.Role)
	}
	return nil
}
