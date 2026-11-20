package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// taskSpawn keeps a separate coding session on its caller's task. Unlike a leader's task_plan, it creates no task,
// role or supervision level. The node supplies the caller; none of those identities come from tool arguments.
func (s *Service) taskSpawn(ctx context.Context, machine, session string, args json.RawMessage) (string, error) {
	var a struct {
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("task_spawn arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", errors.New("task_spawn expects one JSON object")
	}
	a.Title, a.Prompt = strings.TrimSpace(a.Title), strings.TrimSpace(a.Prompt)
	if a.Title == "" || a.Prompt == "" {
		return "", errors.New("task_spawn requires title and prompt; nothing was started")
	}
	machine = s.canonical(machine)
	t, ok := s.spawnTask(ctx, machine, session)
	if !ok {
		return "", errors.New("task_spawn requires an active Agent on an open supervised task; ask your supervisor to assign one")
	}
	parent, ok := s.session(ctx, machine, session)
	if !ok || parent.Assistant || parent.Cwd == "" || parent.Launcher == "" {
		return "", errors.New("Persona cannot verify this Agent's directory and launcher; ask your supervisor, nothing was started")
	}
	// Unknown permission is read-only, never the owner's writable default. Codex does not expose PermissionMode, so
	// the persisted assignment supplies it when this is that assignment's original Agent.
	mode := parent.PermissionMode
	assigned := false
	for _, agent := range t.Agents {
		if agent.MachineID == machine && agent.SessionID == session && agent.active() &&
			(agent.Source == "manual" || agent.Source == sourceLeader || agent.Source == sourceWorker) {
			assigned = true
		}
	}
	var assignment *PlanTask
	if assigned {
		if p, i, err := s.currentPlanAssignment(ctx, t); err == nil && p.Tasks[i].Machine == machine {
			assignment = &p.Tasks[i]
		}
	}
	if mode == "" {
		mode = "plan"
		if assignment != nil && assignment.Permission != "" {
			mode = assignment.Permission
		}
	}
	project := t.Project
	if project == "" {
		if _, d, ok := s.directionForTodo(ctx, t); ok {
			project = d.Project
		}
	}
	child := PlanTask{TodoID: t.ID, Project: project, Supervisor: t.Supervision.Supervisor,
		Title: a.Title, Prompt: a.Prompt, Machine: machine, Cwd: parent.Cwd,
		Launcher: parent.Launcher, Model: s.taskSpawnModel(ctx, machine, parent, assignment), Effort: parent.Effort,
		Permission: mode, Readonly: mode == "plan", preservePermission: true,
		launcherDefaultModel: parent.RequestedModel != nil && *parent.RequestedModel == ""}
	// A caller disconnecting must not abandon a start before its ownership is recorded.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	mid, sid, err := s.startTaskAgent(startCtx, &child, false)
	// This path is not wrapped by runPlanTaskStart, so release the gate's claim itself once the child is linked or the
	// start has failed for good (by then the ledger carries the linked agent, or nothing runs).
	if child.admissionRelease != nil {
		defer child.admissionRelease()
	}
	if err != nil {
		return "", fmt.Errorf("task_spawn: %w; do not retry an uncertain start, ask your supervisor", err)
	}
	current, active := s.spawnTask(startCtx, machine, session)
	if !active || current.ID != t.ID {
		err = errors.New("the caller's task changed or closed during the start")
	} else {
		err = s.linkTodoAgentWithSource(startCtx, t.ID, mid, sid, a.Title, child.Cwd, child.startedAgent, "", "spawned", machine+"/"+session)
	}
	if err != nil {
		stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		stopErr := s.call(stopCtx, mid, deviceSessionStop, map[string]any{"session_id": sid}, nil)
		return "", fmt.Errorf("child %s/%s was not linked: %w; stop result: %v; tell your supervisor", mid, sid, err, stopErr)
	}
	t, ok = s.ensureSupervisor(startCtx, t.ID)
	if !ok {
		return "", fmt.Errorf("child %s/%s was linked, but the task closed or lost its supervisor; tell your supervisor", mid, sid)
	}
	out, _ := json.Marshal(map[string]string{"machine_id": mid, "session_id": sid, "todo_id": t.ID,
		"supervisor": t.Supervision.Supervisor, "parent": machine + "/" + session})
	return string(out), nil
}

// An assignment identifies its own Agent's model. Other managed sessions report the exact launch argument,
// including an empty value for the launcher's default. Only older or observed sessions need a catalog lookup.
func (s *Service) taskSpawnModel(ctx context.Context, machine string, parent proto.Session, assignment *PlanTask) string {
	if assignment != nil && assignment.Model != "" && s.startLauncher(ctx, assignment.Launcher) == parent.Launcher {
		return assignment.Model
	}
	if parent.RequestedModel != nil {
		return *parent.RequestedModel
	}
	if !proto.ValidModelID(parent.Model) {
		return parent.Model
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if parent.Host != "" {
		ctx = context.WithValue(ctx, launcherModelHostKey{}, parent.Host)
	}
	catalog, err := s.launcherModelRows(ctx, machine, parent.Launcher, false)
	if err != nil {
		return parent.Model
	}
	// Use the launcher's original rows: the picker folds rows that run the same model. An exact ID always
	// wins, and separate routes or context windows with the same reported resolution are ambiguous.
	match := ""
	for _, row := range catalog.Models {
		if row.ID == parent.Model || row.ResolvedOffered && row.Resolved == parent.Model || slices.Contains(row.Aliases, parent.Model) {
			return parent.Model
		}
		if row.Resolved != parent.Model || !proto.ValidModelID(row.ID) || row.ID == "default" || slices.Contains(unfoldedAliases, strings.TrimSuffix(strings.ToLower(row.ID), "[1m]")) {
			continue
		}
		if match != "" && match != row.ID {
			return parent.Model
		}
		match = row.ID
	}
	if match == "" || s.declaredSpawnModel(ctx, machine, parent) {
		return parent.Model
	}
	return match
}

func (s *Service) spawnTask(ctx context.Context, machine, session string) (Todo, bool) {
	t, ok := s.taskOf(ctx, machine, session)
	return t, session != "" && ok && !t.Cancelled() && t.Supervision != nil && t.Supervision.Supervisor != ""
}

func (s *Service) spawnTools(ctx context.Context, machine, session string) []proto.Tool {
	if _, ok := s.spawnTask(ctx, s.canonical(machine), session); ok {
		return toolsNamed([]string{"task_spawn"})
	}
	return nil
}
