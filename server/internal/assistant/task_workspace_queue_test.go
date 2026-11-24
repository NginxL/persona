package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func workspaceQueueService(t *testing.T, database *db.DB) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.devices = []Device{
		{ID: "server", Name: "Server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "Box", Home: "/home/me", Online: true, Reachable: true},
	}
	f.sessions["server"], f.sessions["box"] = nil, nil
	f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
	f.ownModels = true
	f.nodes = map[string]nodeAnswer{
		"worktree.status": func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{Path: "/code/app", Branch: "main"}, nil
		},
		"workspace.resolve": func(_ string, raw json.RawMessage) (any, error) {
			var in struct{ Path string }
			_ = json.Unmarshal(raw, &in)
			return proto.Workspace{Cwd: strings.TrimSuffix(in.Path, "/"), Available: true}, nil
		},
		"workspaces.list": func(string, json.RawMessage) (any, error) {
			return proto.WorkspacesResponse{Workspaces: []proto.Workspace{
				{ID: "ws-one", Source: "herdr", SourceID: "ws-one", Cwd: "/code/other", Branch: "fix/login", Available: true},
			}}, nil
		},
	}
	var s *Service
	if database != nil {
		s = newServiceOn(t, f, database)
	} else {
		s = newStorelessService(t, f)
	}
	s.briefsOptional = true
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	return s, f
}

