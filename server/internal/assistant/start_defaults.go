package assistant

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/compat"
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
	// Presets: the Agent presets (model profile tiers) to try, in order. The first one the task's computer can start
	// now wins; weights share starts among the usable ones. A preset that cannot be read, whose launcher the computer
	// lacks or whose launcher is at its usage limit is passed over. None: keep the start's own launcher and model.
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

// PresetChoice is one preset of StartDefaults: its name is a model profile tier, weight its share among usable ones.
type PresetChoice struct {
	Name   string
	Weight int
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
	startDefaultsMaxRules   = 16
	startDefaultsMaxPresets = 16
	startDefaultsMaxWeight  = 1000
	startDefaultsMaxExpiry  = time.Hour
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
type StartDefaultsRule struct {
	When StartDefaultsMatch `json:"when"`
	Then StartDefaultsThen  `json:"then"`
}

// StartDefaultsMatch selects tasks; every set field has to fit and an empty one matches anything.
type StartDefaultsMatch struct {
	Supervisor []string `json:"supervisor,omitempty"`
	Role       string   `json:"role,omitempty"`
	Readonly   *bool    `json:"readonly,omitempty"`
}

// StartDefaultsThen is what a matching task starts with; every field is opt-in.
type StartDefaultsThen struct {
	Presets   []StartDefaultsPreset `json:"presets,omitempty"`
	Effort    string                `json:"effort,omitempty"`
	Ultracode *bool                 `json:"ultracode,omitempty"`
	Goal      string                `json:"goal,omitempty"`
	PlanFirst bool                  `json:"plan_first,omitempty"`
}

// StartDefaultsPreset is an Agent preset to try, with its share among usable presets (0 counts as 1).
type StartDefaultsPreset struct {
	Name   string `json:"name"`
	Weight int    `json:"weight,omitempty"`
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
			return fmt.Errorf("%s.then must name at least one of presets, effort, ultracode, goal or plan_first", at)
		}
		for _, c := range r.When.Supervisor {
			if !slices.Contains(dispatchClasses, c) {
				return fmt.Errorf("%s.when.supervisor has %q; allowed: %v", at, c, dispatchClasses)
			}
		}
		if r.When.Role != "" && !slices.Contains(startDefaultsRoles, r.When.Role) {
			return fmt.Errorf("%s.when.role must be one of %v", at, startDefaultsRoles)
		}
		if r.Then.Effort != "" && !slices.Contains(proto.EffortLevels, r.Then.Effort) {
			return fmt.Errorf("%s.then.effort must be one of %v", at, proto.EffortLevels)
		}
		if r.Then.Goal != "" && r.Then.Goal != GoalFromDoneWhen && r.Then.Goal != GoalOff {
			return fmt.Errorf("%s.then.goal must be %q or %q", at, GoalFromDoneWhen, GoalOff)
		}
		if len(r.Then.Presets) > startDefaultsMaxPresets {
			return fmt.Errorf("%s.then.presets has %d entries; at most %d", at, len(r.Then.Presets), startDefaultsMaxPresets)
		}
		for j, pr := range r.Then.Presets {
			if n := utf8.RuneCountInString(strings.TrimSpace(pr.Name)); n == 0 || n > 64 {
				return fmt.Errorf("%s.then.presets[%d].name must be 1 to 64 characters", at, j)
			}
			if pr.Weight < 0 || pr.Weight > startDefaultsMaxWeight {
				return fmt.Errorf("%s.then.presets[%d].weight must be 0 to %d", at, j, startDefaultsMaxWeight)
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
				d.Presets = append(d.Presets, PresetChoice{Name: strings.TrimSpace(pr.Name), Weight: pr.Weight})
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

// presetIntN is the roll over the usable presets' weights; a var so tests are deterministic.
var presetIntN = func(n int) int { return rand.IntN(n) }

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
	params["launcher"], params["model"], params["effort"], params["tier"] = "", "", caller.effort, ""
	t.Tier = ""
	if len(d.Presets) == 0 {
		return ""
	}
	name, note, found := s.chooseStartPreset(ctx, t, d.Presets)
	if !found {
		return note
	}
	params["launcher"], params["model"], params["effort"], params["tier"] = "", "", caller.effort, name
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

// chooseStartPreset picks the first usable preset, or one of the weighted usable ones. A usable preset is one the
// profile in use has and whose launcher the task's computer starts now: present on the machine and not at its usage
// limit (a launch that failed for another reason still gets tried, the way an ordinary default does).
func (s *Service) chooseStartPreset(ctx context.Context, t *PlanTask, choices []PresetChoice) (name, note string, found bool) {
	p, _, err := s.activeModelProfile(ctx)
	if p == nil || err != nil {
		return "", "", false
	}
	// Read the machine's launcher list once per try; hidden launchers still start (#4661), and hiddenUsable says
	// whether a hidden one runs, so no preset needs another launcher-list RPC.
	list, listKnown := s.launchersHere(ctx, t.Machine)
	hidden := s.hiddenLaunchers(ctx)
	usable, skipped := make([]PresetChoice, 0, len(choices)), make([]string, 0)
	for _, c := range choices {
		if c.Name == "" {
			continue
		}
		tier, ok := p.Tiers[c.Name]
		if !ok {
			skipped = append(skipped, c.Name+": unknown preset")
			continue
		}
		// A hidden launcher is startable when its health check passes; a plain one must be in the machine's list.
		has := !listKnown || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == tier.Launcher })
		if !has && slices.Contains(hidden, tier.Launcher) && s.hiddenUsable(ctx, t.Machine, tier.Launcher) {
			has = true
		}
		if listKnown && !has {
			skipped = append(skipped, c.Name+": this computer has no launcher "+tier.Launcher)
			continue
		}
		if _, bad := s.launcherUnusable(ctx, t.Machine, tier.Launcher); bad {
			skipped = append(skipped, c.Name+": "+tier.Launcher+" is not available yet")
			continue
		}
		usable = append(usable, PresetChoice{Name: c.Name, Weight: max(c.Weight, 1)})
	}
	if len(usable) == 0 {
		return "", strings.Join(skipped, "; "), false
	}
	weighted := func() PresetChoice {
		total := 0
		for _, c := range usable {
			total += c.Weight
		}
		roll := presetIntN(total)
		for _, c := range usable {
			if roll < c.Weight {
				return c
			}
			roll -= c.Weight
		}
		return usable[len(usable)-1]
	}
	pick := usable[0]
	if len(usable) > 1 && slices.ContainsFunc(usable, func(c PresetChoice) bool { return c.Weight != usable[0].Weight }) {
		pick = weighted()
	}
	note = "preset " + pick.Name
	if len(skipped) > 0 {
		note += " (passed over: " + strings.Join(skipped, "; ") + ")"
	}
	return pick.Name, note, true
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
