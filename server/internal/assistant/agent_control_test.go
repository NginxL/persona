package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func runJSON(t *testing.T, s *Service, self, name string, args map[string]any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return s.runTool(context.Background(), "server", self, name, raw)
}

// agent_interrupt stops a working agent's turn and sends the new instruction right after it; an agent not working
// is not stopped (then still goes); assistant conversations are not its to stop.
func TestAgentInterrupt(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})

	res, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "别等 CI 了，先把测试修好"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Stopped") || !strings.Contains(res, "not undone") {
		t.Fatalf("result: %s", res)
	}
	if got := f.interrupted(); len(got) != 1 || got[0] != "box/s1abcdef" {
		t.Fatalf("interrupted %v", got)
	}
	if sent := sentToAgent(f); len(sent) != 1 || sent[0] != "别等 CI 了，先把测试修好" {
		t.Fatalf("sent %q", sent)
	}

	// Idle: nothing to stop; just stopping says so, then is sent as is.
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()})
	if res, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef"}); err != nil ||
		!strings.Contains(res, "nothing to stop") {
		t.Fatalf("idle: %s %v", res, err)
	}
	if res, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "接着做"}); err != nil ||
		!strings.Contains(res, "was not working: sent") {
		t.Fatalf("idle then: %s %v", res, err)
	}
	if got := len(f.interrupted()); got != 1 {
		t.Fatalf("an idle agent was interrupted: %d", got)
	}

	// Not an assistant conversation; not a read-only source.
	f.setSessions("box", proto.Session{SessionID: "asst0001", MachineID: "box", Assistant: true, Status: proto.StatusWorking},
		proto.Session{SessionID: "ro000001", MachineID: "box", Status: proto.StatusWorking, SourceKind: "codex_history",
			Capabilities: &proto.SessionCapabilities{Read: true}, ReadOnlyReason: "只读"})
	for _, id := range []string{"asst0001", "ro000001"} {
		if _, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": id}); err == nil {
			t.Fatalf("%s was interrupted", id)
		}
	}
	if got := len(f.interrupted()); got != 1 {
		t.Fatalf("interrupted %d", got)
	}
}

// agent_supervise makes an agent the main conversation did not hand out one of its supervised tasks (a todo with
// the agent, a card in the feed), whose report gets to the main conversation in a Supervision message; on its own task it
// sets the criteria; another conversation's task stays theirs, and only the main conversation supervises.
func TestAgentSuperviseAndReport(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "修登录页",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})

	// Not supervised yet: a report reaches nobody, and says so without failing.
	if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(`{"kind":"question","text":"用哪个分支？"}`)); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Nobody receives this") {
		t.Fatalf("unsupervised report: %+v", res)
	}

	if _, err := runJSON(t, s, "conv2", "agent_supervise", map[string]any{"machine": "box", "session_id": "s1abcdef"}); err == nil {
		t.Fatal("supervised from another conversation")
	}
	res, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "s1abcdef",
		"done_when": []string{"PR 合并到 master"}, "expect_minutes": 30})
	if err != nil {
		t.Fatal(err)
	}
	todo := onlyTodo(s)
	if !strings.Contains(res, todo.ID) || todo.Title != "修登录页" || todo.Origin != "conv1" || len(todo.Agents) != 1 ||
		todo.Agents[0].SessionID != "s1abcdef" {
		t.Fatalf("task: %s\n%+v", res, todo)
	}
	if v := todo.Supervision; v == nil || v.Supervisor != "conv1" || len(v.DoneWhen) != 1 || v.ExpectMinutes != 30 {
		t.Fatalf("supervision: %+v", todo.Supervision)
	}
	if !strings.Contains(feedKinds(s, "conv1"), "task") {
		t.Fatalf("feed: %s", feedKinds(s, "conv1"))
	}

	// Again on its own task: the criteria change, no second todo.
	if res, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "s1abcdef",
		"done_when": []string{"PR 合并到 master", "截图贴进 PR"}}); err != nil || !strings.Contains(res, "already works for your task") {
		t.Fatalf("again: %s %v", res, err)
	}
	if v := todoNow(s, todo.ID).Supervision; len(v.DoneWhen) != 2 || v.ExpectMinutes != 30 {
		t.Fatalf("updated: %+v", v)
	}
	if n := len(s.todosOf(ctx, "*")); n != 1 {
		t.Fatalf("%d todos", n)
	}

	// Its report goes to the main conversation as a Supervision message.
	args := `{"kind":"question","text":"两种走法：A 先灰度，B 直接全量。选哪个？","evidence":["https://github.com/o/r/pull/901"],"step":2,"steps":5}`
	if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(args)); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
	n := f.notices()[0]
	for _, want := range []string{"📋 督办", "任务「修登录页」", "Agent 主动向你汇报：要你拍板（第 2/5 步）", "A 先灰度，B 直接全量",
		"- https://github.com/o/r/pull/901", "验收标准：PR 合并到 master；截图贴进 PR", "signal=report"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}

	// A bad kind or an empty text is refused; too many reports in the window are too.
	for _, bad := range []string{`{"kind":"chat","text":"x"}`, `{"kind":"progress","text":"  "}`} {
		if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(bad)); !res.IsError {
			t.Fatalf("%s accepted", bad)
		}
	}
	for i := 1; i < reportLimit; i++ {
		if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(`{"kind":"progress","text":"在跑测试"}`)); res.IsError {
			t.Fatalf("report %d: %+v", i, res)
		}
	}
	if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(`{"kind":"progress","text":"在跑测试"}`)); !res.IsError {
		t.Fatal("the limit let one more through")
	}

	// Another conversation's task stays that conversation's.
	other, err := s.createTodo(ctx, "别人的", "x", "conv2", "")
	if err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "s2abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	if err := s.linkTodoAgent(ctx, other.ID, "box", "s2abcdef", "别人的", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "s2abcdef"}); err == nil ||
		!strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("took another conversation's task: %v", err)
	}
}

