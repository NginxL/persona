package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a new Agent's launcher on the computer it starts on (#2640) ----
//
// Model configuration › Agents (agent.new) is the one launcher purpose with no computer, and a model profile's tiers
// have none either: their launcher may be a ~/.zshrc function or a wrapper script only some computers have. Every new
// Agent (a task, agent_start, New Agent in the apps, a message resuming an Agent started outside Persona) is held to
// what its computer can run before the start goes out, rather than the terminal answering "command not found"
// (docs/server/launcher-health.md, "A launcher the computer doesn't have").

// startPick is what a start's caller named itself, before a tier and Model configuration › Agents fill in the rest.
// ultracode is the tri-state the caller gave (nil: it said nothing, false: it explicitly asked for it off). permission
// is a task's permission mode as it stood before the launcher that runs set it (launchPick only; pickOf leaves it "").
type startPick struct {
	launcher, effort, model, tier, permission string
	ultracode                                 *bool
}

// launchPick is what task t holds of its launch now, as pickOf reads it from a start's params, with its ultracode.
func (t *PlanTask) launchPick() startPick {
	return startPick{launcher: strings.TrimSpace(t.Launcher), effort: strings.TrimSpace(t.Effort),
		model: strings.TrimSpace(t.Model), tier: strings.TrimSpace(t.Tier), permission: strings.TrimSpace(t.Permission),
		ultracode: copyBool(t.Ultracode)}
}

// startFill is what a start-defaults policy filled in on one start try: a preset (which then decided the launcher,
// model, effort, tier, and with the agent it runs the permission mode its launcher sets), the policy's effort, its
// ultracode. A rule that fits but fills nothing leaves it empty, and the start is the one without the feature.
type startFill struct{ preset, effort, ultracode bool }

func (f startFill) any() bool { return f.preset || f.effort || f.ultracode }

// storedLaunch is the launch task t keeps in its plan: what this try resolved, as every task keeps it, with the
// task's own value instead for whatever a start-defaults policy filled in on this try. A policy's values are for that
// one start (the Agent's session shows what it runs with): kept with the task, a remedy, a queued start or a retry
// from the card, which read the task back from the store, would take them for the task's own.
func (t PlanTask) storedLaunch() startPick {
	kept := t.launchPick()
	if t.callerPick == nil {
		return kept
	}
	own := *t.callerPick
	if t.startFilled.preset {
		kept.launcher, kept.model, kept.effort, kept.tier, kept.permission = own.launcher, own.model, own.effort, own.tier, own.permission
	}
	if t.startFilled.effort {
		kept.effort = own.effort
	}
	if t.startFilled.ultracode {
		kept.ultracode = copyBool(own.ultracode)
	}
	return kept
}

// copyBool is a copy of an optional switch, so the copy and the original never change together.
func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// pickOf is the caller's own pick in a start's params.
func pickOf(params map[string]any) startPick {
	l, _ := params["launcher"].(string)
	e, _ := params["effort"].(string)
	m, _ := params["model"].(string)
	t, _ := params["tier"].(string)
	u, _ := params["ultracode"].(*bool)
	return startPick{launcher: strings.TrimSpace(l), effort: strings.TrimSpace(e),
		model: strings.TrimSpace(m), tier: strings.TrimSpace(t), ultracode: u}
}

// resolveStart fills params (a session.start's, with the caller's own launcher, model, effort and tier, if any) with
// what the new Agent runs on: the tier, then Model configuration › Agents. It answers the caller's own pick, for
// fitLauncher.
func (s *Service) resolveStart(ctx context.Context, params map[string]any) (startPick, error) {
	own := pickOf(params)
	if err := s.applyStartTier(ctx, params); err != nil {
		return own, err
	}
	return own, s.applyModel(ctx, "agent.new", params)
}

// startAgent is what a start's launcher runs on the computer it starts on (fitLauncher): the agent type (claude, codex,
// a runtime's own such as paseo; "" when the computer's list does not say), and whether that agent asks for approval
// unless its runtime's switch is on, which a start in bypassPermissions turns on (proto.Launcher.AutoAccept: Paseo's
// Auto Accept).
type startAgent struct {
	agent      string
	source     string // the selected launcher's provenance, retained through fitting and health detours
	autoAccept bool
}

