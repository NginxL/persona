package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func spawnFixture(t *testing.T, launcher, mode string) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietService(t)
	// Install metadata before any asynchronous dispatch starts using the hub.
	s.hub = &taskHandoffTestHub{fakeHub: f, info: map[string]proto.NodeInfo{"box": {Executable: "/test/persona-node"}}}
	todo := startTaskLeader(t, s, f)
	// An ordinary worker, not a task leader, may split sessions within its existing assignment.
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Agents[0].Source = "manual"
	s.todos.mu.Unlock()
	f.launcherList = []proto.Launcher{{Name: launcher, Agent: launcher}}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: launcher, Launcher: launcher,
		Cwd: "/code/app", PermissionMode: mode, Status: proto.StatusWorking})
	return s, f, todo
}

const spawnArgs = `{"title":"Check the regression","prompt":"Inspect the tests and return the command and result."}`

func TestTaskSpawnStaysOnCallersSupervisedTodo(t *testing.T) {
	s, f, before := spawnFixture(t, "codex", "plan")
	ctx := context.Background()
	tools := s.sessionMCPTools(ctx, "box", "s1", false, false)
	if !toolListed(tools, "task_spawn") || toolListed(tools, "task_plan") {
		t.Fatalf("ordinary task Agent's tools: %+v", tools)
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	after := onlyTodo(s)
	if after.ID != before.ID || after.Supervision.Supervisor != "conv1" || len(after.Agents) != 2 {
		t.Fatalf("spawn changed the task or its supervision: %+v", after)
	}
	a := after.Agents[1]
	if a.Source != "spawned" || a.Via != "box/s1" || a.SessionID != "s2" || out["session_id"] != a.SessionID ||
		out["todo_id"] != before.ID || out["supervisor"] != "conv1" || out["parent"] != "box/s1" {
		t.Fatalf("child link/receipt: %+v, %+v", a, out)
	}
	start := f.starts[len(f.starts)-1]
	if start["launcher"] != "codex" || start["cwd"] != "/code/app" || start["permission_mode"] != "plan" {
		t.Fatalf("child escaped the parent's machine/directory/permission: %+v", start)
	}
	prompt, _ := start["prompt"].(string)
	for _, guard := range []string{"task_spawn", "create_thread", "~/.persona/hub.json", "/api/v1", "never read"} {
		if !strings.Contains(prompt, guard) {
			t.Errorf("assignment lacks %q: %s", guard, prompt)
		}
	}
}

func TestTaskSpawnRefusesUnscopedArgumentsAndCallers(t *testing.T) {
	for _, args := range []string{
		`{}`, `null`, `[]`, `{"title":"x","prompt":" "}`, spawnArgs + `{}`,
		`{"title":"x","prompt":"x","todo_id":"another"}`,
		`{"title":"x","prompt":"x","machine":"other"}`,
		`{"title":"x","prompt":"x","session_id":"another"}`,
		`{"title":"x","prompt":"x","cwd":"/elsewhere"}`,
		`{"title":"x","prompt":"x","permission":"bypassPermissions"}`,
		`{"title":"x","prompt":"x","role":"leader"}`,
	} {
		t.Run(args, func(t *testing.T) {
			s, f, _ := spawnFixture(t, "codex", "plan")
			n := len(f.starts)
			res := s.mcpCall(context.Background(), "box", "s1", "task_spawn", json.RawMessage(args), false)
			if !res.IsError || len(f.starts) != n || len(onlyTodo(s).Agents) != 1 {
				t.Fatalf("invalid arguments started a session: %s %+v", res.Content, f.starts)
			}
		})
	}
	for _, state := range []string{"unlinked", "other-machine", "released", "done", "unsupervised"} {
		t.Run(state, func(t *testing.T) {
			s, f, todo := spawnFixture(t, "codex", "plan")
			mid, sid := "box", "s1"
			s.todos.mu.Lock()
			switch state {
			case "unlinked":
				sid = "unrelated"
			case "other-machine":
				mid = "other"
			case "released":
				delete(s.todos.activeBySession, [2]string{mid, sid})
			case "done":
				now := time.Now()
				s.todos.todos[todo.ID].DoneAt = &now
			case "unsupervised":
				s.todos.todos[todo.ID].Supervision = nil
			}
			s.todos.mu.Unlock()
			n := len(f.starts)
			if res := s.mcpCall(context.Background(), mid, sid, "task_spawn", json.RawMessage(spawnArgs), false); !res.IsError ||
				len(f.starts) != n || toolListed(s.sessionMCPTools(context.Background(), mid, sid, false, false), "task_spawn") {
				t.Fatalf("unscoped caller accepted: %+v", res)
			}
		})
	}
}

func TestTaskSpawnDoesNotElevatePermissions(t *testing.T) {
	for _, tc := range []struct{ launcher, mode, source, want string }{
		{"codex", "plan", "manual", "plan"},
		{"claude", "default", "manual", "default"},
		{"claude", "plan", "manual", "plan"},
		{"codex", "", "manual", "bypassPermissions"}, // the original assignment's persisted mode
		{"codex", "", "spawned", "plan"},             // an unknown child cannot borrow that assignment's mode
	} {
		t.Run(tc.launcher+"/"+tc.mode+"/"+tc.source, func(t *testing.T) {
			s, f, todo := spawnFixture(t, tc.launcher, tc.mode)
			s.todos.mu.Lock()
			s.todos.todos[todo.ID].Agents[0].Source = tc.source
			s.todos.mu.Unlock()
			res := s.mcpCall(context.Background(), "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
			if res.IsError {
				t.Fatal(res.Content)
			}
			if got := f.starts[len(f.starts)-1]["permission_mode"]; got != tc.want {
				t.Fatalf("child mode = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestTaskSpawnUsesOriginalAssignmentModelOnSameLauncher(t *testing.T) {
	s, f, todo := spawnFixture(t, "claude", "default")
	ctx := context.Background()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "claude", Launcher: "claude",
		Cwd: "/code/app", PermissionMode: "default", Model: "upstream/model-x", Status: proto.StatusWorking})
	if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) {
		task.Launcher, task.Model = "claude", "relay/model-x-1m"
	}); err != nil {
		t.Fatal(err)
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if got := f.starts[len(f.starts)-1]["model"]; got != "relay/model-x-1m" {
		t.Fatalf("child model = %v, want the original assignment's relay/model-x-1m", got)
	}
}

func TestTaskSpawnDoesNotUseAssignmentModelAfterLauncherSwitch(t *testing.T) {
	s, f, todo := spawnFixture(t, "codex", "default")
	ctx := context.Background()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Launcher: "codex",
		Cwd: "/code/app", PermissionMode: "default", Model: "upstream/model-x", Status: proto.StatusWorking})
	if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) {
		task.Launcher, task.Model = "claude", "relay/model-x-1m"
	}); err != nil {
		t.Fatal(err)
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if got := f.starts[len(f.starts)-1]["model"]; got != "upstream/model-x" {
		t.Fatalf("child model = %v, want the current launcher's reported upstream/model-x", got)
	}
}

func TestTaskSpawnUsesOriginalAssignmentModelWithDefaultLauncher(t *testing.T) {
	s, f, todo := spawnFixture(t, "claude", "default")
	ctx := context.Background()
	if got := s.startLauncher(ctx, ""); got != "claude" {
		t.Fatalf("fixture default launcher = %q, want claude for the empty-assignment-launcher case", got)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "claude", Launcher: "claude",
		Cwd: "/code/app", PermissionMode: "default", Model: "upstream/model-x", Status: proto.StatusWorking})
	if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) {
		task.Launcher, task.Model = "", "relay/model-x-1m"
	}); err != nil {
		t.Fatal(err)
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if got := f.starts[len(f.starts)-1]["model"]; got != "relay/model-x-1m" {
		t.Fatalf("child model = %v, want the default-launcher assignment's relay/model-x-1m", got)
	}
}

