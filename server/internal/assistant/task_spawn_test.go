package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func spawnFixture(t *testing.T, launcher, mode string) (*Service, *fakeHub, Todo) {
	t.Helper()
	return spawnFixtureOn(t, launcher, mode, nil)
}

func spawnFixtureOn(t *testing.T, launcher, mode string, store Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, store)
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

func spawnKBFixture(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.hello.KB = &KBSettings{}
	s := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t), KB: &KBOptions{Dir: t.TempDir()}}))
	t.Cleanup(s.kbEnd)
	shortSupervise(t, s)
	f.hello.Assistant.MachineID = "server"
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	s.hub = &taskHandoffTestHub{fakeHub: f, info: map[string]proto.NodeInfo{"box": {Executable: "/test/persona-node"}}}
	todo := startTaskLeader(t, s, f)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Agents[0].Source = "manual"
	s.todos.mu.Unlock()
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Launcher: "codex",
		Cwd: "/code/app", PermissionMode: "plan", Status: proto.StatusWorking})
	return s, f, todo
}

func TestTaskSpawnExplicitProjectAndDirectionBeatForeignCwd(t *testing.T) {
	for _, ownership := range []string{"todo", "direction"} {
		t.Run(ownership, func(t *testing.T) {
			s, f, todo := spawnKBFixture(t)
			ctx := context.Background()
			e := kbEng(t, s)
			if _, err := e.Apply(kb.KindProject, "explicit", kb.Update{Kind: "long"}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Apply(kb.KindProject, "cwd-project", kb.Update{Dirs: []string{"box:/code/app"}}); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.PutSummary(kb.KindProject, "explicit", "Explicit direction summary"); err != nil {
				t.Fatal(err)
			}
			if err := e.AppendProjectMemory("explicit", kb.ProjectMemoryRecord{ID: "before/outcome", Type: kb.ProjectMemoryOutcome,
				Conclusion: "Explicit direction outcome", Verification: kb.ProjectMemoryAgentReported, RecordedAt: "2026-11-20T01:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			if err := e.AppendProjectMemory("explicit", kb.ProjectMemoryRecord{ID: "before/decision", Type: kb.ProjectMemoryDecision,
				Question: "Which project?", Decision: "Use the explicit direction", Reason: "Ownership is explicit",
				Verification: kb.ProjectMemoryAgentReported, RecordedAt: "2026-11-20T01:01:00Z"}); err != nil {
				t.Fatal(err)
			}
			if err := e.AppendProjectMemory("explicit", kb.ProjectMemoryRecord{ID: "before/pitfall", Type: kb.ProjectMemoryPitfall,
				Conclusion: "Do not infer ownership from cwd", Verification: kb.ProjectMemoryAgentReported}); err != nil {
				t.Fatal(err)
			}
			p, _ := e.Store.Project("cwd-project")
			p.OneLine = "Wrong cwd-only project"
			if err := e.Store.PutProject(p); err != nil {
				t.Fatal(err)
			}
			if ownership == "todo" {
				s.todos.mu.Lock()
				s.todos.todos[todo.ID].Project = "explicit"
				s.todos.mu.Unlock()
			} else {
				setSettings(t, s, func(v *Settings) { v.Main = "main"; v.Leaders = []string{"conv1"} })
				s.directions.mu.Lock()
				if !s.directionsLocked(ctx) {
					t.Fatal("directions unreadable")
				}
				s.directions.m["conv1"] = &direction{Project: "explicit", By: directionByUser}
				s.directions.mu.Unlock()
			}
			f.hello.Locale = "en"
			res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
			if res.IsError {
				t.Fatal(res.Content)
			}
			start := f.starts[len(f.starts)-1]
			prompt, _ := start["prompt"].(string)
			for _, want := range []string{"Explicit direction summary", "Explicit direction outcome", "Use the explicit direction",
				"Ownership is explicit", "Do not infer ownership from cwd", "agent_reported", "not supervisor-accepted"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("first prompt lacks %q: %s", want, prompt)
				}
			}
			if strings.Contains(prompt, "Wrong cwd-only project") || start["cwd"] != "/code/app" {
				t.Fatalf("cwd overrode explicit ownership or was changed: %+v", start)
			}

			after := onlyTodo(s)
			child := after.Agents[1]
			f.outcome = `{"outcome":"child finished","decisions":[{"question":"Where does this belong?","choice":"Still the explicit project","reason":"The child inherited it"}],"pitfalls":["Foreign cwd must not win"]}`
			s.recordWorkerOutcome(ctx, after, "box", child.SessionID, "/code/app", 9, 1, "", true, "child finished", nil)
			records := e.ProjectMemory("explicit", 30)
			found := false
			for _, record := range records {
				if record.Conclusion == "child finished" && record.Agent == child.SessionID {
					found = true
				}
			}
			if !found {
				t.Fatalf("child outcome did not reach the explicit project: %+v", records)
			}
			if got := e.ProjectMemory("cwd-project", 30); len(got) != 0 {
				t.Fatalf("foreign cwd project received the child outcome: %+v", got)
			}
		})
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

// Exercise the supervised call through the node start, including the distinction between a
// missing requested_model on an older node and an explicit launcher default on a newer one.
func TestTaskSpawnStartsWithTheParentsLaunchModel(t *testing.T) {
	const reply = "claude-opus-5-5"
	requested, launcherDefault := "relay/model-x-1m", ""
	row := proto.AgentModel{ID: "relay/model-x-1m", Resolved: reply}
	var gatewayRow proto.AgentModel
	if err := json.Unmarshal([]byte(`{"id":"relay/model-x-1m","resolved":"claude-opus-5-5","resolved_offered":true}`), &gatewayRow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		requested         *string
		assignment        string
		assignmentDefault bool
		spawned           bool
		catalog           []proto.AgentModel
		want              string
	}{
		{name: "node records the requested id", requested: &requested, want: requested},
		{name: "node records the launcher default", requested: &launcherDefault},
		{name: "older node uses the assignment launcher default", assignmentDefault: true},
		{name: "spawned agent keeps its own request", requested: &requested, assignment: "relay/someone-else", spawned: true, want: requested},
		{name: "original assignment wins", requested: &requested, assignment: "relay/planned", want: "relay/planned"},
		{name: "older node reports a unique resolution", catalog: []proto.AgentModel{row}, want: row.ID},
		{name: "gateway independently lists the exact reply", catalog: []proto.AgentModel{gatewayRow}, want: reply},
		{name: "older node reports an offered id", catalog: []proto.AgentModel{row, {ID: reply}}, want: reply},
		{name: "two routes resolve to the same reply", catalog: []proto.AgentModel{row, {ID: "relay/other", Resolved: reply}}, want: reply},
		{name: "two context windows resolve to the same reply", catalog: []proto.AgentModel{row, {ID: row.ID + "[1m]", Resolved: reply}}, want: reply},
		{name: "dynamic tier is not an exact source", catalog: []proto.AgentModel{{ID: "opusplan", Resolved: reply}}, want: reply},
		{name: "only native default is reported", catalog: []proto.AgentModel{{ID: "default", Resolved: reply, Default: true}}, want: reply},
		{name: "model list unknown", want: reply},
		{name: "model absent from the list", catalog: []proto.AgentModel{{ID: "unrelated"}}, want: reply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := spawnFixtureOn(t, "claude", "plan", &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}})
			ctx := context.Background()
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", Model: "an-unrelated-task-default", Effort: "low"})
			if tc.spawned {
				s.todos.mu.Lock()
				s.todos.todos[todo.ID].Agents[0].Source = "spawned"
				s.todos.mu.Unlock()
			}
			f.agentModels = map[string][]proto.AgentModel{"claude": tc.catalog}
			f.setSessions("box", proto.Session{SessionID: "s1", Agent: "claude", Launcher: "claude", Cwd: "/code/app",
				PermissionMode: "plan", Model: reply, RequestedModel: tc.requested, Effort: "high", Status: proto.StatusWorking})
			if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(task *PlanTask) {
				task.Launcher, task.Model = "claude", tc.assignment
				task.LauncherDefaultModel = tc.assignmentDefault
			}); err != nil {
				t.Fatal(err)
			}
			res := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
			if res.IsError {
				t.Fatal(res.Content)
			}
			start := f.starts[len(f.starts)-1]
			if got, _ := start["model"].(string); got != tc.want || start["launcher"] != "claude" || start["permission_mode"] != "plan" {
				t.Fatalf("child model=%q launcher=%v permissions=%v; want %q, claude, plan", got, start["launcher"], start["permission_mode"], tc.want)
			}
			if tc.assignmentDefault || tc.requested == &launcherDefault {
				want := map[string]any{}
				if err := s.finishModel(ctx, "agent.new", want, ModelBinding{Provider: "launcher.claude", Effort: "high"}); err != nil {
					t.Fatal(err)
				}
				ordinary := map[string]any{"launcher": "claude", "effort": "high"}
				if err := s.applyModel(ctx, "agent.new", ordinary); err != nil {
					t.Fatal(err)
				}
				if start["model"] != nil || start["effort"] != "high" || start["model_fingerprint"] != want["model_fingerprint"] ||
					ordinary["model"] != "an-unrelated-task-default" || ordinary["model_fingerprint"] == start["model_fingerprint"] {
					t.Fatalf("launcher default start %+v, expected fingerprint %v; ordinary start %+v", start, want["model_fingerprint"], ordinary)
				}
			}
		})
	}
}

