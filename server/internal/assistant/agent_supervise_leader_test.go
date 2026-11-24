package assistant

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAdoptTaskLeader(t *testing.T) {
	for _, tc := range []struct{ agent, caller string }{
		{"claude", "conv1"}, {"codex", "lead0001"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			ctx := t.Context()
			c := boxAgent("external1")
			c.Cwd, c.Agent, c.PaneID = "/code/app", tc.agent, "w1:p1"
			s, f := leadTakeEnv(t, c)
			const exe = "/opt/task tools/persona-node"
			s.hub = &taskHandoffTestHub{fakeHub: f, info: map[string]proto.NodeInfo{"box": {Executable: exe}}}
			args := map[string]any{"machine": "box", "session_id": c.SessionID, "role": "leader", "done_when": []string{"checks pass"}}
			res, err := runJSON(t, s, tc.caller, "agent_supervise", args)
			if err != nil {
				t.Fatal(err)
			}
			task := onlyTodo(s)
			if len(task.Agents) != 1 || task.Agents[0].Source != sourceLeader || task.Supervision.Supervisor != tc.caller || !strings.Contains(res, task.ID) {
				t.Fatalf("adoption: %s, %+v", res, task)
			}
			if len(f.sentOnce) != 1 || f.sentOnce[0]["session_id"] != c.SessionID {
				t.Fatalf("leader note delivery: %+v", f.sentOnce)
			}
			note := f.sentOnce[0]["text"].(string)
			terminal := tc.agent == "codex"
			if !strings.Contains(note, prompts.RenderTaskLeader(prompts.TaskLeader{Terminal: terminal})) {
				t.Fatalf("leader instructions: %q", note)
			}
			if path := taskHandoffTestExecutable(t, note); terminal && path != exe || !terminal && path != "" {
				t.Fatalf("executable: %q", path)
			}
			names := leaderToolNames(s.sessionMCPTools(ctx, "box", c.SessionID, false, false))
			if !slices.Contains(names, "task_plan") || !slices.Contains(names, "task_review") {
				t.Fatalf("discovery: %v", names)
			}
			call := func(sid, name string, args json.RawMessage) proto.ToolResult {
				return s.mcpCall(ctx, "box", sid, name, args, false)
			}
			if res := call(c.SessionID, "task_plan", workerArgs(nil)); res.IsError {
				t.Fatalf("plan: %+v", res)
			}
			var worker Todo
			waitFor(t, "the adopted leader's worker", func() bool {
				rows := todosOpenedBy(s, "box/"+c.SessionID)
				if len(rows) != 1 || len(rows[0].Agents) != 1 {
					return false
				}
				worker = rows[0]
				return true
			})
			w := worker.Agents[0]
			if w.Source != sourceWorker || w.Via != task.ID || worker.Supervision.Supervisor != "box/"+c.SessionID {
				t.Fatalf("worker: %+v", worker)
			}
			if res := call(w.SessionID, "task_plan", workerArgs(nil)); !res.IsError || !strings.Contains(res.Content[0].Text, "supervisor") {
				t.Fatalf("worker delegated: %+v", res)
			}
			review, _ := json.Marshal(map[string]string{"todo_id": worker.ID, "verdict": "cancelled"})
			if res := call(c.SessionID, "task_review", review); res.IsError || todoNow(s, worker.ID).DoneAt == nil {
				t.Fatalf("review: %+v", res)
			}
		})
	}
}

func TestAdoptTaskLeaderRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, caller, role, owner, reason string
		work, noCriteria                  bool
	}{
		{name: "work computer", caller: "conv1", role: "leader", work: true, reason: "work-trust computer"},
		{name: "blank criteria", caller: "lead0001", role: "leader", noCriteria: true, reason: "done_when"},
		{name: "unsupported role", caller: "conv1", role: "manager", reason: "role"},
		{name: "side conversation", caller: "side0001", role: "leader", reason: "only the main conversation and a direction's lead"},
		{name: "main taking another task", caller: "conv1", role: "leader", owner: "lead0001", reason: "another conversation"},
		{name: "lead taking another task", caller: "lead0001", role: "leader", owner: "conv1", reason: "stays theirs"},
		{name: "open worker", caller: "conv1", role: "leader", owner: "conv1", reason: "already works on open task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := boxAgent("external1")
			s, f := leadTakeEnv(t, c)
			if tc.work {
				f.hello.Assistant.WorkMachines = []string{"box"}
			}
			if tc.owner != "" {
				onBoxTask(t, s, c.SessionID, tc.owner, false, "original scope")
			}
			before := todoCount(s)
			args := map[string]any{"machine": "box", "session_id": c.SessionID, "role": tc.role, "done_when": []string{"new scope"}}
			if tc.noCriteria {
				args["done_when"] = []string{"   "}
			}
			if _, err := runJSON(t, s, tc.caller, "agent_supervise", args); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %q, got %v", tc.reason, err)
			}
			if todoCount(s) != before || len(sentOnce(f)) != 0 {
				t.Fatal("refused adoption opened a task or sent instructions")
			}
			if tc.owner != "" {
				task := onlyTodo(s)
				if task.Origin != tc.owner || task.Supervision.Supervisor != tc.owner || !slices.Equal(task.Supervision.DoneWhen, []string{"original scope"}) || task.Agents[0].Source != "manual" {
					t.Fatalf("refused adoption changed task: %+v", task)
				}
			}
		})
	}
}

func TestAdoptWithoutRoleKeepsWorker(t *testing.T) {
	c := boxAgent("external1")
	s, f := leadTakeEnv(t, c)
	f.hello.Assistant.WorkMachines = []string{"box"}
	res, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": c.SessionID})
	if err != nil || !strings.Contains(res, "agent was not told") {
		t.Fatalf("worker adoption: %s %v", res, err)
	}
	task := onlyTodo(s)
	if task.Agents[0].Source != "manual" || len(sentOnce(f)) != 0 || len(sentToAgent(f)) != 0 {
		t.Fatalf("ordinary adoption: %+v", task)
	}
}

func TestAdoptTaskLeaderRetriesNoteOnSameTask(t *testing.T) {
	c := boxAgent("external1")
	s, f := leadTakeEnv(t, c)
	args := map[string]any{"machine": "box", "session_id": c.SessionID, "role": "leader", "done_when": []string{"checks pass"}}
	f.failSendOnce = map[string]error{"box/" + c.SessionID: errors.New("machine offline")}
	_, err := runJSON(t, s, "conv1", "agent_supervise", args)
	task := onlyTodo(s)
	if err == nil || !strings.Contains(err.Error(), task.ID) || !strings.Contains(err.Error(), "machine offline") {
		t.Fatalf("delivery failure: %v", err)
	}
	delete(f.failSendOnce, "box/"+c.SessionID)
	if _, err := runJSON(t, s, "conv1", "agent_supervise", args); err != nil {
		t.Fatal(err)
	}
	ids := sentOnce(f)
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] || todoCount(s) != 1 || onlyTodo(s).ID != task.ID {
		t.Fatalf("retry: %v, %+v", ids, onlyTodo(s))
	}
}

func TestAdoptTaskLeaderEntryUsesSessionSource(t *testing.T) {
	for _, tc := range []struct {
		name, source, host, thread, executable string
		mcp, terminal                          bool
	}{
		{name: "Codex desktop thread", source: "codex_app_server", thread: "thread1", executable: "/opt/personad", terminal: true},
		{name: "Codex on Paseo", source: proto.SourceRuntime, host: proto.EnginePaseo, executable: "/opt/personad", mcp: true},
		{name: "third-party runtime", source: proto.SourceRuntime, executable: "/opt/personad"},
		{name: "unknown executable", executable: "relative/path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			s.hub = &taskHandoffTestHub{fakeHub: f, info: map[string]proto.NodeInfo{"box": {Executable: tc.executable}}}
			entry := s.adoptedTaskEntry(t.Context(), "box", proto.Session{Agent: "codex", SourceKind: tc.source, Host: tc.host, SourceThreadID: tc.thread})
			if entry.MCP != tc.mcp || entry.Terminal() != tc.terminal {
				t.Fatalf("entry: %+v", entry)
			}
		})
	}
}
