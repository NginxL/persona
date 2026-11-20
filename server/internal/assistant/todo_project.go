package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
)

// A todo's project (#2581, docs/assistant/todos.md "Project"): the knowledge-base project it belongs to, usually a
// long-term one standing for an area of the user's work. The apps group open todos by it, and a finished task's outcome
// goes to its timeline (outcomeLog). It is kept as the project's slug. A project merged away later still takes the
// outcomes (ownProject follows the merge); the lists and the apps keep showing the old slug until it is set again.

// todoProject is the slug of the live knowledge-base project name names (its slug, name or an alias); "" for "". An
// error when the knowledge base can't be read or has no such project.
func (s *Service) todoProject(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return "", fmt.Errorf("project %q can't be checked: %w", name, err)
	}
	p := e.FindProject(name)
	if p == nil || !p.Live() || p.Status == "ignored" {
		return "", fmt.Errorf("no knowledge-base project %q: give the slug or name of an existing one (kb_list projects)", name)
	}
	return p.Slug, nil
}

// setTodoProject puts project (nil: unchanged; "" clears it) into e as the slug it names.
func (s *Service) setTodoProject(ctx context.Context, project *string, e *db.TodoEdit) error {
	if project == nil {
		return nil
	}
	slug, err := s.todoProject(ctx, *project)
	if err != nil {
		return err
	}
	e.Project = &slug
	return nil
}

// ownProject is the live project todo t was put in, nil when it has none or it is gone.
func ownProject(e *kb.Engine, t Todo) *kb.Project {
	if t.Project == "" {
		return nil
	}
	p := e.FindProject(t.Project)
	if p == nil || !p.Live() || p.Status == "ignored" {
		return nil
	}
	return p
}

// directionForTodo resolves the one current direction above a todo. A worker supervised by a task-leader Agent first
// goes through leaderAbove; a missing leader, a nested leader ref, the main conversation, or an ended/unknown leader is
// not a direction. No title, cwd, or map-order inference participates in this decision.
func (s *Service) directionForTodo(ctx context.Context, t Todo) (string, *direction, bool) {
	if t.Supervision == nil || t.Supervision.Supervisor == "" {
		return "", nil, false
	}
	lead := t.Supervision.Supervisor
	if s.agentSupervised(t) {
		above, _, ok := s.leaderAbove(t)
		if !ok || above.ID == "" {
			return "", nil, false
		}
		lead, ok = s.uniqueLeaderSupervisorAbove(t)
		if !ok || above.Supervision == nil || above.Supervision.Supervisor != lead {
			return "", nil, false
		}
	}
	cfg := s.settings(ctx)
	if lead == "" || lead == cfg.Main {
		return "", nil, false
	}
	found := false
	for _, id := range cfg.Leaders {
		if id == lead {
			found = true
			break
		}
	}
	if !found {
		return "", nil, false
	}
	d := s.directionOf(lead)
	if d == nil || d.EndedAt != nil {
		return "", nil, false
	}
	return lead, d, true
}

// uniqueLeaderSupervisorAbove validates leaderAbove instead of accepting its first map-order match. Historical and
// current leader todos may retain the same session; different conversation supervisors are ambiguous and confer no
// direction. Repeated rows for the same conversation still describe one direction.
func (s *Service) uniqueLeaderSupervisorAbove(t Todo) (string, bool) {
	if t.Supervision == nil {
		return "", false
	}
	machine, session, ok := parseLeaderRef(t.Supervision.Supervisor)
	if !ok {
		return "", false
	}
	machine = s.canonical(machine)
	candidate := ""
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, lead := range s.todos.todos {
		for _, a := range lead.Agents {
			if a.MachineID != machine || a.SessionID != session || a.Source != sourceLeader {
				continue
			}
			if lead.Supervision == nil || lead.Supervision.Supervisor == "" {
				continue
			}
			supervisor := lead.Supervision.Supervisor
			if _, _, ref := parseLeaderRef(supervisor); ref {
				return "", false
			}
			if candidate != "" && candidate != supervisor {
				return "", false
			}
			candidate = supervisor
		}
	}
	return candidate, candidate != ""
}

// projectsForTask is the single ownership rule for dispatch context and worker memory: a live todo.Project wins, then
// the live project of its unique direction, and only then the working directory. With none, there is no ownership.
func (s *Service) projectsForTask(ctx context.Context, e *kb.Engine, t Todo, machine, cwd string) []*kb.Project {
	if p := ownProject(e, t); p != nil {
		return []*kb.Project{p}
	}
	if _, d, ok := s.directionForTodo(ctx, t); ok && d.Project != "" {
		if p := ownProject(e, Todo{Project: d.Project}); p != nil {
			return []*kb.Project{p}
		}
	}
	if strings.TrimSpace(cwd) == "" {
		return nil
	}
	return e.ProjectsForWork(machine, cwd)
}
