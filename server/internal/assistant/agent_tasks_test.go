package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// agentTasksOn turns assistant.agent_tasks on.
func agentTasksOn(t *testing.T, s *Service) {
	t.Helper()
	setSettings(t, s, func(v *Settings) { v.AgentTasks = new(true) })
}

// appAgent is agent s1 on box as an app's New Agent leaves it after its turn: started by Persona (or not), stopped.
func appAgent(f *fakeHub, byPersona bool, tools ...string) {
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "agent-1",
		Status: proto.StatusDone, State: proto.StateUnread, StartedByPersona: byPersona, LastText: "修好了", UpdatedAt: time.Now()})
	items := []proto.Item{{Seq: 1, Role: "user", Kind: proto.KindText, Text: "登录页横屏崩溃，修一下"}}
	for i, tool := range tools {
		items = append(items, proto.Item{Seq: 2 + i, Role: "assistant", Kind: proto.KindToolUse, ToolName: tool})
	}
	f.setItems(append(items, proto.Item{Seq: 90, Role: "assistant", Kind: proto.KindText, Text: "修好了"})...)
}

// An agent the user opened manually on the Agents page stays outside task tracking, even after a turn that did work.
// Only assistant dispatch and explicit adoption create to-dos (#5732).
func TestAppStartedAgentStaysWithoutATask(t *testing.T) {
	s, f := newQuietService(t)
	agentTasksOn(t, s)
	f.judge = `{"verdict":"done","reason":"修好了"}`
	appAgent(f, true, "Read", "Edit", "Bash")
	s.taskTurnSettled(context.Background(), "box", "s1")

	if todoCount(s) != 0 || feedKinds(s, "conv1") != "" || len(f.notices()) != 0 {
		t.Fatalf("manual start was adopted: todos=%d feed=%q notices=%d", todoCount(s), feedKinds(s, "conv1"), len(f.notices()))
	}

	// Another working turn is still the user's own session, not an assistant dispatch.
	s.taskTurnSettled(context.Background(), "box", "s1")
	if todoCount(s) != 0 {
		t.Fatalf("second turn created %d todos", todoCount(s))
	}
}

// What leaves no todo behind: the setting off (nothing is even read), a turn that only read and answered, an agent
// the user started in a terminal, an assistant conversation, and an agent that was on a todo before.
func TestAgentsLeftWithoutATask(t *testing.T) {
	ctx := context.Background()
	itemReads := func(f *fakeHub) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if c.Method == DeviceSessionItems || c.Method == DeviceSessionGet {
				n++
			}
		}
		return n
	}
	t.Run("off", func(t *testing.T) {
		s, f := newQuietService(t)
		appAgent(f, true, "Edit", "Bash")
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 || feedKinds(s, "conv1") != "" || itemReads(f) != 0 || len(f.notices()) != 0 {
			t.Fatalf("off: %d todos, feed %q, %d reads, %d notices", todoCount(s), feedKinds(s, "conv1"), itemReads(f), len(f.notices()))
		}
		// Explicitly off is the default.
		setSettings(t, s, func(v *Settings) { v.AgentTasks = new(false) })
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 || itemReads(f) != 0 {
			t.Fatalf("set off: %d todos, %d reads", todoCount(s), itemReads(f))
		}
	})
	t.Run("a question and its answer", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		appAgent(f, true, "Read", "Grep")
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 || feedKinds(s, "conv1") != "" {
			t.Fatalf("%d todos, feed %q", todoCount(s), feedKinds(s, "conv1"))
		}
		// A later turn that does work is still the user's own session, not an assistant dispatch.
		appAgent(f, true, "Bash")
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 {
			t.Fatalf("after work: %d todos", todoCount(s))
		}
	})
	t.Run("started in a terminal", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		appAgent(f, false, "Edit", "Bash")
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 || feedKinds(s, "conv1") != "" || len(f.notices()) != 0 {
			t.Fatalf("%d todos, feed %q", todoCount(s), feedKinds(s, "conv1"))
		}
	})
	t.Run("an assistant conversation", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		appAgent(f, true, "Bash")
		f.mu.Lock()
		f.sessions["box"][0].Assistant = true
		f.mu.Unlock()
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 0 {
			t.Fatalf("%d todos", todoCount(s))
		}
	})
	t.Run("on a todo before", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		appAgent(f, true, "Bash")
		old, err := s.createTodo(ctx, "以前的", "x", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, old.ID, "box", "s1", "以前的", "/code/app", "claude"); err != nil {
			t.Fatal(err)
		}
		if w := serve(s, "DELETE", "/api/v1/todos/"+old.ID+"/agents/box/s1", ""); w.Code != 200 {
			t.Fatalf("unlink: %d %s", w.Code, w.Body)
		}
		s.taskTurnSettled(ctx, "box", "s1")
		if todoCount(s) != 1 {
			t.Fatalf("%d todos", todoCount(s))
		}
	})
}

