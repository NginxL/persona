package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// agent_switch (#5973): the assistant moves a running Claude Code agent to another launcher on its computer and goes on
// with it, in one step, when the user asks (the subscription's usage limit stopped it, say, and the user moved the
// main conversation to another provider already). It is the user's own switch from an Agent's chat (#5035,
// node/launcher_switch.go): the pick (session.modes with the launcher and from), then a message that carries it out
// (session.send with apply_switch). Where the user's message would go on the old launcher and leave the switch waiting
// (the agent busy, a draft or a dialog in the box), the assistant's is refused with nothing typed and the switch dropped
// (switch_now): it never types on the launcher the agent runs on, and never leaves a switch the user did not pick
// waiting for their next message. A pending usage-limit continuation of the agent is not sent afterwards: the agent's
// turn moved on, and workGuard drops a wake whose session did.

// agentSwitchWait is how long the tool waits for the node to carry the switch out: its checks before the quit take at
// most a minute and the rest at most five (node switchProbeWait, switchBudget), as the hub's switchSendWait.
const agentSwitchWait = 8 * time.Minute

// agentSwitchArgs are agent_switch's arguments.
type agentSwitchArgs struct {
	Machine   string `json:"machine"`
	SessionID string `json:"session_id"`
	Launcher  string `json:"launcher"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Text      string `json:"text"`
}

func (s *Service) toolAgentSwitch(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a agentSwitchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	t := switchTarget{Launcher: strings.TrimSpace(a.Launcher), Model: strings.TrimSpace(a.Model), Effort: strings.TrimSpace(a.Effort)}
	if t.Launcher == "" {
		return "", errors.New("launcher is required: the name of a launcher on the agent's computer")
	}
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	label := agentLabel(c)
	if err := switchUnsupported(c, t.Launcher); err != nil {
		if errors.Is(err, errOnLauncherAlready) {
			return "", fmt.Errorf("%s runs on launcher %s already: to go on, use agent_send. Nothing was done", label, t.Launcher)
		}
		return "", fmt.Errorf("%w. Nothing was done", err)
	}
	own := cmp.Or(c.Launcher, "claude")
	text := strings.TrimSpace(a.Text)
	if text == "" {
		if c.TurnErrorKind != proto.TurnErrorUsageLimit {
			return "", fmt.Errorf("text is required: what %s should do on the new launcher (only an agent stopped on a usage limit "+
				"goes on without it). Nothing was done", label)
		}
		text = prompts.RenderAgentLimitContinue(answerLanguage(s.hello(ctx).Locale)) // what the manager sends it once the limit resets (agents_work_tick.go)
	}
	if exitText(text) {
		return "", errors.New("text cannot be /exit or /quit: the agent is moved to go on working. Nothing was done")
	}
	if err := s.switchReachable(ctx, am, t.Launcher, nodeproto.LauncherSwitchNow); err != nil {
		return "", fmt.Errorf("%w. Nothing was done", err)
	}
	if ok, err := s.confirmOutbound(ctx, machine, self, "agent_switch", label+" · "+am.Name+" → "+t.Launcher+"\n\n"+text, args); err != nil {
		return "", err
	} else if !ok {
		return "The user declined on the phone: the agent was not moved and nothing was sent.", nil
	}
	// Read after the phone, as agent_send: a task another conversation supervises gets the words as a side conversation.
	if _, theirs, err := s.supervisedElsewhere(ctx, self, am, c); err != nil {
		return "", err
	} else if theirs {
		text = prompts.RenderSideConversation(text)
	}
	if err := s.moveAgent(ctx, am, c, t, text); err != nil {
		return "", fmt.Errorf("%s was not moved to %s: %w. Nothing was sent", label, t.Launcher, err)
	}
	s.logf("agent_switch %s on %s: %s → %s (from %s)", short(c.SessionID), am.ID, own, t.Launcher, short(self))
	return fmt.Sprintf("Moved %s on %s from launcher %s to %s: the same conversation goes on in the same terminal, and it was sent: %s",
		label, am.Name, own, t.Launcher, clip(oneLine(text), 200)), nil
}

// switchTarget is the launcher a switch moves an agent to, with the model and effort there ("": the launcher's default,
// the effort it has).
type switchTarget struct {
	Launcher, Model, Effort string
}

// errOnLauncherAlready: the agent runs on the launcher it would be moved to.
var errOnLauncherAlready = errors.New("on that launcher already")

// switchUnsupported says why agent c cannot be moved to launcher at all, nil when it can: only a Claude Code agent in
// a terminal that is running moves (the node checks the rest).
func switchUnsupported(c proto.Session, launcher string) error {
	label := agentLabel(c)
	switch {
	case c.Assistant:
		return errors.New("that is an assistant conversation, not a coding agent: its launcher is chosen in Model settings")
	case c.SourceKind != "":
		return fmt.Errorf("%s runs in %s, which keeps its own launcher: only a Claude Code agent in a terminal moves", label, c.SourceKind)
	case c.Agent != "" && c.Agent != "claude":
		return fmt.Errorf("%s runs %s: only a Claude Code agent moves to another launcher", label, c.Agent)
	case stoppedAgent(c) || c.Status == proto.StatusOffline:
		return fmt.Errorf("%s has stopped, so it was not moved. agents_switch can make its next resume (the user's Resume, or "+
			"agent_send with resume) come up on the launcher, or start a new agent with agent_start in its directory on that launcher", label)
	case cmp.Or(c.Launcher, "claude") == launcher && !c.OffLauncher: // off it, its own launcher is a switch back (#11792)
		return errOnLauncherAlready
	}
	return nil
}

// switchReachable says why agents on am cannot be moved to launcher now, nil when they can: this manager connects the
// computer, its node can (capability), and the launcher is in the computer's scope.
func (s *Service) switchReachable(ctx context.Context, am Device, launcher, capability string) error {
	nodes, _ := s.devs.(nodeAnnouncer)
	if s.devs == nil || nodes == nil || !s.devs.Has(am.ID) {
		return fmt.Errorf("%s is not connected by this manager: it cannot move agents there", am.Name)
	}
	if !nodes.NodeAnnounced(am.ID, capability) {
		return fmt.Errorf("%s cannot move an agent to another launcher now: its node is offline or older than this. "+
			"Update it; meanwhile the user can switch it from the agent's chat on the web", am.Name)
	}
	return s.launcherScopeErr(ctx, am.ID, launcher) // a provider out of this computer's scope
}

// moveAgent switches agent c on am to t now: the pick (session.modes with the launcher and from), then text with
// switch_now, or with no text session.switch (an idle agent waits on the new launcher). The node's refusal comes back
// as it is (switch_busy for a busy agent), with nothing typed and no switch left waiting.
func (s *Service) moveAgent(ctx context.Context, am Device, c proto.Session, t switchTarget, text string) error {
	// The node carries the switch out on its own clock once it starts (a quit, a start on the new launcher, a way back):
	// a caller that stops waiting must not cut it short.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentSwitchWait)
	defer cancel()
	own := cmp.Or(c.Launcher, "claude")
	pick := map[string]any{"session_id": c.SessionID, "launcher": t.Launcher, "from": own}
	if t.Model != "" {
		pick["model"] = t.Model
	}
	if t.Effort != "" {
		pick["effort"] = t.Effort
	}
	if err := s.call(wctx, am.ID, DeviceSessionModes, pick, nil); err != nil {
		return err
	}
	var err error
	if text == "" {
		err = s.call(wctx, am.ID, DeviceSessionSwitch, map[string]any{"session_id": c.SessionID}, nil)
	} else {
		params := sendParams(c.SessionID, text, false)
		params["apply_switch"], params["switch_now"] = true, true
		err = s.call(wctx, am.ID, DeviceSessionSend, params, nil)
	}
	// For an agent off its launcher, picking that launcher again is no way to drop the switch back to it: it is the
	// switch back (a switch to another launcher it does cancel). The node drops it on a refusal; one whose message never
	// got there waits for the user's next message (or 24 h).
	if err != nil && !(c.OffLauncher && t.Launcher == own) {
		s.dropAgentSwitch(wctx, am.ID, c.SessionID, own)
	}
	return err
}

// dropAgentSwitch drops the switch agent_switch picked for session id when its message did not go: the node drops it
// itself on a refusal, but not when the message never reached it. Picking the launcher the agent ran on (from) drops a
// waiting switch; one carried out meanwhile (the call stopped waiting while it ran) makes that pick stale, and it stays.
func (s *Service) dropAgentSwitch(ctx context.Context, machine, id, own string) {
	if err := s.call(ctx, machine, DeviceSessionModes, map[string]any{"session_id": id, "launcher": own, "from": own}, nil); err != nil {
		s.logf("agent_switch %s on %s: drop the switch: %v", short(id), machine, err)
	}
}