func TestTaskSpawnNonAssignmentAgentKeepsReportedModel(t *testing.T) {
	s, f, todo := spawnFixture(t, "claude", "default")
	ctx := context.Background()
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Agents[0].Source = "spawned"
	s.todos.mu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "claude", Launcher: "claude",
		Cwd: "/code/app", PermissionMode: "default", Model: "upstream/model-x", Status: proto.StatusWorking})
	if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) {
		task.Launcher, task.Model = "claude", "relay/model-x-1m"
	}); err != nil {
		t.Fatal(err)
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if got := f.starts[len(f.starts)-1]["model"]; got != "upstream/model-x" {
		t.Fatalf("child model = %v, want the parent's unchanged upstream/model-x", got)
	}
}

func TestTaskSpawnAmbiguousContinuationAssignmentStaysReadOnly(t *testing.T) {
	s, f, todo := spawnFixture(t, "codex", "")
	ctx := context.Background()
	if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) { task.TodoID = "" }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"continuation-a", "continuation-b"} {
		plan := &TaskPlan{ID: id, Conversation: todo.Origin, State: planStarted,
			Tasks: []PlanTask{{TodoID: todo.ID, Machine: "box", Permission: "bypassPermissions"}}}
		if err := s.savePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if got := f.starts[len(f.starts)-1]["permission_mode"]; got != "plan" {
		t.Fatalf("ambiguous current assignments elevated the child to %v", got)
	}
	if _, _, _, err := s.remedyOf(ctx, todo.ID); !errors.Is(err, errTaskNotInPlan) {
		t.Fatalf("ambiguous current assignments did not fail retry/remedy closed: %v", err)
	}
}

