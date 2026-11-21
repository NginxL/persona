package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// An API purpose may pick a launcher without a gateway the manager can call (plain claude, codex, agy, or a computer's
// own launcher): each request then runs that launcher's CLI headless once on the main chat's computer, which answers
// with the final text (docs/assistant/model-config.md "Running API purposes on a launcher", #5823, #12027). The manager
// still resolves the purpose and reads the answer; the node runs the CLI with no tools and leaves no session (model.run).

// modelRunTimeout bounds a run whose caller set no deadline.
const modelRunTimeout = 2 * time.Minute

// runClient is the client of c, a launcher on c.ViaMachine (c.RunLauncher).
func (s *Service) runClient(c ai.Config) *ai.Client {
	return ai.NewRun(c, func(ctx context.Context, r ai.Request) (ai.Reply, error) { return s.modelRun(ctx, c, r) })
}

// modelRun answers r with one model.run on c's computer.
func (s *Service) modelRun(ctx context.Context, c ai.Config, r ai.Request) (ai.Reply, error) {
	if proto.IsExternalLauncher(c.RunLauncher) {
		return ai.Reply{}, i18n.Err("external-adapter.purpose_unsupported")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, modelRunTimeout)
		defer cancel()
	}
	p := proto.ModelRunParams{Launcher: c.RunLauncher, Model: c.Model, System: r.System, Prompt: runPrompt(r.Messages)}
	effortOK := slices.Contains(proto.EffortLevels, c.ReasoningEffort) ||
		(slices.Contains(proto.CodexEffortLevels, c.ReasoningEffort) && s.runLauncherAgent(ctx, c.ViaMachine, c.RunLauncher) == "codex")
	if effortOK { // "off" / "none" (and Codex-only levels on non-Codex launchers) defer to the launcher's own default
		p.Effort = c.ReasoningEffort
	}
	_, p.Configured = s.configuredLauncher(ctx, c.ViaMachine, c.RunLauncher)
	d, _ := ctx.Deadline()
	p.TimeoutMS = max(time.Until(d).Milliseconds(), 1)
	raw, err := json.Marshal(p)
	if err != nil {
		return ai.Reply{}, err
	}
	var out json.RawMessage
	if s.devs == nil {
		err = devices.ErrNoMachine
	} else {
		out, err = s.devs.CallNode(ctx, c.ViaMachine, proto.MethodModelRun, raw)
	}
	if err != nil {
		return ai.Reply{}, s.modelRunErr(ctx, c, err)
	}
	var a proto.ModelRunAnswer
	if err := json.Unmarshal(out, &a); err != nil {
		return ai.Reply{}, fmt.Errorf("model.run: bad answer: %w", err)
	}
	return ai.Reply{Text: a.Text, Model: a.Model, Usage: a.Usage}, nil
}

// runLauncherAgent resolves the CLI agent behind launcher name on machine for model.run effort filtering.
func (s *Service) runLauncherAgent(ctx context.Context, machine, name string) string {
	if l, ok := s.configuredLauncher(ctx, machine, name); ok {
		if agent := cmp.Or(l.AgentType, l.Agent); agent != "" {
			return agent
		}
	}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Name == name {
			return l.AgentType()
		}
	}
	if machine != "" {
		for _, m := range s.zshrcLaunchers(ctx, false) {
			if m.MachineID != machine {
				continue
			}
			for _, l := range m.Launchers {
				if l.Name == name {
					return viewAgent(LauncherView{Name: l.Name, Agent: l.Agent})
				}
			}
		}
	}
	for _, v := range s.launcherViews(ctx) {
		if v.Name == name {
			return viewAgent(v)
		}
	}
	return viewAgent(LauncherView{Name: name})
}

// runPrompt is the conversation as the CLI reads it: a single user turn as it is, several as one transcript.
func runPrompt(turns []ai.Message) string {
	if len(turns) == 1 && turns[0].Role == ai.RoleUser {
		return turns[0].Content
	}
	in := make([]prompts.LauncherRunTurn, len(turns))
	for i, t := range turns {
		in[i] = prompts.LauncherRunTurn{Assistant: t.Role == ai.RoleAssistant, Text: t.Content}
	}
	return prompts.RenderLauncherRunInput(in)
}

// modelRunErr says what went wrong in the user's language: the computer offline or gone, the CLI not logged in there,
// no answer in time. The node's other errors (an old node, what the CLI printed) go as they are.
func (s *Service) modelRunErr(ctx context.Context, c ai.Config, err error) error {
	if errors.Is(err, devices.ErrNoMachine) {
		return errViaMachine(c.ViaMachine)
	}
	name := c.ViaMachine
	if d, ok := s.device(ctx, c.ViaMachine); ok && d.Name != "" {
		name = d.Name
	}
	args := i18n.Args{"name": name, "launcher": c.RunLauncher}
	var re *rpc.Error
	switch {
	case errors.Is(err, apierr.ErrOffline), errors.Is(err, rpc.ErrClosed):
		return errors.New(i18n.T(s.lang(ctx), "assistant-models.err_run_offline", args))
	case errors.As(err, &re) && re.Code == proto.CodeModelRunLoggedOut:
		return errors.New(i18n.T(s.lang(ctx), "assistant-models.err_run_logged_out", args))
	case errors.As(err, &re) && re.Code == proto.CodeModelRunTimeout, errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-models.err_run_timeout", args), context.DeadlineExceeded)
	}
	return err
}
