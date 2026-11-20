package assistant

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestScheduledTodoMultiAgentSnapshotPostgres(t *testing.T) {
	database := todoTestDB(t)
	ctx := t.Context()
	seedTodos(t, database, db.TodoRow{ID: "multi-agent-progress", Title: "Backfill", Origin: "conv1"})
	if err := database.PutTodoSupervision(ctx, db.TodoSupervisionRow{
		TodoID: "multi-agent-progress", Supervisor: "conv1", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	s, f := newQuietServiceOn(t, database)
	s.superviseTimes.gather = time.Hour
	s.todoAskAfter = time.Nanosecond
	logs := &logBuffer{}
	s.logf = logs.logf
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	todo, _ := s.todoNow("multi-agent-progress")
	// These pairs would collide if joined with an unescaped slash.
	todo.Agents = []TodoAgent{
		{MachineID: "worker/a", SessionID: "b"},
		{MachineID: "worker", SessionID: "a/b"},
	}
	putTodo(s, todo)
	for _, agent := range todo.Agents {
		f.setSessions(agent.MachineID, proto.Session{MachineID: agent.MachineID, SessionID: agent.SessionID})
	}
	answer := "An existing batch result"
	f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
		text := "No batch was started.\x00 Literal text: \\u0000"
		if machine == "worker/a" {
			text = "First worker's existing result"
		} else if machine == "worker" {
			text = answer
		}
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: text}}}, nil
	})
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	for i, want := range []struct {
		state string
		count int
	}{{"baseline", 0}, {"no_progress", 1}, {"progress", 0}, {"no_progress", 1}, {"no_progress", 2}} {
		var change func()
		if i == 2 {
			change = func() {
				f.mu.Lock()
				answer = "Second worker produced a new batch"
				f.mu.Unlock()
			}
		}
		run, result := assessTodoRun(t, s, task.ID, change)
		if result.State != want.state || result.NoProgress != want.count {
			t.Fatalf("round %d: assessment=%+v; PostgreSQL logs:\n%s", i+1, result, logs.String())
		}
		// Only a progress assessment quotes the run's own reply; the others say what the hub saw.
		if strings.ContainsRune(result.Reason, 0) || want.state == "progress" && !strings.Contains(result.Reason, `\u0000`) {
			t.Fatalf("NUL was not removed, or literal backslash-u0000 text was lost: %q", result.Reason)
		}
		var storedTasks []Task
		if _, err := database.Setting(ctx, tasksKey, &storedTasks); err != nil {
			t.Fatal(err)
		}
		index := slices.IndexFunc(storedTasks, func(v Task) bool { return v.ID == task.ID })
		if index < 0 || storedTasks[index].TodoProgress == nil {
			t.Fatalf("round %d: no durable snapshot; PostgreSQL logs:\n%s", i+1, logs.String())
		}
		progress := storedTasks[index].TodoProgress
		if progress.Assessment.State != want.state || progress.NoProgress != want.count || len(progress.Snapshot.Answers) != 2 {
			t.Fatalf("round %d: incomplete persisted multi-agent assessment: %+v", i+1, progress)
		}
		for key := range progress.Snapshot.Answers {
			if strings.ContainsRune(key, 0) {
				t.Fatal("the persisted snapshot contains a NUL-delimited Agent key")
			}
		}
		var storedRuns []ScheduleRun
		if _, err := database.Setting(ctx, runsKey, &storedRuns); err != nil {
			t.Fatal(err)
		}
		index = slices.IndexFunc(storedRuns, func(v ScheduleRun) bool { return v.ID == run.ID })
		if index < 0 || storedRuns[index].TodoProgress == nil || storedRuns[index].TodoProgress.State != want.state {
			t.Fatalf("round %d: run history did not commit its assessment", i+1)
		}
	}
	cur, _ := s.todoNow(todo.ID)
	if cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != 1 ||
		len(cur.Supervision.Reviews[0].Options) != 2 {
		t.Fatalf("two durable no-progress rounds did not create one actionable decision: %+v", cur.Supervision)
	}
	if strings.Contains(logs.String(), "22P05") || strings.Contains(logs.String(), "save to-do progress:") {
		t.Fatalf("progress persistence failed:\n%s", logs.String())
	}
	t.Log("PostgreSQL committed 5 assessments with 2 distinct Agent snapshots; one needs_user decision has 2 options")
}

func TestScheduledTodoSnapshotNULGuard(t *testing.T) {
	for _, tc := range []struct {
		name, criterion, state string
		reject                 bool
	}{
		{"criterion key", "Batch\x00verified", "pending", true},
		{"literal escape", `Batch \u0000 verified`, "pending", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, todo := scheduledTodoFixture(t)
			store := &memStore{}
			s.store = func() Store { return store }
			todo.Supervision.DoneWhen = []string{tc.criterion}
			todo.Supervision.Criteria = []db.TodoCriterion{{Criterion: tc.criterion, State: tc.state}}
			putTodo(s, todo)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			if err := store.PutSetting(t.Context(), tasksKey, []Task{task}); err != nil {
				t.Fatal(err)
			}
			_, result := assessTodoRun(t, s, task.ID, nil)
			want := "baseline"
			if tc.reject {
				want = "error"
			}
			if result.State != want {
				t.Fatalf("invalid snapshot was not rejected before persistence: %+v", result)
			}
			var stored []Task
			if _, err := store.Setting(t.Context(), tasksKey, &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 || (stored[0].TodoProgress == nil) != tc.reject {
				t.Fatalf("snapshot validation published an invalid candidate: %+v", stored)
			}
			if tc.reject {
				s.supervise.mu.Lock()
				blocked := false
				for _, signal := range s.supervise.pending {
					blocked = blocked || signal.Todo == todo.ID && signal.Kind == sigCheck && signal.Report == checkErrorReport &&
						strings.Contains(signal.Detail, "progress snapshot contains NUL")
				}
				s.supervise.mu.Unlock()
				if !blocked {
					t.Fatal("rejecting invalid snapshot data swallowed the supervisor signal")
				}
			}
		})
	}
}
