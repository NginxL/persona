package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The opt-in is narrow: a conversation can close its own agentless memo, while every existing confirmation boundary
// stays in place. A timeout turns an unexpected card in the direct cases into a failure instead of a hanging test.
func TestTodoCompleteAgentlessSupervisor(t *testing.T) {
	direct := func(t *testing.T, e *todoEnv, self, tool, id string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		raw, _ := json.Marshal(map[string]any{"id": id})
		return e.s.toolTodoDone(ctx, "server", self, tool, raw)
	}

	t.Run("default off", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		putTodo(e.s, Todo{ID: "todo_default", Title: "Memo", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}})
		shown := make(chan proto.Interaction, 1)
		go answerArchiveCard(t, e.s, false, shown)
		res, err := e.tool("todo_complete", map[string]any{"id": "todo_default"})
		if err != nil || !strings.Contains(res, "declined") {
			t.Fatalf("default completion: %s %v", res, err)
		}
		if card := <-shown; card.ToolName != "todo_complete" {
			t.Fatalf("card: %+v", card)
		}
		if got := todoNow(e.s, "todo_default"); got.DoneAt != nil {
			t.Fatal("completed after the card was declined")
		}
	})

	for _, tc := range []struct {
		name string
		todo Todo
	}{
		{"supervised memo", Todo{ID: "todo_supervised", Title: "Memo", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}},
		{"originating conversation without a supervision ledger", Todo{ID: "todo_origin", Title: "Memo", Origin: "conv1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTodoEnv(t, nil)
			e.f.hello.Assistant.CompleteAgentlessTodos = true
			putTodo(e.s, tc.todo)
			res, err := direct(t, e, "conv1", "todo_complete", tc.todo.ID)
			if err != nil || !strings.HasPrefix(res, "Completed: ") {
				t.Fatalf("direct completion: %s %v", res, err)
			}
			if got := todoNow(e.s, tc.todo.ID); got.DoneAt == nil {
				t.Fatal("not done")
			}
			if len(e.s.pendingQuestions()) != 0 {
				t.Fatal("the opted-in agentless completion showed a card")
			}
		})
	}

	done := time.Now().UTC()
	for _, tc := range []struct {
		name, self, tool string
		todo             Todo
		health           bool
	}{
		{"another supervisor", "conv1", "todo_complete", Todo{ID: "todo_other_supervisor", Title: "Memo", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv2"}}, false},
		{"another originating conversation", "conv1", "todo_complete", Todo{ID: "todo_other_origin", Title: "Memo", Origin: "conv2"}, false},
		{"attached Agent", "conv1", "todo_complete", Todo{ID: "todo_agent", Title: "Task", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}, Agents: []TodoAgent{{MachineID: "server", SessionID: "agent1"}}}, false},
		{"planned task without an attached Agent", "conv1", "todo_complete", Todo{ID: "todo_planned", Title: "Task", Origin: "conv1", PlanID: "plan_1", Supervision: &Supervision{Supervisor: "conv1"}}, false},
		{"reopen", "conv1", "todo_reopen", Todo{ID: "todo_reopen", Title: "Memo", Origin: "conv1", DoneAt: &done, Supervision: &Supervision{Supervisor: "conv1"}}, false},
		{"health-space outbound confirmation", "conv1", "todo_complete", Todo{ID: "todo_health", Title: "Memo", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTodoEnv(t, nil)
			e.f.hello.Assistant.CompleteAgentlessTodos = true
			if tc.health {
				e.f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Space: SpaceHealth})
			}
			putTodo(e.s, tc.todo)
			shown := make(chan proto.Interaction, 1)
			go answerArchiveCard(t, e.s, false, shown)
			res, err := runJSON(t, e.s, tc.self, tc.tool, map[string]any{"id": tc.todo.ID})
			if err != nil || !strings.Contains(res, "nothing changed") {
				t.Fatalf("declined change: %s %v", res, err)
			}
			if card := <-shown; card.ToolName != tc.tool {
				t.Fatalf("card: %+v", card)
			}
			got := todoNow(e.s, tc.todo.ID)
			if tc.todo.DoneAt == nil && got.DoneAt != nil || tc.todo.DoneAt != nil && got.DoneAt == nil {
				t.Fatalf("done state changed after refusal: before=%v after=%v", tc.todo.DoneAt, got.DoneAt)
			}
		})
	}
}
