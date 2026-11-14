package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The In-meeting conversation's model (meeting.room, #7980, docs/assistant/model-config.md "Meeting minutes and the
// in-meeting conversation (#7980)"). A room runs on Claude Code's CLI only: its removal of built-in tools and of the
// owner's own setup is defined there (docs/assistant/meeting-minutes.md "The room conversation"). So the purpose takes
// a launcher whose CLI is Claude Code, or a Messages API connection (Claude Code started with its address and key, as
// for the main chat); never Codex, agy, omp or a runtime, nor a Responses or Chat Completions connection. It never
// takes a runtime either (effectiveModel).

var errMeetingRoomClaudeOnly = i18n.Err("assistant-models.err_meeting_room_claude_only")

// runtimeSource: a launcher a runtime adds to a machine's list (DSH, Paseo, agy…), not one of the machine's own shell
// or PATH launchers, a provider added in Persona or plain claude.
func runtimeSource(raw string) bool {
	source := compat.Source(raw)
	return source != "" && source != "persona" && source != "builtin" && !machineSource(source)
}

// claudeCodeProvider: provider v, as Model settings lists it, starts Claude Code's CLI. A provider added in Persona is
// told by its record: a direct API one by its protocol, a launcher by its agent.
func claudeCodeProvider(v ModelProvider, stored []storedLauncher) bool {
	if !strings.HasPrefix(v.ID, "launcher.") {
		return false // a hub.json connection or a plugin: never started as a launcher
	}
	for _, l := range stored {
		if v.ID == "launcher."+l.Name {
			if l.Kind == "api" {
				return messagesAPI(l.API)
			}
			return l.AgentType() == "claude"
		}
	}
	return v.Kind == "launcher" && (v.Agent == "" || v.Agent == "claude")
}

// providerNamed is provider id as Model settings lists it, or, when the list does not have it (a wrapper only a
// machine's own list names), a launcher of that name with the agent its name tells.
func providerNamed(id string, providers []ModelProvider) ModelProvider {
	if i := slices.IndexFunc(providers, func(v ModelProvider) bool { return v.ID == id }); i >= 0 {
		return providers[i]
	}
	name := strings.TrimPrefix(id, "launcher.")
	return ModelProvider{ID: id, Name: name, Kind: "launcher", Agent: viewAgent(LauncherView{Name: name})}
}

// ownLauncher is provider's name when it is one of a computer's own launchers (its shell's, its PATH's, a runtime's
// or a hub.json wrapper): not a provider added in Persona, which is the same on every computer, nor plain claude.
func ownLauncher(provider string, stored []storedLauncher) (string, bool) {
	name, launcher := strings.CutPrefix(provider, "launcher.")
	if !launcher || name == "claude" || slices.ContainsFunc(stored, func(l storedLauncher) bool { return l.Name == name }) {
		return "", false
	}
	return name, true
}

// onRoomMachine is provider v as the assistant's computer, where rooms start, runs it. here is that computer's
// launchers with hub.json's applied (sessionLaunchers, roomLaunchers). One of its own launchers runs the agent its entry
// there says, hub.json's agent_type included: Model settings' list keeps the first computer's entry for a name several
// have, which may run another agent. ok false for a runtime's launcher, whatever agent it embeds. A launcher here does
// not name keeps the list's word, and one the list does not have either is not refused on a guess (providerNamed).
func onRoomMachine(v ModelProvider, stored []storedLauncher, here []ConfiguredLauncher) (ModelProvider, bool) {
	name, own := ownLauncher(v.ID, stored)
	if !own {
		return v, true
	}
	i := slices.IndexFunc(here, func(l ConfiguredLauncher) bool { return l.Name == name })
	if i < 0 {
		return v, true
	}
	if runtimeSource(here[i].Source) {
		return v, false
	}
	v.Agent = here[i].AgentType
	return v, true
}

// roomProvider is provider as the assistant's computer runs it (onRoomMachine), and whether that is Claude Code's CLI.
func roomProvider(provider string, providers []ModelProvider, stored []storedLauncher, here []ConfiguredLauncher) (ModelProvider, bool) {
	v, ok := onRoomMachine(providerNamed(provider, providers), stored, here)
	return v, ok && claudeCodeProvider(v, stored)
}

// roomLaunchers is machine's launchers as onRoomMachine reads them, hub.json's applied (sessionLaunchers): asked of its
// node now when live (a save, as the picker asks; name is the launcher picked, listed even while hidden), else as last
// seen (zshrcLaunchers: what Model settings lists, without asking a node). A list that does not come now is the last
// one seen, and with none, hub.json's entries alone: a wrapper configured there is told by the agent it was given
// even while its node can't answer.
func (s *Service) roomLaunchers(ctx context.Context, machine, name string, live bool) []ConfiguredLauncher {
	if machine == "" {
		return nil
	}
	if live {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if list, err := s.pickerLaunchers(withUnhidden(cctx, name), machine); err == nil {
			return list
		}
	}
	var seen []ConfiguredLauncher
	for _, m := range s.zshrcLaunchers(ctx, false) {
		if m.MachineID != machine {
			continue
		}
		for _, l := range m.Launchers {
			l.Source = cmp.Or(compat.Source(l.Source), "zshrc") // as machineLaunchers reads the node's list
			seen = append(seen, ConfiguredLauncher{Launcher: l})
		}
	}
	return sessionLaunchers(seen, s.hello(ctx).Models.Launchers[machine])
}

// roomClaudeCode: provider runs Claude Code's CLI on machine, the assistant's computer where rooms start, as its node
// lists its launchers now (roomLaunchers): another agent or a runtime's launcher there is refused, even when another
// computer runs Claude Code under the same name, and a Claude Code one is taken even when another computer's is not.
func (s *Service) roomClaudeCode(ctx context.Context, machine, provider string, providers []ModelProvider) bool {
	stored := s.storedLaunchers(ctx)
	var here []ConfiguredLauncher
	if name, own := ownLauncher(provider, stored); own {
		here = s.roomLaunchers(ctx, machine, name, true)
	}
	_, ok := roomProvider(provider, providers, stored, here)
	return ok
}

// roomNotClaudeStatus is meeting.room's status when what it would start on is not Claude Code on the assistant's
// computer (here, roomLaunchers as last seen): it follows a main chat on Codex or on a runtime, say, which the provider
// list may not name. Rooms cannot start until a Claude Code launcher is picked for it. "" when it is, and for any other
// purpose.
func roomNotClaudeStatus(l i18n.Lang, p ModelPurpose, providers []ModelProvider, stored []storedLauncher, here []ConfiguredLauncher) string {
	if p.ID != purposeMeetingRoom || p.Effective.Provider == "" {
		return ""
	}
	v, ok := roomProvider(p.Effective.Provider, providers, stored, here)
	if ok {
		return ""
	}
	return mc(l, "status_meeting_room_not_claude", i18n.Args{"launcher": cmp.Or(v.Name, v.ID)})
}