// launcherStartAgent is what launcher l runs.
func launcherStartAgent(l ConfiguredLauncher) startAgent {
	return startAgent{agent: l.AgentType, source: l.Source, autoAccept: l.AutoAccept}
}

// allowAll: the Agents Persona itself starts on the launcher (agent_start, a planned task, a read-only one too) run with
// Allow all, bypassPermissions: Claude Code's, and, by the owner's decision (#8141), those whose approval is a switch
// Allow all turns on (Paseo's providers with Auto Accept: Grok and its other ACP providers, OpenCode), which otherwise
// ask for every tool and leave a card the assistant never answers. Codex keeps its sandbox, on Paseo as in a terminal:
// its tasks take the settled mode (tasks_permission.go). A start a person makes (New Agent) keeps the mode they chose.
func (a startAgent) allowAll() bool { return a.agent == "claude" || a.autoAccept }

// fitLauncher holds params' launcher (resolveStart's) to what computer m can start. One the caller named that m does
// not have is refused, naming the ones it has. One a default gave (the agent.new binding, a profile, a tier) gives way
// to one m has (launcherInstead), with that launcher's own default model and the caller's effort; note says so in the
// user's language, for the task's why and the tool's answer. A list that can't be read (m offline, a timeout) counts
// as having it, as mainChatFit does: nothing is refused or replaced on a guess. What the launcher runs comes from the
// same list, so a caller changing permissions by it does not have to ask for it again.
func (s *Service) fitLauncher(ctx context.Context, m Device, own startPick, params map[string]any) (note string, agent startAgent, err error) {
	name, _ := params["launcher"].(string)
	if proto.IsExternalLauncher(name) {
		return "", startAgent{}, i18n.Err("external-adapter.purpose_unsupported")
	}
	asked, _ := params["host"].(string)
	if host := agentStartHost(m, asked); host != "" {
		// Where the start's engine runs only its own launchers, plain claude and a managed launcher do not run either.
		if note, agent, err, done := s.fitEngineLauncher(ctx, m, host, params); done {
			return note, agent, err
		}
	}
	if s.launcherEverywhere(ctx, name) {
		if name == "" || name == "claude" {
			return "", startAgent{agent: "claude"}, nil
		}
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == name {
				return "", startAgent{agent: l.AgentType(), source: "persona"}, nil
			}
		}
		return "", startAgent{}, nil
	}
	list, known := s.launchersHere(withUnhidden(ctx, name), m.ID) // a hidden default still runs (#4659)
	if !known {
		return "", s.lastSeenAgent(ctx, m.ID, name), nil
	}
	configured := make([]ConfiguredLauncher, 0, len(list))
	for _, l := range list {
		configured = append(configured, ConfiguredLauncher{Launcher: l})
	}
	configured = sessionLaunchers(configured, s.hello(ctx).Models.Launchers[m.ID])
	agentOf := func(name string) startAgent {
		for _, l := range configured {
			if l.Name == name {
				return launcherStartAgent(l)
			}
		}
		return startAgent{}
	}
	if slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name }) {
		return "", agentOf(name), nil
	}
	if own.launcher != "" {
		return "", startAgent{}, i18n.Err("assistant-tasks.launcher_not_on_machine", i18n.Args{
			"machine": firstNonEmpty(m.Name, m.ID), "launcher": name, "launchers": strings.Join(launcherNamesOn(m, list), ", "),
		})
	}
	to := launcherInstead(m, list)
	for _, k := range []string{"model", "effort", "model_fingerprint"} {
		delete(params, k)
	}
	params["launcher"] = to
	if own.effort != "" {
		params["effort"] = own.effort
	}
	if err := s.applyModel(ctx, "agent.new", params); err != nil {
		return "", startAgent{}, err
	}
	s.logf("start on %s: launcher %s is not there, using %s", m.ID, name, to)
	note = i18n.T(s.lang(ctx), "assistant-tasks.launcher_swapped", i18n.Args{
		"launcher": name, "machine": firstNonEmpty(m.Name, m.ID), "to": to,
	})
	current, _ := params["launcher"].(string)
	return note, agentOf(current), nil
}