// agent_start always leaves a tracked task: the calling direction lead owns its own starts, otherwise main does.
func TestAgentStartIsATaskFromAnyConversation(t *testing.T) {
	args := json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI 为什么挂了"}`)
	ctx := context.Background()
	for _, c := range []struct {
		conv, told, supervisor string
	}{{"conv2", startToldMain, "conv1"}, {"lead1", startToldHere, "lead1"}, {"conv1", startToldHere, "conv1"}} {
		s, _ := newQuietService(t)
		setSettings(t, s, func(v *Settings) {
			v.AgentTasks, v.MainTasksQuiet, v.Leaders = new(true), new(false), []string{"lead1"}
		})
		res, err := s.runTool(ctx, "server", c.conv, "agent_start", args)
		if err != nil {
			t.Fatal(err)
		}
		todo := onlyTodo(s)
		if todo.Quiet || todo.Origin != c.conv || len(todo.Agents) != 1 || todo.Agents[0].SessionID != "s1" || todo.Title != "看看 CI 为什么挂了" {
			t.Fatalf("%s: todo = %+v", c.conv, todo)
		}
		if v := todo.Supervision; v == nil || v.Supervisor != c.supervisor || len(v.DoneWhen) != 0 {
			t.Fatalf("%s: supervision = %+v", c.conv, todo.Supervision)
		}
		if !strings.Contains(res, "a task of this conversation (its card is in the conversation; "+c.told+").") || feedKinds(s, c.conv) != "task" {
			t.Fatalf("%s: result %q, feed %q", c.conv, res, feedKinds(s, c.conv))
		}
		if c.supervisor != c.conv && feedKinds(s, "conv1") != "task" {
			t.Fatalf("%s: main feed %q", c.conv, feedKinds(s, "conv1"))
		}
	}

	// The old opt-in switches are irrelevant: both a main and a side start are tracked and main-supervised.
	s, _ := newQuietService(t)
	res, err := s.runTool(ctx, "server", "conv1", "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	const mainTold = ": session s1, a task of this conversation (its card is in the conversation; " +
		"you are told here when it is done or waits for the user, nothing else notifies the user)."
	if !strings.HasPrefix(res, "Started on ") || !strings.HasSuffix(res, mainTold) {
		t.Fatalf("main: %q", res)
	}
	res, err = s.runTool(ctx, "server", "conv2", "agent_start", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, startToldMain) || todoCount(s) != 2 || feedKinds(s, "conv2") != "task" {
		t.Fatalf("side: %q, %d todos, feed %q", res, todoCount(s), feedKinds(s, "conv2"))
	}
	for _, todo := range s.todosOf(ctx, "*") {
		if todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
			t.Fatalf("untracked start: %+v", todo)
		}
	}
}

// agent_start names the to-do it made before the start in the node's model_source: the Agent's gateway requests
// carry it from their first one, whether main or a task leader started it (the link comes only after the start).
func TestAgentStartCarriesTodoModelSource(t *testing.T) {
	args := json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI 为什么挂了"}`)
	for _, conv := range []string{"conv1", "lead1"} {
		s, f := newQuietService(t)
		setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
		if _, err := s.runTool(context.Background(), "server", conv, "agent_start", args); err != nil {
			t.Fatal(err)
		}
		todo := onlyTodo(s)
		var starts []modelhttp.Source
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method != DeviceSessionStart {
				continue
			}
			var p struct {
				Source modelhttp.Source `json:"model_source"`
			}
			if err := json.Unmarshal(c.Params, &p); err != nil {
				t.Fatal(err)
			}
			starts = append(starts, p.Source)
		}
		f.mu.Unlock()
		want := modelhttp.Source{Role: "task_agent", Todo: todo.ID}
		if len(starts) != 1 || starts[0] != want || todo.ID == "" {
			t.Fatalf("%s: session.start model_source = %+v, want %+v", conv, starts, want)
		}
	}
}

