package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// feishuDirections takes one snapshot for a card or a whole report. Use the same
// display names as the direction tree, including a name the owner just changed.
func (s *Service) feishuDirections(ctx context.Context) map[string]string {
	cfg := s.settings(ctx)
	if len(cfg.Leaders) == 0 {
		return nil
	}
	titles, _ := s.conversationTitles()
	return s.directionSessionNames(ctx, cfg, titles)
}

// feishuDirection names the conversation responsible for the work. The main
// conversation and its background supervisor share the Persona direction.
func (s *Service) feishuDirection(ctx context.Context, t Todo, names map[string]string) string {
	supervisor := t.Origin
	if t.Supervision != nil {
		supervisor = t.Supervision.Supervisor
		if _, _, worker := parseLeaderRef(supervisor); worker {
			supervisor, _ = s.supervisorAboveTask(ctx, t)
		}
	}
	if name, isDirection := names[supervisor]; isDirection {
		if strings.TrimSpace(name) == "" {
			name = supervisor
		}
		return "[" + clip(oneLine(name), directionNameMost) + "] "
	}
	return "[Persona] "
}

// feishuAgentPlace identifies the session even when the node supplied no title.
func (s *Service) feishuAgentPlace(ctx context.Context, ix proto.Interaction) string {
	agent := transcript.Title(ix.SessionTitle)
	if agent == "" && ix.Kind == proto.IxScreen {
		if sess, ok := s.nodeSession(ix.MachineID, ix.SessionID); ok {
			agent = agentTitle(&sess, 60)
		}
		if agent == "" || agent == "." {
			agent = short(ix.SessionID)
		}
	}
	if agent == "" {
		agent = "Persona"
	}
	agent = clip(oneLine(agent), 70)
	if ix.Kind == proto.IxScreen {
		if machine, ok := s.fleetMachine(ix.MachineID); ok && machine.Name != "" {
			return agent + " · " + clip(oneLine(machine.Name), 40)
		}
	}
	if d, ok := s.device(ctx, ix.MachineID); ok && d.Name != "" && agent != "Persona" {
		return agent + " · " + clip(oneLine(d.Name), 40)
	}
	if ix.Kind == proto.IxScreen && ix.MachineID != "" {
		return agent + " · " + clip(oneLine(ix.MachineID), 40)
	}
	return agent
}