func planWorkspaceQueueTask(t *testing.T, s *Service, chat string, task map[string]any) (string, *TaskPlan) {
	t.Helper()
	if _, ok := task["role"]; !ok {
		task["role"] = "worker"
	}
	if _, ok := task["machine"]; !ok {
		task["machine"] = "box"
	}
	if _, ok := task["cwd"]; !ok {
		task["cwd"] = "/code/app"
	}
	if _, ok := task["permission"]; !ok {
		task["permission"] = "bypassPermissions"
	}
	if _, ok := task["user_requested"]; !ok {
		task["user_requested"] = true
	}
	if _, ok := task["why"]; !ok {
		task["why"] = "Work sequentially on the feature branch"
	}
	if _, ok := task["done_when"]; !ok {
		task["done_when"] = []string{"Changes verified"}
	}
	raw, err := json.Marshal(map[string]any{"tasks": []any{task}})
	if err != nil {
		t.Fatal(err)
	}
	// The plan this call made is chat's plan that was not there before it. On Postgres, task_plans and assistant_feed
	// are not emptied between tests: chat's feed can begin with plans earlier tests made there, and its first page end
	// before this one. A task queued without asking answers with no plan id to read it by (task_terminal_test.go), and
	// in a synctest bubble the plan's time is the time before the call.
	ctx := context.Background()
	before, err := s.conversationPlans(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(before))
	for _, p := range before {
		known[p.ID] = true
	}
	out, err := s.toolTaskPlan(ctx, chat, raw)
	if err != nil {
		t.Fatalf("task_plan(%v): %v", task["title"], err)
	}
	after, err := s.conversationPlans(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range after {
		if !known[p.ID] {
			return out, p
		}
	}
	t.Fatalf("task_plan(%v) made no plan in %s", task["title"], chat)
	return "", nil
}

func waitWorkspaceStarts(t *testing.T, s *Service, f *fakeHub, want int) {
	t.Helper()
	waitFor(t, "workspace queue starts to settle", func() bool {
		return s.taskStarting.Load() == 0 && len(taskStartMachines(f)) == want
	})
}

func TestTaskWorkspaceQueueJSONValidation(t *testing.T) {
	var w TaskWorkspace
	if err := json.Unmarshal([]byte(`{"existing":"/code/app","queue":true}`), &w); err != nil {
		t.Fatalf("unmarshal valid queued existing: %v", err)
	}
	if w.Mode != workspaceExisting || w.Existing != "/code/app" || !w.Queue {
		t.Fatalf("got %+v", w)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"existing":"/code/app"`) || !strings.Contains(string(raw), `"queue":true`) {
		t.Fatalf("marshal queued existing = %s", raw)
	}
	w.Queue = false
	raw, err = json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"existing":"/code/app"}` {
		t.Fatalf("non-queued existing must keep exact legacy JSON, got %s", raw)
	}
	for _, invalid := range []string{`{"queue":true}`, `{"new":{"branch":"a"},"queue":true}`, `{"backend":{"operation_id":"op_1"},"queue":true}`, `{"container":{"selected_repo_ids":["r1"]},"queue":true}`} {
		var bad TaskWorkspace
		if err := json.Unmarshal([]byte(invalid), &bad); err == nil {
			t.Fatalf("%s should be rejected", invalid)
		}
	}
}

func TestTaskWorkspaceSerialQueueParksWithoutNeedsYouError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		owner, err := s.createTodo(ctx, "Existing writer", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "writer", owner.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}

		out, p := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Follow-up change",
			"prompt":    "Apply follow-up on same branch",
			"workspace": map[string]any{"existing": "/code/app/", "queue": true},
		})
		synctest.Wait()

		saved, err := s.loadPlan(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		pt := saved.Tasks[0]
		checkPlacementGolden(t, pt.Placement, "v1 enforce box ->  (workspace_occupied) | box workspace_occupied")
		if len(f.starts) != 0 {
			t.Fatalf("queued task must not start while workspace is occupied: starts=%v", f.starts)
		}
		if pt.Error != "" {
			t.Fatalf("queued task must not record a start error: %q", pt.Error)
		}
		if pt.Queue == nil || pt.Queue.Key != workspaceQueueKey || pt.Queue.Workspace == nil {
			t.Fatalf("expected workspace queue entry, got %+v", pt.Queue)
		}
		if pt.Queue.Workspace.OwnerID != owner.ID || pt.Queue.Workspace.OwnerTitle != owner.Title {
			t.Fatalf("queue owner = %+v, want %s (%s)", pt.Queue.Workspace, owner.ID, owner.Title)
		}
		if !strings.Contains(pt.Queue.Reason, owner.ID) || !strings.Contains(pt.Queue.Reason, owner.Title) {
			t.Fatalf("queue reason must name occupying task: %q", pt.Queue.Reason)
		}
		if !strings.Contains(out, "queued:") || !strings.Contains(out, owner.ID) {
			t.Fatalf("task_plan response should mention queued state and owner: %q", out)
		}
		td := todoNow(s, pt.TodoID)
		if td.DoneAt != nil || td.StartError != "" {
			t.Fatalf("queued to-do must remain open without start error: %+v", td)
		}
		if strings.Contains(feedKinds(s, "conv1"), feedReceipt) {
			t.Fatalf("queued task must not emit a failure receipt: %s", feedKinds(s, "conv1"))
		}
		// Task card shows waiting (not failed) with the localized owner-naming reason.
		items := []FeedItem{{ID: "plan", Kind: feedPlan, Ref: p.ID, At: time.Now()}}
		cards, err := s.taskCards(ctx, items, map[string]*TaskPlan{p.ID: saved})
		if err != nil || len(cards) != 1 || cards[0].State != "waiting" || cards[0].Reason != pt.Queue.Reason {
			t.Fatalf("card = %+v, err=%v; want waiting with %q", cards, err, pt.Queue.Reason)
		}
		// Triage must not place the queued to-do into "Needs you" (triageWaiting).
		tr := triageTodo(listedTodo{Todo: td}, nil, time.Now(), time.Time{}, time.UTC, false)
		if tr.Section == triageWaiting {
			t.Fatalf("queued to-do must not pollute Needs you: triage=%+v", tr)
		}
	})
}

