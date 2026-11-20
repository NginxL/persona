package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func writeTaskExecutionError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errModelConflict) {
		status = http.StatusConflict
	}
	apierr.Write(w, status, err)
}

// TaskExecution is the shared execution context for scheduled prompts and their content references.
// An empty model or effort means the launcher's own default, not another provider's default.
type TaskExecution struct {
	MachineID          string `json:"machine_id"`
	Launcher           string `json:"launcher"`
	Agent              string `json:"agent,omitempty"`
	Model              string `json:"model,omitempty"`
	Effort             string `json:"effort,omitempty"`
	Runtime            string `json:"runtime,omitempty"` // legacy Claude execution evidence; new Claude runs use the CLI
	Cwd                string `json:"cwd,omitempty"`
	PermissionMode     string `json:"permission_mode,omitempty"`
	BindingFingerprint string `json:"binding_fingerprint,omitempty"` // independent of node discovery
	Fingerprint        string `json:"fingerprint,omitempty"`         // includes the observed launcher identity
	LegacyCompatible   bool   `json:"legacy_compatible,omitempty"`   // the conversation was admitted with inherited, ordinary execution
}

func (s *Service) taskMachine(ctx context.Context, t Task) string {
	return cmp.Or(t.ExecutionMachineID, s.settings(ctx).MachineID)
}

func (t Task) startsFresh() bool {
	return t.FreshEachRun && t.Repeat != repeatOnce
}

func (t Task) continuesSource() bool {
	return t.SessionID != "" && !t.startsFresh()
}

func taskExecutionNeedsOwnChat(t Task, override ModelBinding) bool {
	// Legacy launcher/effort fields were inert when continuing a source conversation.
	// New changes to those fields are refused at save time; unchanged old records may still continue it.
	return t.continuesSource() && (t.ExecutionMachineID != "" || t.Cwd != "" || override.Model != "" || override.ConfigDir != "")
}

// Schedule tools cannot clear the user's execution choices when restoring a task's source conversation.
func (s *Service) checkTaskSourceRestore(ctx context.Context, previous, next Task) error {
	if !previous.startsFresh() || !next.continuesSource() {
		return nil
	}
	record, err := s.modelRecord(ctx, "task."+next.ID)
	if err != nil {
		return err
	}
	if taskExecutionNeedsOwnChat(next, record.Binding) || record.Binding != (ModelBinding{}) {
		return i18n.Err("assistant-schedule.execution_own_chat")
	}
	return nil
}

// A health task may choose its computer and model, but its directory and instructions belong to the space.
// Check both edited candidates and stored tasks; validation in the editor alone cannot protect old records.
func checkTaskExecutionSpace(t Task) error {
	if t.Space == SpaceHealth && t.Cwd != "" {
		return i18n.Err("assistant-schedule.execution_health_directory")
	}
	return nil
}

// Before continuing a Health assistant, verify its actual cwd against the node's canonical Health directory.
func (s *Service) taskSourceCwd(ctx context.Context, t Task) (string, bool) {
	if t.Space != SpaceHealth {
		return "", true
	}
	c, known := s.session(ctx, t.MachineID, t.SessionID)
	if !known || !c.Assistant || c.Space != SpaceHealth {
		return "", false
	}
	cwd, err := s.prepareSpace(ctx, t.MachineID, SpaceHealth)
	return cwd, err == nil && cwd != "" && c.Cwd == cwd
}

