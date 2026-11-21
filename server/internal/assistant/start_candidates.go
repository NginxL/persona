package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// startCandidate is one preset a start-defaults rule may choose on a machine: whether it can start there now, and
// why not. Availability mirrors the start path exactly (a hidden launcher whose health check passes still counts,
// an absent health record leaves a preset offered), so a preview and a real start never disagree.
type startCandidate struct {
	Name      string
	Launcher  string
	Agent     string // configured metadata from the same inventory; empty means unknown, never guessed
	Available bool
	Reason    string // why it cannot start now; "" when Available
}

// startCandidateSource lists the presets a rule can pick from on a machine. The built-in source reads the active
// model profile with the machine's launcher list and health, the same data GET /api/v1/assistant/worker-presets
// serves. A dynamic candidates source implements this one interface and is set on the Service at construction:
// rule matching, priority/weight selection and the per-try retry reset in start_defaults.go stay unchanged.
type startCandidateSource interface {
	candidates(ctx context.Context, machine string, names []string) (map[string]startCandidate, error)
}

// requestedStartCandidateNames limits availability work to the rule's choices, without changing their declared order.
func requestedStartCandidateNames(choices []PresetChoice) []string {
	names := make([]string, 0, len(choices))
	seen := make(map[string]bool, len(choices))
	for _, choice := range choices {
		if choice.Name == "" || seen[choice.Name] {
			continue
		}
		seen[choice.Name] = true
		names = append(names, choice.Name)
	}
	return names
}

// candidateSource answers with the Service's source, or the model-profile one when none was injected.
func (s *Service) candidateSource() startCandidateSource {
	if s.startCandidates != nil {
		return s.startCandidates
	}
	return profileCandidateSource{s: s}
}

// profileCandidateSource is the built-in startCandidateSource: the active model profile's tiers, judged on the
// machine's own launcher list and health (launchersHere, hiddenUsable, launcherUnusable), never pickerTiers'
// filtered list, which would hide the reason a preset was passed over.
type profileCandidateSource struct{ s *Service }

// candidateAgentLookup mirrors fitLauncher's everywhere/last-seen/configured metadata decisions for one inventory.
// An unknown wrapper stays unknown; only real launcher metadata or a named built-in determines the agent.
func (s *Service) candidateAgentLookup(ctx context.Context, machine string, list []proto.Launcher, known bool) func(string) string {
	discovered := make([]ConfiguredLauncher, 0, len(list))
	for _, l := range list {
		discovered = append(discovered, ConfiguredLauncher{Launcher: l})
	}
	agentOf := configuredStartAgentLookup(discovered, s.hello(ctx).Models.Launchers[machine])
	return func(name string) string {
		if name == "" || name == "claude" {
			return "claude"
		}
		if s.launcherEverywhere(ctx, name) {
			for _, l := range s.storedLaunchers(ctx) {
				if l.Name == name {
					return l.AgentType()
				}
			}
			return ""
		}
		if !known {
			return s.lastSeenAgent(ctx, machine, name).agent
		}
		return agentOf(name).agent
	}
}

type hiddenCandidateMetadata struct {
	usable bool
	agent  string
}

func (p profileCandidateSource) candidates(ctx context.Context, machine string, names []string) (map[string]startCandidate, error) {
	prof, _, err := p.s.activeModelProfile(ctx)
	if err != nil {
		return nil, err
	}
	if prof == nil {
		return map[string]startCandidate{}, nil
	}
	// Read the ordinary inventory once. Explicitly requested hidden launchers still start (#4661); cache each
	// unhidden probe and its metadata, including a false result, so repeated tiers never repeat its launcher-list RPC.
	list, listKnown := p.s.launchersHere(ctx, machine)
	agentOf := p.s.candidateAgentLookup(ctx, machine, list, listKnown)
	hidden := p.s.hiddenLaunchers(ctx)
	hiddenRows := make(map[string]hiddenCandidateMetadata)
	out := make(map[string]startCandidate, len(names))
	for _, name := range names {
		tier, ok := prof.Tiers[name]
		if !ok {
			continue
		}
		c := startCandidate{Name: name, Launcher: tier.Launcher, Agent: agentOf(tier.Launcher), Available: true}
		// A hidden launcher is startable when its health check passes; a plain one must be in the machine's list.
		has := !listKnown || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == tier.Launcher })
		if !has && slices.Contains(hidden, tier.Launcher) {
			row, probed := hiddenRows[tier.Launcher]
			if !probed {
				hiddenList, known, usable := p.s.hiddenLauncherInventory(ctx, machine, tier.Launcher)
				row = hiddenCandidateMetadata{usable: usable, agent: p.s.candidateAgentLookup(ctx, machine, hiddenList, known)(tier.Launcher)}
				hiddenRows[tier.Launcher] = row
			}
			has = row.usable
			if has {
				c.Agent = row.agent
			}
		}
		switch {
		case listKnown && !has:
			c.Available, c.Reason = false, "this computer has no launcher "+tier.Launcher
		default:
			if _, bad := p.s.launcherUnusable(ctx, machine, tier.Launcher); bad {
				c.Available, c.Reason = false, tier.Launcher+" is not available yet"
			}
		}
		out[name] = c
	}
	return out, nil
}
