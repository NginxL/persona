package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A failed start because of its launcher on that computer suggests the other launchers there, and starts again on one
// only when someone asks (task_retry's launcher, the app's "Use <launcher> instead"), in the same to-do and workspace
// (#10365). Machine and launcher names are placeholders.

// launcherRetryHub is a service whose computer box has Claude Code and the launchers work and work2 (Claude Code) and
// cx (Codex), with the launcher health records on.
func launcherRetryHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "box", Home: "/Users/me", Online: true, Reachable: true, ClaudeVersion: "2.1.300"},
	}
	f.launcherList = []proto.Launcher{{Name: "work"}, {Name: "work2"}, {Name: "cx", Agent: "codex"}}
	f.checks = map[string]proto.LauncherCheck{}
	f.hello.Agents.LauncherHealth.Enabled = new(true)
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	return newStorelessService(t, f), f
}

// failOnWork plans a read-only task on box with launcher work and a model of its own, which the terminal cannot run:
// the task fails to start, and its plan keeps why.
func failOnWork(t *testing.T, s *Service, f *fakeHub, failure error) (Todo, *TaskPlan) {
	t.Helper()
	f.mu.Lock()
	f.failStart = failure
	f.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "Inspect", "prompt": "Inspect the logs", "machine": "box",
		"cwd": "/code/app", "launcher": "work", "model": "glm-4.6", "readonly": true, "why": "the logs are on box"}}})
	if _, err := s.toolTaskPlan(context.Background(), "conv1", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start to fail", func() bool { return onlyTodo(s).StartError != "" && s.taskStarting.Load() == 0 })
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	todo := onlyTodo(s)
	_, p, _, err := s.remedyOf(context.Background(), todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return todo, p
}

// terminalPathRefusal is the node's refusal of a start whose new tab could not run the file launcher starts through
// (a managed launcher's personad, node/shellmiss.go).
func terminalPathRefusal(launcher, missing string) *rpc.Error {
	data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": launcher, "missing": missing, "kind": "no_such_file",
		"where": "terminal", "pane_shell": "bash"})
	return &rpc.Error{Code: proto.CodeLauncherMissing, Message: "the new tab's shell (bash) can't run " + missing, Data: data}
}

// A launcher the terminal could not run suggests plain claude first (the computer has Claude Code), then the
// computer's other Claude Code launchers, never the failed one or another agent's; a failure that is not the
// launcher's (an error the hub can't place) suggests none.
func TestFailedPlanTaskCarriesRetryWith(t *testing.T) {
	s, f := launcherRetryHub(t)
	_, p := failOnWork(t, s, f, terminalRefusal(proto.CodeLauncherMissing, "work"))
	if got := p.Tasks[0].RetryWith; !slices.Equal(got, []string{"claude", "work2"}) {
		t.Fatalf("retry_with %v, want [claude work2]", got)
	}

	s, f = launcherRetryHub(t)
	_, p = failOnWork(t, s, f, errors.New("the disk is full"))
	if got := p.Tasks[0].RetryWith; len(got) != 0 {
		t.Fatalf("a failure that is not the launcher's suggests %v", got)
	}
}

