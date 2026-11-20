package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func requestTestPiece(t *testing.T, service *Service, root Todo, title string, after ...string) Todo {
	t.Helper()
	pieceTool(t, service, "conv1", "todo_create", map[string]any{"title": title, "part_of": root.ID, "after": after})
	return mustTodo(t, service, title)
}

func requestTestPlan(t *testing.T, service *Service, pieces ...Todo) *TaskPlan {
	t.Helper()
	var tasks []PlanTask
	for _, piece := range pieces {
		task := readonlyTestTask("")
		task.Machine, task.Title, task.ReuseTodo = "server", piece.Title, piece.ID
		tasks = append(tasks, task)
	}
	ctx := context.Background()
	output, err := service.toolTaskPlan(ctx, "conv1", readonlyTaskArgs(t, tasks...))
	if err != nil {
		t.Fatal(err)
	}
	if id := regexp.MustCompile(`plan_[0-9a-f]+`).FindString(output); id != "" {
		plan, err := service.loadPlan(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	return readonlyFeedPlan(t, service, "conv1")
}

func requestTestSetup(t *testing.T, env *todoEnv) Todo {
	t.Helper()
	env.f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
	setSettings(t, env.s, func(settings *Settings) { settings.Main = "conv1" })
	return env.create("Search")
}

func requestTestApprove(t *testing.T, env *todoEnv, plan *TaskPlan) *TaskPlan {
	t.Helper()
	response := env.must(http.MethodPost, "/api/v1/task-plans/"+plan.ID+"/start", `{}`)
	var result struct{ Plan *TaskPlan }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Plan
}

func requestTestIdle(t *testing.T, service *Service) {
	t.Helper()
	waitFor(t, "dispatches to settle", func() bool { return service.taskStarting.Load() == 0 })
}

func requestTestBlockers(t *testing.T, service *Service, planID string, slot int, want ...string) {
	t.Helper()
	plan, err := service.loadPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	queue := plan.Tasks[slot].Queue
	if queue == nil || queue.Claimed || !slices.Equal(queue.Blockers, want) {
		t.Fatalf("queue = %+v, want blockers %v", queue, want)
	}
}

func TestRequestPrerequisitesDispatch(t *testing.T) {
	todoStores(t, func(t *testing.T, env *todoEnv, database *db.DB) {
		ctx := context.Background()
		root := requestTestSetup(t, env)
		env.must(http.MethodPatch, "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
		first := requestTestPiece(t, env.s, root, "API")
		second := requestTestPiece(t, env.s, root, "UI")
		dependent := requestTestPiece(t, env.s, root, "Integration tests", first.ID, second.ID)
		plan := requestTestPlan(t, env.s, first, second, dependent)
		if plan.State != planOpen || len(taskStartMachines(env.f)) != 0 {
			t.Fatal("the dependent task must keep the plan open for owner approval")
		}
		if _, err := env.s.startPlan(ctx, plan.ID, nil, false); err == nil {
			t.Fatal("manager must not accept an automatic start of the gated plan")
		}
		started := requestTestApprove(t, env, plan)
		if queue := started.Tasks[2].Queue; queue == nil || !slices.Equal(queue.Blockers, []string{first.ID, second.ID}) {
			t.Fatalf("the approval response did not name both blockers: %+v", queue)
		}
		requestTestIdle(t, env.s)
		if len(taskStartMachines(env.f)) != 2 || len(todoNow(env.s, first.ID).Agents) != 1 || len(todoNow(env.s, second.ID).Agents) != 1 {
			t.Fatal("both independent pieces should start without waiting for acceptance")
		}
		if _, err := env.s.reusableTodo(ctx, "conv1", dependent.ID, false); err == nil {
			t.Fatal("a second plan must not take over an approved waiting start")
		}
		if closed, err := env.s.autoDoneTodo(ctx, second.ID, "Agent turn completed", time.Now()); err != nil || !closed {
			t.Fatalf("judge completion: %t, %v", closed, err)
		}
		reviewTask(t, env.s, first.ID, map[string]any{"verdict": reviewAccepted, "note": "Checked the API output"})
		env.s.sweepAdmissionQueue(ctx)
		requestTestIdle(t, env.s)
		requestTestBlockers(t, env.s, plan.ID, 2, second.ID)
		if err := env.s.reopenTodo(ctx, second.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.s.updateSupervision(ctx, second.ID, func(ledger *Supervision) bool {
			ledger.State = reviewRework
			return true
		}); err != nil {
			t.Fatal(err)
		}
		env.s.sweepAdmissionQueue(ctx)
		requestTestIdle(t, env.s)
		requestTestBlockers(t, env.s, plan.ID, 2, second.ID)
		reviewTask(t, env.s, second.ID, map[string]any{"verdict": reviewAccepted, "note": "Checked the UI output"})
		if database != nil {
			env.s = newServiceOn(t, env.f, database)
			if err := env.s.recoverTaskStarts(ctx, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		var sweeps sync.WaitGroup
		for range 8 {
			sweeps.Go(func() { env.s.sweepAdmissionQueue(ctx) })
		}
		sweeps.Wait()
		requestTestIdle(t, env.s)
		if len(taskStartMachines(env.f)) != 3 || len(todoNow(env.s, dependent.ID).Agents) != 1 {
			t.Fatalf("expected one dependent launch, got starts %v", taskStartMachines(env.f))
		}
	})
}

func TestRequestPrerequisitesMissingCancelledAndLegacy(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "legacy"
		if strict {
			name = "enforced"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			env := newTodoEnv(t, nil)
			root := requestTestSetup(t, env)
			if strict {
				env.must(http.MethodPatch, "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
			}
			first := requestTestPiece(t, env.s, root, "API")
			second := requestTestPiece(t, env.s, root, "UI")
			dependent := requestTestPiece(t, env.s, root, "Integration tests", first.ID, second.ID)
			env.must(http.MethodDelete, "/api/v1/todos/"+first.ID, "")
			if closed, err := env.s.reviewDoneTodo(ctx, second.ID, "Cancelled", false); err != nil || !closed {
				t.Fatalf("cancel: %t, %v", closed, err)
			}
			plan := requestTestPlan(t, env.s, dependent)
			if plan.State == planOpen {
				requestTestApprove(t, env, plan)
			}
			requestTestIdle(t, env.s)
			if strict {
				requestTestBlockers(t, env.s, plan.ID, 0, first.ID, second.ID)
				if starts := taskStartMachines(env.f); len(starts) != 0 {
					t.Fatalf("blocked task launched: %v", starts)
				}
			} else if starts := taskStartMachines(env.f); len(starts) != 1 {
				t.Fatalf("legacy graph must remain advisory: %v", starts)
			}
		})
	}
}

func TestRequestPrerequisitesUnconfirmedStartNeverReplays(t *testing.T) {
	shortTaskStartReconcile(t)
	database := todoTestDB(t)
	env := newTodoEnv(t, database)
	ctx := context.Background()
	root := requestTestSetup(t, env)
	env.must(http.MethodPatch, "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
	first := requestTestPiece(t, env.s, root, "API")
	dependent := requestTestPiece(t, env.s, root, "Integration tests", first.ID)
	plan := requestTestPlan(t, env.s, dependent)
	requestTestApprove(t, env, plan)
	requestTestIdle(t, env.s)
	reviewTask(t, env.s, first.ID, map[string]any{"verdict": reviewAccepted, "note": "Verified the API"})
	env.f.mu.Lock()
	env.f.failStart = rpc.ErrClosed
	env.f.mu.Unlock()
	env.s.sweepAdmissionQueue(ctx)
	requestTestIdle(t, env.s)
	loaded, err := env.s.loadPlan(ctx, plan.ID)
	if err != nil || loaded.Tasks[0].Queue == nil || !loaded.Tasks[0].Queue.Claimed || len(taskStartMachines(env.f)) != 1 {
		t.Fatalf("unconfirmed launch: loaded=%+v error=%v starts=%v", loaded, err, taskStartMachines(env.f))
	}
	env.s = newServiceOn(t, env.f, database)
	if err := env.s.recoverTaskStarts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	env.s.sweepAdmissionQueue(ctx)
	requestTestIdle(t, env.s)
	if starts := taskStartMachines(env.f); len(starts) != 1 {
		t.Fatalf("unconfirmed launch was replayed after restart: %v", starts)
	}
}

func TestRequestPrerequisitesCancelledTargetDoesNotStart(t *testing.T) {
	env := newTodoEnv(t, nil)
	ctx := context.Background()
	root := requestTestSetup(t, env)
	env.must(http.MethodPatch, "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
	first := requestTestPiece(t, env.s, root, "API")
	dependent := requestTestPiece(t, env.s, root, "Integration tests", first.ID)
	plan := requestTestPlan(t, env.s, dependent)
	requestTestApprove(t, env, plan)
	requestTestIdle(t, env.s)
	if closed, err := env.s.reviewDoneTodo(ctx, dependent.ID, "Cancelled", false); err != nil || !closed {
		t.Fatalf("cancel: %t, %v", closed, err)
	}
	reviewTask(t, env.s, first.ID, map[string]any{"verdict": reviewAccepted, "note": "Verified the API"})
	env.s.sweepAdmissionQueue(ctx)
	requestTestIdle(t, env.s)
	loaded, err := env.s.loadPlan(ctx, plan.ID)
	if err != nil || loaded.Tasks[0].Queue != nil || len(taskStartMachines(env.f)) != 0 {
		t.Fatalf("ended target retained approval or launched: loaded=%+v error=%v starts=%v", loaded, err, taskStartMachines(env.f))
	}
}

func TestRequestPrerequisitesStalePlanLinksRefused(t *testing.T) {
	env := newTodoEnv(t, nil)
	root := requestTestSetup(t, env)
	env.must(http.MethodPatch, "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
	first := requestTestPiece(t, env.s, root, "API")
	task := readonlyTestTask("")
	task.Machine, task.PartOf, task.After = "server", root.ID, []string{first.ID}
	if _, err := env.s.toolTaskPlan(context.Background(), "conv1", readonlyTaskArgs(t, task)); err != nil {
		t.Fatal(err)
	}
	plan := readonlyFeedPlan(t, env.s, "conv1")
	env.must(http.MethodDelete, "/api/v1/todos/"+first.ID, "")
	response := env.do(http.MethodPost, "/api/v1/task-plans/"+plan.ID+"/start", `{}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), first.ID) || len(taskStartMachines(env.f)) != 0 {
		t.Fatalf("stale inline links: response=%d %s, starts=%v", response.Code, response.Body, taskStartMachines(env.f))
	}
}