func TestTaskSpawnDoesNotWidenWorkTrustTools(t *testing.T) {
	s, f, _ := spawnFixture(t, "codex", "plan")
	delegationMachineTrust(t, s, "box", "work")
	ctx := context.Background()
	n := len(f.starts)
	if res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false); !res.IsError ||
		len(f.starts) != n || toolListed(s.sessionMCPTools(ctx, "box", "s1", false, false), "task_spawn") {
		t.Fatalf("work-trust dispatch was not refused: %+v", res)
	}
}

func TestTaskSpawnFailureDoesNotLeaveAnUntrackedChild(t *testing.T) {
	for _, fail := range []string{"start", "task-closed"} {
		t.Run(fail, func(t *testing.T) {
			s, f, todo := spawnFixture(t, "codex", "plan")
			if fail == "start" {
				f.failStart = errors.New("start refused")
			} else {
				f.onStart = func(_ *fakeHub, _ map[string]any) {
					s.todos.mu.Lock()
					now := time.Now()
					s.todos.todos[todo.ID].DoneAt = &now
					s.todos.mu.Unlock()
				}
			}
			res := s.mcpCall(context.Background(), "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
			if !res.IsError || len(onlyTodo(s).Agents) != 1 {
				t.Fatalf("failed start/link not reported: %+v", res)
			}
			if fail == "task-closed" && !strings.Contains(res.Content[0].Text, "was not linked") {
				t.Fatalf("child cleanup not reported: %+v", res)
			}
			if fail == "task-closed" {
				stopped := false
				for _, call := range f.calls {
					if call.Method == deviceSessionStop && call.Device == "box" {
						stopped = true
					}
				}
				if !stopped {
					t.Fatal("unlinked child was not stopped")
				}
			}
		})
	}
}

func TestNativeSidecarAncestryAloneDoesNotLinkATodo(t *testing.T) {
	s, _, todo := spawnFixture(t, "codex", "plan")
	s.autoLinkTodos("box", []proto.Session{{SessionID: "sidecar", Agent: "codex", IsSubagent: true,
		ParentSessionID: "s1", RootSessionID: "s1", Cwd: "/code/app"}})
	if len(onlyTodo(s).Agents) != 1 {
		t.Fatal("display ancestry silently became task membership")
	}
	s.autoLinkTodos("box", []proto.Session{{SessionID: "tracked", Agent: "codex",
		SpawnFrom: todo.ID + "@box/s1", Cwd: "/code/app"}})
	if a := onlyTodo(s).Agents; len(a) != 2 || a[1].Source != "spawned" || a[1].Via != "box/s1" {
		t.Fatalf("explicit spawned marker was not attributed: %+v", a)
	}
}

func TestGoldenTaskHandoff(t *testing.T) {
	golden(t, "task_handoff", prompts.RenderTaskHandoff(prompts.TaskHandoff{MCP: true}))
}
