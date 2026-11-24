package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/promptcontent"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Rich editors put their serialized prompt only in structured_prompt. An older manager requires
// prompt, so it rejects this write without changing the task or silently dropping its references.
func readStructuredTaskPrompt(raw map[string]json.RawMessage) error {
	content, rich := raw["structured_prompt"]
	if !rich {
		return nil
	}
	for field := range raw {
		if strings.EqualFold(field, "prompt") {
			return errors.New("specify only one task prompt")
		}
	}
	var prompt string
	if err := json.Unmarshal(content, &prompt); err != nil {
		return err
	}
	if prefix, _ := taskPromptBody(prompt); prefix == "" {
		return i18n.Err("assistant-schedule.input_context_unavailable")
	}
	raw["prompt"] = content
	return nil
}

// taskInputContext resolves the task execution environment without saving the draft or starting a conversation.
func (s *Service) taskInputContext(ctx context.Context, raw map[string]json.RawMessage) (taskInputTarget, error) {
	t := Task{ID: "__input_draft"}
	var id string
	if v, ok := raw["id"]; ok {
		if err := json.Unmarshal(v, &id); err != nil {
			return taskInputTarget{}, err
		}
	}
	if id != "" {
		sc, err := s.lockedSchedule(ctx)
		if err != nil {
			return taskInputTarget{}, err
		}
		i := sc.indexLocked(id)
		if i >= 0 {
			t = sc.tasks[i]
		}
		sc.mu.Unlock()
		if i < 0 {
			return taskInputTarget{}, i18n.Err("assistant-schedule.input_context_unavailable")
		}
	}
	previous := t
	if v, ok := raw["session_id"]; ok {
		var sid string
		if err := json.Unmarshal(v, &sid); err != nil {
			return taskInputTarget{}, err
		}
		// The edit route may detach the original chat, never attach an arbitrary one.
		if sid == "" {
			t.SessionID = ""
		}
	}
	for key, dst := range map[string]any{"push": &t.Push, "repeat": &t.Repeat, "cron": &t.Cron, "fresh_each_run": &t.FreshEachRun} {
		if v, ok := raw[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return taskInputTarget{}, err
			}
		}
	}
	if err := readTopicPolicy(raw, &t); err != nil {
		return taskInputTarget{}, err
	}
	_, hasCron := raw["cron"]
	keepLegacyCronRule(previous, &t, hasCron)
	isolateScheduledConversation(&t, s.mayBeMainNow(ctx))
	edit, err := s.prepareTaskExecutionEdit(ctx, previous, &t, raw)
	if err != nil {
		return taskInputTarget{}, err
	}
	return s.taskInputExecution(ctx, t, edit.record)
}

// A task that wants visible updates runs in its own conversation when its source is another task's quiet chat.
func (s *Service) taskInputDestination(ctx context.Context, t Task) Task {
	if t.continuesSource() && !t.pushesOnlyAsked() && s.quietConversation(ctx, t.SessionID) {
		t.SessionID = ""
	}
	return t
}

type taskInputTarget struct {
	TaskExecution
	session, profile string
	strict           bool
}

