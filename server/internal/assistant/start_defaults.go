package assistant

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Start defaults: what a task Agent starts with beyond what its caller named, by the kind of work. The owner, or an
// installed extension granted the assistant.start_defaults hub scope, stores a policy of rules (GET/PUT
// /api/v1/assistant/start-defaults, start_defaults_routes.go); a policy an extension writes must expire within
// startDefaultsMaxExpiry, so a stopped or uninstalled extension leaves nothing in force. With no policy, an expired one
// or no rule that fits a task, the task starts exactly as it would without the feature (apart from the Codex goal
// default, startGoalSource), and explicit caller values always win.

// StartDefaults is what one policy rule gives a task start.
type StartDefaults struct {
	// By says who wrote the policy (an extension's id, or owner), for the task's why and the logs.
	By string
	// Presets: the Agent presets (model profile tiers) to try. The highest priority with a usable candidate wins;
	// within it the first one the task's computer can start now wins, or one of them by weight when weights differ.
	// A preset that cannot be read, whose launcher the computer lacks or whose launcher is at its usage limit is
	// passed over. None: keep the start's own launcher and model.
	Presets []PresetChoice
	// Effort: applied when the caller gave none and the caller's own tier has none either.
	Effort string
	// Ultracode: applied only to a Claude Code start whose caller did not say.
	Ultracode *bool
	// Goal: GoalFromDoneWhen turns the task's done_when into one checkable condition typed as /goal after the first
	// message (docs/integrations/agent-goal.md "A goal set at the start"); GoalOff asks for none, not even the Codex
	// default; "" leaves it to the default (startGoalSource). Only new Claude Code and Codex starts take a goal.
	Goal string
	// PlanFirst asks for the self-planning constraint in the task's prompt, for a launcher whose model does not act
	// on a thinking-effort setting (the prompt is then the only lever).
	PlanFirst bool
}

// The goal sources a rule can name. GoalFromDoneWhen: build the start's goal from the task's done_when. GoalOff: no
// goal for the tasks the rule matches, which turns the Codex default off (startGoalSource).
const (
	GoalFromDoneWhen = "done_when"
	GoalOff          = "off"
)

// PresetChoice is one preset of StartDefaults: its name is a model profile tier, Priority orders the usable ones
// (larger wins; omitted is 0) and Weight shares starts within the highest-priority usable group (0 counts as 1).
type PresetChoice struct {
	Name     string
	Priority int
	Weight   int
}

// StartFacts is what a rule is matched against: only the kind of work, never the task's words.
type StartFacts struct {
	Machine, Role string
	Readonly      bool
	// Supervisor: the class of the conversation that follows the task up, as the dispatch policy names classes (main,
	// leader, background_supervisor, scheduled, other); "" when no conversation does.
	Supervisor string
	// DoneWhen: how many acceptance criteria the task has (the criteria themselves stay in the task).
	DoneWhen int
}

// Policy bounds.
const (
	startDefaultsMaxRules    = 16
	startDefaultsMaxPresets  = 16
	startDefaultsMaxWeight   = 1000
	startDefaultsMaxPriority = 1000
	startDefaultsMaxExpiry   = time.Hour
	// startDefaultsBodyLimit bounds a policy write and a preview. Reads answer both the current and the legacy field
	// names, so a round-tripped body can hold every rule and candidate twice: the worst case (16 rules × 16 candidates
	// of 64 runes each, at the highest weight and priority) stays comfortably under 256 KiB, while a single-name body
	// of the same policy is roughly half that. A client may always write just one name; both are accepted.
	startDefaultsBodyLimit = 256 << 10
)

// The task roles a rule can name.
var startDefaultsRoles = []string{"worker", roleLeader}

// StartDefaultsPolicy is the stored policy (Settings.StartDefaults). ExpiresAt is optional for the owner's own write;
// one an extension writes must expire within startDefaultsMaxExpiry. An expired policy is no policy.
type StartDefaultsPolicy struct {
	Rules     []StartDefaultsRule `json:"rules"`
	ExpiresAt *time.Time          `json:"expires_at,omitempty"`
	// By is who wrote it: the extension's id, or "owner"; set by the manager from the request, never from its body.
	By string `json:"by,omitempty"`
}