// A newer report replaces only a waiting one of its own kind: a progress report does not push a question out.
func TestReportSignalIDs(t *testing.T) {
	q := superviseSignal{Todo: "todo_1", Kind: sigReport, Report: upReportQuestion}
	p := superviseSignal{Todo: "todo_1", Kind: sigReport, Report: upReportProgress}
	if q.id() == p.id() {
		t.Fatal("a progress report replaces a question")
	}
	if !q.urgent() || p.urgent() {
		t.Fatal("urgency")
	}
	if (superviseSignal{Todo: "todo_1", Kind: sigIdle}).id() != "todo_1|idle" {
		t.Fatal("other signals' ids changed")
	}
}

// Two questions before the Supervision message goes are both in it, with both reports' evidence; a progress report beside
// them is its own entry.
func TestReportsOfAKindAreJoined(t *testing.T) {
	a := superviseSignal{Todo: "todo_1", Kind: sigReport, Report: upReportQuestion, Key: "1", Detail: "用哪个分支？", Evidence: []string{"pr/1"},
		Step: 2, Steps: 5}
	b := superviseSignal{Todo: "todo_1", Kind: sigReport, Report: upReportQuestion, Key: "2", Detail: "要不要先停写？", Evidence: []string{"pr/1", "log"}}
	g := joinReports(a, b)
	if !strings.Contains(g.Detail, "用哪个分支？") || !strings.Contains(g.Detail, "要不要先停写？") || len(g.Evidence) != 2 || g.Step != 2 || g.Key != "2" {
		t.Fatalf("joined: %+v", g)
	}
	merged := mergeSignals([]superviseSignal{a}, []superviseSignal{b, {Todo: "todo_1", Kind: sigReport, Report: upReportProgress, Key: "3", Detail: "跑测试"}})
	if len(merged) != 2 || !strings.Contains(merged[0].Detail, "用哪个分支？") || !strings.Contains(merged[0].Detail, "要不要先停写？") {
		t.Fatalf("merged: %+v", merged)
	}
}

// A report is the task's progress (step, kind, a line of its text) on the ledger the apps get, without touching what
// the ledger's review is keyed on; a ledger write afterwards keeps it.
func TestReportIsTheTasksProgress(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "迁移数据库", "x", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"迁移完成"}, 0)
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1abcdef", "迁移数据库", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	before := todoNow(s, todo.ID).Supervision.UpdatedAt
	res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(`{"kind":"progress","text":"在跑迁移脚本\n第二行","step":3,"steps":5}`))
	if res.IsError {
		t.Fatalf("report: %+v", res)
	}
	v := todoNow(s, todo.ID).Supervision
	if p := v.Progress; p == nil || p.Kind != "progress" || p.Step != 3 || p.Steps != 5 || p.Text != "在跑迁移脚本 第二行" || p.At.IsZero() {
		t.Fatalf("progress: %+v", v.Progress)
	}
	if !v.UpdatedAt.Equal(before) {
		t.Fatal("the report moved the ledger's updated_at (the review key)")
	}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.ExpectMinutes = 40; return true }); err != nil {
		t.Fatal(err)
	}
	if v := todoNow(s, todo.ID).Supervision; v.Progress == nil || v.Progress.Step != 3 || v.ExpectMinutes != 40 {
		t.Fatalf("after a ledger write: %+v", v)
	}
	raw, _ := json.Marshal(formatTodo(todoNow(s, todo.ID)))
	if !strings.Contains(string(raw), `"progress":{"kind":"progress","step":3,"steps":5`) {
		t.Fatalf("the apps do not get it: %s", raw)
	}
	// Progress stays off the conversation's stream; a question goes in it.
	if strings.Contains(feedKinds(s, "conv1"), "report") {
		t.Fatalf("a progress report is in the feed: %s", feedKinds(s, "conv1"))
	}
	if res := s.agentTool(ctx, "box", "s1abcdef", "report_to_supervisor", json.RawMessage(`{"kind":"question","text":"要不要先停写？"}`)); res.IsError {
		t.Fatalf("question: %+v", res)
	}
	if !strings.Contains(feedKinds(s, "conv1"), "report") {
		t.Fatalf("the question is not in the feed: %s", feedKinds(s, "conv1"))
	}
	if p := todoNow(s, todo.ID).Supervision.Progress; p.Kind != "question" || p.Step != 0 {
		t.Fatalf("progress after the question: %+v", p)
	}
}

