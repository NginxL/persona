package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func roleContractGeneralTask(role string) map[string]any {
	task := map[string]any{
		"title": "Role contract", "prompt": "Exercise the existing task dispatch path", "machine": "box", "cwd": "/code/app",
		"readonly": false, "permission": "bypassPermissions", "done_when": []string{"the existing dispatch path ran"},
		"why": "focused role contract test", "user_requested": false,
	}
	if role != "" {
		task["role"] = role
	}
	return task
}

func roleContractLeaderTask(role string) map[string]any {
	task := map[string]any{
		"title": "Restricted worker", "prompt": "Exercise the existing restricted dispatch path", "readonly": false,
		"permission": "bypassPermissions", "done_when": []string{"the restricted dispatch path ran"},
	}
	if role != "" {
		task["role"] = role
	}
	return task
}

func roleContractGeneralCall(t *testing.T, s *Service, self string, task map[string]any) proto.ToolResult {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"tasks": []any{task}})
	if err != nil {
		t.Fatal(err)
	}
	return s.mcpCall(context.Background(), "server", self, "task_plan", raw, true)
}

func roleContractStoredService(t *testing.T) (*Service, *fakeHub, *profileTaskStore) {
	t.Helper()
	st := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	s, f := newQuietServiceOn(t, st)
	return s, f, st
}

func roleContractStoredPlanCount(st *profileTaskStore) int {
	st.planMu.Lock()
	defer st.planMu.Unlock()
	return len(st.plans)
}