// StartDefaultsRule is one match and what a matching task starts with; the first rule that fits wins, except for the
// goal, which the first fitting rule naming one decides (matchStartDefaults).
//
// The wire name of the match is applies_when; when is its pre-convergence alias (#11719). Both names are accepted on
// every decode — a stored policy, PUT /api/v1/assistant/start-defaults and the nested start_defaults of PUT
// /api/v1/assistant alike — and a body naming both is refused, never merged. Reads answer with both names carrying
// the same match, so a client of either generation round-trips the policy unchanged.
type StartDefaultsRule struct {
	When StartDefaultsMatch `json:"applies_when"`
	Then StartDefaultsThen  `json:"then"`
}

// UnmarshalJSON accepts when and applies_when (StartDefaultsRule) with strict field checking of its own: the
// decoder's DisallowUnknownFields does not reach inside a custom unmarshaler. Presence is read from the raw bytes,
// so an explicit null counts as present and cannot slip past the conflict check the way a decoded nil pointer would.
func (r *StartDefaultsRule) UnmarshalJSON(data []byte) error {
	var raw struct {
		When        json.RawMessage   `json:"when"`
		AppliesWhen json.RawMessage   `json:"applies_when"`
		Then        StartDefaultsThen `json:"then"`
	}
	if err := strictJSON(data, &raw); err != nil {
		return err
	}
	*r = StartDefaultsRule{Then: raw.Then}
	switch hasWhen, hasApplies := len(raw.When) > 0, len(raw.AppliesWhen) > 0; {
	case hasWhen && hasApplies:
		// Both names are normal: the GET answer and a stored policy carry both, and they must read back unchanged.
		// Null is not the same wire value as an empty object, even though both decode to the zero match.
		if rawJSONNull(raw.When) != rawJSONNull(raw.AppliesWhen) {
			return i18n.ErrEnglish("assistant-start_defaults.alias_match_conflict")
		}
		var w, aw StartDefaultsMatch
		if err := strictJSON(raw.When, &w); err != nil {
			return err
		}
		if err := strictJSON(raw.AppliesWhen, &aw); err != nil {
			return err
		}
		if !reflect.DeepEqual(w, aw) {
			return i18n.ErrEnglish("assistant-start_defaults.alias_match_conflict")
		}
		r.When = aw
	case hasApplies:
		if err := strictJSON(raw.AppliesWhen, &r.When); err != nil {
			return err
		}
	case hasWhen:
		if err := strictJSON(raw.When, &r.When); err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON answers both names (StartDefaultsRule), the match under each.
func (r StartDefaultsRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		When        StartDefaultsMatch `json:"when"`
		AppliesWhen StartDefaultsMatch `json:"applies_when"`
		Then        StartDefaultsThen  `json:"then"`
	}{r.When, r.When, r.Then})
}

// StartDefaultsMatch selects tasks; every set field has to fit and an empty one matches anything.
type StartDefaultsMatch struct {
	Supervisor []string `json:"supervisor,omitempty"`
	Role       string   `json:"role,omitempty"`
	Readonly   *bool    `json:"readonly,omitempty"`
}

// UnmarshalJSON validates the match strictly and normalizes supervisor to its distinct classes, first-seen order
// kept: the classes are a set, so a duplicate carries no meaning, and accepting rather than rejecting a repeated
// class while storing it once keeps every accepted policy's dual-name read inside the write body limit.
func (m *StartDefaultsMatch) UnmarshalJSON(data []byte) error {
	type plain StartDefaultsMatch
	var raw plain
	if err := strictJSON(data, &raw); err != nil {
		return err
	}
	*m = StartDefaultsMatch(raw)
	m.Supervisor = distinctStrings(m.Supervisor)
	return nil
}

