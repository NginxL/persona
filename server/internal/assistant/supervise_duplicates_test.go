package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDuplicateHintsTodoPRURL(t *testing.T) {
	task := PlanTask{Title: "Coordinate release", Prompt: "Follow up on #1234"}
	for _, tc := range []struct {
		name  string
		prURL string
		done  bool
		want  bool
	}{
		{name: "open with idle agent", prURL: "https://github.com/example/project/pull/1234", want: true},
		{name: "completed", prURL: "https://github.com/example/project/pull/1234", done: true},
		{name: "different PR", prURL: "https://github.com/example/project/pull/5678"},
		{name: "no PR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			todo := Todo{ID: "existing", Title: "Repair upload parser", Notes: "Improve parsing.", PRURL: tc.prURL,
				Agents: []TodoAgent{{MachineID: "box", SessionID: "idle-agent"}}}
			if tc.done {
				todo.DoneAt = new(time.Now())
			}
			hints := duplicateHints(task, []Todo{todo}, []proto.Session{{
				MachineID: "box", SessionID: "idle-agent", State: proto.StateIdle,
			}})
			if !tc.want {
				if len(hints) != 0 {
					t.Fatalf("unexpected duplicate hints: %v", hints)
				}
				return
			}
			if len(hints) != 1 || !strings.Contains(hints[0], todo.ID) || !strings.Contains(hints[0], "#1234") {
				t.Fatalf("want one hint for %s and #1234, got %v", todo.ID, hints)
			}
		})
	}
}

func TestTaskPlanSaysDuplicatesForTodoPRURL(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Repair upload parser", "Improve parsing.", "", "")
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].PRURL = "https://github.com/example/project/pull/1234"
	s.todos.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Coordinate release", "prompt": "Follow up on #1234", "machine": "box", "cwd": "/code/app",
		"readonly": true, "why": "The repository is on this computer.", "permission": "plan",
	}}})
	res, err := s.toolTaskPlan(ctx, "conv1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Possible duplicates") || !strings.Contains(res, todo.ID) || !strings.Contains(res, "#1234") {
		t.Fatalf("missing PR-linked to-do duplicate warning: %s", res)
	}
	waitFor(t, "the new task", func() bool { return todoCount(s) == 2 })
}
