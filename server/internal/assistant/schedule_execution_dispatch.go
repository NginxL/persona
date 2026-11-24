package assistant

import (
	"cmp"
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Task-specific wire parameters and execution evidence stay together; runTask owns the scheduling lifecycle.
func (s *Service) taskSourceParams(ctx context.Context, t Task, text, cwd string, lang i18n.Lang) (map[string]any, error) {
	params := map[string]any{"session_id": t.SessionID, "text": text, "message_source": taskMessageSource(t), "permission_mode": t.PermissionMode, "resume_only": true}
	if cwd != "" {
		params["cwd"], params["task_execution"] = cwd, true
	}
	err := s.sendModel(ctx, t.MachineID, t.SessionID, params, lang)
	params["model_source"] = modelhttp.Source{Role: "routine", Todo: t.TodoID, Routine: t.ID}.Safe()
	return params, err
}

func taskResumeParams(t Task, text string, ex TaskExecution) map[string]any {
	params := map[string]any{"model_source": modelhttp.Source{Role: "routine", Todo: t.TodoID, Routine: t.ID}, "session_id": t.RunSessionID, "text": text, "message_source": taskMessageSource(t), "permission_mode": t.PermissionMode, "resume_only": true, "cwd": ex.Cwd, "task_execution": true}
	if ex.LegacyCompatible {
		params[devicewire.LegacyTaskExecution] = true
	}
	return params
}

func taskStartParams(t Task, text string, ex TaskExecution) startParams {
	return startParams{modelSource: modelhttp.Source{Role: "routine", Todo: t.TodoID, Routine: t.ID}, Text: text, Source: taskMessageSource(t), PermissionMode: t.PermissionMode,
		Purpose: "task." + t.ID, Space: t.Space, Origin: OriginTask, OriginRef: t.ID, MachineID: ex.MachineID, Cwd: ex.Cwd,
		TaskExecution: true, LegacyTaskExecution: ex.LegacyCompatible, PromptSelections: t.PromptSelections,
		TaskModel: &ModelBinding{Provider: launcherProvider(ex.Launcher), Model: ex.Model, Effort: ex.Effort, PermissionMode: ex.PermissionMode}}
}

func (s *Service) sourceTaskExecution(ctx context.Context, t Task, params map[string]any) *TaskExecution {
	c, _ := s.session(ctx, t.MachineID, t.SessionID)
	used, _ := params["launcher"].(string)
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	cwd, _ := params["cwd"].(string)
	runtime, _ := params["runtime"].(string)
	return &TaskExecution{MachineID: t.MachineID, Launcher: used, Model: model, Effort: effort,
		Agent: c.Agent, Cwd: cmp.Or(cwd, c.Cwd), Runtime: runtime, PermissionMode: cmp.Or(t.PermissionMode, c.PermissionMode)}
}

// A pre-dispatch refusal may select the task's own conversation. A lost RPC response is not such proof.
func taskSourceDefinitelyUnsent(err error, id string) bool {
	return errors.Is(err, apierr.ErrOffline) || errors.Is(err, ErrNoDevice) || nodeOutdated(err) || (err != nil && taskConversationUnavailable(err, id))
}

func (s *Service) taskConversationChanged(t Task, ex TaskExecution, override ModelBinding, run *ScheduleRun) bool {
	reason := taskExecutionChangeReason(t, ex, override)
	if reason == "" {
		return false
	}
	run.ConversationReason = reason
	s.logf("task %s: %s; starting a new conversation", t.ID, run.ConversationReason)
	return true
}

func taskExecutionChangeReason(t Task, ex TaskExecution, override ModelBinding) string {
	switch {
	case t.RunExecution != nil && !compatibleTaskExecution(*t.RunExecution, ex):
		return "execution_changed"
	case t.RunExecution == nil && (t.Cwd != "" || t.ExecutionMachineID != "" || override != (ModelBinding{})):
		return "legacy_context"
	}
	return ""
}

func (s *Service) failSourceTaskRun(ctx context.Context, t Task, run ScheduleRun, err error) (proto.Session, error) {
	// ErrNotSent retries this same source after the existing delay; it does not select another conversation.
	return s.failTaskRun(ctx, t, run, err, !errors.Is(err, rpc.ErrNotSent) && nodeDelivered(err) != "none")
}

func taskRunCompatibilityEvidence(ctx context.Context, run *ScheduleRun) {
	if devicewire.LegacyTaskExecutionUsed(ctx) {
		run.CompatibilityFallback = devicewire.LegacyTaskExecutionFallback
	}
}

func (s *Service) failTaskRun(ctx context.Context, t Task, run ScheduleRun, err error, unknown bool) (proto.Session, error) {
	taskRunCompatibilityEvidence(ctx, &run)
	run.Status, run.Error, run.DeliveryUnknown = runFailed, err.Error(), unknown
	s.recordRun(run)
	if !unknown && run.Execution != nil {
		ex := run.Execution
		s.logf("task %s refused before dispatch: retried=%t launcher=%q model=%q effort=%q: %v", t.ID, run.Retried, ex.Launcher, ex.Model, ex.Effort, err)
	}
	if unknown {
		s.oneOffFailed(ctx, t, run)
	} else {
		s.oneOffNotStarted(ctx, t, run, err)
	}
	return proto.Session{}, err
}

// Only structured parameter refusals stop the automatic retry. Provider configuration can change between
// admission and request preparation, so provider/RPC/discovery errors retain their existing recovery path.
func taskConfigurationRefusal(err error) bool {
	var message *i18n.Message
	if !errors.As(err, &message) {
		return false
	}
	switch message.Key {
	case "assistant-models.err_effort_unsupported", "assistant-models.err_model_invalid", "assistant-schedule.task_execution_wait_timeout":
		return true
	}
	return false
}