// distinctStrings returns the values with duplicates dropped, first-seen order kept.
func distinctStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// StartDefaultsThen is what a matching task starts with; every field is opt-in.
//
// The candidate list's wire name is candidates; presets is its pre-convergence alias (#11719), accepted and answered
// the same way as when / applies_when (StartDefaultsRule).
type StartDefaultsThen struct {
	Presets   []StartDefaultsPreset `json:"candidates,omitempty"`
	Effort    string                `json:"effort,omitempty"`
	Ultracode *bool                 `json:"ultracode,omitempty"`
	Goal      string                `json:"goal,omitempty"`
	PlanFirst bool                  `json:"plan_first,omitempty"`
}

// UnmarshalJSON accepts presets and candidates (StartDefaultsThen), strictly, and refuses a body naming both with
// different lists. Presence is read from the raw bytes (StartDefaultsRule), so an explicit null cannot bypass it.
func (t *StartDefaultsThen) UnmarshalJSON(data []byte) error {
	var raw struct {
		Presets    json.RawMessage `json:"presets"`
		Candidates json.RawMessage `json:"candidates"`
		Effort     string          `json:"effort,omitempty"`
		Ultracode  *bool           `json:"ultracode,omitempty"`
		Goal       string          `json:"goal,omitempty"`
		PlanFirst  bool            `json:"plan_first,omitempty"`
	}
	if err := strictJSON(data, &raw); err != nil {
		return err
	}
	*t = StartDefaultsThen{Effort: raw.Effort, Ultracode: raw.Ultracode, Goal: raw.Goal, PlanFirst: raw.PlanFirst}
	switch hasPresets, hasCandidates := len(raw.Presets) > 0, len(raw.Candidates) > 0; {
	case hasPresets && hasCandidates:
		if rawJSONNull(raw.Presets) != rawJSONNull(raw.Candidates) {
			return i18n.ErrEnglish("assistant-start_defaults.alias_candidates_conflict")
		}
		var p, c []StartDefaultsPreset
		if err := strictJSON(raw.Presets, &p); err != nil {
			return err
		}
		if err := strictJSON(raw.Candidates, &c); err != nil {
			return err
		}
		if !reflect.DeepEqual(p, c) {
			return i18n.ErrEnglish("assistant-start_defaults.alias_candidates_conflict")
		}
		t.Presets = c
	case hasCandidates:
		if err := strictJSON(raw.Candidates, &t.Presets); err != nil {
			return err
		}
	case hasPresets:
		if err := strictJSON(raw.Presets, &t.Presets); err != nil {
			return err
		}
	}
	// Compare aliases before canonicalizing, so padded and unpadded aliases still disagree. Once one list is chosen,
	// store the same name validation and selection already use; unbounded outer whitespace cannot double on a read.
	for i := range t.Presets {
		t.Presets[i].Name = strings.TrimSpace(t.Presets[i].Name)
	}
	return nil
}

// MarshalJSON answers both names (StartDefaultsThen), the same candidate list under each.
func (t StartDefaultsThen) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Presets    []StartDefaultsPreset `json:"presets,omitempty"`
		Candidates []StartDefaultsPreset `json:"candidates,omitempty"`
		Effort     string                `json:"effort,omitempty"`
		Ultracode  *bool                 `json:"ultracode,omitempty"`
		Goal       string                `json:"goal,omitempty"`
		PlanFirst  bool                  `json:"plan_first,omitempty"`
	}{t.Presets, t.Presets, t.Effort, t.Ultracode, t.Goal, t.PlanFirst})
}

// rawJSONNull distinguishes an explicit JSON null from every non-null value before decoded equality is considered.
func rawJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// strictJSON decodes data into out refusing unknown fields, for the custom unmarshalers above (a decoder's
// DisallowUnknownFields stops at their boundary).
func strictJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// StartDefaultsPreset is an Agent preset to try: Priority orders the usable ones (larger wins, 0 when omitted) and
// Weight is its share within that priority's group (0 counts as 1).
type StartDefaultsPreset struct {
	Name     string `json:"name"`
	Priority int    `json:"priority,omitempty"`
	Weight   int    `json:"weight,omitempty"`
}