func TestTaskWorkspaceSerialQueueFIFOAcrossThreeTasksAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		owner, err := s.createTodo(ctx, "Owner task", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "owner-sess", owner.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}

		_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Queued 1",
			"prompt":    "Run first queued task",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Queued 2",
			"prompt":    "Run second queued task",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		_, p3 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Queued 3",
			"prompt":    "Run third queued task",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		synctest.Wait()
		if len(f.starts) != 0 {
			t.Fatalf("expected 0 starts while owner holds workspace, got %d", len(f.starts))
		}
		q1ID, q2ID, q3ID := p1.Tasks[0].TodoID, p2.Tasks[0].TodoID, p3.Tasks[0].TodoID

		// Cancel Queued 2 before it ever starts: its queue clears immediately and its card shows cancelled.
		if ok, err := s.reviewDoneTodo(ctx, q2ID, "Dropped by supervisor", false); err != nil || !ok {
			t.Fatalf("cancel q2: ok=%v err=%v", ok, err)
		}
		synctest.Wait()
		loaded2, _ := s.loadPlan(ctx, p2.ID)
		if loaded2.Tasks[0].Queue != nil {
			t.Fatalf("cancelled queued task must clear its queue entry: %+v", loaded2.Tasks[0].Queue)
		}
		cards2, err := s.taskCards(ctx, []FeedItem{{ID: "plan2", Kind: feedPlan, Ref: p2.ID, At: time.Now()}}, map[string]*TaskPlan{p2.ID: loaded2})
		if err != nil || len(cards2) != 1 || cards2[0].State != "cancelled" {
			t.Fatalf("cancelled queued card = %+v, err=%v", cards2, err)
		}

		// Cancel owner (task_review cancelled) -> workspace freed -> Q1 auto-starts, Q3 updates to wait on Q1, Q2 never starts.
		if ok, err := s.reviewDoneTodo(ctx, owner.ID, "Owner cancelled", false); err != nil || !ok {
			t.Fatalf("cancel owner: ok=%v err=%v", ok, err)
		}
		synctest.Wait()
		if len(f.starts) != 1 {
			t.Fatalf("expected exactly 1 start (Q1), got %d: %v", len(f.starts), f.starts)
		}
		if len(todoNow(s, q1ID).Agents) != 1 {
			t.Fatalf("Q1 should have its agent linked: %+v", todoNow(s, q1ID))
		}
		if len(todoNow(s, q2ID).Agents) != 0 || len(todoNow(s, q3ID).Agents) != 0 {
			t.Fatalf("Q2 and Q3 must not have started yet")
		}
		loaded1, _ := s.loadPlan(ctx, p1.ID)
		if loaded1.Tasks[0].Queue != nil {
			t.Fatalf("started Q1 must clear its queue entry: %+v", loaded1.Tasks[0].Queue)
		}
		loaded3, _ := s.loadPlan(ctx, p3.ID)
		if loaded3.Tasks[0].Queue == nil || loaded3.Tasks[0].Queue.Workspace == nil || loaded3.Tasks[0].Queue.Workspace.OwnerID != q1ID {
			t.Fatalf("Q3 should now display waiting behind Q1 (%s), got %+v", q1ID, loaded3.Tasks[0].Queue)
		}

		// Complete Q1 via editTodo (todo_complete path) -> Q3 auto-starts.
		done := true
		if _, err := s.editTodo(ctx, q1ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 2 {
			t.Fatalf("expected 2 total starts (Q1 then Q3), got %d", len(f.starts))
		}
		if len(todoNow(s, q3ID).Agents) != 1 {
			t.Fatalf("Q3 should have started after Q1 completed: %+v", todoNow(s, q3ID))
		}
		if len(todoNow(s, q2ID).Agents) != 0 {
			t.Fatalf("cancelled Q2 must never start")
		}
	})
}