func roleContractStartGeneral(t *testing.T, s *Service, self, role, model string) (*TaskPlan, Todo) {
	t.Helper()
	task := roleContractGeneralTask(role)
	if model != "" {
		task["model"] = model
	}
	res := roleContractGeneralCall(t, s, self, task)
	if res.IsError {
		t.Fatalf("task_plan with explicit role %q was rejected: %s", role, res.Content[0].Text)
	}
	planID := planIDRe.FindString(res.Content[0].Text)
	if planID == "" {
		t.Fatalf("task_plan with explicit role %q returned no plan id: %+v", role, res)
	}
	w := request(s, http.MethodPost, "/api/v1/task-plans/"+planID+"/start", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("starting explicit %s: %d %s", role, w.Code, w.Body.String())
	}
	var body struct {
		Plan *TaskPlan `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Plan == nil || len(body.Plan.Tasks) != 1 {
		t.Fatalf("decoding the started %s plan: %+v, %v", role, body.Plan, err)
	}
	todoID := body.Plan.Tasks[0].TodoID
	waitFor(t, "the explicit "+role+" agent link", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	return body.Plan, todoNow(s, todoID)
}

// New general tasks must spell out their role. Rejection happens at the real MCP entry, before a stored plan, to-do,
// or Agent start exists. The unknown-role case is a control for the rejection path already present on the base.
func TestTaskPlanRoleContractRejectsNewGeneralTasksWithoutDurableEffects(t *testing.T) {
	for _, role := range []string{"", "boss"} {
		name := "missing"
		if role != "" {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			s, f, st := roleContractStoredService(t)
			res := roleContractGeneralCall(t, s, "conv1", roleContractGeneralTask(role))
			if !res.IsError || !strings.Contains(res.Content[0].Text, "role") {
				t.Fatalf("new general task with role %q was accepted or refused for the wrong reason: %+v", role, res)
			}
			if got := roleContractStoredPlanCount(st); got != 0 {
				t.Fatalf("refused role %q stored %d plans", role, got)
			}
			if got := todoCount(s); got != 0 {
				t.Fatalf("refused role %q created %d to-dos", role, got)
			}
			if got := len(f.started()); got != 0 {
				t.Fatalf("refused role %q started %d Agents", role, got)
			}
		})
	}
}

// Explicit worker keeps ordinary dispatch; an ordinary eligible conversation may still dispatch an explicit leader and
// supervises it itself. Deliberately misleading synthetic model ids cannot participate in either role decision.
func TestTaskPlanRoleContractExplicitDispatch(t *testing.T) {
	for _, tt := range []struct {
		role, model, source string
	}{
		{role: "worker", model: "synthetic-leader-named-71c4", source: "manual"},
		{role: "leader", model: "synthetic-worker-named-a905", source: sourceLeader},
	} {
		t.Run(tt.role, func(t *testing.T) {
			s, f := newQuietService(t)
			f.known["conv2"] = true
			f.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
			plan, todo := roleContractStartGeneral(t, s, "conv2", tt.role, tt.model)
			if got := plan.Tasks[0].Role; got != tt.role {
				t.Fatalf("stored role = %q, want %q", got, tt.role)
			}
			if todo.Origin != "conv2" || todo.Supervision == nil || todo.Supervision.Supervisor != "conv2" {
				t.Fatalf("the dispatching conversation no longer supervises its %s: origin %q, supervision %+v", tt.role, todo.Origin, todo.Supervision)
			}
			if len(todo.Agents) != 1 || todo.Agents[0].Source != tt.source || todo.Agents[0].Via != "" {
				t.Fatalf("explicit %s changed the existing agent link: %+v", tt.role, todo.Agents)
			}
			starts := f.started()
			if len(starts) != 1 || starts[0]["model"] != tt.model {
				t.Fatalf("the synthetic model was not preserved for explicit %s: %+v", tt.role, starts)
			}
		})
	}
}

// A task leader's restricted task_plan has the same explicit-role admission boundary, but its only valid role is
// worker. Each case gets a fresh, valid leader so a rejected child cannot be confused with the leader's own to-do.
func TestTaskLeaderRoleContractRestrictedTaskPlan(t *testing.T) {
	for _, role := range []string{"", "boss", "leader", "worker"} {
		name := role
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			s, f := newQuietService(t)
			ctx := context.Background()
			_, leader := roleContractStartGeneral(t, s, "conv1", "leader", "synthetic-restricted-parent-3e81")
			agent := leader.Agents[0]
			ref := leaderRef(agent.MachineID, agent.SessionID)
			res := s.mcpCall(ctx, agent.MachineID, agent.SessionID, "task_plan", mustJSON(map[string]any{
				"tasks": []any{roleContractLeaderTask(role)},
			}), false)
			if role != "worker" {
				if !res.IsError || !strings.Contains(res.Content[0].Text, "role") {
					t.Fatalf("restricted task_plan with role %q was accepted or refused for the wrong reason: %+v", role, res)
				}
				if got := len(todosOpenedBy(s, ref)); got != 0 {
					t.Fatalf("restricted role %q created %d child to-dos", role, got)
				}
				plans, err := s.conversationPlans(ctx, ref)
				if err != nil || len(plans) != 0 {
					t.Fatalf("restricted role %q stored child plans: %+v, %v", role, plans, err)
				}
				if got := len(f.started()); got != 1 {
					t.Fatalf("restricted role %q started a child Agent; total starts = %d", role, got)
				}
				return
			}
			if res.IsError {
				t.Fatalf("restricted explicit worker was rejected: %+v", res)
			}
			var child Todo
			waitFor(t, "the restricted explicit worker link", func() bool {
				children := todosOpenedBy(s, ref)
				if len(children) == 1 && len(children[0].Agents) == 1 {
					child = children[0]
					return true
				}
				return false
			})
			if child.Agents[0].Source != sourceWorker || child.Agents[0].Via != leader.ID ||
				child.Supervision == nil || child.Supervision.Supervisor != ref {
				t.Fatalf("restricted explicit worker changed dispatch: %+v, supervision %+v", child.Agents, child.Supervision)
			}
			plans, err := s.conversationPlans(ctx, ref)
			if err != nil || len(plans) != 1 || len(plans[0].Tasks) != 1 {
				t.Fatalf("restricted explicit worker plan: %+v, %v", plans, err)
			}
			if task := plans[0].Tasks[0]; task.Role != "worker" || task.Parent != leader.ID || task.TodoID != child.ID {
				t.Fatalf("restricted explicit worker was not stored beneath its leader: %+v", task)
			}
		})
	}
}

// This is old stored JSON, not a newly admitted plan: its omitted role remains readable and both stored-plan start and
// retry take the pre-existing ordinary-worker path.
func TestTaskPlanRoleContractLegacyStoredPlanStartAndRetry(t *testing.T) {
	ctx := context.Background()
	s, f, st := roleContractStoredService(t)
	now := time.Now().UTC()
	legacyJSON := json.RawMessage(`[{"title":"Legacy worker","prompt":"Start and retry the old stored task","machine":"box","cwd":"/code/app","readonly":false,"permission":"bypassPermissions","done_when":["old plan starts and retries"]}]`)
	if err := st.CreateTaskPlan(ctx, db.TaskPlanRow{
		ID: "plan_legacy_without_role", Conversation: "conv1", Tasks: legacyJSON, State: planOpen, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadPlan(ctx, "plan_legacy_without_role")
	if err != nil || len(loaded.Tasks) != 1 || loaded.Tasks[0].Role != "" {
		t.Fatalf("legacy stored plan is not readable with an empty role: %+v, %v", loaded, err)
	}
	if w := request(s, http.MethodGet, "/api/v1/task-plans/plan_legacy_without_role", ""); w.Code != http.StatusOK {
		t.Fatalf("reading legacy stored plan: %d %s", w.Code, w.Body.String())
	}
	w := request(s, http.MethodPost, "/api/v1/task-plans/plan_legacy_without_role/start", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("starting legacy stored plan: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Plan *TaskPlan `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || started.Plan == nil || len(started.Plan.Tasks) != 1 {
		t.Fatalf("decoding started legacy plan: %+v, %v", started.Plan, err)
	}
	if started.Plan.Tasks[0].Role != "" {
		t.Fatalf("starting rewrote the legacy task's role: %+v", started.Plan.Tasks[0])
	}
	todoID := started.Plan.Tasks[0].TodoID
	waitFor(t, "the legacy ordinary worker link", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	if agent := todoNow(s, todoID).Agents[0]; agent.Source != "manual" || agent.Via != "" {
		t.Fatalf("legacy start was not ordinary-worker dispatch: %+v", agent)
	}
	if err := s.failTodo(ctx, todoID, "synthetic failed start for legacy retry"); err != nil {
		t.Fatal(err)
	}
	if w := request(s, http.MethodPost, "/api/v1/todos/"+todoID+"/retry", `{}`); w.Code != http.StatusOK {
		t.Fatalf("retrying legacy stored plan: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, "the legacy ordinary worker retry", func() bool {
		return len(f.started()) == 2 && todoNow(s, todoID).DoneAt == nil
	})
	agents := todoNow(s, todoID).Agents
	if len(agents) == 0 {
		t.Fatal("legacy retry has no ordinary-worker agent link")
	}
	for _, agent := range agents {
		if agent.Source != "manual" || agent.Via != "" {
			t.Fatalf("legacy retry was not ordinary-worker dispatch: %+v", agents)
		}
	}
	stored, err := s.loadPlan(ctx, "plan_legacy_without_role")
	if err != nil || stored.Tasks[0].Role != "" {
		t.Fatalf("legacy retry rewrote or lost the empty role: %+v, %v", stored, err)
	}
}

// Role admission does not alter tier resolution. Both explicit roles, and the empty role found only in legacy stored
// data, resolve exactly the same arbitrary synthetic tier defaults through the existing tier function.
func TestTaskPlanRoleContractTierDefaultsUnchanged(t *testing.T) {
	ctx := context.Background()
	s, _, st := newProfileTaskService(t)
	const model = "synthetic-tier-default-6bf2"
	profiles := modelProfiles{Revision: 2, Profiles: []ModelProfile{{
		ID: "economy", Name: "Economy",
		Tiers: map[string]ModelTier{"standard": {Launcher: "claude", Model: model, Effort: "low"}},
	}}}
	if err := st.PutSetting(ctx, modelProfilesKey, profiles); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"worker", "leader", ""} {
		t.Run("role_"+role, func(t *testing.T) {
			task := PlanTask{Role: role, Tier: "standard"}
			if err := s.applyTaskTier(ctx, &task); err != nil {
				t.Fatal(err)
			}
			if task.Launcher != "claude" || task.Model != model || task.Effort != "low" || task.Tier != "standard" {
				t.Fatalf("role %q changed tier defaults: %+v", role, task)
			}
		})
	}
}

// Assert the schemas exposed to models directly, including the restricted leader tool under its runtime alias.
func TestTaskPlanRoleContractMCPSchemas(t *testing.T) {
	for _, tt := range []struct {
		name string
		tool proto.Tool
		enum []string
	}{
		{name: "general", tool: toolsNamed([]string{"task_plan"})[0], enum: []string{"worker", "leader"}},
		{name: "leader", tool: leaderPlanTool(), enum: []string{"worker"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var schema struct {
				Properties struct {
					Tasks struct {
						Items struct {
							Required   []string `json:"required"`
							Properties map[string]struct {
								Enum []string `json:"enum"`
							} `json:"properties"`
						} `json:"items"`
					} `json:"tasks"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tt.tool.InputSchema, &schema); err != nil {
				t.Fatalf("invalid %s task_plan schema: %v", tt.name, err)
			}
			item := schema.Properties.Tasks.Items
			if !slices.Contains(item.Required, "role") {
				t.Errorf("%s task_plan task schema does not require role: required=%v", tt.name, item.Required)
			}
			role, ok := item.Properties["role"]
			if !ok {
				t.Fatalf("%s task_plan task schema has no role property: %s", tt.name, tt.tool.InputSchema)
			}
			if len(role.Enum) != len(tt.enum) {
				t.Fatalf("%s task_plan role enum = %v, want %v", tt.name, role.Enum, tt.enum)
			}
			for _, value := range tt.enum {
				if !slices.Contains(role.Enum, value) {
					t.Errorf("%s task_plan role enum %v does not contain %q", tt.name, role.Enum, value)
				}
			}
		})
	}
}