// validate checks the shape without touching the store. allowNoExpiry is true for the owner's own write.
func (p *StartDefaultsPolicy) validate(allowNoExpiry bool, now time.Time) error {
	if len(p.Rules) == 0 {
		return errors.New("start_defaults.rules must name at least one rule")
	}
	if len(p.Rules) > startDefaultsMaxRules {
		return fmt.Errorf("start_defaults.rules has %d entries; at most %d", len(p.Rules), startDefaultsMaxRules)
	}
	for i, r := range p.Rules {
		at := fmt.Sprintf("start_defaults.rules[%d]", i)
		if len(r.Then.Presets) == 0 && r.Then.Effort == "" && r.Then.Ultracode == nil && r.Then.Goal == "" && !r.Then.PlanFirst {
			return fmt.Errorf("%s.then must name at least one of candidates, effort, ultracode, goal or plan_first", at)
		}
		if len(r.When.Supervisor) > len(dispatchClasses) {
			// UnmarshalJSON already dedupes, so this is a defensive bound on the distinct classes a rule may name.
			return fmt.Errorf("%s.applies_when.supervisor has %d entries; at most the %d distinct classes", at, len(r.When.Supervisor), len(dispatchClasses))
		}
		for _, c := range r.When.Supervisor {
			if !slices.Contains(dispatchClasses, c) {
				return fmt.Errorf("%s.applies_when.supervisor has %q; allowed: %v", at, c, dispatchClasses)
			}
		}
		if r.When.Role != "" && !slices.Contains(startDefaultsRoles, r.When.Role) {
			return fmt.Errorf("%s.applies_when.role must be one of %v", at, startDefaultsRoles)
		}
		if r.Then.Effort != "" && !slices.Contains(proto.EffortLevels, r.Then.Effort) {
			return fmt.Errorf("%s.then.effort must be one of %v", at, proto.EffortLevels)
		}
		if r.Then.Goal != "" && r.Then.Goal != GoalFromDoneWhen && r.Then.Goal != GoalOff {
			return fmt.Errorf("%s.then.goal must be %q or %q", at, GoalFromDoneWhen, GoalOff)
		}
		if len(r.Then.Presets) > startDefaultsMaxPresets {
			return fmt.Errorf("%s.then.candidates has %d entries; at most %d", at, len(r.Then.Presets), startDefaultsMaxPresets)
		}
		for j, pr := range r.Then.Presets {
			if n := utf8.RuneCountInString(strings.TrimSpace(pr.Name)); n == 0 || n > 64 || strings.ContainsRune(pr.Name, '\x00') {
				return fmt.Errorf("%s.then.candidates[%d].name must be 1 to 64 characters", at, j)
			}
			if pr.Weight < 0 || pr.Weight > startDefaultsMaxWeight {
				return fmt.Errorf("%s.then.candidates[%d].weight must be 0 to %d", at, j, startDefaultsMaxWeight)
			}
			if pr.Priority < 0 || pr.Priority > startDefaultsMaxPriority {
				return fmt.Errorf("%s.then.candidates[%d].priority must be 0 to %d", at, j, startDefaultsMaxPriority)
			}
		}
	}
	if p.ExpiresAt == nil {
		if !allowNoExpiry {
			return errors.New("start_defaults.expires_at is required on this route")
		}
		return nil
	}
	if !p.ExpiresAt.After(now) {
		return errors.New("start_defaults.expires_at must be in the future")
	}
	if !allowNoExpiry {
		if limit := now.Add(startDefaultsMaxExpiry); p.ExpiresAt.After(limit) {
			return fmt.Errorf("start_defaults.expires_at must be at most %s ahead", startDefaultsMaxExpiry)
		}
	}
	return nil
}