func TestTaskWorkspaceSerialQueueAfterImplicitOptInAndRequestGraph(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		root, err := s.createTodo(ctx, "Feature branch rollout", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		partOf := root.ID
		taskA, err := s.createTodo(ctx, "Step A", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.editTodo(ctx, taskA.ID, db.TodoEdit{PartOf: &partOf}); err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, taskA.ID, "box", "sess-a", taskA.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}

		// Task B names taskA in `after` without explicit workspace.queue: true -> implicitly opts into serial queueing.
		_, pB := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Step B",
			"prompt":    "Build on Step A",
			"part_of":   root.ID,
			"after":     []string{taskA.ID},
			"workspace": map[string]any{"existing": "/code/app"},
		})
		taskBID := pB.Tasks[0].TodoID
		// Task C names taskB in `after` (transitively after taskA) -> also queues cleanly behind taskA.
		_, pC := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Step C",
			"prompt":    "Verify Step B",
			"part_of":   root.ID,
			"after":     []string{taskBID},
			"workspace": map[string]any{"existing": "/code/app"},
		})
		taskCID := pC.Tasks[0].TodoID
		synctest.Wait()

		if len(f.starts) != 0 {
			t.Fatalf("neither B nor C may start while A holds workspace: %v", f.starts)
		}
		todoB, todoC := todoNow(s, taskBID), todoNow(s, taskCID)
		if todoB.PartOf != root.ID || !slices.Equal(todoB.After, []string{taskA.ID}) {
			t.Fatalf("B request graph links lost: %+v", todoB)
		}
		if todoC.PartOf != root.ID || !slices.Equal(todoC.After, []string{taskBID}) {
			t.Fatalf("C request graph links lost: %+v", todoC)
		}
		// Another plan must not steal a queued to-do via todo_id.
		if _, err := s.reusableTodo(ctx, "conv1", taskBID, false); err == nil {
			t.Fatal("queued to-do must not be reusable by another plan")
		}

		// Accept taskA -> taskB auto-starts; taskC stays queued (waiting on taskB); no duplicate todo_ready signal for B.
		if ok, err := s.reviewDoneTodo(ctx, taskA.ID, "Accepted Step A", true); err != nil || !ok {
			t.Fatalf("accept A: ok=%v err=%v", ok, err)
		}
		synctest.Wait()
		if len(f.starts) != 1 || len(todoNow(s, taskBID).Agents) != 1 || len(todoNow(s, taskCID).Agents) != 0 {
			t.Fatalf("after A accepted: starts=%d B.Agents=%d C.Agents=%d", len(f.starts), len(todoNow(s, taskBID).Agents), len(todoNow(s, taskCID).Agents))
		}
		s.supervise.mu.Lock()
		_, readyPending := s.supervise.pending[taskBID+"|"+sigTodoReady]
		s.supervise.mu.Unlock()
		if readyPending {
			t.Fatal("auto-started workspace-queued task must not also emit a duplicate todo_ready supervision signal")
		}

		// Complete taskB -> taskC auto-starts.
		if ok, err := s.reviewDoneTodo(ctx, taskBID, "Accepted Step B", true); err != nil || !ok {
			t.Fatalf("accept B: ok=%v err=%v", ok, err)
		}
		synctest.Wait()
		if len(f.starts) != 2 || len(todoNow(s, taskCID).Agents) != 1 {
			t.Fatalf("after B accepted: starts=%d C.Agents=%d", len(f.starts), len(todoNow(s, taskCID).Agents))
		}
	})
}