// Retry with another launcher: checked, then the same to-do is reopened and its agent starts on that launcher with its
// default model, in the same directory; the plan task names it and says why (in the owner's language, as the plan's
// other notes), and the task's history records the move.
// The HTTP route takes the body; one with none retries the launcher the plan has.
func TestTaskRetryWithLauncherOverride(t *testing.T) {
	s, f := launcherRetryHub(t)
	todo, _ := failOnWork(t, s, f, terminalRefusal(proto.CodeLauncherMissing, "work"))
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "en", `{"launcher":"claude"}`)
	if status != 200 || body["todo"] == nil {
		t.Fatalf("retry: %d %v", status, body)
	}
	waitFor(t, "the agent to start on claude", func() bool { return len(onlyTodo(s).Agents) == 1 })
	f.mu.Lock()
	starts := slices.Clone(f.starts)
	f.mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("%d starts", len(starts))
	}
	if second := starts[1]; second["launcher"] != "claude" || second["model"] != nil && second["model"] != "" || second["cwd"] != starts[0]["cwd"] {
		t.Fatalf("second start %v (first %v)", second, starts[0])
	}
	if got := onlyTodo(s); got.ID != todo.ID || got.DoneAt != nil || got.StartError != "" || todoCount(s) != 1 {
		t.Fatalf("not the same to-do, reopened: %+v", got)
	}
	_, p, i, err := s.remedyOf(context.Background(), todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	pt := p.Tasks[i]
	if pt.Launcher != "claude" || pt.Model != "" || pt.Tier != "" || len(pt.RetryWith) != 0 || pt.Error != "" ||
		!strings.Contains(pt.Why, "改用 claude 重新开始：work 在 box 上没开起来") {
		t.Fatalf("plan task %+v", pt)
	}
	items, _, _ := s.feed(context.Background(), "conv1", time.Time{})
	moved := slices.ContainsFunc(items, func(it FeedItem) bool {
		var b startRemedyBody
		_ = json.Unmarshal(it.Body, &b)
		return it.Kind == feedStartRemedy && b.By == "user" && b.LauncherFrom == "work" && b.LauncherTo == "claude"
	})
	if !moved {
		t.Fatalf("the move is not in the task's history: %s", feedKinds(s, "conv1"))
	}
}

// A launcher the computer does not have, one its health record says cannot run there now, or a model with no launcher
// is refused before anything changes: the to-do stays a failed start and nothing starts.
func TestTaskRetryLauncherOverrideRefusesUnknownOrUnusable(t *testing.T) {
	s, f := launcherRetryHub(t)
	todo, _ := failOnWork(t, s, f, terminalRefusal(proto.CodeLauncherMissing, "work"))
	ctx := context.Background()
	f.mu.Lock()
	f.checks["box/work2"] = proto.LauncherCheck{Agent: "claude", Error: "API Error: 401 invalid x-api-key"}
	f.mu.Unlock()
	s.recordHealth(ctx, "box", "work2", healthResult{err: "API Error: 401 invalid x-api-key", source: healthSourceCheck})
	for _, c := range []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"not on the computer", `{"launcher":"nope"}`, 409, "Launchers that computer has: claude, work"},
		{"unusable there", `{"launcher":"work2"}`, 409, "work2 can’t start on box right now"},
		{"a model alone", `{"model":"glm-4.6"}`, 400, "name the launcher too"},
		{"not a model id", `{"launcher":"claude","model":"a model"}`, 400, "model"},
	} {
		status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "en", c.body)
		if text, _ := body["error"].(string); status != c.status || !strings.Contains(text, c.want) {
			t.Errorf("%s: %d %v, want %d with %q", c.name, status, body, c.status, c.want)
		}
	}
	if got := onlyTodo(s); got.DoneAt == nil || got.StartError == "" {
		t.Fatalf("a refused retry changed the to-do: %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 1 {
		t.Fatalf("%d starts after refusals", len(f.starts))
	}
}