func (s *Service) taskInputExecution(ctx context.Context, t Task, record *modelRecord) (taskInputTarget, error) {
	t = s.taskInputDestination(ctx, t)
	if t.continuesSource() {
		c, ok := s.session(ctx, t.MachineID, t.SessionID)
		d, online := s.device(ctx, t.MachineID)
		if !online || !d.Online || s.isWorkMachine(t.MachineID) || !ok || !c.Assistant || c.Cwd == "" || s.isGuest(ctx, t.MachineID, t.SessionID) {
			return taskInputTarget{}, i18n.Err("assistant-schedule.input_context_unavailable")
		}
		sourceCwd := "" // Ordinary continuations use the source conversation's directory.
		if t.Space != "" {
			// A space's conversation continues only in the space's canonical directory; a space without one
			// (spaceCwd) never continues.
			var canonical struct {
				Cwd string `json:"cwd"`
			}
			if s.spaceCwd(ctx, d, t.Space) == "" {
				return taskInputTarget{}, i18n.Err("assistant-schedule.input_context_unavailable")
			}
			if c.Space != t.Space || s.call(ctx, t.MachineID, assistantwire.DeviceAssistantDirectory, map[string]any{"path": s.spaceCwd(ctx, d, t.Space)}, &canonical) != nil || canonical.Cwd != c.Cwd {
				return taskInputTarget{}, i18n.Err("assistant-schedule.input_context_unavailable")
			}
			sourceCwd = canonical.Cwd
		}
		params, err := s.taskSourceParams(ctx, t, "", sourceCwd, s.requestCtxLang(ctx))
		if err != nil {
			return taskInputTarget{}, err
		}
		// An omitted profile lets the node resolve the source session's profile just as it does on send.
		profile, _ := params["config_dir"].(string)
		return taskInputTarget{TaskExecution: *s.sourceTaskExecution(ctx, t, params), session: t.SessionID,
			profile: profile, strict: params["task_execution"] == true}, nil
	}
	base, err := s.taskModelBaseline(ctx, s.taskMachine(ctx, t))
	if err != nil {
		return taskInputTarget{}, err
	}
	if record == nil {
		stored, err := s.modelRecord(ctx, "task."+t.ID)
		if err != nil {
			return taskInputTarget{}, err
		}
		record = &stored
	}
	b := mergeModel(base, record.Binding)
	ex, err := s.taskExecution(ctx, t, true, b)
	if err == nil && ex.Cwd == "" {
		ex.Cwd, err = s.taskInputDefaultDirectory(ctx, ex.MachineID, t.Space)
	}
	if err != nil {
		return taskInputTarget{}, err
	}
	target := taskInputTarget{TaskExecution: ex, strict: true}
	// startConversation uses the same signed-in profile as meetings. A compatible task-owned
	// conversation keeps its own profile on resume (including the signed-out repair in resumeOwn).
	if !t.startsFresh() && t.RunSessionID != "" && t.RunMachineID == ex.MachineID && t.RunLauncher == ex.Launcher &&
		taskExecutionChangeReason(t, ex, record.Binding) == "" && !((t.pushesOnlyAsked() || t.Repeat != repeatOnce) && s.mayBeMain(ctx, t.RunSessionID)) {
		target.session = t.RunSessionID
		if c, ok := s.session(ctx, ex.MachineID, t.RunSessionID); ok && signedOut(c) && (c.Agent == "" || c.Agent == "claude") {
			target.profile = meetingProfile
		}
	} else if ex.Launcher == "claude" || ex.Agent == "" || ex.Agent == "claude" {
		target.profile = meetingProfile
	}
	return target, nil
}

// Only a verified missing default allows preparation. Explicit paths and permission errors still
// go through taskExecution's existing checks.
func (s *Service) taskInputDefaultDirectory(ctx context.Context, machine, space string) (string, error) {
	d, ok := s.device(ctx, machine)
	if !ok || !d.Online {
		return "", i18n.Err("assistant-schedule.execution_offline")
	}
	cwd := s.spaceCwd(ctx, d, space)
	if space != "" && cwd == "" {
		// A space without a directory (spaceCwd) never falls back to the assistant's.
		return "", i18n.Err("assistant-schedule.input_context_unavailable")
	}
	resolve := func(path string) (string, error) {
		var result struct {
			Cwd string `json:"cwd"`
		}
		if err := s.call(ctx, machine, assistantwire.DeviceAssistantDirectory, map[string]any{"path": path}, &result); err != nil {
			return "", err
		}
		if result.Cwd == "" {
			return "", i18n.Err("assistant-schedule.input_context_unavailable")
		}
		return result.Cwd, nil
	}
	path, err := resolve(cwd)
	var nodeErr *rpc.Error
	if !errors.As(err, &nodeErr) || nodeErr.Code != assistantwire.CodeTaskDirectoryMissing {
		return path, err
	}
	prepared, err := s.prepareSpace(ctx, machine, space)
	if err != nil {
		return "", err
	}
	return resolve(prepared)
}

func (target taskInputTarget) environmentParams() map[string]any {
	params := map[string]any{"cwd": target.Cwd, "launcher": target.Launcher}
	if target.profile != "" {
		params["config_dir"] = target.profile
	}
	return params
}

