package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A continuation's replacement is its to-do's Agent from then on, in the calls an Agent's MCP server or `personad
// tool` in its pane sends (mcp.call with assistant false and the session the node names, #12096): it reads the
// decisions the to-do kept from before and, as the continuation made it the task's leader, plans and reviews workers.
// The leader it replaced (released), the replacement's worker and a call that names no session get nothing of it,
// and a manager restart widens nothing.
func TestContinuationReplacementKeepsItsTaskTools(t *testing.T) {
	database := taskRecoveryDatabase(t)
	e := newThreadEnv(t, database)
	ctx := context.Background()
	e.f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
	e.f.ownModels = true
	call := func(s *Service, session, name, args string) proto.ToolResult {
		t.Helper()
		return s.mcpCall(ctx, "box", session, name, json.RawMessage(args), false)
	}
	text := func(res proto.ToolResult) string {
		if len(res.Content) == 0 {
			return ""
		}
		return res.Content[0].Text
	}
	refused := func(what string, res proto.ToolResult, want string) {
		t.Helper()
		if !res.IsError || !strings.Contains(text(res), want) {
			t.Fatalf("%s: %+v, want a refusal saying %q", what, res, want)
		}
	}
	answered := func(what string, res proto.ToolResult, want ...string) string {
		t.Helper()
		if res.IsError {
			t.Fatalf("%s: %+v", what, res)
		}
		for _, w := range want {
			if !strings.Contains(text(res), w) {
				t.Fatalf("%s: %q lacks %q", what, text(res), w)
			}
		}
		return text(res)
	}
	const decision = `{"question":"Which platform first?","choice":"iOS","reason":"Most users are there"}`

	// A read-only Codex task leader notes a decision on its to-do.
	first := readonlyTestTask("bypassPermissions")
	first.Launcher, first.Goal, first.ExpectMinutes, first.Role = "codex", "Find and fix the failure", 10, roleLeader
	if _, err := e.s.toolTaskPlan(ctx, "m1", readonlyTaskArgs(t, first)); err != nil {
		t.Fatal(err)
	}
	id := planOf(t, e.s, "m1").Tasks[0].TodoID
	waitFor(t, "the read-only Agent", func() bool { return len(todoNow(e.s, id).Agents) == 1 })
	old := todoNow(e.s, id).Agents[0].SessionID
	e.sessions("box", proto.Session{SessionID: old, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusWorking})
	answered("the first Agent's decision", call(e.s, old, "todo_decision_add", decision))
	if _, a, ok := e.s.leaderTask(ctx, "box", old); !ok || a.Source != sourceLeader {
		t.Fatalf("the first Agent does not lead its task: %+v", a)
	}

	// The main conversation continues it, writable, with the replacement leading the task in its place.
	next := first
	next.Readonly, next.Permission = false, "bypassPermissions"
	next.Continues, next.ContinuesWhy = id, "Diagnosis is complete; lead the fix."
	next.Prompt = "Split the fix between workers and review them."
	res, err := e.s.toolTaskPlan(ctx, "m1", readonlyTaskArgs(t, next))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatalf("%s: %v", res, err)
	}
	waitFor(t, "the replacement on the same to-do", func() bool { return len(todoNow(e.s, id).Agents) == 2 })
	cur := todoNow(e.s, id)
	replacement := cur.Agents[1]
	if replacement.SessionID == old || replacement.Source != sourceLeader || cur.Agents[0].ReleasedAt == nil || todoCount(e.s) != 1 {
		t.Fatalf("the continuation's Agents: %+v", cur.Agents)
	}
	e.sessions("box", proto.Session{SessionID: old, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusIdle},
		proto.Session{SessionID: replacement.SessionID, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusWorking})

	// What holds for the replacement, the released Agent and a call naming nobody, before and after a restart.
	holds := func(s *Service, when string) {
		t.Helper()
		answered(when+": the replacement's todo_decisions", call(s, replacement.SessionID, "todo_decisions", `{}`),
			`"id":"`+id+`"`, "Which platform first?", "iOS")
		if got := answered(when+": the released Agent's todo_decisions", call(s, old, "todo_decisions", `{}`), "not working on an open todo"); strings.Contains(got, "iOS") {
			t.Fatalf("%s: the released Agent read the decisions: %q", when, got)
		}
		refused(when+": the released Agent's task_plan", call(s, old, "task_plan", string(workerArgs(nil))), "unknown tool task_plan")
		refused(when+": the released Agent's task_review", call(s, old, "task_review", `{"todo_id":"`+id+`","verdict":"accepted"}`), "unknown tool task_review")
		refused(when+": the released Agent's task_spawn", call(s, old, "task_spawn", spawnArgs), "requires an active Agent")
		answered(when+": the released Agent's todo_decision_add", call(s, old, "todo_decision_add", decision), "not working on an open todo")
		if rows, err := database.ListTodoDecisions(ctx, id, true); err != nil || len(rows) != 1 {
			t.Fatalf("%s: the to-do's decisions: %+v (%v), want only the first Agent's", when, rows, err)
		}
		refused(when+": task_plan naming no session", call(s, "", "task_plan", string(workerArgs(nil))), "unknown tool task_plan")
		refused(when+": todo_decisions naming no session", call(s, "", "todo_decisions", `{}`), "could not tell which agent session")
		if _, _, ok := s.leaderTask(ctx, "box", replacement.SessionID); !ok {
			t.Fatalf("%s: the replacement does not lead its task", when)
		}
	}
	holds(e.s, "running")

	// The replacement leads: its task_plan hands a worker beneath this to-do, and nothing else.
	answered("the replacement's task_plan", call(e.s, replacement.SessionID, "task_plan", string(workerArgs(nil))), "beneath your task "+id)
	lead := "box/" + replacement.SessionID
	waitFor(t, "the worker's link", func() bool {
		l := todosOpenedBy(e.s, lead)
		return len(l) == 1 && len(l[0].Agents) == 1
	})
	w := todosOpenedBy(e.s, lead)[0]
	worker := w.Agents[0].SessionID
	if w.Agents[0].Source != sourceWorker || w.Agents[0].Via != id {
		t.Fatalf("the worker's link: %+v", w.Agents[0])
	}
	e.sessions("box", proto.Session{SessionID: old, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusIdle},
		proto.Session{SessionID: replacement.SessionID, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusWorking},
		proto.Session{SessionID: worker, Agent: "codex", Launcher: "codex", Cwd: "/code/app", Status: proto.StatusWorking})
	workerHolds := func(s *Service, when string) {
		t.Helper()
		refused(when+": the worker's task_plan", call(s, worker, "task_plan", string(workerArgs(nil))), "does not hand out Persona tasks")
		refused(when+": the worker's task_review", call(s, worker, "task_review", `{"todo_id":"`+id+`","verdict":"accepted"}`), "unknown tool task_review")
		if got := answered(when+": the worker's todo_decisions", call(s, worker, "todo_decisions", `{}`), w.ID); strings.Contains(got, "iOS") {
			t.Fatalf("%s: the worker read its leader's to-do's decisions: %q", when, got)
		}
	}
	workerHolds(e.s, "running")

	// A new manager on the same database reads the same links: the replacement still leads, nobody else gains.
	restarted := newServiceOn(t, e.f, database)
	holds(restarted, "restarted")
	workerHolds(restarted, "restarted")
	if open := todosOpenedBy(restarted, "box/"+old); len(open) != 0 {
		t.Fatalf("the released Agent planned workers: %+v", open)
	}
}
