package assistant

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func scheduledTodoFixture(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietService(t)
	setTasks(s)
	s.turnPoll = time.Millisecond
	s.superviseTimes.gather = time.Hour
	s.todoAskAfter = time.Nanosecond // the escalation's mechanics, not its floor (TestScheduledTodoAskWaitsForTheFloor)
	todo := Todo{ID: "todo-backfill", Title: "Backfill", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", DoneWhen: []string{"Backfill verified"},
			Criteria: []db.TodoCriterion{{Criterion: "Backfill verified", State: "pending"}}}}
	putTodo(s, todo)
	return s, f, todo
}

func scheduleJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScheduleTodoAssociationRegression(t *testing.T) {
	for _, tool := range []string{"schedule_followup", "schedule_routine"} {
		t.Run(tool, func(t *testing.T) {
			s, _, todo := scheduledTodoFixture(t)
			args := map[string]any{"title": "Backfill check", "prompt": "Advance the backfill",
				"todo_id": todo.ID, "push": "asked"}
			if tool == "schedule_followup" {
				args["every"] = "30m"
			} else {
				args["time"] = "22:00"
			}
			if _, err := runJSON(t, s, "conv1", tool, args); err != nil {
				t.Fatal(err)
			}
			tasks, _ := scheduleState(s)
			if len(tasks) != 1 || scheduleJSON(t, tasks[0])["todo_id"] != todo.ID {
				t.Fatalf("the recurring schedule lost its explicit to-do association: %+v", tasks)
			}
		})
	}
}

func TestScheduleTodoAuthorityRegression(t *testing.T) {
	for _, tc := range []struct {
		name, self, todo string
		once             bool
	}{
		{"missing", "conv1", "missing", false},
		{"another supervisor", "unrelated", "todo-backfill", false},
		{"one shot", "conv1", "todo-backfill", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := scheduledTodoFixture(t)
			args := map[string]any{"prompt": "Advance the backfill", "todo_id": tc.todo}
			if tc.once {
				args["at"] = "30m"
			} else {
				args["every"] = "30m"
			}
			if _, err := runJSON(t, s, tc.self, "schedule_followup", args); err == nil {
				t.Fatal("an invalid or unauthorized to-do association was accepted")
			}
			if tasks, _ := scheduleState(s); len(tasks) != 0 {
				t.Fatal("a refused association left a schedule behind")
			}
		})
	}
}

func TestScheduledTodoQuietNoProgressRegression(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	if _, err := runJSON(t, s, "conv1", "schedule_followup", map[string]any{
		"title": "Backfill check", "prompt": "Advance the backfill", "todo_id": todo.ID,
		"every": "30m", "new_conversation": true, "push": "asked",
	}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := scheduleState(s)
	id := tasks[0].ID
	reason := "All six execution slots are busy; no backfill work was started."
	f.mu.Lock()
	reads := 0
	f.page = func(int) []proto.Item {
		reads++
		return []proto.Item{{ID: strconv.Itoa(reads), Kind: proto.KindText, Role: "assistant", Text: reason}}
	}
	f.onAssistantSend = func(f *fakeHub, args map[string]any) {
		sid, _ := args["session_id"].(string)
		if sid == "" {
			sid, _ = args["new_session_id"].(string)
		}
		if sid == "" {
			sid = "s" + strconv.Itoa(f.made+1)
		}
		f.sessions["server"] = append(f.sessions["server"], proto.Session{MachineID: "server",
			SessionID: sid, Status: proto.StatusIdle, State: proto.StateIdle})
	}
	f.mu.Unlock()
	// The first run sets the baseline; the two after it see no progress.
	for n, want := range []struct {
		state string
		count float64
	}{{"baseline", 0}, {"no_progress", 1}, {"no_progress", 2}} {
		if _, err := runNow(t, s, id); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "a completed run's persisted progress assessment", func() bool {
			_, runs := scheduleState(s)
			if len(runs) != n+1 {
				return false
			}
			p, _ := scheduleJSON(t, runs[n])["todo_progress"].(map[string]any)
			return p != nil && p["state"] == want.state
		})
		_, runs := scheduleState(s)
		p := scheduleJSON(t, runs[n])["todo_progress"].(map[string]any)
		got, _ := p["no_progress"].(float64)
		if got != want.count {
			t.Fatalf("no-progress count lost: %+v", p)
		}
		// The reason is what the hub saw, never the run's own summary (#11587).
		if reason := p["reason"].(string); want.state == "no_progress" &&
			(strings.Contains(reason, "six execution slots") || !strings.Contains(reason, "没有新回复")) {
			t.Fatalf("the no-progress reason is not the hub's evidence: %+v", p)
		}
		if n == 1 {
			s.supervise.mu.Lock()
			signals := len(s.supervise.pending)
			s.supervise.mu.Unlock()
			if signals == 0 {
				t.Fatal("the first quiet no-progress run did not signal its supervisor")
			}
		}
	}
	waitFor(t, "an actionable owner decision", func() bool {
		cur, _ := s.todoNow(todo.ID)
		return cur.Supervision.State == reviewNeedsUser
	})
	cur, _ := s.todoNow(todo.ID)
	review := cur.Supervision.Reviews[len(cur.Supervision.Reviews)-1]
	if len(review.Options) != 2 || review.Reason == "" || review.Recommend != review.Options[0].Label ||
		strings.Contains(review.Reason, "six execution slots") || !strings.Contains(review.Note, "没有新回复") {
		t.Fatalf("the owner cannot choose a different approach or keep waiting, or is told the run's summary: %+v", review)
	}
	if cur.DoneAt != nil {
		t.Fatal("escalation closed the to-do")
	}
}
