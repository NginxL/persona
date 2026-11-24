package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestConversationModelSourceRoles(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "main", Leaders: []string{"lead"}, BackgroundSupervisor: "back", BackgroundSupervisorMachineID: "server"}
	f.setSessions("server", []proto.Session{{SessionID: "main", Assistant: true}, {SessionID: "lead", Assistant: true}, {SessionID: "back", Assistant: true}, {SessionID: "routine", Assistant: true}, {SessionID: "inbox", Assistant: true}, {SessionID: "worker"}}...)
	s := newTestService(t, f)
	ctx := context.Background()
	s.originSet(ctx, "server", "routine", OriginTask, "routine_123", "")
	s.originSet(ctx, "server", "inbox", OriginInbox, "example:item", "")
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos["todo_123"] = &Todo{ID: "todo_123", Agents: []TodoAgent{{MachineID: "server", SessionID: "worker", LinkedAt: time.Now()}}}
	s.todos.mu.Unlock()
	for _, tc := range []struct {
		id   string
		want modelhttp.Source
	}{
		{"main", modelhttp.Source{Role: "main"}},
		{"lead", modelhttp.Source{Role: "leader"}},
		{"back", modelhttp.Source{Role: "background"}},
		{"routine", modelhttp.Source{Role: "routine", Routine: "routine_123"}},
		{"worker", modelhttp.Source{Role: "task_agent", Todo: "todo_123"}},
		{"inbox", modelhttp.Source{Role: "background"}},
	} {
		tc.want.Session = tc.id
		if got := s.conversationModelSource(ctx, "server", tc.id); got != tc.want {
			t.Fatalf("%s source=%+v, want %+v", tc.id, got, tc.want)
		}
	}
	// The same session ID on another computer must not inherit a supervisor's role or task.
	if got := s.conversationModelSource(ctx, "other", "worker"); got.Todo != "" {
		t.Fatal("task crossed machine identity")
	}
}

func TestScheduledModelSourceIncludesBothIDs(t *testing.T) {
	if got := planModelSource(PlanTask{TodoID: "todo_123", Schedule: "routine_456"}); got != (modelhttp.Source{Role: "task_agent", Todo: "todo_123", Routine: "routine_456"}) {
		t.Fatalf("scheduled work Agent source=%+v", got)
	}
	s := newTestService(t, newFakeHub().withMachine())
	task := Task{ID: "routine_123", TodoID: "todo_456"}
	start := taskStartParams(task, "example prompt", TaskExecution{})
	ctx := context.WithValue(context.Background(), modelSourceKey{}, start.modelSource)
	want := modelhttp.Source{Role: "routine", Todo: task.TodoID, Routine: task.ID}
	if got := s.startModelSource(ctx, StartParams{}, false); got != want {
		t.Fatalf("start source=%+v", got)
	}
	if got := taskResumeParams(task, "example prompt", TaskExecution{})["model_source"]; got != want {
		t.Fatalf("resume source=%+v", got)
	}
	if got := s.startModelSource(context.Background(), StartParams{Origin: OriginInbox}, false); got.Role != "background" {
		t.Fatal("Inbox start not background")
	}
	if got := s.startModelSource(context.Background(), StartParams{Purpose: purposeSupervise}, false); got.Role != "background" {
		t.Fatal("supervisor start not background")
	}
}

func TestSendParamsRejectsCallerModelSource(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server"}
	s := newTestService(t, f)
	s.originSet(t.Context(), "server", "inbox", OriginInbox, "example:item", "")
	p := map[string]any{"model_source": modelhttp.Source{Role: "main", Todo: "todo_forged"}}
	params, _, err := s.sendParamsOwn(t.Context(), SendParams{MachineID: "server", SessionID: "inbox", Params: p})
	if err != nil {
		t.Fatal(err)
	}
	if got := params["model_source"]; got != (modelhttp.Source{Role: "background", Session: "inbox"}) {
		t.Fatalf("caller source trusted: %+v", got)
	}
}
