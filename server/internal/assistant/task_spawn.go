package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if mode == "" {
		mode = "plan"
		assigned := false
		for _, agent := range t.Agents {
			if agent.MachineID == machine && agent.SessionID == session && agent.active() &&
				(agent.Source == "manual" || agent.Source == sourceLeader || agent.Source == sourceWorker) {
				assigned = true
			}
		}
		if p, err := s.loadPlan(ctx, t.PlanID); assigned && err == nil {
			for _, task := range p.Tasks {
				if task.TodoID == t.ID && task.Machine == machine && task.Permission != "" {
					mode = task.Permission
					break
				}
			}
		}
	}
	child := PlanTask{TodoID: t.ID, Title: a.Title, Prompt: a.Prompt, Machine: machine, Cwd: parent.Cwd,
		Launcher: parent.Launcher, Model: parent.Model, Effort: parent.Effort, Permission: mode, Readonly: mode == "plan",
		preservePermission: true}
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
		err = s.linkTodoAgentWithSource(startCtx, t.ID, mid, sid, a.Title, child.Cwd, "", "", "spawned", machine+"/"+session)
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
