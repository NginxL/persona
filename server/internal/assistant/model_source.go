package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

type modelSourceKey struct{}

func planModelSource(task PlanTask) modelhttp.Source {
	return modelhttp.Source{Role: "task_agent", Todo: task.TodoID, Routine: task.Schedule}.Safe()
}

func (s *Service) startModelSource(ctx context.Context, p StartParams, main bool) modelhttp.Source {
	if source, ok := ctx.Value(modelSourceKey{}).(modelhttp.Source); ok {
		return source.Safe()
	}
	source := modelhttp.Source{Role: "main"}
	switch {
	case main:
	case p.Origin == OriginTask && p.OriginRef != "":
		source.Role, source.Routine = "routine", p.OriginRef
		if task, ok := s.scheduledTask(ctx, p.OriginRef); ok {
			source.Todo = task.TodoID
		}
	case p.Origin == OriginInbox || p.Purpose == purposeSupervise || p.Origin == OriginTask:
		source.Role = "background"
	}
	return source.Safe()
}

// conversationModelSource uses manager records, never the message body or launcher name.
func (s *Service) conversationModelSource(ctx context.Context, machine, id string) modelhttp.Source {
	cfg := s.settings(ctx)
	source := modelhttp.Source{Role: "main", Session: id}
	if id == "" {
		return source
	}
	switch {
	case id == cfg.Main && machine == cfg.mainRouteMachine():
		return source
	case machine == cfg.MachineID && slices.Contains(cfg.Leaders, id):
		source.Role = "leader"
		return source
	case s.isBackgroundCallerOn(ctx, machine, id):
		source.Role = "background"
		return source
	}
	s.origins.mu.Lock()
	var origin, ref string
	originsKnown := s.originsLocked(ctx)
	if originsKnown {
		if o := s.origins.m[id]; o != nil && (o.MachineID == "" || o.MachineID == machine) {
			origin, ref = o.Origin, o.Ref
		}
	}
	s.origins.mu.Unlock()
	if !originsKnown {
		source.Role = "background"
		return source.Safe()
	}
	switch {
	case origin == OriginTask && ref != "":
		source.Role, source.Routine = "routine", ref
		if task, ok := s.scheduledTask(ctx, ref); ok {
			source.Todo = task.TodoID
		}
	case origin == OriginInbox || origin == OriginTask:
		source.Role = "background"
	default:
		if todo := s.modelSourceTodo(ctx, machine, id); todo != "" {
			source.Role, source.Todo = "task_agent", todo
			if task, ok := s.todoNow(todo); ok && task.PlanID != "" {
				if plan, index, err := s.currentPlanAssignment(ctx, task); err == nil {
					source.Routine = plan.Tasks[index].Schedule
				}
			}
		} else if c, ok := s.session(ctx, machine, id); ok && !c.Assistant {
			source.Role = "task_agent"
		}
	}
	return source.Safe()
}

func (s *Service) modelSourceTodo(ctx context.Context, machine, id string) string {
	if s.ensureTodosLoaded(ctx) != nil {
		return ""
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if todo := s.todos.activeBySession[[2]string{machine, id}]; todo != "" {
		return todo
	}
	// A completed task still owns its Agent's subsequent requests until it is reassigned.
	var latest time.Time
	var found string
	for _, todo := range s.todos.todos {
		for _, agent := range todo.Agents {
			if agent.MachineID == machine && agent.SessionID == id && (found == "" || agent.LinkedAt.After(latest) || agent.LinkedAt.Equal(latest) && todo.ID > found) {
				found, latest = todo.ID, agent.LinkedAt
			}
		}
	}
	return found
}
