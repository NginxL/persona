package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// supervisionStartup marks a manager-authorized start before its conversation id exists.
// It is never read from RPC parameters or inferred from a model purpose supplied by a caller.
type supervisionStartup struct{}

// runtimeSessionLacksSupervisionPolicy reports a session hosted by the runtime protocol whose runtime has not
// advertised supervision_policy support. Absent or unknown capability stays conservatively unsupported (#10338, #11173).
func runtimeSessionLacksSupervisionPolicy(c proto.Session) bool {
	return c.SourceKind == proto.SourceRuntime && (c.Capabilities == nil || !c.Capabilities.SupervisionPolicy)
}

// runtimeWithoutSupervisionPolicy reports a known session hosted by a runtime that lacks a separate policy channel.
// Launcher names and agent kinds alone do not identify that execution path.
func (s *Service) runtimeWithoutSupervisionPolicy(ctx context.Context, machine, id string) bool {
	if id == "" {
		return false
	}
	c, ok := s.session(ctx, machine, id)
	return ok && runtimeSessionLacksSupervisionPolicy(c)
}

func (s *Service) supervisionInstructions(ctx context.Context, machine, id string) (string, error) {
	cfg := s.settings(ctx)
	if id == "" || s.isWorkMachine(machine) {
		return "", nil
	}
	main := id == cfg.Main
	if main {
		if machine != cfg.mainRouteMachine() || cfg.MainSupervisesOwnTasks == nil || !*cfg.MainSupervisesOwnTasks {
			return "", nil
		}
	} else if machine != cfg.MachineID {
		return "", nil
	}
	c, known := s.session(ctx, machine, id)
	if known && runtimeSessionLacksSupervisionPolicy(c) {
		return "", nil
	}
	if !main && !(id == cfg.BackgroundSupervisor && cfg.BackgroundSupervisorMachineID == machine) && !slices.Contains(cfg.Leaders, id) {
		// Side chats genuinely supervise their own open tasks (#4669). Sharing the
		// directory, a scheduled purpose, or the ability to plan is not a role.
		if known && (!c.Assistant || c.Space != "") {
			return "", nil
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			return "", err // unknown ownership must not silently demote a side supervisor
		}
		owns := false
		s.todos.mu.RLock()
		for _, todo := range s.todos.todos {
			if todo.DoneAt == nil && todo.Supervision != nil && todo.Supervision.Supervisor == id {
				owns = true
				break
			}
		}
		s.todos.mu.RUnlock()
		if !owns {
			return "", nil
		}
	}
	// Only a candidate supervisor needs this fail-closed check. An unreadable guest
	// registry must not stop ordinary chats that would receive no role policy anyway.
	if _, guest, err := s.guestOf(ctx, machine, id); err != nil {
		return "", err
	} else if guest {
		return "", nil
	}
	// A leader's own direction notes follow its policy (direction_notes.go): changing them restarts only this one, on
	// a send that finds it idle.
	return s.leaderPolicy(ctx, id, prompts.RenderSupervisionInstructions(), leadIdle(c, known)), nil
}

func (s *Service) putSupervisionInstructions(ctx context.Context, machine, id string, p map[string]any) error {
	// Always overwrite caller input, including empty roles, so demotion clears the node's
	// process fingerprint instead of retaining an earlier conversation's policy.
	text, err := s.supervisionInstructions(ctx, machine, id)
	if err != nil {
		return err
	}
	if id == "" && ctx.Value(supervisionStartup{}) == true && p["guest"] == nil {
		text = prompts.RenderSupervisionInstructions()
	}
	p["supervision_instructions"] = text
	return nil
}