// While the terminal could not run personad itself, every Persona-managed launcher would fail the same way: retrying
// on one is refused, and none is suggested; the computer's own launchers are still taken.
func TestTaskRetrySuggestsNoManagedLauncherWhenPersonadMissing(t *testing.T) {
	s, f := launcherRetryHub(t)
	ctx := withManagedSnapshot(context.Background(), []storedLauncher{
		{ManagedLauncher: proto.ManagedLauncher{Name: "gw-a", Kind: "api", BaseURL: "https://gateway.example.com", Model: "m"}},
		{ManagedLauncher: proto.ManagedLauncher{Name: "gw-b", Kind: "api", BaseURL: "https://gateway.example.com", Model: "m"}},
	})
	m := Device{ID: "box", Name: "box", Reachable: true, ClaudeVersion: "2.1.300"}
	failed := PlanTask{Machine: "box", Cwd: "/code/app", Launcher: "gw-a", Model: "m", TodoID: "todo_x",
		ErrorCode: proto.CodeLauncherMissing, ErrorArgs: map[string]string{"launcher": "gw-a", "missing": "/opt/persona/bin/personad",
			"kind": "no_such_file", "where": "terminal"}}
	if !s.personadMissing(ctx, failed) {
		t.Fatal("personad missing not seen")
	}
	if got := s.retryLaunchers(ctx, m, failed, terminalPathRefusal("gw-a", "/opt/persona/bin/personad")); slices.Contains(got, "gw-b") ||
		!slices.Contains(got, "claude") {
		t.Fatalf("suggested %v", got)
	}
	// Asked for by name, a managed one is still taken: the terminal may have been fixed since that record.
	task := failed
	if err := s.retryOn(ctx, &task, m, retryWith{launcher: "gw-b"}); err != nil || task.Launcher != "gw-b" {
		t.Fatalf("a managed launcher asked for by name refused: %v, task %+v", err, task)
	}
	task = failed
	if err := s.retryOn(ctx, &task, m, retryWith{launcher: "claude"}); err != nil {
		t.Fatal(err)
	}
	if task.Launcher != "claude" || task.Model != "" || task.Permission != "bypassPermissions" {
		t.Fatalf("task %+v", task)
	}
	// Another failure of a managed launcher (its key) does not rule the others out.
	other := failed
	other.ErrorArgs = map[string]string{"launcher": "gw-a", "missing": "gw-a", "kind": "not_found", "where": "terminal"}
	if s.personadMissing(ctx, other) {
		t.Fatal("a launcher missing from the shell read as personad missing")
	}
	_ = f
}

// task_retry with launcher does the same for the main conversation: a start that failed for good starts again on that
// launcher, counted as a retry, and its answer says so.
func TestToolTaskRetryWithLauncher(t *testing.T) {
	s, f := launcherRetryHub(t)
	setSettings(t, s, func(v *Settings) { v.Main = "conv1"; v.MachineID = "server" })
	todo, _ := failOnWork(t, s, f, terminalRefusal(proto.CodeLauncherMissing, "work"))
	args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "work cannot start in this terminal", "launcher": "claude"})
	res, err := s.toolTaskRetry(context.Background(), "conv1", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "with launcher claude") || !strings.Contains(res, "retry 1 of 3") {
		t.Fatalf("answer %q", res)
	}
	waitFor(t, "the agent to start on claude", func() bool { return len(onlyTodo(s).Agents) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.starts[len(f.starts)-1]; last["launcher"] != "claude" {
		t.Fatalf("started %v", last)
	}
}

// A Retry of a start whose shell stopped on a question goes back to the tab its computer still keeps (in_pane), so an
// answer given there is not lost to a new tab that asks again; once the tab is no longer kept, a new tab.
func TestRetryTaskReusesTheKeptTab(t *testing.T) {
	for _, c := range []struct {
		name  string
		until time.Duration
		pane  any
	}{
		{"kept", time.Minute, "p7"},
		{"expired", -time.Minute, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := launcherRetryHub(t)
			data, _ := json.Marshal(map[string]string{"delivered": "none", "pane": "p7", "line": "Would you like to update? [Y/n]",
				"kept_until": time.Now().Add(c.until).UTC().Format(time.RFC3339)})
			todo, _ := failOnWork(t, s, f, &rpc.Error{Code: proto.CodeShellWaiting, Message: "shell waits", Data: data})
			if _, err := s.retryTask(context.Background(), todo.ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the agent to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
			f.mu.Lock()
			defer f.mu.Unlock()
			if got := f.starts[len(f.starts)-1]["in_pane"]; got != c.pane {
				t.Fatalf("in_pane %v, want %v", got, c.pane)
			}
		})
	}
}