func (s *Service) validateTaskInput(ctx context.Context, t Task, record *modelRecord) error {
	if !t.PromptSelections {
		return nil
	}
	prefix, _ := taskPromptBody(t.Prompt)
	skills, err := assistantskills.References(prefix)
	if err != nil {
		return skillReferenceError()
	}
	files, err := promptcontent.Files(prefix)
	if err != nil {
		return i18n.Err("assistant-schedule.selected_file_unavailable")
	}
	if len(skills) == 0 && len(files) == 0 {
		return nil
	}
	ex, err := s.taskInputExecution(ctx, t, record)
	if err != nil {
		return err
	}
	return s.checkTaskContent(ctx, ex, prefix)
}

// The node must acknowledge the task-input RPC, even when no skill inventory is requested.
// An empty prefix probes support without reading any file or running an assistant turn.
func (s *Service) checkTaskContent(ctx context.Context, ex taskInputTarget, prefix string) error {
	var ack struct {
		Valid bool `json:"valid"`
	}
	params := ex.environmentParams()
	params["text"], params["skill_owner"] = prefix, s.skillOwner()
	if ex.strict {
		params["task_execution"] = true
	}
	if ex.session != "" {
		params["session_id"] = ex.session
		if origin, known := s.managerOriginDecision(ctx, ex.MachineID, ex.session, "", false); known {
			params["origin"], params["origin_known"] = origin, true
		}
	}
	if ex.Runtime != "" {
		params["runtime"] = ex.Runtime
	}
	if err := s.call(ctx, ex.MachineID, "assistant.content", params, &ack); err != nil {
		return err
	}
	if !ack.Valid {
		return i18n.Err("hub-machine.node_no_task_input")
	}
	return nil
}

func (s *Service) taskInputRoutes() {
	s.handle("POST /api/v1/assistant/tasks/input-context", func(w http.ResponseWriter, r *http.Request) {
		raw := map[string]json.RawMessage{}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if err := decoder.Decode(&raw); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			apierr.Write(w, 400, errors.New("invalid request"))
			return
		}
		// A topic_policy that is not one is the request's fault, not the node's (apierr.Call would answer 502).
		if err := readTopicPolicy(raw, &Task{}); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		ex, err := s.taskInputContext(r.Context(), raw)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		// The browser compares this alongside the visible execution scope. A source may keep its own profile
		// even when the launcher and directory match the task's own conversation.
		scope, _ := json.Marshal([2]string{ex.session, ex.profile})
		sum := sha256.Sum256(scope)
		result := map[string]any{"execution": ex.TaskExecution, "skill_scope": hex.EncodeToString(sum[:]), "task_input": false}
		if err := s.checkTaskContent(r.Context(), ex, ""); err != nil {
			result["input_error"] = i18n.Localize(err, s.requestLang(r))
			apierr.WriteJSON(w, result)
			return
		}
		result["task_input"] = true
		var catalog bool
		_ = json.Unmarshal(raw["catalog"], &catalog)
		if catalog {
			params := ex.environmentParams()
			params["task_execution"] = true // use the task's resolved directory for a new conversation
			c, e := s.skillCatalogFor(r.Context(), ex.MachineID, ex.session, params)
			if e != nil {
				result["skill_error"] = e.Error()
			} else {
				result["catalog"] = c
			}
		}
		apierr.WriteJSON(w, result)
	})
}

func (t Task) promptParts() (prefix, body string) {
	if !t.PromptSelections {
		return "", t.Prompt
	}
	return taskPromptBody(t.Prompt)
}

// Keep the original envelope before the schedule header, so the shared selection parsers see it.
func taskPromptBody(prompt string) (string, string) {
	prefix, body := promptcontent.Split(prompt)
	if prefix == "" {
		return "", prompt
	}
	files, fileErr := promptcontent.Files(prefix)
	skills, skillErr := assistantskills.References(prefix)
	if fileErr == nil && skillErr == nil && len(files) == 0 && len(skills) == 0 {
		return "", prompt
	}
	return strings.TrimSpace(prefix), strings.TrimLeft(body, "\r\n")
}

var errTaskSelectionsOwner = errors.New("change the run settings of a task with selected content in the Persona app")

// A tool-authored prompt is literal even when it matches the task's previous text.
func (t *Task) setLiteralPrompt(prompt string) {
	t.Prompt, t.PromptSelections = prompt, false
}
