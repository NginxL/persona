package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// startDefaultsPreviewRoutes serves POST /api/v1/assistant/start-defaults/preview: what a policy would do for a
// task, read-only. Matching, goal, candidate selection and the effective agent are the same a real start resolves
// (matchStartDefaults, startGoalSource, chooseStartPreset, the caller's or candidate's launcher), so a preview never
// advertises a value the start would not set. Nothing is written: no settings change, no task or plan, no Agent
// start, no admission claim, no note. A draft policy in the body is only decoded and validated, never stored, so it
// needs no expires_at; an empty draft previews the no-policy start (it does not clear the stored policy). Registered
// from startDefaultsRoutes (start_defaults_routes.go), same assistant.start_defaults scope and read-allowed role.
func (s *Service) startDefaultsPreviewRoutes() {
	s.handle("POST /api/v1/assistant/start-defaults/preview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Task struct {
				Machine    string `json:"machine"`
				Role       string `json:"role,omitempty"`
				Readonly   bool   `json:"readonly,omitempty"`
				Supervisor string `json:"supervisor,omitempty"`
				DoneWhen   int    `json:"done_when,omitempty"`
			} `json:"task"`
			Caller struct {
				Launcher  string `json:"launcher,omitempty"`
				Model     string `json:"model,omitempty"`
				Tier      string `json:"tier,omitempty"`
				Effort    string `json:"effort,omitempty"`
				Ultracode *bool  `json:"ultracode,omitempty"`
			} `json:"caller"`
			StartDefaults *StartDefaultsPolicy `json:"start_defaults,omitempty"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, startDefaultsBodyLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		ctx := r.Context()
		if strings.TrimSpace(in.Task.Machine) == "" {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("assistant-start_defaults_preview.field_required", i18n.Args{"field": i18n.Verbatim("task.machine")}))
			return
		}
		m, ok := s.machineByName(ctx, in.Task.Machine)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		if in.Task.Role != "" && !slices.Contains(startDefaultsRoles, in.Task.Role) {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("assistant-start_defaults_preview.field_one_of", i18n.Args{
				"field": i18n.Verbatim("task.role"), "values": i18n.Verbatim(strings.Join(startDefaultsRoles, ", ")),
			}))
			return
		}
		if in.Task.Supervisor != "" && !slices.Contains(dispatchClasses, in.Task.Supervisor) {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("assistant-start_defaults_preview.field_one_of", i18n.Args{
				"field": i18n.Verbatim("task.supervisor"), "values": i18n.Verbatim(strings.Join(dispatchClasses, ", ")),
			}))
			return
		}
		if in.Task.DoneWhen < 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("assistant-start_defaults_preview.field_nonnegative", i18n.Args{"field": i18n.Verbatim("task.done_when")}))
			return
		}
		policy := in.StartDefaults
		if policy != nil {
			// A draft is never stored: validate it as the owner's write would, but with no expiry requirement. An empty
			// rules list is the clear operation the PUT accepts, and previewing it must work too: it means the
			// no-policy start, not a validation error, and it leaves the stored policy untouched.
			if len(policy.Rules) > 0 {
				if err := policy.validate(true, time.Now().UTC()); err != nil {
					apierr.Write(w, http.StatusBadRequest, err)
					return
				}
			}
		} else {
			policy = s.activeStartDefaults(ctx)
		}
		facts := StartFacts{
			// roleOf: an ordinary task is a worker; only a task leader is a leader. An omitted role is a worker, exactly
			// as a real start reads it, so an empty role cannot silently dodge a worker-scoped rule.
			Machine: m.ID, Role: in.Task.Role, Readonly: in.Task.Readonly,
			Supervisor: in.Task.Supervisor, DoneWhen: in.Task.DoneWhen,
		}
		if facts.Role == "" {
			facts.Role = "worker"
		}
		out, status, err := s.previewStartDefaults(ctx, policy, facts, in.Caller.Launcher, in.Caller.Model, in.Caller.Tier, in.Caller.Effort, in.Caller.Ultracode)
		if err != nil {
			var re *rpc.Error
			if errors.As(err, &re) {
				apierr.CallStatus(w, status, err) // keep coded refusals and their structured data
			} else {
				apierr.Write(w, status, err)
			}
			return
		}
		apierr.WriteJSON(w, out)
	})
}

// startDefaultsPreviewErrorStatus separates deterministic resolve/fit refusals from operational read failures.
// Candidate-source failures keep their separate candidates_error fallback and never come through here.
func startDefaultsPreviewErrorStatus(err error) int {
	var re *rpc.Error
	if errors.As(err, &re) {
		return http.StatusBadRequest
	}
	var msg *i18n.Message
	if errors.As(err, &msg) {
		if strings.HasPrefix(msg.Key, "external-adapter.") {
			return http.StatusBadRequest
		}
		switch msg.Key {
		case "assistant-model_profiles.err_active_profile_missing",
			"assistant-model_profiles.err_no_active_profile",
			"assistant-model_profiles.err_tier_not_found",
			"assistant-models.err_model_invalid",
			"assistant-models.err_not_launcher",
			"assistant-models.err_api_as_launcher",
			"assistant-tasks.launcher_not_on_machine",
			"assistant-tasks.launcher_kind_missing":
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}

// previewStartDefaults answers the preview body. A nil or empty policy, or no fitting rule, reports matched=false,
// which is exactly the no-policy start (the Codex default goal still applies). The illustrative start goes through the
// same read-only resolve -> machine fit -> speed validation as a plan edit, so goal, effort and ultracode follow the
// fitted agent. It performs only reads; the nonzero status accompanies an error and is otherwise zero.
func (s *Service) previewStartDefaults(ctx context.Context, policy *StartDefaultsPolicy, facts StartFacts, launcher, model, tier, effort string, ultracode *bool) (map[string]any, int, error) {
	out := map[string]any{"matched": false}
	var d StartDefaults
	matched := false
	if policy != nil {
		if policy.By != "" {
			out["by"] = policy.By
		}
		d, matched = matchStartDefaults(policy, facts)
	}
	out["matched"] = matched

	// Resolve candidates first: the sampled pick's launcher decides the agent a start would actually run, so the
	// extras below are computed for that agent, not for an assumed one.
	callerNamedLaunch := strings.TrimSpace(launcher) != "" || strings.TrimSpace(model) != "" || strings.TrimSpace(tier) != ""
	var source map[string]startCandidate
	var usable []PresetChoice
	var skipped []skippedPreset
	sampled := ""
	if matched && !callerNamedLaunch && len(d.Presets) > 0 {
		src, err := s.candidateSource().candidates(ctx, facts.Machine, requestedStartCandidateNames(d.Presets))
		if err != nil {
			out["candidates_error"] = err.Error()
		} else {
			source = src
			usable, skipped = usablePresets(d.Presets, source)
			if len(usable) > 0 {
				sampled = weightedPreset(presetIntN, usable).Name
			}
		}
	}

	// Mirror applyStartPreset before the real resolve: a matched rule clears blank caller launch fields even when it
	// offers only extras. With no matching rule the raw caller fields stay, exactly as startTaskAgent leaves them.
	previewTask := PlanTask{Machine: facts.Machine, Launcher: launcher, Model: model, Tier: tier, Effort: effort, Readonly: facts.Readonly}
	if matched && !callerNamedLaunch {
		previewTask.Launcher, previewTask.Model, previewTask.Tier = "", "", ""
		previewTask.Effort = strings.TrimSpace(effort)
		if sampled != "" {
			previewTask.Tier = sampled
		}
	}
	fit, params, machineFound, err := s.resolveFitPlanTask(ctx, &previewTask)
	if err != nil {
		return nil, startDefaultsPreviewErrorStatus(err), err
	}
	if !machineFound {
		return nil, http.StatusNotFound, errUnknownMachine
	}
	if err := checkStartSpeed(params, fit, ""); err != nil {
		return nil, http.StatusBadRequest, err
	}
	agent := fit.agent
	if vocab := previewEffortVocab(agent); previewTask.Effort != "" && !slices.Contains(vocab, previewTask.Effort) {
		return nil, http.StatusBadRequest, i18n.ErrEnglish("assistant-start_defaults_preview.field_one_of", i18n.Args{
			"field": i18n.Verbatim("caller.effort"), "values": i18n.Verbatim(strings.Join(vocab, ", ")),
		})
	}

	// The goal follows startGoalSource for that agent, with or without a fitting rule. A built-in Codex default is
	// omitted where the real start already knows the launcher cannot accept one; a rule's explicit goal still applies.
	goal, byDefault := startGoalSource(d, matched, agent, false, facts.DoneWhen)
	if byDefault {
		if m, ok := s.machineByName(ctx, facts.Machine); ok && defaultGoalRefusedOn(m, params, fit) {
			goal = false
		}
	}
	if goal {
		if byDefault {
			out["goal_by_default"] = true
		} else {
			out["goal"] = d.Goal
		}
	}
	if !matched {
		return out, http.StatusOK, nil
	}
	ruleIndex := -1
	for i, r := range policy.Rules {
		if startDefaultsMatch(r.When, facts) {
			ruleIndex = i
			break
		}
	}
	out["rule_index"] = ruleIndex
	out["applies_when"] = policy.Rules[ruleIndex].When
	if d.PlanFirst {
		out["plan_first"] = true
	}

	// Caller values win, but only where the policy actually offers something it could apply on this agent: a caller
	// launcher, model or tier leaves no room for a preset only when the rule names candidates; the caller's effort (or
	// its tier's effort) wins only over an effort the rule could set on Claude Code; an explicit ultracode wins only
	// over a rule that would turn it on (applyStartExtras never acts on a policy's false).
	callerWins := []string{}
	tierEffort := false
	if strings.TrimSpace(tier) != "" {
		if tm, err := s.tierModel(ctx, tier); err == nil && tm.Effort != "" {
			tierEffort = true
		}
	}
	if d.Effort != "" && agent == "claude" && (strings.TrimSpace(effort) != "" || tierEffort) {
		callerWins = append(callerWins, "effort")
	}
	if d.Ultracode != nil && *d.Ultracode && agent == "claude" && ultracode != nil {
		callerWins = append(callerWins, "ultracode")
	}
	if callerNamedLaunch && len(d.Presets) > 0 {
		out["preset_blocked_by_caller"] = true
	}

	if !callerNamedLaunch && len(d.Presets) > 0 && source != nil {
		// The winning group's candidates, each with the agent its own launcher runs, so a mixed-agent group is legible
		// per candidate rather than forced under one assumed agent.
		rows := make([]map[string]any, 0, len(usable))
		for _, c := range topPriorityGroup(usable) {
			rowAgent := source[c.Name].Agent
			if c.Name == sampled {
				rowAgent = agent // the full fit, including fallback, is authoritative for the illustrative start
			}
			rows = append(rows, map[string]any{
				"name": c.Name, "priority": c.Priority, "weight": c.Weight,
				"launcher": source[c.Name].Launcher, "agent": rowAgent,
			})
		}
		out["usable"] = rows
		if len(skipped) > 0 {
			passed := make([]map[string]any, 0, len(skipped))
			for _, sk := range skipped {
				passed = append(passed, map[string]any{"name": sk.Name, "reason": sk.Reason})
			}
			out["passed_over"] = passed
		}
		if sampled != "" {
			// One illustrative pick, whose launcher is the agent the extras above were computed for; it is not a
			// promise of what a later start rolls.
			out["sample"] = sampled
			out["sample_agent"] = agent
		}
	}
	// Effort and ultracode reach Claude Code only (applyStartExtras); on any other agent the policy's values are not
	// applied, so the preview leaves them out rather than advertising a start that would not carry them.
	if agent == "claude" {
		if d.Effort != "" {
			out["effort"] = d.Effort
		}
		// Only a rule that turns ultracode on is applied (applyStartExtras never acts on a policy's false), so a false
		// is left out rather than shown as a value the start would send over the caller's own choice.
		if d.Ultracode != nil && *d.Ultracode {
			out["ultracode"] = true
		}
	}
	if len(callerWins) > 0 {
		out["caller_wins"] = callerWins
	}
	return out, http.StatusOK, nil
}

// previewEffortVocab is the effort levels the preview accepts from a caller on agent. A known agent keeps its own
// vocabulary (providerEffortVocab). An agent the hub cannot tell (no live launcher list and no last-seen metadata for
// that launcher) is not assumed to be Claude Code: a real start sends the caller's effort to such a launcher as given
// and the node makes the final capability check, so the preview accepts any level some task agent takes.
func previewEffortVocab(agent string) []string {
	if agent != "" {
		return providerEffortVocab("", agent)
	}
	return slices.DeleteFunc(slices.Clone(proto.EffortWords), func(e string) bool {
		return !slices.Contains(proto.EffortLevels, e) && !slices.Contains(proto.CodexEffortLevels, e)
	})
}