// Redirecting the agent of this conversation's task shows in its feed as a line to the task, marked as cutting in.
func TestInterruptOfATaskIsInTheFeed(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "修登录页", "x", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1abcdef", "修登录页", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "先修测试"}); err != nil {
		t.Fatal(err)
	}
	items, _, err := s.feed(ctx, "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var routed string
	for _, it := range items {
		if it.Kind == feedRouted {
			routed = string(it.Body)
		}
	}
	if routed != `{"from":"assistant","interrupted":true,"machine_id":"box","session_id":"s1abcdef","text":"先修测试"}` {
		t.Fatalf("routed: %q (%s)", routed, feedKinds(s, "conv1"))
	}
	// Stopping without a new instruction adds nothing.
	n := len(items)
	if _, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef"}); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := s.feed(ctx, "conv1", time.Time{}); len(items) != n {
		t.Fatalf("a bare stop added to the feed: %s", feedKinds(s, "conv1"))
	}
}

// agent_interrupt says "Stopped" only once the turn has ended (#8097): a node that only asked the agent to stop
// (no settled) is looked at again; an agent that stops reading as working counts, one still working is reported as
// not stopped yet, with the new instruction queued behind its turn. A send the runtime only queued says so.
func TestAgentInterruptReportsOnlyAnEndedTurnAsStopped(t *testing.T) {
	wait, every := interruptSettleWait, interruptSettleEvery
	interruptSettleWait, interruptSettleEvery = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { interruptSettleWait, interruptSettleEvery = wait, every })
	s, f := newQuietService(t)
	working := proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "pi",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()}
	f.setSessions("box", working)
	queued := false
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) { return map[string]any{"ok": true, "queued": queued}, nil })

	// Still working after the wait: not reported as stopped.
	res, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef"})
	if err != nil || strings.Contains(res, "Stopped") || !strings.Contains(res, "has not ended yet") {
		t.Fatalf("still working: %s %v", res, err)
	}
	queued = true
	res, err = runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "先修测试"})
	if err != nil || strings.Contains(res, "Stopped") || !strings.Contains(res, "queued") {
		t.Fatalf("still working, then: %s %v", res, err)
	}

	// The agent stops reading as working once asked: stopped; the instruction delivered.
	queued = false
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) {
		idle := working
		idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
		go f.setSessions("box", idle) // after the call, as the node's list catches up
		return map[string]any{"ok": true}, nil
	})
	res, err = runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "先修测试"})
	if err != nil || !strings.HasPrefix(res, "Stopped") || !strings.Contains(res, "sent the new instruction") {
		t.Fatalf("stopped: %s %v", res, err)
	}

	// The runtime saw the turn end (settled), but the instruction still only queued: said so.
	f.setSessions("box", working)
	queued = true
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) { return map[string]any{"ok": true, "settled": true}, nil })
	res, err = runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef", "then": "先修测试"})
	if err != nil || !strings.HasPrefix(res, "Stopped") || !strings.Contains(res, "queued") {
		t.Fatalf("settled, queued: %s %v", res, err)
	}

	// A stop the runtime could not make happen is an error, not "Stopped".
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) {
		return nil, errors.New("pi 的这一轮在 10 秒内没有停下来")
	})
	if res, err := runJSON(t, s, "conv1", "agent_interrupt", map[string]any{"machine": "box", "session_id": "s1abcdef"}); err == nil {
		t.Fatalf("a failed stop reported: %s", res)
	}
}

// agent_send to an agent whose runtime only queued the message behind its turn says it is queued, not sent (#8097).
func TestAgentSendSaysQueued(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "pi",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) { return map[string]any{"ok": true, "queued": true}, nil })
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "s1abcdef", "text": "看一下 CI"})
	if err != nil || !strings.HasPrefix(res, "Queued for") {
		t.Fatalf("queued: %s %v", res, err)
	}
}