// lastSeenAgent is what launcher name runs on machine by the last launcher list the hub saw there (the persisted
// snapshot, zshrcLaunchersLastSeen), with hub.json's launchers applied, for a start whose live list could not be read
// (a slow or busy computer). Without it a Claude wrapper such as a relay script counted as an unknown agent: its task
// fell back to the computer's Claude default mode, and when that read timed out too, to "default", so the Agent
// stopped on its first Bash approval. Nothing when the snapshot does not name it: nothing is guessed.
func (s *Service) lastSeenAgent(ctx context.Context, machine, name string) startAgent {
	if name == "" {
		return startAgent{}
	}
	seen, _ := s.zshrcLaunchersLastSeen(ctx)
	for _, z := range seen {
		if z.MachineID != machine {
			continue
		}
		var configured []ConfiguredLauncher
		for _, l := range z.Launchers {
			if l.Name == name {
				configured = append(configured, ConfiguredLauncher{Launcher: l})
			}
		}
		if len(configured) == 0 {
			return startAgent{}
		}
		for _, l := range sessionLaunchers(configured, s.hello(ctx).Models.Launchers[machine]) {
			if l.Name == name {
				return launcherStartAgent(l)
			}
		}
	}
	return startAgent{}
}

// launchersHere is what machine can start: machineLaunchers (its own launchers but the hidden ones, and the managed
// ones) and those hub.json names for it (a wrapper the node may not list, configuredLauncher). known false when its
// list can't be read, or the answer names none of its own: a node that read its shell lists plain claude at least
// (node mergeLaunchers), so such an answer is no list. The hub-added built-in is not evidence of a readable list.
func (s *Service) launchersHere(ctx context.Context, machine string) (list []proto.Launcher, known bool) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool {
		source := compat.Source(l.Source)
		return source != "persona" && source != "builtin"
	}) {
		return nil, false
	}
	for _, c := range s.hello(ctx).Models.Launchers[machine] {
		if c.Name != "" && !proto.IsExternalLauncher(c.Name) && c.Source != proto.ExternalLauncherSource &&
			!slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == c.Name }) {
			list = append(list, c.Launcher)
		}
	}
	return list, true
}

// launcherEverywhere: every machine has name (plain claude; a managed launcher, which the node runs through personad
// launch), so its list need not be asked.
func (s *Service) launcherEverywhere(ctx context.Context, name string) bool {
	return !proto.IsExternalLauncher(name) && (name == "" || name == "claude" || slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == name }))
}

// launcherInstead is the launcher a default m does not have gives way to: plain claude when m has Claude Code (its
// node said a version, or its list names claude), else the first in its list that starts an agent with a prompt (not a
// runtime's), else claude all the same.
func launcherInstead(m Device, list []proto.Launcher) string {
	if m.ClaudeVersion != "" || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		return "claude"
	}
	if i := slices.IndexFunc(list, startableLauncher); i >= 0 {
		return list[i].Name
	}
	return "claude"
}

// startableLauncher: l starts an agent with a prompt: one of the machine's own, or a managed one (healthLaunchers).
func startableLauncher(l proto.Launcher) bool {
	return l.Name != "" && !proto.IsExternalLauncher(l.Name) && l.Source != proto.ExternalLauncherSource &&
		(l.Source == "" || machineSource(l.Source) || compat.Source(l.Source) == "persona")
}

// launcherNamesOn are the names a refusal lists: plain claude first when m has Claude Code, then its list's.
func launcherNamesOn(m Device, list []proto.Launcher) []string {
	var names []string
	if m.ClaudeVersion != "" {
		names = append(names, "claude")
	}
	for _, l := range list {
		if l.Name != "" && !slices.Contains(names, l.Name) {
			names = append(names, l.Name)
		}
	}
	if len(names) == 0 {
		names = append(names, "claude")
	}
	return names
}