func (s *Service) taskExecution(ctx context.Context, t Task, validate bool, binding ...ModelBinding) (TaskExecution, error) {
	machine := s.taskMachine(ctx, t)
	ex := TaskExecution{MachineID: machine, Cwd: t.Cwd, PermissionMode: cmp.Or(t.PermissionMode, s.settings(ctx).DefaultPermissionMode, "bypassPermissions")}
	if err := checkTaskExecutionSpace(t); err != nil {
		return ex, err
	}
	var b ModelBinding
	if len(binding) > 0 {
		b = binding[0]
	} else {
		var err error
		b, err = s.resolveModel(ctx, "task."+t.ID)
		if err != nil {
			return ex, err
		}
	}
	ex.Launcher, ex.Model, ex.Effort = strings.TrimPrefix(b.Provider, "launcher."), b.Model, b.Effort
	ex.BindingFingerprint, ex.Fingerprint = s.taskExecutionFingerprints(ctx, machine, b)
	if !validate {
		ex.Agent = s.launcherAgent(withUnhidden(ctx, ex.Launcher), machine, ex.Launcher)
		if ex.Cwd == "" {
			if d, ok := s.device(ctx, machine); ok {
				ex.Cwd = s.assistantCwd(d)
				if t.Space == SpaceHealth {
					ex.Cwd = s.healthCwd(d)
				}
			}
		}
		return ex, nil
	}
	d, ok := s.device(ctx, machine)
	if machine == "" {
		return ex, s.missingAssistantMachine(ctx)
	}
	if !ok || !d.Online {
		return ex, i18n.Err("assistant-schedule.execution_offline")
	}
	if s.isWorkMachine(machine) {
		return ex, errWorkAssistant
	}
	p := ModelPurpose{ID: "task." + t.ID, Kind: "launcher", CategoryID: "assistant", Parent: "assistant", MachineID: machine}
	if err := checkModelID(b.Model); err != nil {
		return ex, err
	}
	known, err := s.checkTaskLauncher(ctx, machine, ex.Launcher)
	if err != nil {
		return ex, err
	}
	if err := s.validateTaskExecutionModel(ctx, p, b, known); err != nil {
		return ex, err
	}
	if err := s.checkTaskModelCapabilities(ctx, machine, b); err != nil {
		return ex, err
	}
	ex.Agent = s.launcherAgent(withUnhidden(ctx, ex.Launcher), machine, ex.Launcher)
	if ex.Cwd != "" {
		var result struct {
			Cwd string `json:"cwd"`
		}
		if err := s.call(ctx, machine, assistantwire.DeviceAssistantDirectory, map[string]any{"path": ex.Cwd}, &result); err != nil {
			return ex, i18n.Err("assistant-schedule.execution_directory", i18n.Args{"error": err.Error()})
		}
		if result.Cwd == "" {
			return ex, i18n.Err("assistant-schedule.execution_directory", i18n.Args{"error": "directory validation is unavailable; update this computer"})
		}
		ex.Cwd = result.Cwd
	}
	return ex, nil
}

type taskExecutionEdit struct {
	revision int64
	record   *modelRecord
}

// prepareTaskExecutionEdit reads the model revision that the node checks will validate against.
// The schedule and model writers remain free while the node answers.
func (s *Service) prepareTaskExecutionEdit(ctx context.Context, previous Task, t *Task, raw map[string]json.RawMessage) (taskExecutionEdit, error) {
	old, err := s.modelRecord(ctx, "task."+t.ID)
	if err != nil {
		return taskExecutionEdit{}, err
	}
	record, err := s.patchTaskExecution(ctx, t, raw, old, previous.startsFresh())
	return taskExecutionEdit{revision: old.Revision, record: record}, err
}

