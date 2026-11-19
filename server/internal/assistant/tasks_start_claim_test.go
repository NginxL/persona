package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// One start at a time per to-do (#10365): since todo_id takes up a failed start, a plan waiting on one sits beside the
// failed row's Retry and "Use <launcher> instead", and task_retry may act on the same start. A Retry, task_retry and a
// plan taking the to-do over share one claim (claimTaskStart), so whichever comes first is the one start; the others
// start nothing and say so when they asked for a launcher. Machine and launcher names are placeholders.

// errDiskFull fails a start for a reason that is not its launcher's: no health record keeps work off box after it.
var errDiskFull = errors.New("the disk is full")

// waitingTodoIDPlan plans, from conv1, a task on to-do id that waits for the owner's Start (not read-only), and
// answers the plan's id.
func waitingTodoIDPlan(t *testing.T, s *Service, id string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "Inspect", "prompt": "Inspect the logs", "machine": "box",
		"cwd": "/code/app", "launcher": "work", "permission": "bypassPermissions", "todo_id": id, "why": "the logs are on box"}}})
	res, err := s.toolTaskPlan(context.Background(), "conv1", args)
	if err != nil {
		t.Fatalf("a todo_id plan on the failed start: %v", err)
	}
	return planID(t, res)
}

// holdLaunchers makes every launchers.list wait until release (or the test's end), so a start that fits its launcher
// (work) stays on its way meanwhile.
func holdLaunchers(t *testing.T, f *fakeHub) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	f.mu.Lock()
	f.launcherGate = gate
	f.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release
}

// startPlanBounded is startPlan, failing the test rather than hanging when it waits on a held launcher list.
func startPlanBounded(t *testing.T, s *Service, id string) *TaskPlan {
	t.Helper()
	type result struct {
		p   *TaskPlan
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := s.startPlan(context.Background(), id, nil, false)
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.p
	case <-time.After(30 * time.Second):
		t.Fatal("startPlan did not return")
		return nil
	}
}

// Retry first: while its agent is still starting, Start on a plan waiting with todo_id on the same to-do is refused for
// that task (the to-do is starting again already), and only the Retry's agent starts.
func TestTodoIDPlanWhileARetryIsStarting(t *testing.T) {
	s, f := launcherRetryHub(t)
	todo, _ := failOnWork(t, s, f, errDiskFull)
	plan := waitingTodoIDPlan(t, s, todo.ID)
	release := holdLaunchers(t, f)
	if _, err := s.retryTask(context.Background(), todo.ID); err != nil {
		t.Fatal(err)
	}
	if !s.taskStartOnWay(todo.ID) || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("the Retry is not on its way: %+v", todoNow(s, todo.ID))
	}
	// Planned while it was still a failed start, the waiting plan is refused only now, and at plan time too.
	if _, err := s.reusableTodo(context.Background(), "conv1", todo.ID, false); err == nil || !strings.Contains(err.Error(), "starting again") {
		t.Fatalf("todo_id while the Retry starts: %v", err)
	}
	p := startPlanBounded(t, s, plan)
	if task := p.Tasks[0]; !strings.Contains(task.Error, "starting again") {
		t.Fatalf("the plan's task started beside the Retry: %+v", task)
	}
	if n := len(startsOf(f)); n != 1 {
		t.Fatalf("%d starts before the held start went on", n)
	}
	release()
	waitFor(t, "the starts to end", func() bool { return s.taskStarting.Load() == 0 })
	if n := len(startsOf(f)); n != 2 {
		t.Fatalf("%d starts, want the failed one and the Retry's", n)
	}
	if got := todoNow(s, todo.ID); len(got.Agents) != 1 || got.DoneAt != nil {
		t.Fatalf("the to-do after the Retry: %+v", got)
	}
}

