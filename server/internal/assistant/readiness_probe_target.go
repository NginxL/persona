package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// What "Try it" checks (readinessProbe, #10365): the main conversation's next turn as the send path resolves it (its
// kept pick, else Model configuration › Main chat, a launcher that can't run it given way), its profile, and shaped as
// a turn of it (launchers.check assistant_turn, node/launchercheck_assistant.go). It used to check the Main chat
// launcher's own default model with the plain one-word check: a pass there cleared a model the chat could not run, and
// the next message failed again. A probe now speaks for one model of one launcher, the one the next turn runs, which is
// what the model step is judged on (readinessTurnLauncher): a pass of another model or launcher clears nothing, nor
// does one from an older node that ran a Claude Code launcher's plain check instead of the conversation's real turn.
// An external adapter's launcher is not checked at all (readinessProbe).

// readinessTargetWait bounds working out what the next turn runs (the conversation, the machine's launcher list).
var readinessTargetWait = 20 * time.Second

// readinessTarget is what the main conversation's next turn runs: the launcher, its model ("" its default) and effort,
// the profile asked for when the conversation has none recorded, and the conversation (none yet: "").
type readinessTarget struct {
	launcher, model, effort, configDir, conversation string
}

// readinessProbeTarget is the main conversation's next turn on machine as a message would run it (sendModel, or a new
// main conversation's start when there is none yet). It only reads: nothing is kept, started or switched, and a pick
// that can't run there is the refusal the message would get.
func (s *Service) readinessProbeTarget(ctx context.Context, machine string, v Settings) (readinessTarget, error) {
	ctx, cancel := context.WithTimeout(withLauncherPurpose(ctx, "assistant"), readinessTargetWait)
	defer cancel()
	p := map[string]any{}
	if v.Main != "" {
		if err := s.sendModel(ctx, machine, v.Main, p, s.requestCtxLang(ctx)); err != nil {
			if other := (*launcherOtherKind)(nil); errors.As(err, &other) {
				return readinessTarget{}, other.message()
			}
			return readinessTarget{}, err
		}
	} else {
		if err := s.applyModel(ctx, purposeMain, p); err != nil {
			return readinessTarget{}, err
		}
		if _, err := s.finishMainChatModel(ctx, machine, purposeMain, p, false); err != nil {
			return readinessTarget{}, err
		}
	}
	s.withNewConversationProfile(ctx, machine, p)
	t := readinessTarget{conversation: v.Main}
	t.launcher, _ = p["launcher"].(string)
	t.model, _ = p["model"].(string)
	t.effort, _ = p["effort"].(string)
	t.configDir, _ = p["config_dir"].(string)
	return t, nil
}

// params are launchers.check's for t.
func (t readinessTarget) params() map[string]any {
	p := map[string]any{"name": t.launcher, "purpose": "assistant", "assistant_turn": true}
	for k, v := range map[string]string{"model": t.model, "effort": t.effort, "config_dir": t.configDir, "conversation": t.conversation} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

// checks says p is a probe of t on machine: what a probe asked again within readinessProbeReuse may answer instead.
func (p readinessProbe) checks(machine string, t readinessTarget) bool {
	return p.Targeted && p.Machine == machine && p.Launcher == t.launcher && p.Model == t.model
}

// readinessProbeEvidence is what the last probe p says for the model step of launcher on machine, the one the main
// conversation's next turn runs (readinessTurnLauncher): only a probe of the model that turn asks for there
// (readinessTurnModel), and a pass only from a check the node shaped as the assistant's turn when the conversation's
// own last turn failed (turnFailed, as readinessModel counts it) and the node shapes that launcher's check
// (readinessCheckShaped). A probe an older manager kept checked the launcher's default with the plain check: its
// failure still counts, its pass says nothing.
func (s *Service) readinessProbeEvidence(ctx context.Context, p readinessProbe, machine, launcher string, binding ModelBinding,
	turnFailed bool) (readinessEvidence, bool) {
	if p.Machine != machine || p.Launcher != launcher || !p.Targeted && p.Check.OK {
		return readinessEvidence{}, false
	}
	if p.Targeted {
		if model, ok := s.readinessTurnModel(ctx, machine, launcher, binding); !ok || model != p.Model {
			return readinessEvidence{}, false
		}
	}
	if p.Check.OK && !p.Check.AssistantTurn && turnFailed && s.readinessCheckShaped(ctx, machine, launcher, p.Check.Agent) {
		return readinessEvidence{}, false
	}
	return readinessEvidence{at: p.At, ok: p.Check.OK, text: p.Check.Error}, true
}

// readinessCheckShaped says a check of launcher on machine, which answered as agent, could have run as the main
// conversation's turn: the node shapes only a Claude Code launcher it runs itself (node/launchercheck_assistant.go).
// Codex, another agent and a runtime's launcher (the runtime's own probe) get the one check there is, and its pass of
// the turn's model counts, or "Try it" could never clear such a chat's failed turn. One the list can't tell is shaped.
func (s *Service) readinessCheckShaped(ctx context.Context, machine, launcher, agent string) bool {
	if agent != "" && agent != "claude" {
		return false
	}
	kind, _ := s.readinessLauncher(ctx, machine, launcher)
	return kind != "runtime"
}

// readinessTurnLauncher is the launcher the main conversation's next turn on machine runs, as readinessProbeTarget
// resolves it but read from what the manager keeps (no node asked): the conversation's kept pick's launcher, else
// mainLauncher (Model configuration › Main chat's). A pick only records the launcher; the turn after it moves the
// conversation there (sendModel), so until then readiness judges the pick's launcher and model, as "Try it" checks
// them, not the one the conversation last ran. A pick on an external adapter's launcher, which no probe tries, leaves
// mainLauncher.
func (s *Service) readinessTurnLauncher(ctx context.Context, machine, mainLauncher string) string {
	if main := s.settings(ctx).Main; main != "" {
		if pick, ok := s.modelPickOf(ctx, machine, main); ok && pick.Launcher != "" && !proto.IsExternalLauncher(pick.Launcher) {
			return pick.Launcher
		}
	}
	return mainLauncher
}

// readinessTurnModel is the model the main conversation's next turn on launcher asks for, as readinessProbeTarget
// resolves it but read from what the manager keeps (no node asked): the conversation's kept pick when it runs on
// launcher, else Model configuration › Main chat's model when that is launcher's (binding, readinessModelScope); "" the
// launcher's default. ok is false when the kept pick runs on another launcher: no probe of launcher speaks for it.
func (s *Service) readinessTurnModel(ctx context.Context, machine, launcher string, binding ModelBinding) (string, bool) {
	if main := s.settings(ctx).Main; main != "" {
		if pick, ok := s.modelPickOf(ctx, machine, main); ok {
			if pick.Launcher != launcher {
				return "", false
			}
			if m := strings.TrimSpace(pick.Model); m != "" {
				return m, true
			}
		}
	}
	if binding.Provider == launcherProvider(launcher) && proto.ValidModelID(binding.Model) {
		return binding.Model, true // applyModel drops one that is no model id
	}
	return "", true
}