func TestTaskSpawnKeepsExactGatewayID(t *testing.T) {
	const name, reply, route = "test-relay", "upstream/model-x", "relay/model-x-1m"
	models := []proto.ProviderModel{{ID: reply}}
	presetModels := []extwire.ProviderModel{{ID: reply}}
	for _, tc := range []struct {
		name, kind, presetKind string
		models                 []proto.ProviderModel
	}{
		{name: "provider catalog", kind: "launcher", models: models},
		{name: "Messages API catalog", kind: "api", models: models},
		{name: "associated launcher preset", kind: "launcher", presetKind: "launcher"},
		{name: "associated Messages API preset", kind: "api", presetKind: "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, f, todo := spawnFixtureOn(t, name, "plan", &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}})
			provider := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: name, Kind: tc.kind, API: "messages",
				BaseURL: "https://relay.example.invalid", Models: tc.models}}
			if tc.presetKind != "" {
				provider.Preset = "plugin/relay"
				f.presets = []LauncherPreset{{ID: provider.Preset, Kind: tc.presetKind, API: "messages", Models: presetModels}}
			}
			putLaunchers(t, s, provider)
			f.agentModels = map[string][]proto.AgentModel{name: {{ID: route, Resolved: reply}}}
			f.setSessions("box", proto.Session{SessionID: "s1", Agent: "claude", Launcher: name, Cwd: "/code/app",
				PermissionMode: "plan", Host: "herdr", Model: reply, Status: proto.StatusWorking})
			if err := s.patchPlanTask(ctx, todo.PlanID, 0, func(p *PlanTask) { p.Launcher, p.Model = name, "" }); err != nil {
				t.Fatal(err)
			}
			result := s.mcpCall(ctx, "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
			if result.IsError {
				t.Fatal(result.Content)
			}
			start := f.starts[len(f.starts)-1]
			if start["model"] != reply || start["launcher"] != name || start["permission_mode"] != "plan" {
				t.Fatalf("session.start model=%v, launcher=%v, permission=%v", start["model"], start["launcher"], start["permission_mode"])
			}
		})
	}
}

