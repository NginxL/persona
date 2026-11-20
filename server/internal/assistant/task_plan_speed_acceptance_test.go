package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func newSpeedProfileService11141(t *testing.T, tierSpeed string) (*Service, *fakeHub, *profileTaskStore) {
	t.Helper()
	s, f, st := newProfileTaskService(t)
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}, {Name: "claude", Agent: "claude"}, {Name: "agy", Agent: "agy"}}
	saveTiers(t, s, map[string]ModelTier{"standard": speedTier11141(t,
		ModelTier{Launcher: "codex", Model: "test-standard", Effort: "high"}, tierSpeed)})
	return s, f, st
}

func speedNodeStart11141(t *testing.T, call DeviceCall) map[string]any {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal(call.Params, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestTaskPlanSpeedAtPlanAndStart11141(t *testing.T) {
	for _, when := range []string{"plan", "start"} {
		for _, tc := range []struct {
			name, speed, tierSpeed, wantSpeed, wantLauncher, wantModel string
			override                                                   PlanTask
		}{
			{name: "preset", tierSpeed: "fast", wantSpeed: "fast", wantLauncher: "codex", wantModel: "test-standard"},
			{name: "explicit wins over preset", speed: "ultrafast", tierSpeed: "fast", wantSpeed: "ultrafast", wantLauncher: "codex", wantModel: "test-standard"},
			{name: "standard overrides preset without an override", speed: "standard", tierSpeed: "fast", wantLauncher: "codex", wantModel: "test-standard"},
			{name: "another model leaves preset speed behind", tierSpeed: "fast", override: PlanTask{Model: "test-custom"}, wantLauncher: "codex", wantModel: "test-custom"},
			{name: "another launcher leaves preset speed behind", tierSpeed: "fast", override: PlanTask{Launcher: "claude"}, wantLauncher: "claude"},
			{name: "explicit speed follows another launcher", speed: "fast", tierSpeed: "ultrafast", override: PlanTask{Launcher: "claude"}, wantSpeed: "fast", wantLauncher: "claude"},
			{name: "omitted preserves launcher default", wantLauncher: "codex", wantModel: "test-standard"},
		} {
			t.Run(when+"/"+tc.name, func(t *testing.T) {
				s, f, st := newSpeedProfileService11141(t, tc.tierSpeed)
				task := profilePlanTask()
				body := ""
				if when == "plan" {
					task.Launcher, task.Model = tc.override.Launcher, tc.override.Model
					task = speedTask11141(t, task, tc.speed)
				} else {
					override := speedTask11141(t, tc.override, tc.speed)
					raw, err := json.Marshal(map[string]any{"tasks": []PlanTask{override}})
					if err != nil {
						t.Fatal(err)
					}
					body = string(raw)
				}
				p := dispatchProfilePlan(t, s, st, task)
				if calls := profileNodeStarts(f); len(calls) != 0 {
					t.Fatalf("pending plan started agents: %v", calls)
				}
				if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", body); w.Code != http.StatusOK {
					t.Fatalf("start: %d %s", w.Code, w.Body)
				}
				waitFor(t, "the task dispatch", func() bool {
					return len(profileNodeStarts(f)) != 0 || onlyTodo(s).StartError != ""
				})
				if todo := onlyTodo(s); todo.StartError != "" || len(todo.Agents) != 1 {
					t.Fatalf("task failed: %+v", todo)
				}
				calls := profileNodeStarts(f)
				if len(calls) != 1 {
					t.Fatalf("session.start calls: %v", calls)
				}
				params := speedNodeStart11141(t, calls[0])
				if got, _ := params["launcher"].(string); got != tc.wantLauncher {
					t.Errorf("launcher = %q, want %q: %v", got, tc.wantLauncher, params)
				}
				if got, _ := params["model"].(string); got != tc.wantModel {
					t.Errorf("model = %q, want %q: %v", got, tc.wantModel, params)
				}
				if got, _ := params["speed"].(string); got != tc.wantSpeed {
					t.Errorf("speed = %q, want %q: %v", got, tc.wantSpeed, params)
				}
			})
		}
	}
}

func TestTaskPlanCardEditRetainsSpeed11141(t *testing.T) {
	s, f, st := newSpeedProfileService11141(t, "")
	task := speedTask11141(t, profilePlanTask(), "ultrafast")
	p := dispatchProfilePlan(t, s, st, task)
	if got := speedField11141(t, p.Tasks[0]); got != "ultrafast" {
		t.Errorf("plan lost its task's speed: %+v", p.Tasks[0])
	}
	w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", `{"tasks":[{"title":"Renamed without a speed chip"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the edited task dispatch", func() bool { return len(profileNodeStarts(f)) == 1 })
	params := speedNodeStart11141(t, profileNodeStarts(f)[0])
	if params["speed"] != "ultrafast" {
		t.Errorf("an edit that omitted speed cleared it: %v", params)
	}
	stored, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := speedField11141(t, stored.Tasks[0]); got != "ultrafast" {
		t.Errorf("stored plan lost speed after the edit: %+v", stored.Tasks[0])
	}
}

