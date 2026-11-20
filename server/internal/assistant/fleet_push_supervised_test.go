package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// supAgent is the Agent the tests drive: its id is long enough for agent_supervise to name it.
const supAgent = "s1agent0"

// supAgentSession is agentSession (feishu_agent_done_test.go) as supAgent.
func supAgentSession() proto.Session {
	sess := agentSession()
	sess.SessionID = supAgent
	return sess
}

// supervisedEnv is a manager whose assistant runs on "server" with the main conversation conv1, the leader conversation
// lead1, the side conversation side1 and the background supervisor bg1 (on unless background is false: then the main
// conversation follows its own tasks up). Tasks are not quiet (main_tasks_quiet off), so nothing but supervision keeps
// a finished turn from the pushes. The Lark bot is paired with its pushes switch on, and the Agent supAgent is listed on mac.
// logs keeps what the assistant logged.
type supervisedEnv struct {
	*fleetTest
	mu   sync.Mutex
	logs []string
}

func newSupervisedEnv(t *testing.T, background bool) *supervisedEnv {
	t.Helper()
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	d.add("server", "Server")
	st := newFleetMem()
	e := &supervisedEnv{}
	inner := testLogf(t)
	logf := func(format string, args ...any) {
		e.mu.Lock()
		e.logs = append(e.logs, fmt.Sprintf(format, args...))
		e.mu.Unlock()
		inner(format, args...)
	}
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: logf, Devices: d})
	e.fleetTest = &fleetTest{t: t, s: s, d: d, f: f, st: st}
	shortSupervise(t, s)
	noDirectionsTimers(s)
	fastDone(e.fleetTest)
	answerAgentDoneBot(e.fleetTest, true)
	f.hello.Assistant.MachineID = "server"
	var convs []proto.Session
	for _, id := range []string{"conv1", "lead1", "side1", "bg1"} {
		f.known[id] = true
		convs = append(convs, proto.Session{SessionID: id, MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle})
	}
	e.sessions("server", convs...)
	f.setSessions("server", convs...)
	idle := supAgentSession()
	idle.MachineID, idle.Status = "mac", proto.StatusIdle
	e.sessions("mac", idle)
	f.setSessions("mac", idle)
	setSettings(t, s, func(v *Settings) {
		v.Main, v.MachineID, v.Leaders = "conv1", "server", []string{"lead1"}
		off := false
		v.MainTasksQuiet = &off
		if background {
			v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
		} else {
			on := true
			v.MainSupervisesOwnTasks = &on
		}
	})
	return e
}

// task is a to-do opened from conversation origin with the Agent supAgent on it, its ledger naming supervisor ("": none), as
// the start of a planned task writes it (tasks_start.go: superviseTodo, then the Agent linked).
func (e *supervisedEnv) task(origin, supervisor string) Todo {
	e.t.Helper()
	ctx := context.Background()
	todo, err := e.s.createTodo(ctx, "修登录崩溃", "iPad 横屏时登录页崩溃", origin, "")
	if err != nil {
		e.t.Fatal(err)
	}
	if supervisor != "" {
		e.s.superviseTodo(ctx, todo.ID, supervisor, []string{"PR 合并到 main"}, 30)
	}
	if err := e.s.linkTodoAgent(ctx, todo.ID, "mac", supAgent, "修登录", "/code/app", "claude"); err != nil {
		e.t.Fatal(err)
	}
	return todoNow(e.s, todo.ID)
}

// agentDoneCards are the Lark cards posted for an Agent's finished turn (they carry the reply hint, #5438).
func (e *supervisedEnv) agentDoneCards() []map[string]any {
	hint := i18n.T("", "assistant-feishu_cards.agent_done_reply_hint")
	var out []map[string]any
	for _, p := range e.f.appParams(AppFeishu, FeishuPost) {
		if card, _ := p["card"].(string); strings.Contains(card, hint) {
			out = append(out, p)
		}
	}
	return out
}

// holdDone puts the finished turn's delay out of the test's reach (an hour), so finishNow decides it instead.
func (e *supervisedEnv) holdDone() {
	e.f.mu.Lock()
	e.f.hello.Notify.DoneDelayMS = 3_600_000
	e.f.mu.Unlock()
}

// finishNow runs the delayed decision on supAgent's latest turn end now (notifyFinishedNow), as its timer would, so the
// check that follows does not depend on the delay's timer firing in time.
func (e *supervisedEnv) finishNow() {
	e.t.Helper()
	book := e.s.turnsBook()
	gen := 0
	waitFor(e.t, "the turn's end tracked", func() bool {
		book.mu.Lock()
		defer book.mu.Unlock()
		tr := book.turns["mac/"+supAgent]
		if tr == nil {
			return false
		}
		gen = tr.gen
		return true
	})
	e.s.notifyFinishedNow("mac", supAgent, gen, e.s.notifyTimings())
	// A push and a Feishu card the decision made go out on goroutines of their own (deliver, feishuAgentDone), and
	// nothing marks one that was never started: give them a moment to land before a check that none did.
	time.Sleep(100 * time.Millisecond) // sleep-ok: a negative check, no event marks a push or card that was not sent
}