func TestTaskWorkspaceSerialQueueSamePlanFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		raw, err := json.Marshal(map[string]any{
			"tasks": []map[string]any{
				{
					"title": "First", "prompt": "Do first", "role": "worker", "machine": "box", "cwd": "/code/app",
					"permission": "bypassPermissions", "user_requested": true, "why": "Step 1",
					"done_when": []string{"Done 1"}, "workspace": map[string]any{"existing": "/code/app", "queue": true},
				},
				{
					"title": "Second", "prompt": "Do second", "role": "worker", "machine": "box", "cwd": "/code/app",
					"permission": "bypassPermissions", "user_requested": true, "why": "Step 2",
					"done_when": []string{"Done 2"}, "workspace": map[string]any{"existing": "/code/app", "queue": true},
				},
				{
					"title": "Third", "prompt": "Do third", "role": "worker", "machine": "box", "cwd": "/code/app",
					"permission": "bypassPermissions", "user_requested": true, "why": "Step 3",
					"done_when": []string{"Done 3"}, "workspace": map[string]any{"existing": "/code/app", "queue": true},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		p := readonlyFeedPlan(t, s, "conv1")
		if len(f.starts) != 1 || len(todoNow(s, p.Tasks[0].TodoID).Agents) != 1 {
			t.Fatalf("only First should start initially: starts=%d plan=%+v", len(f.starts), p.Tasks)
		}
		if p.Tasks[1].Queue == nil || p.Tasks[2].Queue == nil {
			t.Fatalf("Second and Third should be queued: %+v", p.Tasks)
		}
		done := true
		if _, err := s.editTodo(ctx, p.Tasks[0].TodoID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 2 || len(todoNow(s, p.Tasks[1].TodoID).Agents) != 1 || len(todoNow(s, p.Tasks[2].TodoID).Agents) != 0 {
			t.Fatalf("Second should start after First: starts=%d", len(f.starts))
		}
		if _, err := s.editTodo(ctx, p.Tasks[1].TodoID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 3 || len(todoNow(s, p.Tasks[2].TodoID).Agents) != 1 {
			t.Fatalf("Third should start after Second: starts=%d", len(f.starts))
		}
	})
}

func TestTaskWorkspaceSerialQueueAfterBlockedAndCancelledPrereq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		root, _ := s.createTodo(ctx, "Release", "", "conv1", "")
		partOf := root.ID
		owner, _ := s.createTodo(ctx, "Backend", "", "conv1", "")
		other, _ := s.createTodo(ctx, "Schema", "", "conv1", "")
		_, _ = s.editTodo(ctx, owner.ID, db.TodoEdit{PartOf: &partOf})
		_, _ = s.editTodo(ctx, other.ID, db.TodoEdit{PartOf: &partOf})
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "backend-sess", owner.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}

		// Q1 waits on both owner and external prerequisite `other`; Q2 waits only on workspace.
		_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Needs both",
			"prompt":    "Needs backend and schema",
			"part_of":   root.ID,
			"after":     []string{owner.ID, other.ID},
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Independent",
			"prompt":    "Only needs workspace",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		synctest.Wait()

		// Completing owner frees workspace; Q1 is still blocked on `other`, so Q2 starts without head-of-line deadlock.
		done := true
		if _, err := s.editTodo(ctx, owner.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 1 || len(todoNow(s, p2.Tasks[0].TodoID).Agents) != 1 || len(todoNow(s, p1.Tasks[0].TodoID).Agents) != 0 {
			t.Fatalf("Q2 should bypass after-blocked Q1: starts=%d q1=%d q2=%d",
				len(f.starts), len(todoNow(s, p1.Tasks[0].TodoID).Agents), len(todoNow(s, p2.Tasks[0].TodoID).Agents))
		}
		// Complete Q2 -> workspace free, but `other` is still open -> Q1 updates to waiting on `other` and stays queued.
		if _, err := s.editTodo(ctx, p2.Tasks[0].TodoID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		loaded1, _ := s.loadPlan(ctx, p1.ID)
		if len(f.starts) != 1 || loaded1.Tasks[0].Queue == nil || !slices.Equal(loaded1.Tasks[0].Queue.Blockers, []string{other.ID}) {
			t.Fatalf("Q1 should wait on external prereq %s: starts=%d queue=%+v", other.ID, len(f.starts), loaded1.Tasks[0].Queue)
		}
		// Now complete `other` -> Q1 deterministically auto-starts!
		if _, err := s.editTodo(ctx, other.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 2 || len(todoNow(s, p1.Tasks[0].TodoID).Agents) != 1 {
			t.Fatalf("Q1 should auto-start once external prereq completes: starts=%d", len(f.starts))
		}
	})

	// Cancelled after prerequisite must never trigger auto-start of a task that explicitly depends on it.
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		root, _ := s.createTodo(ctx, "Request", "", "conv1", "")
		partOf := root.ID
		owner, _ := s.createTodo(ctx, "Prereq", "", "conv1", "")
		_, _ = s.editTodo(ctx, owner.ID, db.TodoEdit{PartOf: &partOf})
		_ = s.linkTodoAgent(ctx, owner.ID, "box", "prereq-sess", owner.Title, "/code/app", "")
		_, p := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Dependent",
			"prompt":    "Depends on Prereq",
			"part_of":   root.ID,
			"after":     []string{owner.ID},
			"workspace": map[string]any{"existing": "/code/app"},
		})
		synctest.Wait()
		if ok, err := s.reviewDoneTodo(ctx, owner.ID, "Cancelled", false); err != nil || !ok {
			t.Fatalf("cancel owner: ok=%v err=%v", ok, err)
		}
		synctest.Wait()
		if len(f.starts) != 0 || len(todoNow(s, p.Tasks[0].TodoID).Agents) != 0 {
			t.Fatalf("cancelled after prerequisite must not start dependent: starts=%v", f.starts)
		}
	})
}