// activeStartDefaults is the stored policy while it is in force; absent or expired is none.
func (s *Service) activeStartDefaults(ctx context.Context) *StartDefaultsPolicy {
	p := s.settings(ctx).StartDefaults
	if p == nil || len(p.Rules) == 0 {
		return nil
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(time.Now().UTC()) {
		return nil
	}
	return p
}

// startDefaults is the defaults the policy in force gives a task with these facts; false when none applies (no
// policy, an expired one, or no rule fits). Tests stub it with startDefaultsFor.
func (s *Service) startDefaults(ctx context.Context, facts StartFacts) (StartDefaults, bool, error) {
	if s.startDefaultsFor != nil {
		return s.startDefaultsFor(ctx, facts)
	}
	p := s.activeStartDefaults(ctx)
	if p == nil {
		return StartDefaults{}, false, nil
	}
	d, ok := matchStartDefaults(p, facts)
	return d, ok, nil
}

// matchStartDefaults turns the first rule of p whose when fits the task into the start's defaults. The goal is decided
// on its own: by the first fitting rule that names one, which need not be that rule, so a later goal "off" still turns
// the Codex default off for a task an earlier rule silent on the goal fits (startGoalSource).
func matchStartDefaults(p *StartDefaultsPolicy, facts StartFacts) (StartDefaults, bool) {
	var d StartDefaults
	fits := false
	for _, r := range p.Rules {
		if !startDefaultsMatch(r.When, facts) {
			continue
		}
		if !fits {
			fits = true
			d = StartDefaults{By: p.By, Effort: r.Then.Effort, Ultracode: r.Then.Ultracode, PlanFirst: r.Then.PlanFirst}
			for _, pr := range r.Then.Presets {
				d.Presets = append(d.Presets, PresetChoice{Name: strings.TrimSpace(pr.Name), Priority: pr.Priority, Weight: pr.Weight})
			}
		}
		if r.Then.Goal != "" {
			d.Goal = r.Then.Goal
			break
		}
	}
	return d, fits
}

// startDefaultsMatch says whether a rule's when fits a task's facts; every set field has to fit.
func startDefaultsMatch(m StartDefaultsMatch, f StartFacts) bool {
	if len(m.Supervisor) > 0 && !slices.Contains(m.Supervisor, f.Supervisor) {
		return false
	}
	if m.Role != "" && m.Role != f.Role {
		return false
	}
	if m.Readonly != nil && *m.Readonly != f.Readonly {
		return false
	}
	return true
}

// presetEntropy is the trusted source the weight roll reads: crypto/rand in production, swappable in a test so the
// test can prove the roll really draws from it rather than from a predictable generator.
var presetEntropy io.Reader = crand.Reader

// presetIntN is the roll over the usable presets' weights, from a crypto-grade source; a var so tests are
// deterministic. It keeps the func(int) int shape (session_adopt.go's precedent for an entropy failure: panic with
// the cause, never a silent zero).
var presetIntN = func(n int) int {
	v, err := crand.Int(presetEntropy, big.NewInt(int64(n)))
	if err != nil {
		panic(fmt.Sprintf("assistant: cannot roll a start preset's weight: %v", err))
	}
	return int(v.Int64())
}

// taskSupervisorClass is the class (dispatch policy's) of the conversation that follows a task up: the one its
// supervision ledger names (written when the plan started, before the agent), else the leader conversation the plan
// was handed to. A task leader's worker takes the class of the conversation that follows up its leader's task. ""
// when no conversation follows the task up.
func (s *Service) taskSupervisorClass(ctx context.Context, t *PlanTask) string {
	sup := s.taskSupervisorOf(t)
	if sup == "" || s.isLeaderRef(sup) {
		return ""
	}
	return s.dispatchConversationClass(ctx, s.settings(ctx).MachineID, sup)
}

// taskSupervisorOf is the conversation that follows task t up: the one its supervision ledger names (written when the
// plan started, before the agent), else the leader conversation the plan was handed to (PlanTask.Supervisor, set only
// when the main conversation hands a task over: a leader's own plan leaves it empty). A task leader's worker is
// followed up through its leader's task, so it takes that task's. "" when no conversation follows the task up.
func (s *Service) taskSupervisorOf(t *PlanTask) string {
	sup := t.Supervisor
	todoID := t.TodoID
	if t.Parent != "" {
		todoID, sup = t.Parent, "" // the leader's own task
	}
	if todoID != "" {
		if todo, ok := s.todoNow(todoID); ok && todo.Supervision != nil && todo.Supervision.Supervisor != "" {
			sup = todo.Supervision.Supervisor
		}
	}
	return sup
}

// taskDirectionOf is the conversation whose direction notes accompany the task's first prompt. Direction notes are
// prompt context; acceptance follows the current supervisor. A leader's own plan takes its
// origin; a worker takes the legacy direction label from its parent task's saved plan (#10623).
func (s *Service) taskDirectionOf(ctx context.Context, t *PlanTask) string {
	if t.Supervisor != "" {
		return t.Supervisor
	}
	todoID := t.TodoID
	if t.Parent != "" {
		todoID = t.Parent
	}
	if todoID != "" {
		if todo, ok := s.todoNow(todoID); ok {
			if s.isLeader(ctx, todo.Origin) {
				return todo.Origin
			}
			if t.Parent != "" && todo.PlanID != "" {
				if p, err := s.loadPlan(ctx, todo.PlanID); err == nil {
					for _, planned := range p.Tasks {
						if planned.TodoID == todoID && planned.Supervisor != "" {
							return planned.Supervisor
						}
					}
				}
			}
		}
	}
	return s.taskSupervisorOf(t)
}

// roleOf is the task's role as a fact: roleLeader for a task leader, else worker.
func roleOf(t *PlanTask) string {
	if t.Role == roleLeader {
		return roleLeader
	}
	return "worker"
}

// taskStartDefaults is the policy's defaults for task t's start, false when none applies. It is asked once per start
// try, before anything is resolved, with only the kind of work.
func (s *Service) taskStartDefaults(ctx context.Context, t *PlanTask) (StartDefaults, bool) {
	d, ok, err := s.startDefaults(ctx, StartFacts{
		Machine: t.Machine, Role: roleOf(t), Readonly: t.Readonly,
		Supervisor: s.taskSupervisorClass(ctx, t), DoneWhen: len(t.DoneWhen),
	})
	if err != nil {
		s.logf("task %s: start defaults: %v", t.TodoID, err)
		return StartDefaults{}, false
	}
	return d, ok
}

// applyStartPreset picks the defaults' preset for a task whose caller named no launcher, model or tier, before the
// start is resolved: the start then goes through the tier exactly as if the task had named it (the launcher fit, its
// permission and the launch fingerprint all follow). params' launcher, model and effort go back to the caller's own,
// so a preset chosen by an earlier try does not pin this one. The returned note is for the task's why.
func (s *Service) applyStartPreset(ctx context.Context, t *PlanTask, caller startPick, d StartDefaults, params map[string]any) string {
	if caller.launcher != "" || caller.model != "" || caller.tier != "" {
		return "" // a preset would override a launcher, model or tier the task named itself
	}
	// Start every try from what the task named, so a preset an earlier try picked cannot pin this one; only a new
	// pick below changes it.
	params["launcher"], params["model"], params["effort"], params["speed"], params["tier"] = "", "", caller.effort, caller.speed, ""
	t.Tier = ""
	if len(d.Presets) == 0 {
		return ""
	}
	name, note, found := s.chooseStartPreset(ctx, t.Machine, d.Presets)
	if !found {
		return note
	}
	params["launcher"], params["model"], params["effort"], params["speed"], params["tier"] = "", "", caller.effort, caller.speed, name
	t.Tier = name
	t.startFilled.preset = true // the preset now decides the launch, from here to the end of this try
	return note
}

// applyStartExtras adds the defaults' effort, ultracode and self-planning prompt to a task's start, after the launcher
// fit (agent: what runs, "claude" for Claude Code); the goal is applyStartGoal's. caller is what the task named before
// any tier, Model Settings, the fit or a preset filled something in. Explicit caller values always win; why is the
// preset's note, said with the rest.
func (s *Service) applyStartExtras(ctx context.Context, t *PlanTask, caller startPick, d StartDefaults, agent string, params map[string]any, why string) {
	claude := agent == "claude"
	// Effort, strongest first: the caller's own effort, then the effort of a tier the caller named; otherwise the
	// policy's effort wins over a preset's and Model Settings' default (the policy says how this kind of task starts).
	// Claude Code only; other agents keep whatever they were given.
	tierEffort := false
	if caller.tier != "" {
		if tm, err := s.tierModel(ctx, caller.tier); err == nil && tm.Effort != "" {
			tierEffort = true
		}
	}
	if claude && strings.TrimSpace(d.Effort) != "" && caller.effort == "" && !tierEffort {
		params["effort"] = d.Effort
		t.Effort = d.Effort
		t.startFilled.effort = true
	}
	if claude && d.Ultracode != nil && *d.Ultracode && t.Ultracode == nil {
		params["ultracode"] = true
		on := true
		t.Ultracode = &on
		t.startFilled.ultracode = true
	}
	t.planFirst = d.PlanFirst
	if d.By != "" && why != "" {
		t.noteWhy(d.By + ": " + why)
	}
}

// startGoalSource says whether a task start takes a goal from its done_when, and whether that goal is the built-in
// default rather than a rule's. ruled says a policy rule fits the task (d is its defaults, whose Goal is the first
// fitting rule's that names one: matchStartDefaults). Only a new Claude Code or Codex start (not a resume) whose task
// has done_when criteria takes a goal at all. A rule that names a goal decides: done_when sets it on both agents, off
// on neither. Otherwise (no policy, an expired one, no rule that fits, or none that fits naming the goal) a Codex start
// takes its done_when by default: Codex has no Stop hook, so its goal is the only thing that keeps it working toward
// the criteria between the supervisor's reviews. Claude Code keeps starting without one.
func startGoalSource(d StartDefaults, ruled bool, agent string, resume bool, doneWhen int) (goal, byDefault bool) {
	if resume || doneWhen == 0 || agent != "claude" && agent != "codex" {
		return false, false
	}
	rule := ""
	if ruled {
		rule = d.Goal
	}
	switch {
	case rule == GoalFromDoneWhen:
		return true, false
	case rule == GoalOff:
		return false, false
	case agent == "codex":
		return true, true // the default
	}
	return false, false
}

// applyStartGoal adds the task's goal to its start when startGoalSource says so: Claude Code's /goal and Codex's
// alike, typed after the first message (docs/integrations/agent-goal.md). It runs on every start try, with or without
// a policy rule; a goal over proto.StartGoalMax is left out and logged. refusedHere says the start is one the node is
// known to refuse a goal for (defaultGoalRefusedOn): the built-in default is then left out, logged, rather than sent
// to be refused and started again; a rule's goal still goes, so its refusal reaches the task's why. byDefault says the
// goal sent is the built-in default, whose refusal is only logged (startTaskAgent).
func (s *Service) applyStartGoal(t *PlanTask, d StartDefaults, ruled bool, agent string, refusedHere bool, params map[string]any) (byDefault bool) {
	resume, _ := params["resume_session_id"].(string)
	goal, byDefault := startGoalSource(d, ruled, agent, resume != "", len(t.DoneWhen))
	if !goal {
		return false
	}
	if byDefault && refusedHere {
		s.logf("task %s: this start cannot take a goal; starting without the default goal", t.TodoID)
		return false
	}
	text := prompts.RenderTaskGoal(t.DoneWhen)
	if utf8.RuneCountInString(text) > proto.StartGoalMax {
		s.logf("task %s: goal longer than %d runes, no /goal set", t.TodoID, proto.StartGoalMax)
		return false
	}
	params["goal"] = text
	return byDefault
}

// defaultGoalRefusedOn says, from what the hub already knows, that m's node refuses a goal for this start: one in an
// engine runtime (Paseo, Cindy) or a desktop app's launcher (claude-desktop, codex-app), which have no screen to type
// /goal at (node/startgoal.go). Other refusals (an old node, a screenless terminal host) show only when it answers.
func defaultGoalRefusedOn(m Device, params map[string]any, agent startAgent) bool {
	host, _ := params["host"].(string)
	if agentStartHost(m, host) != "" {
		return true
	}
	switch source := compat.Source(agent.source); {
	case source == "codex_app", source == "claude_desktop", proto.RuntimeEngine(source):
		return true
	}
	return false
}

// chooseStartPreset picks among the usable presets: the highest priority with at least one usable wins, and within
// it the first usable one, or one of the weighted ones. Usability comes from the candidate source
// (start_candidates.go), the same data GET worker-presets serves: a preset the profile lacks, whose launcher the
// computer lacks or whose launcher is at its usage limit is passed over (a launch that failed for another reason
// still gets tried, the way an ordinary default does). Omitted priorities are all 0, one group, the pre-priority
// behavior. It reads and writes nothing of the task, so a preview shares this exact path.
func (s *Service) chooseStartPreset(ctx context.Context, machine string, choices []PresetChoice) (name, note string, found bool) {
	source, err := s.candidateSource().candidates(ctx, machine, requestedStartCandidateNames(choices))
	if err != nil {
		return "", "", false
	}
	usable, skipped := usablePresets(choices, source)
	if len(usable) == 0 {
		return "", joinSkipped(skipped), false
	}
	pick := weightedPreset(presetIntN, usable)
	note = "preset " + pick.Name
	if len(skipped) > 0 {
		note += " (passed over: " + joinSkipped(skipped) + ")"
	}
	return pick.Name, note, true
}

// skippedPreset is one candidate a rule passed over, with its name and reason kept apart, so a structured reader
// (the preview) never has to split them back out of a display string — a legal name may itself contain ": ".
type skippedPreset struct {
	Name   string
	Reason string
}

// joinSkipped renders the skipped presets for a task's why: "name: reason", in the order they were passed over.
func joinSkipped(skipped []skippedPreset) string {
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, s.Name+": "+s.Reason)
	}
	return strings.Join(parts, "; ")
}

