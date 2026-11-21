package assistant

import (
	"context"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// validateStartSpeed checks and normalizes a start's optional speed without
// collapsing explicit standard: that still overrides a preset's faster speed.
func validateStartSpeed(params map[string]any) (string, error) {
	raw, ok := params["speed"]
	if !ok || raw == nil {
		return "", nil
	}
	speed, ok := raw.(string)
	if !ok {
		return "", i18n.ErrEnglish("assistant-tasks.speed_type")
	}
	speed = strings.TrimSpace(speed)
	if !proto.ValidSpeed(speed) {
		return "", i18n.ErrEnglish("assistant-tasks.speed_invalid", i18n.Args{"speed": i18n.Verbatim(strconv.Quote(speed))})
	}
	params["speed"] = speed
	return speed, nil
}

// clearStandardSpeed leaves no override on the wire: the launcher's own setting
// applies, including on a node that does not know the speed field.
func clearStandardSpeed(params map[string]any) {
	speed, _ := params["speed"].(string)
	if !proto.FasterSpeed(speed) {
		delete(params, "speed")
	}
}

// checkStartSpeed refuses unsupported faster choices before an approval card or
// start. An unknown agent is checked again by the node rather than guessed here.
func checkStartSpeed(params map[string]any, agent startAgent, resume string) error {
	speed, err := validateStartSpeed(params)
	if err != nil {
		return err
	}
	if !proto.FasterSpeed(speed) {
		return nil
	}
	if resume != "" {
		return i18n.ErrEnglish("assistant-tasks.speed_resume", i18n.Args{"speed": i18n.Verbatim(strconv.Quote(speed))})
	}
	source := compat.Source(agent.source)
	terminal := source == "" || source == "zshrc" || source == "path" || source == "persona" || source == "builtin"
	if !terminal || agent.agent != "" && !proto.SpeedSupported(agent.agent, speed) {
		launcher, _ := params["launcher"].(string)
		kind := agent.agent
		if kind == "" {
			kind = source
		}
		return i18n.ErrEnglish("assistant-tasks.speed_unsupported", i18n.Args{
			"launcher": i18n.Verbatim(strconv.Quote(launcher)), "kind": i18n.Verbatim(kind), "speed": i18n.Verbatim(strconv.Quote(speed)),
		})
	}
	return nil
}

// resolveFitPlanTask is the read-only resolve -> machine-fit half of fitPlanTaskSpeed. The boolean is false only
// when the machine disappeared, preserving that path's existing early return before speed validation; the resolved
// params are still returned then so callers can read the configured permission mode.
func (s *Service) resolveFitPlanTask(ctx context.Context, t *PlanTask) (startAgent, map[string]any, bool, error) {
	params := map[string]any{"launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "speed": t.Speed, "tier": t.Tier}
	own, err := s.resolveStart(ctx, params, false)
	if err != nil {
		return startAgent{}, nil, false, err
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return startAgent{}, params, false, nil // the ordinary machine check reports this
	}
	fit, err := s.fitTaskLauncher(ctx, t, m, own, params)
	if err != nil {
		return startAgent{}, nil, false, err
	}
	return fit, params, true, nil
}

// fitPlanTaskSpeed resolves a plan task's current tier and launcher before its
// card or Start edit is accepted, and validates the resulting speed.
func (s *Service) fitPlanTaskSpeed(ctx context.Context, t *PlanTask) (startAgent, error) {
	fit, _, err := s.fitPlanTask(ctx, t)
	return fit, err
}

// fitPlanTask resolves a plan task's tier, launcher and configured permission mode (from tier or agent.new)
// and validates its resulting speed.
func (s *Service) fitPlanTask(ctx context.Context, t *PlanTask) (startAgent, string, error) {
	fit, params, ok, err := s.resolveFitPlanTask(ctx, t)
	if err != nil {
		return startAgent{}, "", err
	}
	if !ok {
		configuredMode, _ := params["permission_mode"].(string)
		return startAgent{}, configuredMode, nil // the ordinary machine check reports this
	}
	configuredMode, _ := params["permission_mode"].(string)
	return fit, configuredMode, checkStartSpeed(params, fit, "")
}
