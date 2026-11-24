package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// handoverFixture is a supervised task of the main conversation conv1 waiting for review, a task conv1 handed to the
// leader lead1, a side conversation's task, and a closed task of conv1.
func handoverFixture(t *testing.T) (s *Service, f *fakeHub, todo, led, side, closed Todo) {
	t.Helper()
	s, f, todo = newSupervisedService(t)
	ctx := context.Background()
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了，提了 PR", 0)
	waitFor(t, "the review state", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	mk := func(title, origin, supervisor string) Todo {
		x, err := s.newTodo(ctx, title, "", origin, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if supervisor != "" {
			s.superviseTodo(ctx, x.ID, supervisor, []string{"做完"}, 30)
		}
		return todoNow(s, x.ID)
	}
	led = mk("交给 leader 的", "conv1", "lead1")
	side = mk("旁路对话的", "side1", "side1")
	closed = mk("关掉的", "conv1", "conv1")
	done := true
	if _, err := s.editTodo(ctx, closed.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	return s, f, todoNow(s, todo.ID), led, side, todoNow(s, closed.ID)
}

// Putting the main conversation away and starting the next one hands its open tasks over (#4866): the supervisor and
// the origin move together, the ledger is kept, a task waiting for review is told to the new conversation, and its card
// goes there. A leader keeps its task (only where the task's receipts go moves); another conversation's task and a
// closed one stay as they are. The new main conversation is no outsider to the task's agent any more.
func TestNewMainConversationTakesOverOpenTasks(t *testing.T) {
	s, f, todo, led, side, closed := handoverFixture(t)
	ctx := context.Background()
	before := *todo.Supervision
	if old, _, err := s.newMain(ctx, false, "conv1"); err != nil || old != "conv1" {
		t.Fatalf("new main: %q %v", old, err)
	}
	sess, err := s.start(ctx, StartParams{Main: true})
	if err != nil || sess.SessionID == "" || s.settings(ctx).Main != sess.SessionID {
		t.Fatalf("started: %+v %v", sess, err)
	}
	next := sess.SessionID
	f.known[next] = true
	f.setSessions("server", proto.Session{SessionID: next, MachineID: "server", Assistant: true, Status: proto.StatusIdle})

	got := todoNow(s, todo.ID)
	if got.Origin != next || got.Supervision == nil || got.Supervision.Supervisor != next {
		t.Fatalf("not handed over: origin %q, supervision %+v", got.Origin, got.Supervision)
	}
	v := got.Supervision
	if v.State != reviewPending || v.Round != before.Round || strings.Join(v.DoneWhen, "|") != strings.Join(before.DoneWhen, "|") || got.DoneAt != nil {
		t.Fatalf("the ledger changed: %+v (was %+v)", v, before)
	}
	if !strings.Contains(feedKinds(s, next), feedTask) {
		t.Fatalf("no card in the new conversation: %s", feedKinds(s, next))
	}
	waitFor(t, "the review told to the new main conversation", func() bool {
		return strings.Contains(strings.Join(f.noticesTo(next), "\n"), todo.ID)
	})

	if l := todoNow(s, led.ID); l.Origin != next || l.Supervision.Supervisor != "lead1" {
		t.Fatalf("the leader's task: origin %q, supervisor %q", l.Origin, l.Supervision.Supervisor)
	}
	if n := strings.Count(feedKinds(s, next), feedTask); n != 1 {
		t.Fatalf("%d cards in the new conversation (only the task it supervises now has one)", n)
	}
	if x := todoNow(s, side.ID); x.Origin != "side1" || x.Supervision.Supervisor != "side1" {
		t.Fatalf("a side conversation's task moved: origin %q, supervisor %q", x.Origin, x.Supervision.Supervisor)
	}
	if c := todoNow(s, closed.ID); c.Origin != "conv1" || c.Supervision.Supervisor != "conv1" {
		t.Fatalf("a closed task moved: origin %q, supervisor %q", c.Origin, c.Supervision.Supervisor)
	}

	// Its agent is the new main conversation's now: a reply goes as the supervisor's, not as a side conversation, and
	// agent_supervise may change its criteria.
	agent := proto.Session{SessionID: "s1", MachineID: "box"}
	if _, elsewhere, err := s.supervisedElsewhere(ctx, next, Device{ID: "box"}, agent); err != nil || elsewhere {
		t.Fatalf("supervised elsewhere: %v %v", elsewhere, err)
	}
	if err := othersTask(agent, got, next); err != nil {
		t.Fatal(err)
	}
	if err := othersTask(agent, got, "conv1"); err == nil {
		t.Fatal("the archived conversation still holds the task")
	}
}

// A switch made before tasks moved with the main conversation (or one cut short) is finished by the supervision loop's
// pass: what a former main conversation still holds goes to the main one, once.
func TestFormerMainConversationTasksHandedOverByThePass(t *testing.T) {
	s, f, todo, led, side, _ := handoverFixture(t)
	ctx := context.Background()
	f.known["conv2"] = true
	f.setSessions("server", proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) {
		v.Main = "conv2"
		v.Former = []FormerMain{{MachineID: "server", SessionID: "conv1", Until: time.Now().UTC()}}
	})
	if x := todoNow(s, todo.ID); x.Supervision.Supervisor != "conv1" {
		t.Fatalf("moved before the pass: %q", x.Supervision.Supervisor)
	}
	s.handOverFormerMainTasks(ctx)
	if x := todoNow(s, todo.ID); x.Origin != "conv2" || x.Supervision.Supervisor != "conv2" || x.Supervision.State != reviewPending {
		t.Fatalf("after the pass: origin %q, supervision %+v", x.Origin, x.Supervision)
	}
	if l := todoNow(s, led.ID); l.Origin != "conv2" || l.Supervision.Supervisor != "lead1" {
		t.Fatalf("the leader's task: origin %q, supervisor %q", l.Origin, l.Supervision.Supervisor)
	}
	if x := todoNow(s, side.ID); x.Origin != "side1" {
		t.Fatalf("a side conversation's task moved: %q", x.Origin)
	}
	cards := strings.Count(feedKinds(s, "conv2"), feedTask)
	if n := s.handOverMainTasks(ctx, "conv1", "conv2"); n != 0 {
		t.Fatalf("a second pass moved %d", n)
	}
	s.handOverFormerMainTasks(ctx)
	if again := strings.Count(feedKinds(s, "conv2"), feedTask); again != cards {
		t.Fatalf("cards %d → %d on a second pass", cards, again)
	}
	// A task already moved by hand ("Move to…": its supervisor and card are the main conversation's) only has its origin
	// moved: no second card.
	moved, err := s.newTodo(ctx, "已经手动转过的", "", "conv1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, moved.ID, "conv2", []string{"做完"}, 30)
	s.taskCard(ctx, "conv2", moved.ID, wakeActive)
	cards = strings.Count(feedKinds(s, "conv2"), feedTask)
	s.handOverFormerMainTasks(ctx)
	if x := todoNow(s, moved.ID); x.Origin != "conv2" || x.Supervision.Supervisor != "conv2" || strings.Count(feedKinds(s, "conv2"), feedTask) != cards {
		t.Fatalf("a task moved by hand: origin %q, supervisor %q, cards %d → %d", x.Origin, x.Supervision.Supervisor, cards,
			strings.Count(feedKinds(s, "conv2"), feedTask))
	}
	// A former main conversation that is a leader now keeps what it supervises.
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1", "conv0"} })
	kept, err := s.newTodo(ctx, "leader 自己的", "", "conv0", "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, kept.ID, "conv0", []string{"做完"}, 30)
	if n := s.handOverMainTasks(ctx, "conv0", "conv2"); n != 0 || todoNow(s, kept.ID).Supervision.Supervisor != "conv0" {
		t.Fatalf("a leader's own task moved (%d)", n)
	}
}

// Settings making another conversation the main one hand the open tasks of the one before over too.
func TestSettingsMainSwitchHandsOverTasks(t *testing.T) {
	s, f, todo, _, _, _ := handoverFixture(t)
	f.known["conv2"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	body, _ := json.Marshal(map[string]any{"main": "conv2"})
	w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "", "full", string(body))
	if w.Code != 200 || view.Main != "conv2" {
		t.Fatalf("PUT main: %d %s", w.Code, w.Body.String())
	}
	if x := todoNow(s, todo.ID); x.Origin != "conv2" || x.Supervision.Supervisor != "conv2" {
		t.Fatalf("after the switch: origin %q, supervisor %q", x.Origin, x.Supervision.Supervisor)
	}
}

// A main switch moves the task ledger to the new main conversation. New digests go there, while an existing background
// supervisor may still finish work already delegated to it (#5732).
func TestMainTaskHandoverKeepsMainSupervision(t *testing.T) {
	s, f, todo, led, _, _ := handoverFixture(t)
	ctx := context.Background()
	s.noBackground = false
	f.known["conv2"], f.known["bg1"] = true, true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "server"
	})
	w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "", "full", `{"main":"conv2"}`)
	if w.Code != 200 || view.Main != "conv2" {
		t.Fatalf("PUT main: %d %s", w.Code, w.Body.String())
	}
	got := todoNow(s, todo.ID)
	if got.Origin != "conv2" || got.Supervision.Supervisor != "conv2" || got.Supervision.State != reviewPending {
		t.Fatalf("task after the switch: origin %q, supervision %+v", got.Origin, got.Supervision)
	}
	if wake := taskCardWake(s, "conv2", todo.ID); wake != wakeActive {
		t.Fatalf("the transferred task's card wake = %q", wake)
	}
	if to := s.digestTo(ctx, s.settings(ctx), superviseSignal{Todo: todo.ID, Kind: sigReview}); to != "conv2" {
		t.Fatalf("review routed to %q", to)
	}
	if to := s.digestTo(ctx, s.settings(ctx), superviseSignal{Todo: led.ID, Kind: sigReview}); to != "lead1" {
		t.Fatalf("the leader's review routed to %q", to)
	}
	waitFor(t, "the transferred task's review sent to the new main conversation", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv2"), "\n"), todo.ID)
	})
	// Closeout is a legacy exception only after an old notice actually reached the background conversation.
	tellLegacyBackground(t, s, f, todo.ID, "bg1")
	agent := proto.Session{SessionID: "s1", MachineID: "box"}
	if _, elsewhere, err := s.supervisedElsewhere(ctx, "bg1", Device{ID: "box"}, agent); err != nil || elsewhere {
		t.Fatalf("the background supervisor lost its agent: elsewhere %v, %v", elsewhere, err)
	}
	if res, err := runJSON(t, s, "bg1", "task_review", map[string]any{
		"todo_id": todo.ID, "verdict": "accepted", "note": "Verified after the main conversation changed",
		"checks": completeReviewChecks(todoNow(s, todo.ID)),
	}); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("background acceptance: %s %v", res, err)
	}
	got = todoNow(s, todo.ID)
	if got.DoneAt == nil || got.Supervision.State != reviewAccepted || got.Origin != "conv2" || got.Supervision.Supervisor != "conv2" {
		t.Fatalf("accepted task: %+v, supervision %+v", got, got.Supervision)
	}
}