func TestTaskSpawnReadsOnlyTheParentsCatalog(t *testing.T) {
	const launcher, reply, route = "relay", "upstream/model-x", "relay/model-x"
	requested, launcherDefault := "explicit/model", ""
	for _, tc := range []struct {
		name, host, defaultHost, source, reported, assigned, want string
		requested                                                 *string
		exact                                                     bool
		reads                                                     int
	}{
		{name: "assignment", reported: reply, requested: &requested, assigned: "plan/model", want: "plan/model"},
		{name: "requested", reported: reply, requested: &requested, want: requested},
		{name: "launcher default", reported: reply, requested: &launcherDefault},
		{name: "no model"},
		{name: "exact node row on cold fleet", host: "herdr", exact: true, reported: reply, want: reply, reads: 1},
		{name: "shell provider", host: "herdr", reported: reply, want: reply, reads: 1},
		{name: "runtime owns the same name", host: "paseo", reported: reply, want: route, reads: 1},
		{name: "default engine owns the same name", defaultHost: "paseo", reported: reply, want: route, reads: 1},
		{name: "runtime source without a host", source: proto.SourceRuntime, reported: reply, want: route, reads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietServiceOn(t, &memStore{})
			f.devices = []Device{{ID: "box", Online: true, Engine: tc.defaultHost}, {ID: "other", Online: true}}
			if !tc.exact {
				putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: launcher, Model: reply}})
			}
			rows := []proto.AgentModel{{ID: route, Resolved: reply}}
			if tc.exact {
				rows = append(rows, proto.AgentModel{ID: reply})
			}
			f.agentModels = map[string][]proto.AgentModel{launcher: rows}
			parent := proto.Session{Launcher: launcher, Model: tc.reported, RequestedModel: tc.requested, Host: tc.host, SourceKind: tc.source}
			before := f.callCount()
			got := s.taskSpawnModel(t.Context(), "box", parent, &PlanTask{Launcher: launcher, Model: tc.assigned})
			if got != tc.want {
				t.Fatalf("model %q, want %q", got, tc.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			reads := 0
			for _, call := range f.calls[before:] {
				if call.Method == DeviceLaunchersList || call.Device == "other" {
					t.Fatalf("unexpected inventory read on %s: %s", call.Device, call.Method)
				}
				if call.Method == assistantwire.DeviceLaunchersModels {
					reads++
					var args struct {
						Name, Host string
						Refresh    bool
					}
					if err := json.Unmarshal(call.Params, &args); err != nil || call.Device != "box" || args.Name != launcher || args.Host != tc.host || args.Refresh {
						t.Fatalf("wrong catalog request: %+v", call)
					}
				}
			}
			if reads != tc.reads {
				t.Fatalf("target model reads %d, want %d", reads, tc.reads)
			}
		})
	}
}

