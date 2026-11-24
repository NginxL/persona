package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A plan task must not start without its supervision ledger, on either the ordinary main-supervised path or a task
// leader's worker path (#5732).
func TestPlanSupervisionFailureDoesNotStart(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
	}{
		{"main supervised", ""},
		{"task leader worker", "todo_leader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := failingSupervisionStore{DB: todoTestDB(t)}
			s, f := newQuietServiceOn(t, st)
			ctx := context.Background()
			p := &TaskPlan{ID: "plan_" + randomHex(8), State: planOpen, Conversation: "conv1", Tasks: []PlanTask{{
				Title: "Check the build", Prompt: "Check the build", Machine: "box", Cwd: "/code/app", Readonly: true,
				Goal: "A checked build", ExpectMinutes: 5, Parent: tc.parent,
			}}}
			if err := s.savePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			started, err := s.startPlan(ctx, p.ID, nil, false)
			if err == nil && started.Tasks[0].Error == "" {
				waitFor(t, "the unguarded session.start", func() bool { return len(f.started()) != 0 })
			}
			if err == nil && started.Tasks[0].Error == "" {
				t.Fatalf("startPlan returned no supervision failure: %+v", started.Tasks[0])
			}
			if got := len(f.started()); got != 0 {
				t.Fatalf("session.start ran %d times without a supervision ledger", got)
			}
			todos := s.todosOf(ctx, "*")
			if len(todos) != 1 || todos[0].DoneAt == nil || todos[0].StartError == "" || todos[0].Supervision != nil || len(todos[0].Agents) != 0 {
				t.Fatalf("failed admission left an open unsupervised task: %+v", todos)
			}
		})
	}
}

// A failed supervision write has no async dispatcher left to release an Inbox task's reserved todo ID.
func TestPlanSupervisionFailureReleasesInboxReservation(t *testing.T) {
	ctx := context.Background()
	st := failingSupervisionStore{DB: todoTestDB(t)}
	s, _ := newQuietServiceOn(t, st)
	const conversation = "inbox-supervision-failure"
	it := taskInboxItem(t, s, "example:supervision-failure", "server", conversation)
	if _, err := s.toolTaskPlanFrom(ctx, "server", conversation, taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, conversation).ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tasks[0].Error == "" || p.Tasks[0].TodoID == "" {
		t.Fatalf("the prepared Inbox task did not fail visibly: %+v", p.Tasks[0])
	}
	s.tasks.mu.Lock()
	held := s.tasks.reusing[p.Tasks[0].TodoID]
	s.tasks.mu.Unlock()
	if held {
		t.Fatalf("the failed task still holds todo %s", p.Tasks[0].TodoID)
	}
}

// The start itself deliberately outlives the caller, so a definite refusal's bookkeeping must too. Unknown outcomes keep
// their separate neutral/adoption path; this is the definite coded-refusal path only (#5732).
func TestAgentStartDefiniteFailureSurvivesCallerCancellation(t *testing.T) {
	st := todoTestDB(t)
	s, f := newTaskServiceOn(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	f.onStart = func(_ *fakeHub, _ map[string]any) { cancel() }
	f.failStart = &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"}
	res, err := s.runTool(ctx, "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err == nil || !strings.Contains(err.Error(), "open elsewhere") {
		t.Fatalf("agent_start = %q (%v), want the definite refusal", res, err)
	}
	todos := s.todosOf(context.Background(), "*")
	if len(todos) != 1 || todos[0].DoneAt == nil || todos[0].StartError == "" || len(todos[0].Agents) != 0 {
		t.Fatalf("caller cancellation lost the failed-start bookkeeping: %+v", todos)
	}
	row, err := st.GetTodo(context.Background(), todos[0].ID)
	if err != nil || row == nil || row.DoneAt == nil || row.StartError == "" {
		t.Fatalf("the store did not record the definite failure: %+v (%v)", row, err)
	}
}