func (s *Service) patchTaskExecution(ctx context.Context, t *Task, raw map[string]json.RawMessage, old modelRecord, wasFresh bool) (*modelRecord, error) {
	changed := false
	for key, dst := range map[string]*string{"execution_machine_id": &t.ExecutionMachineID, "cwd": &t.Cwd} {
		if v, ok := raw[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return nil, err
			}
			if key != "cwd" {
				*dst = strings.TrimSpace(*dst)
			}
			if strings.ContainsAny(*dst, "\x00\r\n") {
				return nil, i18n.Err("assistant-models.err_field_invalid")
			}
			changed = true
		}
	}
	if err := checkTaskExecutionSpace(*t); err != nil {
		return nil, err
	}
	patch := modelPatch{}
	for key, dst := range map[string]*json.RawMessage{"launcher": &patch.Provider, "model": &patch.Model, "effort": &patch.Effort} {
		if v, ok := raw[key]; ok {
			var text string
			if err := json.Unmarshal(v, &text); err != nil {
				return nil, err
			}
			text = strings.TrimSpace(text)
			if key == "launcher" && text != "" {
				if !launcherName.MatchString(text) {
					return nil, i18n.Err("assistant-models.err_field_invalid")
				}
				text = launcherProvider(text)
			}
			*dst = jsonStringOrNull(&text)
			changed = true
		}
	}
	if v, ok := raw["model_revision"]; ok {
		var rev int64
		if err := json.Unmarshal(v, &rev); err != nil {
			return nil, err
		}
		if rev != old.Revision {
			return nil, errModelConflict
		}
	}
	b, err := patchModel(old.Binding, patch)
	if err != nil {
		return nil, err
	}
	// Old clients echo launcher/effort during unrelated saves, without the revision used by
	// execution-aware clients. Preserve an equal effective binding rather than creating new pins.
	if changed && raw["model_revision"] == nil && raw["model"] == nil && raw["cwd"] == nil && raw["execution_machine_id"] == nil && string(patch.Provider) != "null" && string(patch.Effort) != "null" {
		if b != old.Binding {
			p := ModelPurpose{ID: "task." + t.ID, Kind: "launcher", CategoryID: "assistant", Parent: "assistant", MachineID: s.taskMachine(ctx, *t)}
			base, _, err := s.modelBaseline(ctx, p)
			if err != nil {
				return nil, err
			}
			if mergeModel(base, b) == mergeModel(base, old.Binding) {
				b = old.Binding
			}
		}
		changed = b != old.Binding
	}
	source := t.continuesSource()
	if taskExecutionNeedsOwnChat(*t, b) || source && b != (ModelBinding{}) && (wasFresh || b != old.Binding) {
		return nil, i18n.Err("assistant-schedule.execution_own_chat")
	}
	if !changed {
		return nil, nil
	}
	if source {
		// Clearing overrides restores the source conversation's context, even while its computer is offline.
		if b == old.Binding {
			return nil, nil
		}
		t.StartGrant = nil // a conversation-backed task cannot use a schedule start grant
		return &modelRecord{Revision: old.Revision + 1, Binding: b}, nil
	}
	p := ModelPurpose{ID: "task." + t.ID, Kind: "launcher", CategoryID: "assistant", Parent: "assistant", MachineID: s.taskMachine(ctx, *t)}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return nil, err
	}
	if b.Provider == "" && (b.Model != "" || b.Effort != "") {
		b.Provider = base.Provider
	}
	effective := mergeModel(base, b)
	if err := checkModelID(effective.Model); err != nil {
		return nil, err
	}
	d, ok := s.device(ctx, p.MachineID)
	if !ok || !d.Online {
		return nil, i18n.Err("assistant-schedule.execution_offline")
	}
	if s.isWorkMachine(p.MachineID) {
		return nil, errWorkAssistant
	}
	known, err := s.checkTaskLauncher(ctx, p.MachineID, strings.TrimPrefix(effective.Provider, "launcher."))
	if err != nil {
		return nil, err
	}
	if err := s.validateTaskExecutionModel(ctx, p, effective, known); err != nil {
		return nil, err
	}
	if err := s.checkTaskModelCapabilities(ctx, p.MachineID, effective); err != nil {
		return nil, err
	}
	if t.Cwd != "" {
		var result struct {
			Cwd string `json:"cwd"`
		}
		if err := s.call(ctx, p.MachineID, assistantwire.DeviceAssistantDirectory, map[string]any{"path": t.Cwd}, &result); err != nil {
			return nil, i18n.Err("assistant-schedule.execution_directory", i18n.Args{"error": err.Error()})
		}
		if result.Cwd == "" {
			return nil, i18n.Err("assistant-schedule.execution_directory", i18n.Args{"error": "directory validation is unavailable; update this computer"})
		}
		// Keep the path the user chose. Each run resolves symlinks again on that computer.
	}
	if _, specified := raw["execution_machine_id"]; !specified && (b.Model != "" || b.Effort != "") {
		// Keep the computer on which this override was validated, just as we keep its provider.
		// An explicit empty computer still restores inheritance.
		t.ExecutionMachineID = p.MachineID
	}
	if b == old.Binding {
		return nil, nil
	}
	if t.StartGrant != nil {
		basis := t.grantBasis()
		if b != (ModelBinding{}) {
			basis = t.grantBasis(effective)
		}
		if t.liveGrant(basis) == nil {
			t.StartGrant = nil
		}
	}
	return &modelRecord{Revision: old.Revision + 1, Binding: b}, nil
}

func (s *Service) validateTaskExecutionModel(ctx context.Context, p ModelPurpose, b ModelBinding, catalogKnown bool) error {
	providers := s.modelProviders(ctx)
	name := strings.TrimPrefix(b.Provider, "launcher.")
	if !catalogKnown && strings.HasPrefix(b.Provider, "launcher.") && launcherName.MatchString(name) && !slices.ContainsFunc(providers, func(v ModelProvider) bool { return v.ID == b.Provider }) {
		// Unknown discovery cannot prove absence. Keep model, effort, capability and machine-scope checks.
		providers = append(providers, ModelProvider{ID: b.Provider, Kind: "launcher"})
	}
	return s.validateModel(ctx, p, b, providers)
}

