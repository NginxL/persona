package assistant

import (
	"cmp"
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Task-specific wire parameters and execution evidence stay together; runTask owns the scheduling lifecycle.
func taskSourceParams(t Task, text, cwd string) map[string]any {
	params := map[string]any{"session_id": t.SessionID, "text": text, "message_source": taskMessageSource(t), "permission_mode": t.PermissionMode, "resume_only": true}
	if cwd != "" {
		params["cwd"], params["task_execution"] = cwd, true
	}
	return params
}

func taskResumeParams(t Task, text string, ex TaskExecution) map[string]any {
	params := map[string]any{"session_id": t.RunSessionID, "text": text, "message_source": taskMessageSource(t), "permission_mode": t.PermissionMode, "resume_only": true, "cwd": ex.Cwd, "task_execution": true}
	if ex.LegacyCompatible {
		params[devicewire.LegacyTaskExecution] = true
	}
	return params
}

func taskStartParams(t Task, text string, ex TaskExecution) startParams {
	return startParams{Text: text, Source: taskMessageSource(t), PermissionMode: t.PermissionMode,
		Purpose: "task." + t.ID, Space: t.Space, Origin: OriginTask, OriginRef: t.ID, MachineID: ex.MachineID, Cwd: ex.Cwd,
		TaskExecution: true, LegacyTaskExecution: ex.LegacyCompatible, TaskModel: &ModelBinding{Provider: launcherProvider(ex.Launcher), Model: ex.Model, Effort: ex.Effort, PermissionMode: ex.PermissionMode}}
}

func (s *Service) sourceTaskExecution(ctx context.Context, t Task, params map[string]any) *TaskExecution {
	c, _ := s.session(ctx, t.MachineID, t.SessionID)
	used, _ := params["launcher"].(string)
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	return &TaskExecution{MachineID: t.MachineID, Launcher: used, Model: model, Effort: effort, Agent: c.Agent, Cwd: c.Cwd, PermissionMode: cmp.Or(t.PermissionMode, c.PermissionMode)}
}

// A pre-dispatch refusal may select the task's own conversation. A lost RPC response is not such proof.
func taskSourceDefinitelyUnsent(err error, id string) bool {
	return errors.Is(err, apierr.ErrOffline) || errors.Is(err, ErrNoDevice) || nodeOutdated(err) || (err != nil && taskConversationUnavailable(err, id))
}

func (s *Service) taskConversationChanged(t Task, ex TaskExecution, override ModelBinding, run *ScheduleRun) bool {
	switch {
	case t.RunExecution != nil && !compatibleTaskExecution(*t.RunExecution, ex):
		run.ConversationReason = "execution_changed"
		s.logf("task %s: execution context changed; starting a new conversation", t.ID)
	case t.RunExecution == nil && (t.Cwd != "" || t.ExecutionMachineID != "" || override != (ModelBinding{})):
		run.ConversationReason = "legacy_context"
		s.logf("task %s: previous execution context is unknown; starting a new conversation", t.ID)
	default:
		return false
	}
	return true
}

func (s *Service) failSourceTaskRun(ctx context.Context, t Task, run ScheduleRun, err error) (proto.Session, error) {
	// ErrNotSent retries this same source after the existing delay; it does not select another conversation.
	return s.failTaskRun(ctx, t, run, err, !errors.Is(err, rpc.ErrNotSent))
}

func (s *Service) failTaskRun(ctx context.Context, t Task, run ScheduleRun, err error, unknown bool) (proto.Session, error) {
	run.Status, run.Error, run.DeliveryUnknown = runFailed, err.Error(), unknown
	s.recordRun(run)
	if unknown {
		s.oneOffFailed(ctx, t, run)
	} else {
		s.oneOffNotStarted(ctx, t, run)
	}
	return proto.Session{}, err
}