// donePushes are the phone's "finished" pushes for the Agent supAgent.
func (e *supervisedEnv) donePushes() []Notification {
	var out []Notification
	for _, p := range e.pushesOf(assistantwire.NotifyDone, 0, 0) {
		if p.SessionID == supAgent {
			out = append(out, p)
		}
	}
	return out
}

// oneDonePush checks that supAgent's finished turn was pushed to the phone once. deliver sends the push on a goroutine
// of its own, apart from the Lark card's (feishuAgentDone), so a card that has landed says nothing of the push: wait
// for the push as for the card, then count (#8684).
func (e *supervisedEnv) oneDonePush(t *testing.T) {
	t.Helper()
	waitFor(t, "the finished push", func() bool { return len(e.donePushes()) >= 1 })
	if got := e.donePushes(); len(got) != 1 {
		t.Fatalf("pushed %d finished pushes, want 1: %+v", len(got), got)
	}
}

// logged are the lines logged so far that contain all of want.
func (e *supervisedEnv) logged(want ...string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
next:
	for _, l := range e.logs {
		for _, w := range want {
			if !strings.Contains(l, w) {
				continue next
			}
		}
		out = append(out, l)
	}
	return out
}

// A turn of an Agent on a supervised task sends neither the phone's "finished" push nor the Lark card, whoever
// supervises it (#8270): its supervisor follows the stop up and tells the owner what is theirs. The to-dos are not
// quiet, so before the fix every one of these posted a card and pushed.
func TestSupervisedAgentFinishIsNotPushed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		background bool
		supervise  func(e *supervisedEnv)
	}{
		{"a leader conversation's task", true, func(e *supervisedEnv) { e.task("lead1", "lead1") }},
		{"the main conversation's task handed to a leader", true, func(e *supervisedEnv) { e.task("conv1", "lead1") }},
		{"the main conversation's task, followed up by the background supervisor", true, func(e *supervisedEnv) { e.task("conv1", "conv1") }},
		{"the main conversation's task, followed up by the main conversation", false, func(e *supervisedEnv) { e.task("conv1", "conv1") }},
		{"a side conversation's own task", true, func(e *supervisedEnv) { e.task("side1", "side1") }},
		{"a task leader's worker", true, func(e *supervisedEnv) { e.task(leaderRef("mac", "lead-agent"), leaderRef("mac", "lead-agent")) }},
		{"an Agent taken in with agent_supervise", true, func(e *supervisedEnv) {
			raw, _ := json.Marshal(map[string]any{"machine": "mac", "session_id": supAgent, "done_when": []string{"PR 合并到 main"}})
			if _, err := e.s.runTool(context.Background(), "server", "conv1", "agent_supervise", raw); err != nil {
				e.t.Fatal(err)
			}
			if todo := onlyTodo(e.s); todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" || todo.Quiet {
				e.t.Fatalf("not a supervised, unquiet task: %+v", todo)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSupervisedEnv(t, tc.background)
			tc.supervise(e)
			if e.s.quietSession("mac", supAgent) {
				t.Fatal("the Agent is quiet: the test would not show supervision keeps the push")
			}
			e.holdDone()
			for _, last := range []string{"改好了", "又改了一处"} { // every turn, not only the first
				sess := supAgentSession()
				sess.LastText = last
				driveAgentDone(e.fleetTest, sess)
				e.finishNow()
			}
			if got := e.agentDoneCards(); len(got) != 0 {
				t.Fatalf("posted %d finished cards, want none: %+v", len(got), got)
			}
			if got := e.donePushes(); len(got) != 0 {
				t.Fatalf("pushed %d finished pushes, want none: %+v", len(got), got)
			}
			if got := e.logged("kind=agent_done"); len(got) != 0 {
				t.Fatalf("logged a finished card sent: %q", got)
			}
		})
	}
}

// An Agent nobody supervises (on no to-do, or on a to-do the user made, which has no ledger) is pushed and gets its
// Lark card as before, and the card that lands is logged with its kind, computer and session.
func TestUnsupervisedAgentFinishStillPushed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *supervisedEnv)
	}{
		{"no to-do", func(*supervisedEnv) {}},
		{"the user's own to-do", func(e *supervisedEnv) { e.task("", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSupervisedEnv(t, true)
			tc.setup(e)
			driveAgentDone(e.fleetTest, supAgentSession())
			waitFor(t, "the finished card", func() bool { return len(e.agentDoneCards()) == 1 })
			e.oneDonePush(t)
			waitFor(t, "the line saying it was sent", func() bool {
				return len(e.logged("bots: card sent: kind=agent_done machine=mac session="+supAgent+" message=om_done")) == 1
			})
		})
	}
}