// A successful task catalog is authoritative, including an empty list. The ordinary assistant's legacy fallback
// treats an empty list as unknown; tasks must not silently retain a removed choice or substitute another launcher.
// Configured wrappers and managed providers are merged before checking. Failed discovery remains unknown.
func (s *Service) checkTaskLauncher(ctx context.Context, machine, name string) (known bool, err error) {
	stored, known := s.storedLaunchersKnown(ctx)
	if !known {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(withUnhidden(withManagedSnapshot(ctx, stored), name), 10*time.Second)
	defer cancel()
	picker, known, err := s.pickerLaunchersKnown(ctx, machine, "")
	if err != nil || !known {
		return false, nil
	}
	fits, err := s.assistantLaunchersOf(ctx, machine, picker)
	if err != nil {
		return false, nil
	}
	named := func(l ConfiguredLauncher) bool { return l.Name == name }
	if slices.ContainsFunc(fits, named) {
		return true, nil
	}
	if slices.ContainsFunc(picker, named) {
		return true, s.mainChatRefusal(ctx, machine, name, "")
	}
	return true, i18n.Err("assistant-models.err_provider_missing")
}

var errTaskEditGone = i18n.Err("assistant-schedule.gone")

// taskEditConfig keeps the fields that identify the edit's validated configuration and authorization.
func taskEditConfig(t Task) Task {
	t.RunMachineID, t.RunSessionID, t.RunLauncher, t.RunExecution = "", "", "", nil
	t.LastRun, t.StatusRun = time.Time{}, time.Time{}
	t.LastSessionID, t.LastStatus, t.LastError = "", "", ""
	t.TodoProgress, t.StateNote = nil, nil
	if t.Repeat == repeatInterval {
		t.At = time.Time{} // due runs advance At; ArmedAt records a deliberate retime
	}
	return t
}

func mergeTaskEditRuntime(task *Task, before, current Task) error {
	if !reflect.DeepEqual(taskEditConfig(before), taskEditConfig(current)) {
		return errModelConflict
	}
	if reflect.DeepEqual(before.StateNote, task.StateNote) {
		task.StateNote = current.StateNote
	} else if !reflect.DeepEqual(before.StateNote, current.StateNote) {
		return errTaskStateChanged
	}
	task.RunMachineID, task.RunSessionID = current.RunMachineID, current.RunSessionID
	task.RunLauncher, task.RunExecution = current.RunLauncher, current.RunExecution
	task.LastRun, task.StatusRun = current.LastRun, current.StatusRun
	task.LastSessionID, task.LastStatus, task.LastError = current.LastSessionID, current.LastStatus, current.LastError
	task.TodoProgress = current.TodoProgress
	if before.Repeat == repeatInterval && task.Repeat == repeatInterval && task.EveryMinutes == before.EveryMinutes &&
		task.ArmedAt.Equal(before.ArmedAt) && task.Enabled == before.Enabled && !current.At.Equal(before.At) {
		task.At = current.At
	}
	return nil
}

// commitTaskExecutionEdit commits the validated configuration with the scheduler's latest runtime state.
func (s *Service) commitTaskExecutionEdit(ctx context.Context, before *Task, task Task, edit taskExecutionEdit) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return err
	}
	defer sc.mu.Unlock()
	i := sc.indexLocked(task.ID)
	if before != nil && i < 0 {
		return errTaskEditGone
	}
	if before == nil && i >= 0 {
		return errModelConflict
	}
	current, err := s.modelRecord(ctx, "task."+task.ID)
	if err != nil {
		return err
	}
	if current.Revision != edit.revision {
		return errModelConflict
	}
	if before != nil {
		if err := mergeTaskEditRuntime(&task, *before, sc.tasks[i]); err != nil {
			return err
		}
	}
	tasks := slices.Clone(sc.tasks)
	if i < 0 {
		tasks = append(tasks, task)
	} else {
		tasks[i] = task
	}
	return s.saveTaskConfiguredLocked(ctx, sc, tasks, task.ID, edit.record)
}

func writeTaskExecutionCommitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTaskEditGone):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errTaskStateChanged):
		writeTaskStateError(w, err)
	case errors.Is(err, errModelConflict):
		writeTaskExecutionError(w, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}

func (s *Service) saveTaskConfiguredLocked(ctx context.Context, sc *schedule, tasks []Task, id string, record *modelRecord) error {
	if record == nil {
		return s.saveLocked(sc, tasks)
	}
	batch, ok := s.store().(interface {
		PutSettings(context.Context, map[string]any) error
	})
	if !ok {
		return errors.New("atomic task configuration storage is unavailable")
	}
	dropEditedTaskGrants(sc.tasks, tasks)
	if err := batch.PutSettings(ctx, map[string]any{tasksKey: tasks, modelRecordKey("task." + id): *record}); err != nil {
		return err
	}
	sc.tasks = tasks
	return nil
}

func compatibleTaskExecution(old, next TaskExecution) bool {
	if old.BindingFingerprint != "" && old.BindingFingerprint != next.BindingFingerprint {
		return false
	}
	// Older snapshots can still compare an observed digest. Their empty-identity digest only records the binding.
	if old.BindingFingerprint == "" && old.Fingerprint == next.BindingFingerprint {
		old.Fingerprint = ""
	}
	if old.Fingerprint == "" || next.Fingerprint == "" {
		old.Fingerprint, next.Fingerprint = "", ""
	}
	if old.Agent == "" || next.Agent == "" {
		old.Agent, next.Agent = "", ""
	}
	old.BindingFingerprint, next.BindingFingerprint = "", ""
	old.LegacyCompatible, next.LegacyCompatible = false, false
	// Permission changes can be applied to a compatible session; the agent, directory and model identity cannot.
	old.PermissionMode, next.PermissionMode = "", ""
	return old == next
}

type taskRunModel struct {
	effective ModelBinding
	override  ModelBinding
}

// taskRunSnapshot pairs the task's directory/computer with its model record under the writers' lock order.
func (s *Service) taskRunSnapshot(ctx context.Context, t Task) (Task, taskRunModel, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return t, taskRunModel{}, err
	}
	if i := sc.indexLocked(t.ID); i >= 0 {
		t = sc.tasks[i]
	}
	sc.mu.Unlock()
	if err := checkTaskExecutionSpace(t); err != nil {
		return t, taskRunModel{}, err
	}
	r, err := s.modelRecord(ctx, "task."+t.ID)
	if err != nil {
		return t, taskRunModel{}, err
	}
	if taskExecutionNeedsOwnChat(t, r.Binding) {
		return t, taskRunModel{}, i18n.Err("assistant-schedule.execution_own_chat")
	}
	b, err := s.resolveModelRecord(ctx, "task."+t.ID, &r)
	t.runGrantBasis = t.grantBasis()
	if r.Binding != (ModelBinding{}) {
		t.runGrantBasis = t.grantBasis(b)
	}
	return t, taskRunModel{effective: b, override: r.Binding}, err
}

// Only explicit pre-delivery refusals permit starting a replacement conversation.
func taskConversationUnavailable(err error, id string) bool {
	text := err.Error()
	return text == "conversation "+id+" is gone" ||
		text == "task execution context differs from this conversation; start a new conversation" ||
		strings.HasPrefix(text, "config_dir cannot move an existing conversation;")
}

// Use the launcher's own capability metadata when it advertises model-specific thinking levels.
func (s *Service) checkTaskModelCapabilities(ctx context.Context, machine string, b ModelBinding) error {
	launcher := strings.TrimPrefix(b.Provider, "launcher.")
	if b.Model != "" {
		if err := s.pickedModelRefusal(ctx, machine, launcher, b.Model); err != nil {
			return err
		}
	}
	if b.Effort == "" {
		return nil
	}
	list, err := s.askLauncherModels(ctx, machine, launcher, false)
	if err != nil {
		return nil
	} // older catalogs do not advertise these levels; the node still validates its input
	for _, m := range list.Models {
		if b.Model == m.ID || b.Model != "" && (b.Model == m.Resolved || slices.Contains(m.Aliases, b.Model)) || b.Model == "" && m.Default {
			if len(m.Efforts) > 0 && !slices.Contains(m.Efforts, b.Effort) {
				return i18n.Err("assistant-models.err_effort_unsupported")
			}
			break
		}
	}
	return nil
}
