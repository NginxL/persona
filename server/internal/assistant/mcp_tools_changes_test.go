package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestLeaderToolWatchDoesNotCacheStateReadFailure(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx := context.Background()
	cursor, _ := s.mcpChanges.snapshot()
	s.mcpChanges.notify()
	ft.st.setFailSetting(errors.New("database down"))
	failed := s.sessionMCPToolsSince(ctx, "mac", "leader", false, false, cursor)
	if failed["error"] == nil || failed["revision"] != nil || failed["tools"] != nil {
		t.Fatalf("failed state read became a cacheable empty list: %+v", failed)
	}
	ft.st.setFailSetting(nil)
	// No further task-link change: retrying the unconsumed cursor must recover.
	recovered := s.sessionMCPToolsSince(ctx, "mac", "leader", false, false, cursor)
	if recovered["error"] != nil || recovered["revision"] == nil || recovered["unchanged"] == true {
		t.Fatalf("state recovery needs another link change: %+v", recovered)
	}
}

func TestLeaderToolWatchCancellationAndRestart(t *testing.T) {
	s, _ := newQuietService(t)
	cursor, changed := s.mcpChanges.snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := s.sessionMCPToolsSince(ctx, "box", "unknown", false, false, cursor)
	if got["unchanged"] != true || got["revision"] != cursor {
		t.Fatalf("cancelled watch: %+v", got)
	}
	select {
	case <-changed:
		t.Fatal("cancellation changed rights")
	default:
	}
	other, _ := newQuietService(t)
	restarted := other.sessionMCPToolsSince(context.Background(), "box", "unknown", false, false, cursor)
	if restarted["revision"] == cursor || restarted["unchanged"] == true {
		t.Fatalf("restart did not invalidate the old cursor: %+v", restarted)
	}
}

func TestLeaderContinuationUsesNewPlanRole(t *testing.T) {
	for _, backend := range []string{"memory", "database"} {
		t.Run(backend, func(t *testing.T) {
			var store Store
			if backend == "database" {
				store = taskRecoveryDatabase(t)
			}
			e := newThreadEnv(t, store)
			ctx := context.Background()
			todo, err := e.s.createTaskTodo(ctx, "Continue ordinary task", "Original notes", "m1", "", "")
			if err != nil {
				t.Fatal(err)
			}
			e.must("PUT", "/api/v1/todos/"+todo.ID+"/agents/box/agent01", "")
			e.stoppedAgent01()
			var args struct {
				Tasks []map[string]any `json:"tasks"`
			}
			if err := json.Unmarshal(continuesArgs(todo.ID), &args); err != nil {
				t.Fatal(err)
			}
			args.Tasks[0]["role"] = roleLeader
			raw, _ := json.Marshal(args)
			if _, err := e.s.toolTaskPlan(ctx, "m1", raw); err != nil {
				t.Fatal(err)
			}
			old := newAgentOn(t, e, todo.ID)
			if old.ReleasedAt == nil || old.Source == sourceLeader {
				t.Fatalf("old worker: %+v", old)
			}
			var fresh TodoAgent
			for _, agent := range todoNow(e.s, todo.ID).Agents {
				if agent.active() {
					fresh = agent
				}
			}
			if fresh.Source != sourceLeader || todoCount(e.s) != 1 {
				t.Fatalf("continued leader: %+v", fresh)
			}
			check := func(s *Service) {
				t.Helper()
				lead, _, ok := s.leaderTask(ctx, fresh.MachineID, fresh.SessionID)
				if !ok || lead.ID != todo.ID {
					t.Fatalf("not leader of original task: %+v", lead)
				}
				names := leaderToolNames(s.sessionMCPTools(ctx, fresh.MachineID, fresh.SessionID, false, false))
				if !slices.Contains(names, "task_plan") || !slices.Contains(names, "task_review") {
					t.Fatalf("tools: %v", names)
				}
				if res := s.mcpCall(ctx, "box", "agent01", "task_plan", workerArgs(nil), false); !res.IsError {
					t.Fatal("old worker can dispatch")
				}
			}
			check(e.s)
			if store != nil {
				loaded := newThreadEnv(t, store)
				check(loaded.s)
			}
			res := e.s.mcpCall(ctx, fresh.MachineID, fresh.SessionID, "task_plan", workerArgs(nil), false)
			if res.IsError {
				t.Fatalf("continued leader cannot dispatch: %+v", res)
			}
			waitFor(t, "continued leader's worker", func() bool {
				for _, worker := range e.s.todosOf(ctx, leaderRef(fresh.MachineID, fresh.SessionID)) {
					if len(worker.Agents) > 0 {
						return worker.Agents[0].Source == sourceWorker
					}
				}
				return false
			})
		})
	}
}

func TestLeaderWatchRegisteredBeforeLink(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTaskTodo(ctx, "Leader", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := s.mcpChanges.snapshot()
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan map[string]any, 1)
	go func() { result <- s.sessionMCPToolsSince(watchCtx, "box", "leader", false, false, cursor) }()
	if err := s.linkPlanAgent(ctx, PlanTask{TodoID: todo.ID, Role: roleLeader}, "box", "leader", false); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if !slices.Contains(leaderToolNames(got["tools"].([]proto.Tool)), "task_review") {
		t.Fatalf("watch result: %+v", got)
	}
}