func TestTaskPlanUnsupportedSpeedRefusedAtPlanAndStart11141(t *testing.T) {
	for _, tc := range []struct{ name, launcher, speed string }{
		{name: "unsupported agent", launcher: "agy", speed: "fast"},
		{name: "Claude ultrafast", launcher: "claude", speed: "ultrafast"},
		{name: "invalid speed", launcher: "codex", speed: "ludicrous"},
	} {
		t.Run("plan/"+tc.name, func(t *testing.T) {
			s, f, st := newSpeedProfileService11141(t, "")
			task := profilePlanTask()
			task.Tier, task.Launcher = "", tc.launcher
			task = speedTask11141(t, task, tc.speed)
			res := profileMCPCall(t, s, "main", "task_plan", map[string]any{"tasks": []PlanTask{task}}, true, false)
			if !res.IsError {
				t.Fatalf("task_plan accepted %s %s: %+v", tc.launcher, tc.speed, res)
			}
			st.planMu.Lock()
			plans := len(st.plans)
			st.planMu.Unlock()
			if plans != 0 || onlyTodo(s).ID != "" || len(profileNodeStarts(f)) != 0 {
				t.Fatalf("refusal planned or started work: plans %d todo %+v starts %v", plans, onlyTodo(s), profileNodeStarts(f))
			}
		})
		t.Run("start/"+tc.name, func(t *testing.T) {
			s, f, st := newSpeedProfileService11141(t, "")
			task := profilePlanTask()
			initialSpeed := tc.speed
			if tc.name == "invalid speed" {
				initialSpeed = ""
			}
			p := dispatchProfilePlan(t, s, st, speedTask11141(t, task, initialSpeed))
			edit := PlanTask{Launcher: tc.launcher}
			if tc.name == "invalid speed" {
				edit.Launcher = ""
			}
			edit = speedTask11141(t, edit, tc.speed)
			raw, err := json.Marshal(map[string]any{"tasks": []PlanTask{edit}})
			if err != nil {
				t.Fatal(err)
			}
			w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", string(raw))
			if w.Code != http.StatusBadRequest {
				if w.Code == http.StatusOK {
					waitFor(t, "the incorrectly accepted dispatch", func() bool { return len(profileNodeStarts(f)) != 0 })
				}
				t.Fatalf("unsupported start edit: %d %s", w.Code, w.Body)
			}
			current, err := s.loadPlan(context.Background(), p.ID)
			if err != nil || current.State != planOpen || onlyTodo(s).ID != "" || len(profileNodeStarts(f)) != 0 {
				t.Fatalf("refusal moved or started the plan: %+v todo %+v starts %v err %v", current, onlyTodo(s), profileNodeStarts(f), err)
			}
		})
	}
}