func TestTaskSpawnUsesOnlyDeclarationsOnTheParentsMachine(t *testing.T) {
	const reply, route, gateway = "upstream/model-x", "relay/model-x", "https://relay.example.invalid"
	ctx := t.Context()
	s, f := newQuietServiceOn(t, &memStore{})
	f.devices = []Device{{ID: "box", Online: true}, {ID: "other", Online: true}}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "scoped", Model: reply},
		Share: shareSelected, ScopeMachines: []string{"other"}})
	if err := s.putSetting(ctx, launchersZshrcKey, map[string][]proto.Launcher{
		"box":   {{Name: "shell", Source: "zshrc", BaseURL: gateway + "/v1"}, {Name: "wrapper", Source: "path", Model: reply}},
		"other": {{Name: "other-shell", Source: "zshrc", BaseURL: gateway}},
	}); err != nil {
		t.Fatal(err)
	}
	f.presets = []LauncherPreset{{ID: "plugin/gw", Kind: "launcher", BaseURL: gateway, Models: []extwire.ProviderModel{{ID: reply}}}}
	if err := s.putSetting(ctx, launchersCustomModelsKey, []customModel{
		{Machine: "box", Launcher: "remembered", Model: reply}, {Machine: "other", Launcher: "other-pick", Model: reply},
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"remembered": reply, "shell": reply, "wrapper": reply,
		"scoped": route, "other-shell": route, "other-pick": route, "unknown": route} {
		t.Run(name, func(t *testing.T) {
			f.agentModels = map[string][]proto.AgentModel{name: {{ID: route, Resolved: reply}}}
			got := s.taskSpawnModel(ctx, "box", proto.Session{Launcher: name, Model: reply, Host: "herdr"}, nil)
			if got != want {
				t.Fatalf("model %q, want %q for box/%s", got, want, name)
			}
		})
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