func TestTaskWorkspaceSerialQueueRestartDurabilityAndIdempotency(t *testing.T) {
	t.Run("simulated_restart", func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()

		owner, err := s.createTodo(ctx, "Long running task", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "long-sess", owner.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}
		_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Restart Q1",
			"prompt":    "First after restart",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Restart Q2",
			"prompt":    "Second after restart",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		waitWorkspaceStarts(t, s, f, 0)
		q1ID, q2ID := p1.Tasks[0].TodoID, p2.Tasks[0].TodoID

		// Simulate hub restart: deserialize persisted plans & todos into a brand-new Service with fresh in-memory state.
		restarted := newStorelessService(t, f)
		restarted.briefsOptional = true
		s.tasks.mu.Lock()
		restarted.tasks.mu.Lock()
		for id, plan := range s.tasks.plans {
			raw, err := json.Marshal(plan)
			if err != nil {
				restarted.tasks.mu.Unlock()
				s.tasks.mu.Unlock()
				t.Fatal(err)
			}
			var cloned TaskPlan
			if err := json.Unmarshal(raw, &cloned); err != nil {
				restarted.tasks.mu.Unlock()
				s.tasks.mu.Unlock()
				t.Fatal(err)
			}
			restarted.tasks.plans[id] = &cloned
		}
		restarted.tasks.mu.Unlock()
		s.tasks.mu.Unlock()
		s.todos.mu.RLock()
		restarted.todos.mu.Lock()
		if restarted.todos.todos == nil {
			restarted.todos.todos = map[string]*Todo{}
		}
		if restarted.todos.activeBySession == nil {
			restarted.todos.activeBySession = map[[2]string]string{}
		}
		for id, td := range s.todos.todos {
			cp := copyTodoLocked(td)
			restarted.todos.todos[id] = &cp
			if cp.DoneAt == nil {
				for _, a := range cp.Agents {
					if a.active() {
						restarted.todos.activeBySession[[2]string{a.MachineID, a.SessionID}] = cp.ID
					}
				}
			}
		}
		restarted.todos.loaded = true
		restarted.todos.mu.Unlock()
		s.todos.mu.RUnlock()
		setSettings(t, restarted, func(v *Settings) { v.Main = "conv1" })

		if err := restarted.recoverTaskStarts(ctx, time.Now().UTC().Add(time.Second)); err != nil {
			t.Fatalf("recoverTaskStarts: %v", err)
		}
		if td := todoNow(restarted, q1ID); td.DoneAt != nil || td.StartError != "" {
			t.Fatalf("restart recovery failed queued Q1: %+v", td)
		}
		if td := todoNow(restarted, q2ID); td.DoneAt != nil || td.StartError != "" {
			t.Fatalf("restart recovery failed queued Q2: %+v", td)
		}

		// Owner finished during downtime (mark done directly, then trigger recovery sweeps concurrently):
		// Q1 must start strictly once and Q2 must remain queued.
		now := time.Now().UTC()
		restarted.todos.mu.Lock()
		restarted.todos.todos[owner.ID].DoneAt = &now
		restarted.todos.mu.Unlock()
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() { restarted.sweepAdmissionQueue(ctx) })
		}
		wg.Wait()
		waitWorkspaceStarts(t, restarted, f, 1)
		if len(todoNow(restarted, q1ID).Agents) != 1 || len(todoNow(restarted, q2ID).Agents) != 0 {
			t.Fatalf("after restart + sweeps: Q1 agents=%d Q2 agents=%d",
				len(todoNow(restarted, q1ID).Agents), len(todoNow(restarted, q2ID).Agents))
		}

		// Complete Q1 -> Q2 auto-starts strictly once.
		done := true
		if _, err := restarted.editTodo(ctx, q1ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitWorkspaceStarts(t, restarted, f, 2)
		if len(todoNow(restarted, q2ID).Agents) != 1 {
			t.Fatalf("Q2 did not start after Q1 completed: %+v", todoNow(restarted, q2ID))
		}
	})

	t.Run("postgres", func(t *testing.T) {
		database := todoTestDB(t)
		s, f := workspaceQueueService(t, database)
		ctx := context.Background()

		owner, err := s.createTodo(ctx, "Long running task", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "long-sess", owner.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}
		_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Restart Q1",
			"prompt":    "First after restart",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Restart Q2",
			"prompt":    "Second after restart",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		waitWorkspaceStarts(t, s, f, 0)
		q1ID, q2ID := p1.Tasks[0].TodoID, p2.Tasks[0].TodoID

		restarted, _ := workspaceQueueService(t, database)
		restarted.hub = f
		if err := restarted.recoverTaskStarts(ctx, time.Now().UTC().Add(time.Second)); err != nil {
			t.Fatalf("recoverTaskStarts: %v", err)
		}
		if td := todoNow(restarted, q1ID); td.DoneAt != nil || td.StartError != "" {
			t.Fatalf("restart recovery failed queued Q1: %+v", td)
		}
		if td := todoNow(restarted, q2ID); td.DoneAt != nil || td.StartError != "" {
			t.Fatalf("restart recovery failed queued Q2: %+v", td)
		}

		done := true
		if _, err := restarted.editTodo(ctx, owner.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() { restarted.sweepAdmissionQueue(ctx) })
		}
		wg.Wait()
		waitWorkspaceStarts(t, restarted, f, 1)
		if len(todoNow(restarted, q1ID).Agents) != 1 || len(todoNow(restarted, q2ID).Agents) != 0 {
			t.Fatalf("after restart + owner complete: Q1 agents=%d Q2 agents=%d",
				len(todoNow(restarted, q1ID).Agents), len(todoNow(restarted, q2ID).Agents))
		}

		if _, err := restarted.editTodo(ctx, q1ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitWorkspaceStarts(t, restarted, f, 2)
		if len(todoNow(restarted, q2ID).Agents) != 1 {
			t.Fatalf("Q2 did not start after Q1 completed: %+v", todoNow(restarted, q2ID))
		}
	})
}