// Start first: while the plan's agent taking the to-do over is still starting, it holds the to-do's start, so a Retry
// starts nothing, and "Use claude instead" is refused (409, nothing was started on claude) rather than answered as if it
// had; only the plan's agent starts.
func TestRetryWhileATodoIDPlanIsStarting(t *testing.T) {
	s, f := launcherRetryHub(t)
	todo, _ := failOnWork(t, s, f, errDiskFull)
	plan := waitingTodoIDPlan(t, s, todo.ID)
	release := holdLaunchers(t, f)
	if p := startPlanBounded(t, s, plan); p.Tasks[0].Error != "" {
		t.Fatalf("the plan's task: %+v", p.Tasks[0])
	}
	if !s.taskStartOnWay(todo.ID) {
		t.Fatal("the plan taking the to-do over does not hold its start")
	}
	if _, err := s.retryTask(context.Background(), todo.ID); err != nil {
		t.Fatal(err)
	}
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "en", `{"launcher":"claude"}`)
	if text, _ := body["error"].(string); status != 409 || !strings.Contains(text, "Nothing was started on claude") {
		t.Fatalf("Use claude instead while the plan starts: %d %v", status, body)
	}
	if n := len(startsOf(f)); n != 1 {
		t.Fatalf("%d starts before the held start went on", n)
	}
	release()
	waitFor(t, "the starts to end", func() bool { return s.taskStarting.Load() == 0 && !s.taskStartOnWay(todo.ID) })
	if n := len(startsOf(f)); n != 2 {
		t.Fatalf("%d starts, want the failed one and the plan's", n)
	}
	if last := startsOf(f)[1]; last["launcher"] != "work" {
		t.Fatalf("the plan's start %v", last)
	}
}

// Another assistant sharing the store reopened the failed start first (its Retry): the plan taking it over through
// todo_id starts nothing beside it.
func TestTodoIDPlanAfterAnotherReopenedTheFailedStart(t *testing.T) {
	_, f := launcherRetryHub(t)
	database := todoTestDB(t)
	s := newServiceOn(t, f, database)
	todo, _ := failOnWork(t, s, f, errDiskFull)
	plan := waitingTodoIDPlan(t, s, todo.ID)
	if _, ok, err := database.RetryTodo(context.Background(), todo.ID, time.Now()); err != nil || !ok {
		t.Fatalf("the other assistant's reopen: %v %v", ok, err)
	}
	p := startPlanBounded(t, s, plan)
	if task := p.Tasks[0]; !strings.Contains(task.Error, "starting again") {
		t.Fatalf("the plan's task started beside the other's Retry: %+v", task)
	}
	waitFor(t, "the starts to end", func() bool { return s.taskStarting.Load() == 0 })
	if n := len(startsOf(f)); n != 1 {
		t.Fatalf("%d starts, want only the failed one", n)
	}
}

// task_retry while another start of the task is on its way (the owner's Retry): nothing starts, so it answers that
// rather than "starting again with launcher claude", records no move, and its remedy mark goes again; with no mark left
// behind, a todo_id plan may take the to-do up once that start ended.
func TestToolTaskRetryWhileAnotherStartIsOnItsWay(t *testing.T) {
	s, f := launcherRetryHub(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Main = "conv1"; v.MachineID = "server" })
	todo, _ := failOnWork(t, s, f, terminalRefusal(proto.CodeLauncherMissing, "work"))
	st, ok := s.claimTaskStart(todo.ID) // another start on its way
	if !ok {
		t.Fatal("claimed already")
	}
	args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "work cannot start in this terminal", "launcher": "claude"})
	res, err := s.toolTaskRetry(ctx, "conv1", args)
	if err == nil || !strings.Contains(err.Error(), "nothing was started") {
		t.Fatalf("task_retry while a start is on its way: %q %v", res, err)
	}
	if retryMoves(s, "conv1", todo.ID, "work", "claude") {
		t.Fatal("a move that never happened is in the task's history")
	}
	_, p, i, _ := s.remedyOf(ctx, todo.ID)
	if r := p.Tasks[i].Start; r != nil && r.State == remedying {
		t.Fatalf("the remedy mark stayed: %+v", r)
	}
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "zh-Hans", `{"launcher":"claude"}`)
	if text, _ := body["error"].(string); status != 409 || !strings.Contains(text, "没有改用 claude 启动") {
		t.Fatalf("Use claude instead while a start is on its way: %d %v", status, body)
	}
	s.endTaskStart(todo.ID, st, proto.Session{})
	if n := len(startsOf(f)); n != 1 {
		t.Fatalf("%d starts", n)
	}
	if _, err := s.reusableTodo(ctx, "conv1", todo.ID, false); err != nil {
		t.Fatalf("todo_id once that start ended: %v", err)
	}
}