// A supervised task closed while its Agent's last turn waited for the push's delay (accepted, as the done judge or the
// supervisor may do at once, or cancelled) is that turn's all the same: no card and no push for the turn that ended it.
// A cancelled task stays closed when its Agent works again, so the Agent's next turn, started after the close, is its
// own again and is pushed.
func TestSupervisedTaskClosedWhileThePushWaits(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprintf("accepted=%v", accepted), func(t *testing.T) {
			e := newSupervisedEnv(t, true)
			e.holdDone() // the turn's push waits; the task is closed meanwhile, then the wait ends (finishNow)
			todo := e.task("conv1", "conv1")
			driveAgentDone(e.fleetTest, supAgentSession())
			if ok, err := e.s.reviewDoneTodo(context.Background(), todo.ID, "验收通过", accepted); err != nil || !ok {
				t.Fatalf("close: %v %v", ok, err)
			}
			if e.s.activeTodoOf("mac", supAgent) != "" {
				t.Fatal("the Agent still works on an open to-do")
			}
			e.finishNow()
			if got := e.agentDoneCards(); len(got) != 0 {
				t.Fatalf("posted %d finished cards, want none: %+v", len(got), got)
			}
			if got := e.donePushes(); len(got) != 0 {
				t.Fatalf("pushed %d finished pushes, want none: %+v", len(got), got)
			}
			if accepted {
				return // an accepted task reopens when its Agent works again (closedTaskOf): its next turn is supervised
			}
			fastDone(e.fleetTest)
			sess := supAgentSession()
			sess.LastText = "后来又问了它一句"
			driveAgentDone(e.fleetTest, sess)
			waitFor(t, "the later turn's card", func() bool { return len(e.agentDoneCards()) == 1 })
			e.oneDonePush(t)
		})
	}
}

// notify_user's Lark copy is not a finished turn's: the supervisor's notice reaches the phone and Lark as before while
// its task's Agent finishes unpushed, and the copy that lands is logged as a push of that conversation.
func TestSupervisorNotifyUserStillCopiedToLark(t *testing.T) {
	e := newSupervisedEnv(t, true)
	e.task("conv1", "conv1")
	e.holdDone()
	driveAgentDone(e.fleetTest, supAgentSession())
	e.finishNow() // the Agent's turn ends unpushed before the supervisor's notice
	raw, _ := json.Marshal(map[string]any{"title": "登录修好了", "body": "PR 已合并，等你看一眼。"})
	if _, err := e.s.runTool(context.Background(), "server", "conv1", "notify_user", raw); err != nil {
		t.Fatal(err)
	}
	var push []map[string]any
	waitFor(t, "the notice's Lark card", func() bool {
		push = nil
		for _, p := range e.f.appParams(AppFeishu, FeishuPost) {
			if card, _ := p["card"].(string); strings.Contains(card, `"content":"通知"`) && strings.Contains(card, "登录修好了") {
				push = append(push, p)
			}
		}
		return len(push) == 1
	})
	if got := e.pushesOf(assistantwire.NotifyPush, 1, time.Second); len(got) != 1 || got[0].Title != "登录修好了" {
		t.Fatalf("phone push: %+v", got)
	}
	waitFor(t, "the line saying it was sent", func() bool {
		return len(e.logged("bots: card sent: kind=push machine=server session=conv1 message=om_done")) == 1
	})
	if got := e.agentDoneCards(); len(got) != 0 {
		t.Fatalf("posted %d finished cards, want none: %+v", len(got), got)
	}
	if got := e.donePushes(); len(got) != 0 {
		t.Fatalf("pushed %d finished pushes, want none: %+v", len(got), got)
	}
}

// A card that does not land is not logged as sent (its sender logs the error, as before); one posted with no label is
// logged as kind "other", with no computer or session.
func TestCardSentLogLine(t *testing.T) {
	e := newSupervisedEnv(t, true)
	ctx := context.Background()
	e.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return nil, errors.New("feishu down") })
	if _, err := e.s.botPost(ctx, "", feishubot.NewCard("", "", "测试"), ""); err == nil {
		t.Fatal("the post did not fail")
	}
	if got := e.logged("bots: card sent"); len(got) != 0 {
		t.Fatalf("logged a card that did not land: %q", got)
	}
	e.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_x"}, nil })
	if _, err := e.s.botPost(ctx, "", feishubot.NewCard("", "", "测试"), ""); err != nil {
		t.Fatal(err)
	}
	if got := e.logged("bots: card sent: kind=other machine=- session=- message=om_x"); len(got) != 1 {
		t.Fatalf("logged %q", e.logged("bots: card sent"))
	}
}