func TestTaskWorkspaceSerialQueueUnlinkReleasesWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceQueueService(t, nil)
		ctx := context.Background()
		owner, _ := s.createTodo(ctx, "Agent holder", "", "conv1", "")
		_ = s.linkTodoAgent(ctx, owner.ID, "box", "holder-sess", owner.Title, "/code/app", "")
		_, p := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
			"title":     "Waiter",
			"prompt":    "Wait for agent unlink",
			"workspace": map[string]any{"existing": "/code/app", "queue": true},
		})
		synctest.Wait()
		if len(f.starts) != 0 {
			t.Fatalf("waiter started early: %v", f.starts)
		}
		w := request(s, http.MethodDelete, "/api/v1/todos/"+owner.ID+"/agents/box/holder-sess", "")
		if w.Code != http.StatusOK {
			t.Fatalf("unlink agent = %d %s", w.Code, w.Body.String())
		}
		synctest.Wait()
		if len(f.starts) != 1 || len(todoNow(s, p.Tasks[0].TodoID).Agents) != 1 {
			t.Fatalf("waiter did not auto-start after agent unlink: starts=%d", len(f.starts))
		}
	})
}

func TestTaskWorkspaceSerialQueueI18nLocales(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		q := i18n.T(lang, "assistant-tasks_workspace.queued", i18n.Args{
			"id": "todo_a", "title": "Auth fix", "machine": "box", "path": "/code/app",
		})
		if q == "" || strings.Contains(q, "{") || !strings.Contains(q, "todo_a") || !strings.Contains(q, "Auth fix") {
			t.Fatalf("lang %s queued = %q", lang, q)
		}
		qa := i18n.T(lang, "assistant-tasks_workspace.queued_after", i18n.Args{
			"blockers": "todo_a, todo_b", "machine": "box", "path": "/code/app",
		})
		if qa == "" || strings.Contains(qa, "{") || !strings.Contains(qa, "todo_a, todo_b") {
			t.Fatalf("lang %s queued_after = %q", lang, qa)
		}
	}
}