// report_to_supervisor from an agent on an open todo nobody supervises reaches the main conversation while agent tasks
// are on: the user's own todo gets the main conversation as its supervisor, with no done_when. Off, and for an agent
// on no todo, nobody receives it, as before.
func TestReportFromAnUnsupervisedTodoReachesTheMainConversation(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "修登录页",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	own, err := s.createTodo(ctx, "自己记的", "x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, own.ID, "box", "s1abcdef", "自己记的", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	report := json.RawMessage(`{"kind":"done","text":"做完了，PR 在这","evidence":["https://github.com/o/r/pull/7"]}`)
	nobody := func(sid string) {
		t.Helper()
		if res := s.agentTool(ctx, "box", sid, "report_to_supervisor", report); res.IsError || !strings.Contains(res.Content[0].Text, "Nobody receives this") {
			t.Fatalf("%s: %+v", sid, res)
		}
	}
	nobody("s1abcdef") // off
	if todoNow(s, own.ID).Supervision != nil || len(f.notices()) != 0 {
		t.Fatalf("off: supervision %+v", todoNow(s, own.ID).Supervision)
	}

	agentTasksOn(t, s)
	nobody("s9abcdef") // on no todo
	if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", report); res.IsError || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	got := todoNow(s, own.ID)
	if v := got.Supervision; v == nil || v.Supervisor != "conv1" || len(v.DoneWhen) != 0 || got.Origin != "" || got.Quiet || got.DoneAt != nil {
		t.Fatalf("todo = %+v, supervision %+v", got, got.Supervision)
	}
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "📋") || !strings.Contains(n, "做完了，PR 在这") || !strings.Contains(n, "https://github.com/o/r/pull/7") {
		t.Fatalf("message:\n%s", n)
	}
	if !strings.Contains(feedKinds(s, "conv1"), "report") {
		t.Fatalf("feed = %q", feedKinds(s, "conv1"))
	}
}