func TestLeaderTaskPlanSpeedParserAndResponse11141(t *testing.T) {
	s, f := newQuietService(t)
	f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}
	startTaskLeader(t, s, f)
	before := len(startsOf(f))
	res := s.mcpCall(context.Background(), "box", "s1", "task_plan", workerArgs(map[string]any{"launcher": "codex", "speed": "fast"}), false)
	if res.IsError {
		t.Fatalf("restricted leader task_plan refused speed: %s", res.Content[0].Text)
	}
	if len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "Queued 1 worker") {
		t.Fatalf("leader response: %+v", res)
	}
	waitFor(t, "the leader's worker start", func() bool { return len(startsOf(f)) == before+1 })
	st := startsOf(f)[before]
	if st["launcher"] != "codex" || st["speed"] != "fast" {
		t.Fatalf("worker session.start: %v", st)
	}
}

func TestStartDefaultsPresetSpeedAndCallerOverride11141(t *testing.T) {
	for _, tc := range []struct{ name, callerSpeed, wantSpeed string }{
		{name: "policy selected preset", wantSpeed: "fast"},
		{name: "caller speed wins", callerSpeed: "ultrafast", wantSpeed: "ultrafast"},
		{name: "caller standard wins without an override", callerSpeed: "standard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			f.zshrc["box"] = []proto.Launcher{{Name: "gateway", Source: "zshrc", Agent: "codex"}}
			saveTiers(t, s, map[string]ModelTier{"deep": speedTier11141(t,
				ModelTier{Launcher: "gateway", Model: "gw-deep", Effort: "high"}, "fast")})
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
				return StartDefaults{Presets: []PresetChoice{{Name: "deep"}}, By: "Example"}, true
			})
			task := speedTask11141(t, defaultsTask(), tc.callerSpeed)
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			starts := startsOf(f)
			if len(starts) != 1 || starts[0]["launcher"] != "gateway" || starts[0]["model"] != "gw-deep" {
				t.Fatalf("policy preset start: %v", starts)
			}
			if got, _ := starts[0]["speed"].(string); got != tc.wantSpeed {
				t.Errorf("session.start speed = %q, want %q: %v", got, tc.wantSpeed, starts[0])
			}
		})
	}
}

func TestStartDefaultsPresetSpeedNotStoredOnRetry11141(t *testing.T) {
	s, f := defaultsHub(t)
	f.zshrc["box"] = []proto.Launcher{{Name: "gateway", Source: "zshrc", Agent: "codex"}}
	saveTiers(t, s, map[string]ModelTier{"deep": speedTier11141(t,
		ModelTier{Launcher: "gateway", Model: "gw-deep", Effort: "high"}, "fast")})
	policy := true
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "deep"}}, By: "Example"}, policy
	})
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Fix the build","prompt":"fix the build",
		"machine":"box","cwd":"/Users/me/code/app","why":"the repo is on box","permission":"bypassPermissions",
		"done_when":["CI is green"],"expect_minutes":30}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the policy start", func() bool { return len(startsOf(f)) == 1 })
	if got := startsOf(f)[0]["speed"]; got != "fast" {
		t.Errorf("policy preset speed = %v, want fast: %v", got, startsOf(f)[0])
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the agent linked", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	waitFor(t, "the start bookkeeping", func() bool { return s.taskStarting.Load() == 0 })
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored := p.Tasks[0]
	if got := speedField11141(t, stored); got != "" {
		t.Errorf("policy speed became the stored caller's speed: %+v", stored)
	}
	policy = false
	if _, _, err := s.startTaskAgent(ctx, &stored, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; !unset(st["speed"]) || st["launcher"] != "gateway" || st["model"] != "gw-model" {
		t.Errorf("retry without the policy kept its speed or preset: %v", st)
	}
}

func TestTaskPresetSpeedDroppedWithLauncherFallback11141(t *testing.T) {
	s, f := launcherFitHub(t)
	saveTiers(t, s, map[string]ModelTier{"deep": speedTier11141(t,
		ModelTier{Launcher: "gateway", Model: "gw-deep", Effort: "high"}, "fast")})
	task := defaultsTask()
	task.Machine, task.Cwd, task.Tier = "l4", "/home/me/code/app", "deep"
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "claude" || !unset(st["speed"]) {
		t.Fatalf("preset speed survived its launcher's fallback: %v", st)
	}
	if got := speedField11141(t, task.storedLaunch()); got != "" {
		t.Fatalf("fallback speed became caller-owned: %+v", task.storedLaunch())
	}
}
