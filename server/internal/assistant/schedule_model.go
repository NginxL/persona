package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/nwverse/persona/server/internal/i18n"
)

// errTaskModelNeedsOwnConversation: a task that runs into the conversation it came from keeps that conversation's
// launcher, so its own launcher / model / effort would never be used.
var errTaskModelNeedsOwnConversation = errors.New("launcher / model / effort need the task to run in its own conversation (pass new_conversation: true for a follow-up)")

// hasTaskModel says whether a schedule tool set any of the task's own launcher, model or effort.
func hasTaskModel(launcher, model, effort *string) bool {
	return launcher != nil || model != nil || effort != nil
}

// taskModel is a scheduled task's own launcher / model / effort from the schedule tools (schedule_routine,
// schedule_followup). A nil field is left as it is; an empty launcher (or model, or effort) clears that part, and
// clearing the launcher returns the task to Main chat. Validation is the one PATCH /model-config applies: the launcher
// has to be one the task's machine starts, and a model is checked on that machine. With save false it only validates
// (an existing task, before its schedule is changed).
func (s *Service) taskModel(ctx context.Context, taskID string, launcher, model, effort *string, save bool) (ModelBinding, error) {
	if !hasTaskModel(launcher, model, effort) {
		return ModelBinding{}, nil
	}
	if launcher != nil && *launcher != "" && !launcherName.MatchString(*launcher) {
		return ModelBinding{}, i18n.Err("assistant-schedule.bad_launcher", i18n.Args{"launcher": strconv.Quote(*launcher)})
	}
	ctx = withSettingsMemo(ctx)
	id := "task." + taskID
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	if save {
		// An owner save takes the same model lock before changing the schedule.
		// Recheck the current task after the tool's earlier schedule write.
		sc, err := s.lockedSchedule(ctx)
		if err != nil {
			return ModelBinding{}, err
		}
		i := sc.indexLocked(taskID)
		selected := i >= 0 && sc.tasks[i].PromptSelections
		sc.mu.Unlock()
		if selected {
			return ModelBinding{}, errTaskSelectionsOwner
		}
	}
	providers := s.modelProviders(ctx)
	p, err := s.modelPurpose(ctx, id, providers)
	if err != nil {
		return ModelBinding{}, err
	}
	if len(p.Fields) == 0 {
		return ModelBinding{}, i18n.Err("assistant-models.err_purpose_readonly")
	}
	in := modelPatch{Model: jsonStringOrNull(model), Effort: jsonStringOrNull(effort)}
	if launcher != nil {
		if *launcher == "" {
			in.Provider = json.RawMessage("null")
		} else {
			in.Provider, _ = json.Marshal(launcherProvider(*launcher))
		}
	}
	b, err := patchModel(p.Override, in)
	if err != nil {
		return ModelBinding{}, err
	}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return ModelBinding{}, err
	}
	if b.Provider == "" && (nonEmpty(model) || nonEmpty(effort)) && base.Provider != "" {
		// A model or effort without a launcher is checked against the launcher the task runs on now; store that
		// launcher too, or a later change to Main chat could pair the model with another agent.
		b.Provider = base.Provider
	}
	if p.Kind == "launcher" {
		if err := checkModelID(b.Model); err != nil {
			return ModelBinding{}, err
		}
	}
	effective := mergeModel(base, b)
	if b != (ModelBinding{}) {
		if err := s.validateModel(ctx, p, effective, providers); err != nil {
			return ModelBinding{}, err
		}
	}
	if !save {
		return b, nil
	}
	return b, s.saveModelRecord(ctx, id, p.Revision, b)
}

// setTaskModel writes the task's own model configuration from the schedule tools.
func (s *Service) setTaskModel(ctx context.Context, taskID string, launcher, model, effort *string) error {
	_, err := s.taskModel(ctx, taskID, launcher, model, effort, true)
	return err
}

// validateTaskModel checks what setTaskModel would write without writing it.
func (s *Service) validateTaskModel(ctx context.Context, taskID string, launcher, model, effort *string) error {
	_, err := s.taskModel(ctx, taskID, launcher, model, effort, false)
	return err
}

// forgetTaskModel makes a deleted task's override inert, so a later task that happened to get the same id does not
// inherit it. Best effort: a task without a record writes nothing.
func (s *Service) forgetTaskModel(ctx context.Context, taskID string) {
	id := "task." + taskID
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	r, err := s.modelRecord(ctx, id)
	if err != nil || (r.Revision == 0 && r.Binding == (ModelBinding{})) {
		return
	}
	if err := s.saveModelRecord(ctx, id, r.Revision, ModelBinding{}); err != nil {
		s.logf("task %s: clear its model configuration: %v", taskID, err)
	}
}

func nonEmpty(v *string) bool { return v != nil && *v != "" }

// jsonStringOrNull is a tool argument as a sparse patch field: nil leaves the field out, "" clears it (null).
func jsonStringOrNull(v *string) json.RawMessage {
	if v == nil {
		return nil
	}
	if *v == "" {
		return json.RawMessage("null")
	}
	raw, _ := json.Marshal(*v)
	return raw
}