// A todo the user made gets a supervisor when they link an agent to it (no done_when). Off it gets none; a reporting
// conversation's task gets that conversation as supervisor when its agent reports.
func TestLinkedTodoGetsASupervisor(t *testing.T) {
	ctx := context.Background()
	link := func(s *Service, origin string) Todo {
		t.Helper()
		todo, err := s.createTodo(ctx, "自己记的", "x", origin, "")
		if err != nil {
			t.Fatal(err)
		}
		if w := serve(s, "PUT", "/api/v1/todos/"+todo.ID+"/agents/box/s1", ""); w.Code != 200 {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
		return todoNow(s, todo.ID)
	}
	s, _ := newQuietService(t)
	if got := link(s, ""); got.Supervision != nil {
		t.Fatalf("off: %+v", got.Supervision)
	}
	s, _ = newQuietService(t)
	agentTasksOn(t, s)
	got := link(s, "")
	if v := got.Supervision; v == nil || v.Supervisor != "conv1" || len(v.DoneWhen) != 0 || got.Quiet {
		t.Fatalf("on: %+v, quiet %v", got.Supervision, got.Quiet)
	}
	// A hand-off (the app's start or message with todo_id) links the same way.
	other, err := s.createTodo(ctx, "另一条", "x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TodoLink(ctx, TodoLinkParams{TodoID: other.ID, MachineID: "box", SessionID: "s2", Prompt: "做这个"}); err != nil {
		t.Fatal(err)
	}
	if v := todoNow(s, other.ID).Supervision; v == nil || v.Supervisor != "conv1" {
		t.Fatalf("hand-off: %+v", v)
	}
	// A reporting conversation's task keeps its notices; its agent's report gives it that conversation as supervisor.
	s.markReporter(ctx, "server", "lark1")
	theirs := link(s, "lark1")
	if theirs.Supervision != nil {
		t.Fatalf("a reporting conversation's task was supervised before its report: %+v", theirs.Supervision)
	}
	reported, ok := s.superviseOpenTodoOf(ctx, "box", "s1")
	if !ok || reported.Supervision == nil || reported.Supervision.Supervisor != "lark1" {
		t.Fatalf("a reporting conversation's task on report: ok %v, supervision %+v", ok, reported.Supervision)
	}
}

// The depth cap holds for the tasks agent tasks make: an agent that became a task hands out none itself.
func TestAgentTaskWorkerCannotHandOutTasks(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	args, _ := json.Marshal(map[string]any{"machine": "box", "cwd": "/code/app", "prompt": "再开一个"})
	if _, err := s.runTool(ctx, "server", "conv2", "agent_start", args); err != nil { // s1, a task
		t.Fatal(err)
	}
	started := onlyTodo(s)
	if _, err := s.runTool(ctx, "server", "conv1", "agent_start", args); err != nil { // a second task, not a manual adoption
		t.Fatal(err)
	}
	if todoCount(s) != 2 {
		t.Fatalf("%d todos", todoCount(s))
	}
	var sessions []string
	for _, todo := range s.todosOf(ctx, "*") {
		sessions = append(sessions, todo.Agents[0].SessionID)
	}
	for _, sid := range sessions {
		err := s.workerRefusal(ctx, "box", sid)
		if err == nil || !strings.Contains(err.Error(), "does not hand out Persona tasks") {
			t.Fatalf("%s: %v", sid, err)
		}
		res := s.Tool(ctx, ToolCall{Name: "agent_start", MachineID: "box", SessionID: sid, Arguments: args})
		if !res.IsError || !strings.Contains(res.Content[0].Text, "does not hand out Persona tasks") {
			t.Fatalf("agent_start from %s: %+v", sid, res)
		}
	}
	if !strings.Contains(s.workerRefusal(ctx, "box", "s1").Error(), started.ID) || todoCount(s) != 2 {
		t.Fatalf("%d todos", todoCount(s))
	}
	// The conversations that hand out are not limited.
	for _, self := range []string{"conv1", "conv2"} {
		if err := s.workerRefusal(ctx, "server", self); err != nil {
			t.Fatalf("%s refused: %v", self, err)
		}
	}
}

// An assistant conversation is never made a task, also when the row the turn end reads says `assistant: false` (it
// is listed outside this manager's assistant directory, #10801): one this manager's assistant opened, by its own
// records (the apps' and a channel's or an extension's POST /api/v1/assistant/conversations, the main conversation,
// the background supervisor), and a headless one in any manager's assistant directory (another manager's, left on a
// computer that moved here). agent_supervise refuses them too. An app's New Agent still becomes a task, both ways.
func TestAssistantConversationsListedAsAgentsAreNoTasks(t *testing.T) {
	ctx := context.Background()
	const drifted = "/home/me/.persona/assistant/reports" // an older node's row at rest, where a `cd` left it
	// listed lists id on server (beside the main conversation, or as it) with a turn that ran a command.
	listed := func(f *fakeHub, id, cwd string) {
		main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
		row := proto.Session{SessionID: id, MachineID: "server", Cwd: cwd, Agent: "claude", Title: "Persona", Status: proto.StatusDone,
			State: proto.StateUnread, StartedByPersona: true, LastText: "done", UpdatedAt: time.Now()}
		if id == "conv1" {
			main = row
		}
		f.setSessions("server", main, row)
		f.setItems(proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the build"},
			proto.Item{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
			proto.Item{Seq: 3, Role: "assistant", Kind: proto.KindText, Text: "done"})
	}
	// itemReads counts the transcript reads of the turn end's check (lastTurnItems).
	itemReads := func(f *fakeHub) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if c.Method == DeviceSessionItems {
				n++
			}
		}
		return n
	}
	// noTask: id's turn end made no todo and did not even read its turn, and agent_supervise refuses it as an
	// assistant conversation.
	noTask := func(t *testing.T, s *Service, f *fakeHub, id, cwd string) {
		t.Helper()
		listed(f, id, cwd)
		reads := itemReads(f)
		s.taskTurnSettled(ctx, "server", id)
		if n := todoCount(s); n != 0 || itemReads(f) != reads {
			t.Fatalf("turn end: %d todos, %d reads of its turn", n, itemReads(f)-reads)
		}
		if len(id) < 8 { // agent_supervise needs 8 characters of an id
			return
		}
		_, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "server", "session_id": id})
		if err == nil || !strings.Contains(err.Error(), "assistant conversation") || todoCount(s) != 0 {
			t.Fatalf("agent_supervise: %v, %d todos", err, todoCount(s))
		}
	}
	// opened is the conversation a POST /api/v1/assistant/conversations opens with headers (role, extension) and body.
	opened := func(t *testing.T, s *Service, f *fakeHub, role, ext, body string) string {
		t.Helper()
		f.mu.Lock()
		f.made = 1000000 // ids of 8 characters (s1000001)
		f.mu.Unlock()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body))
		if role != "" {
			r.Header.Set(RoleHeader, role)
			r.Header.Set(ExtensionHeader, ext)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var out struct {
			Session proto.Session `json:"session"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil || len(out.Session.SessionID) < 8 {
			t.Fatalf("open: %d %s", w.Code, w.Body)
		}
		return out.Session.SessionID
	}
	for _, c := range []struct{ name, role, ext, body string }{
		{"an app's new chat", "", "", `{"prompt":"check the build"}`},
		{"a channel's /agent", roleExtension, "larkext", `{"prompt":"check the build","purpose":"lark.agent"}`},
		{"an extension's", roleExtension, "github", `{"prompt":"check the build"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newQuietService(t)
			agentTasksOn(t, s)
			noTask(t, s, f, opened(t, s, f, c.role, c.ext, c.body), drifted)
		})
	}
	t.Run("the background supervisor", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg000001", "server" })
		noTask(t, s, f, "bg000001", drifted)
	})
	t.Run("the main conversation", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		noTask(t, s, f, "conv1", drifted)
	})
	t.Run("another manager's assistant directory", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		noTask(t, s, f, "old00001", "/home/me/.persona/assistant-m-0a1b2c")
	})
	// A session ID is only unique on its own computer: an Agent on box that shares the main conversation's or the
	// background supervisor's ID (recorded on server) is an Agent like any other.
	t.Run("the same ID on another computer", func(t *testing.T) {
		for _, id := range []string{"conv1", "bg000001"} {
			s, f := newQuietService(t)
			agentTasksOn(t, s)
			setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg000001", "server" })
			f.setSessions("box", proto.Session{SessionID: id, MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "agent-1",
				Status: proto.StatusDone, State: proto.StateUnread, StartedByPersona: true, LastText: "done", UpdatedAt: time.Now()})
			f.setItems(proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the build"},
				proto.Item{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
				proto.Item{Seq: 3, Role: "assistant", Kind: proto.KindText, Text: "done"})
			if c, ok := s.session(ctx, "box", id); !ok || s.assistantsConversation(ctx, "box", c) {
				t.Fatalf("%s on box was mistaken for the server's assistant conversation: %+v, ok %v", id, c, ok)
			}
			s.taskTurnSettled(ctx, "box", id)
			if got := todoCount(s); got != 0 {
				t.Fatalf("%s on box was automatically adopted after an ordinary turn: %d todos", id, got)
			}
		}
	})
	t.Run("an agent in a folder of the user's", func(t *testing.T) {
		s, f := newQuietService(t)
		agentTasksOn(t, s)
		listed(f, "a1000001", "/home/me/code/app")
		s.taskTurnSettled(ctx, "server", "a1000001")
		if got := todoCount(s); got != 0 {
			t.Fatalf("an ordinary turn automatically adopted the app-opened Agent: %d todos", got)
		}
		if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "server", "session_id": "a1000001"}); err != nil {
			t.Fatalf("explicitly supervise a1000001: %v", err)
		}
		if todo := onlyTodo(s); len(todo.Agents) != 1 || todo.Agents[0].SessionID != "a1000001" {
			t.Fatalf("explicit adoption: todo = %+v", todo)
		}
		listed(f, "a2000001", "/home/me/code/app")
		if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "server", "session_id": "a2000001"}); err != nil ||
			todoCount(s) != 2 {
			t.Fatalf("agent_supervise: %v, %d todos", err, todoCount(s))
		}
	})
}