// usablePresets splits the choices into those the source says can start now, in their stated order with weight 0
// read as 1, and the reasons the rest were passed over. An empty or unreadable source leaves nothing usable.
func usablePresets(choices []PresetChoice, source map[string]startCandidate) (usable []PresetChoice, skipped []skippedPreset) {
	for _, c := range choices {
		if c.Name == "" {
			continue
		}
		cand, ok := source[c.Name]
		if !ok {
			skipped = append(skipped, skippedPreset{Name: c.Name, Reason: "unknown preset"})
			continue
		}
		if !cand.Available {
			skipped = append(skipped, skippedPreset{Name: c.Name, Reason: cand.Reason})
			continue
		}
		usable = append(usable, PresetChoice{Name: c.Name, Priority: c.Priority, Weight: max(c.Weight, 1)})
	}
	return usable, skipped
}

// topPriorityGroup is the usable presets of the highest priority that has any, order and weights kept.
func topPriorityGroup(usable []PresetChoice) []PresetChoice {
	if len(usable) == 0 {
		return nil
	}
	top := usable[0].Priority
	for _, c := range usable[1:] {
		top = max(top, c.Priority)
	}
	group := make([]PresetChoice, 0, len(usable))
	for _, c := range usable {
		if c.Priority == top {
			group = append(group, c)
		}
	}
	return group
}

// weightedPreset picks one of the usable presets of the winning priority group: the first when the group's weights
// are equal, else a roll over the weights (roll is injected so tests are deterministic; presetIntN in production).
func weightedPreset(roll func(int) int, usable []PresetChoice) PresetChoice {
	group := topPriorityGroup(usable)
	if len(group) == 0 {
		return PresetChoice{} // nothing usable: the callers report no preset rather than picking one
	}
	pick := group[0]
	if len(group) > 1 && slices.ContainsFunc(group, func(c PresetChoice) bool { return c.Weight != group[0].Weight }) {
		total := 0
		for _, c := range group {
			total += c.Weight
		}
		left := roll(total)
		for _, c := range group {
			if left < c.Weight {
				return c
			}
			left -= c.Weight
		}
		return group[len(group)-1]
	}
	return pick
}

// goalRefusal: a session.start that carried a goal was refused for it (an old node, an agent other than Claude Code or
// Codex, a resume or a screenless host). A goal is an extra, so the caller starts the agent again without it and says why.
func (s *Service) goalRefusal(err error, params map[string]any) (reason string, refused bool) {
	if on, _ := params["goal"].(string); on == "" {
		return "", false
	}
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeStartGoalUnsupported {
		return "", false
	}
	return re.Message, true
}
