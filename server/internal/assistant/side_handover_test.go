package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sideReviewFixture is a task side conversation side1 handed out, finished by its agent: it waits for side1's review,
// and side1 got the supervision message for it. conv1 is the main conversation; both are idle.
func sideReviewFixture(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietService(t)
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	f.known["side1"] = true
	listSide(f, proto.StatusIdle)
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "这个仓库最近在 box 上", "done_when": []string{"PR 合并到 main"}, "expect_minutes": 30,
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "side1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	todo := onlyTodo(s)
	waitFor(t, "the side conversation's supervision message", func() bool { return reviewsTold(f, "side1", todo.ID) == 1 })
	if v := todoNow(s, todo.ID).Supervision; v == nil || v.Supervisor != "side1" || v.State != reviewPending {
		t.Fatalf("ledger = %+v, want side1's task waiting for review", v)
	}
	return s, f, todoNow(s, todo.ID)
}

// listSide has the assistant's machine list the main conversation conv1, idle, and the side conversation side1 in
// status.
func listSide(f *fakeHub, status string) {
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: status})
}

// reviewsTold counts the supervision messages sent into conversation conv that name task id.
func reviewsTold(f *fakeHub, conv, id string) int {
	n := 0
	for _, text := range f.noticesTo(conv) {
		if strings.Contains(text, "📋 督办") && strings.Contains(text, id) {
			n++
		}
	}
	return n
}

// A review a side conversation leaves waiting is handed to the main conversation's follow-up (#8043). Within the
// bound, and while the side conversation is in a turn (it may be the review), the task stays its own. Past the bound
// with the conversation stopped, the ledger names the main conversation, the origin stays, and the main conversation is
// told once, with why; later passes tell nobody again. The main conversation's review closes the task and the side
// conversation gets its end notice, as for any task the main conversation supervises for another conversation.
func TestReviewLeftWaitingByASideConversationGoesToTheMainConversation(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	supervisor := func() string { return todoNow(s, todo.ID).Supervision.Supervisor }

	s.superviseWatch(ctx, time.Now().Add(10*time.Minute))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q after 10 minutes: the side conversation still has time to review", got)
	}
	listSide(f, proto.StatusWorking)
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the side conversation is in a turn", got)
	}

	listSide(f, proto.StatusIdle)
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	got := todoNow(s, todo.ID)
	if v := got.Supervision; v.Supervisor != "conv1" || v.State != reviewPending || got.Origin != "side1" || got.DoneAt != nil {
		t.Fatalf("not handed over as it was: origin %q, done %v, ledger %+v", got.Origin, got.DoneAt, v)
	}
	waitFor(t, "the review told to the main conversation", func() bool { return reviewsTold(f, "conv1", todo.ID) == 1 })
	var told string
	for _, text := range f.noticesTo("conv1") {
		if strings.Contains(text, todo.ID) {
			told = text
		}
	}
	if !strings.Contains(told, "待逐项复核，不代表完成") || !strings.Contains(told, "转给你验收") {
		t.Fatalf("the main conversation is not told that the review is handed to it, and why:\n%s", told)
	}

	// Once: the next passes find a task of the main conversation, and its review was told.
	s.superviseWatch(ctx, time.Now().Add(90*time.Minute))
	s.superviseWatch(ctx, time.Now().Add(2*time.Hour))
	s.sendDigest(ctx)
	if n, m := reviewsTold(f, "conv1", todo.ID), reviewsTold(f, "side1", todo.ID); n != 1 || m != 1 {
		t.Fatalf("told the main conversation %d time(s) and the side conversation %d: want once each", n, m)
	}

	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "PR 已合并",
		"checks": []map[string]any{{"criterion": "PR 合并到 main", "met": true, "evidence": "pull/7"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task closed", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	waitFor(t, "the side conversation's end notice", func() bool { return len(endNotices(f, "side1")) == 1 })
}

// With the background supervisor on, as it is by default, the handed-over review goes to it and not to the main
// conversation: the ledger names the main conversation, whose own tasks the background supervisor follows up.
func TestReviewLeftWaitingByASideConversationGoesToTheBackgroundSupervisor(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	s.noBackground = false
	f.mu.Lock()
	f.made = 100 // the conversations the fake starts get ids from s101 on, not the agent's
	f.mu.Unlock()

	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "conv1" {
		t.Fatalf("the ledger names %q, want the main conversation", got)
	}
	id := backgroundID(t, s)
	listBackground(t, s, f, id)
	waitFor(t, "the review told to the background supervisor", func() bool { return reviewsTold(f, id, todo.ID) == 1 })
	if n := reviewsTold(f, "conv1", todo.ID); n != 0 {
		t.Fatalf("the main conversation got %d supervision message(s) for a task the background supervisor follows up", n)
	}
	if _, err := runJSON(t, s, id, "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "PR 已合并",
		"checks": []map[string]any{{"criterion": "PR 合并到 main", "met": true, "evidence": "pull/7"}}}); err != nil {
		t.Fatalf("the background supervisor cannot review the task handed to it: %v", err)
	}
	waitFor(t, "the task closed", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}

// A side conversation that is gone from its machine's list no longer reviews anything: its review is handed over too.
// While the machine cannot say (it is away, or has listed no assistant conversation yet, as after a manager's start)
// nothing moves.
func TestReviewOfASideConversationThatIsGoneIsHandedOver(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	supervisor := func() string { return todoNow(s, todo.ID).Supervision.Supervisor }

	f.setSessions("server") // nothing listed yet
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the machine has listed no assistant conversation", got)
	}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.mu.Lock()
	f.down = map[string]bool{"server": true}
	f.mu.Unlock()
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the assistant's machine is away", got)
	}
	f.mu.Lock()
	f.down = nil
	f.mu.Unlock()

	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "conv1" {
		t.Fatalf("the review of a conversation that is gone stays with %q", got)
	}
	waitFor(t, "the review told to the main conversation", func() bool { return reviewsTold(f, "conv1", todo.ID) == 1 })
}

// A leader conversation keeps the reviews of its tasks however long they wait: the ledger's supervisor is what places
// a task in its direction, so handing the review over would take the task out of the direction the owner set up.
func TestReviewLeftWaitingByALeaderStaysWithIt(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"side1"} })

	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	s.sendDigest(ctx)
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "side1" {
		t.Fatalf("a leader's review was handed to %q", got)
	}
	if n := reviewsTold(f, "conv1", todo.ID); n != 0 {
		t.Fatalf("the main conversation got %d supervision message(s) for a leader's task", n)
	}
}
