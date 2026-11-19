package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
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
		return "", fmt.Errorf("speed must be a string: standard, fast or ultrafast")
	}
	speed = strings.TrimSpace(speed)
	if !proto.ValidSpeed(speed) {
		return "", fmt.Errorf("invalid Agent speed %q: use standard, fast or ultrafast", speed)
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
		return fmt.Errorf("speed %q applies only to a new Agent start, not a resume; leave speed out to use the launcher's own setting", speed)
	}
	source := compat.Source(agent.source)
	terminal := source == "" || source == "zshrc" || source == "path" || source == "persona" || source == "builtin"
	if !terminal || agent.agent != "" && !proto.SpeedSupported(agent.agent, speed) {
		launcher, _ := params["launcher"].(string)
		kind := agent.agent
		if kind == "" {
			kind = source
		}
		return fmt.Errorf("launcher %q (%s) cannot apply speed %q to a new Agent start; leave speed out or choose standard", launcher, kind, speed)
	}
	return nil
}

// fitPlanTaskSpeed resolves a plan task's current tier and launcher before its
// card or Start edit is accepted, and validates the resulting speed.
func (s *Service) fitPlanTaskSpeed(ctx context.Context, t *PlanTask) (startAgent, error) {
	params := map[string]any{"launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "speed": t.Speed, "tier": t.Tier}
	own, err := s.resolveStart(ctx, params)
	if err != nil {
		return startAgent{}, err
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return startAgent{}, nil // the ordinary machine check reports this
	}
	fit, err := s.fitTaskLauncher(ctx, t, m, own, params)
	if err != nil {
		return startAgent{}, err
	}
	return fit, checkStartSpeed(params, fit, "")
}
