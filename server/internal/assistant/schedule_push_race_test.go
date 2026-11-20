package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// End-to-end cases for an asked patrol posting into a leader conversation (#3673). The fake hub captures every
// notification; no device is contacted. Items follow the order the node's transcript reader gives them: a user
// message absorbed mid-turn is a queued item before the turn's final answer; one held until the turn ended starts the
// next turn as an ordinary user input.

type patrolEnv struct {
	t    *testing.T
	ft   *fleetTest
	task Task
	sess proto.Session
}

func newPatrolEnv(t *testing.T) *patrolEnv {
	t.Helper()
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	ft.f.withMachine()
	ft.f.fakeModel(t)
	setSettings(t, ft.s, func(v *Settings) { v.Main, v.MachineID = "main-1", "mac" })
	task := Task{ID: "fu_patrol", Title: "Patrol", Prompt: "Check the pending work", Repeat: repeatInterval,
		EveryMinutes: 30, At: time.Now().Add(time.Hour), Enabled: true, MachineID: "mac", SessionID: "lead1", Push: pushAsked}
	setTasks(ft.s, task)
	sess := proto.Session{SessionID: "lead1", Title: "Project", Cwd: "/home/u/.persona/assistant", Assistant: true,
		Status: proto.StatusDone, LastText: "Previous answer", UpdatedAt: time.Now(), Rev: 1}
	ft.sessions("mac", sess)
	ft.f.sessions["mac"] = []proto.Session{sess}
	ft.f.known[sess.SessionID] = true // the listed leader has a transcript that resume_only may continue
	return &patrolEnv{t: t, ft: ft, task: task, sess: sess}
}

// run sends the patrol into lead1 as the scheduler does.
func (e *patrolEnv) run() {
	e.t.Helper()
	if _, err := e.ft.s.runTask(e.task, time.Now(), time.Now(), true, false); err != nil {
		e.t.Fatal(err)
	}
	if p := e.ft.f.lastSend(e.t); p["session_id"] != "lead1" {
		e.t.Fatal("the patrol did not go into its leader conversation")
	}
}

func (e *patrolEnv) patrolInput() proto.Item {
	return proto.Item{Role: "user", Kind: proto.KindText, Text: prompts.MessageSourceText(e.task.Prompt, taskMessageSource(e.task))}
}

// working shows lead1 on a turn.
func (e *patrolEnv) working() {
	e.sess.Status, e.sess.Rev, e.sess.UpdatedAt = proto.StatusWorking, e.sess.Rev+1, e.sess.UpdatedAt.Add(time.Second)
	e.ft.sessions("mac", e.sess)
}

// done ends the turn with items as the node pages them; lastText is what the node's list shows (a snippet).
func (e *patrolEnv) done(items []proto.Item, lastText string) {
	e.ft.f.mu.Lock()
	e.ft.f.items = items
	e.ft.f.mu.Unlock()
	e.sess.Status, e.sess.LastText, e.sess.Rev, e.sess.UpdatedAt = proto.StatusDone, lastText, e.sess.Rev+1, e.sess.UpdatedAt.Add(time.Second)
	e.ft.sessions("mac", e.sess)
}

// pushes waits out the finished push's delay and counts the completion notifications so far.
func (e *patrolEnv) pushes(want int) int {
	return len(e.ft.pushesOf(assistantwire.NotifyDone, want, 600*time.Millisecond))
}

func (e *patrolEnv) mirrored() bool {
	return !e.ft.s.quietTaskTurn(context.Background(), "mac", "lead1")
}

// snippet is how the node's list shows a long or multi-line answer (node/sessions.go): runs of white space become one
// space, and more than 200 runes are cut.
func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// The user writes while the patrol works and the message waits for the turn to end: the patrol's answer stays quiet,
// the turn the message then starts is the user's and is pushed.
func TestQuietPatrolThenUsersHeldMessage(t *testing.T) {
	e := newPatrolEnv(t)
	e.run()
	e.working()
	e.ft.s.UserSpoke(context.Background(), SessionRef{MachineID: "mac", SessionID: "lead1"})
	e.done([]proto.Item{e.patrolInput(), {Role: "assistant", Kind: proto.KindText, Text: "Nothing new"}}, "Nothing new")
	if n := e.pushes(1); n != 0 {
		t.Errorf("the patrol's answer was pushed after the user wrote: %d notifications, want 0", n)
	}
	if e.mirrored() {
		t.Error("the patrol's answer would be mirrored to channels")
	}
	e.working()
	e.done([]proto.Item{{Role: "user", Kind: proto.KindText, Text: "Show me the details"},
		{Role: "assistant", Kind: proto.KindText, Text: "Here are the details"}}, "Here are the details")
	got := e.ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
	if len(got) != 1 || got[0].Body != "Here are the details" {
		t.Errorf("the user's own turn: %d notifications, want 1 with its answer", len(got))
	}
	if !e.mirrored() {
		t.Error("the user's own turn would be hidden from channels")
	}
}

// The user writes while the patrol works and the patrol's turn absorbs it: the final answer is the user's answer too.
func TestQuietPatrolAbsorbedUserQuestionPushes(t *testing.T) {
	e := newPatrolEnv(t)
	e.run()
	e.working()
	e.ft.s.UserSpoke(context.Background(), SessionRef{MachineID: "mac", SessionID: "lead1"})
	e.done([]proto.Item{e.patrolInput(),
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu1"},
		{Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1", Text: "ok"},
		{Role: "user", Kind: proto.KindText, Text: "MR 12 合了吗？", Queued: true},
		{Role: "assistant", Kind: proto.KindText, Text: "MR 12 已合入。"}}, "MR 12 已合入。")
	if n := e.pushes(1); n != 1 {
		t.Errorf("an answer that took in the user's question: %d notifications, want 1", n)
	}
	if !e.mirrored() {
		t.Error("an answer that took in the user's question would be hidden from channels")
	}
}

// A patrol answer the node's list shows only as a snippet (several lines, more than 200 runes) stays quiet.
func TestQuietPatrolLongAndMultilineAnswers(t *testing.T) {
	for _, answer := range []string{"Nothing new.\n- MR 1: waiting\n- MR 2: waiting", strings.Repeat("无新情况", 60)} {
		e := newPatrolEnv(t)
		e.run()
		e.working()
		e.done([]proto.Item{e.patrolInput(), {Role: "assistant", Kind: proto.KindText, Text: answer}}, snippet(answer))
		if n := e.pushes(1); n != 0 {
			t.Errorf("%d-rune patrol answer: %d notifications, want 0", len([]rune(answer)), n)
		}
		if e.mirrored() {
			t.Errorf("%d-rune patrol answer would be mirrored", len([]rune(answer)))
		}
	}
}

// Two patrols in a row, nothing new each time: no notification at all.
func TestQuietPatrolTwiceInARow(t *testing.T) {
	e := newPatrolEnv(t)
	for run := 1; run <= 2; run++ {
		e.run()
		e.working()
		e.done([]proto.Item{e.patrolInput(), {Role: "assistant", Kind: proto.KindText, Text: "Nothing new"}}, "Nothing new")
		if n := e.pushes(1); n != 0 {
			t.Errorf("patrol %d: %d notifications, want 0", run, n)
		}
		if e.mirrored() {
			t.Errorf("patrol %d would be mirrored", run)
		}
	}
}

// A background task wakes the conversation after the patrol answered. One the user started earlier reports the user's
// result: mirrored as ever. One the patrol itself launched is the patrol's: still quiet.
func TestQuietPatrolWakeByABackgroundTask(t *testing.T) {
	e := newPatrolEnv(t)
	e.run()
	e.working()
	e.done([]proto.Item{e.patrolInput(), {Role: "assistant", Kind: proto.KindText, Text: "Nothing new"},
		{Role: "user", Kind: proto.KindNotice, Text: "Background command finished", Wake: true,
			Task: &proto.TaskNote{Kind: "shell", Status: "completed", TaskID: "user-bg", ToolUseID: "tu-user"}},
		{Role: "assistant", Kind: proto.KindText, Text: "The tests you started all passed."}}, "The tests you started all passed.")
	if !e.mirrored() {
		t.Error("the result of a task the user started earlier would be hidden from channels")
	}

	e = newPatrolEnv(t)
	e.run()
	e.working()
	e.done([]proto.Item{e.patrolInput(),
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "tu-patrol"},
		{Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu-patrol", Text: "Command running in background with ID: patrol-bg."},
		{Role: "assistant", Kind: proto.KindText, Text: "Baseline running"},
		{Role: "user", Kind: proto.KindNotice, Text: "Background command finished", Wake: true,
			Task: &proto.TaskNote{Kind: "shell", Status: "completed", TaskID: "patrol-bg"}},
		{Role: "assistant", Kind: proto.KindText, Text: "Baseline unchanged"}}, "Baseline unchanged")
	if e.mirrored() {
		t.Error("the result of the patrol's own background task would be mirrored")
	}

	// The patrol checks on the user's earlier task and its result names that task: the task is still the user's.
	e = newPatrolEnv(t)
	e.run()
	e.working()
	e.done([]proto.Item{e.patrolInput(),
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "TaskOutput", ToolUseID: "tu-chk"},
		{Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu-chk", Text: "<task_id>bx1</task_id><status>running</status>"},
		{Role: "assistant", Kind: proto.KindText, Text: "Nothing new"},
		{Role: "user", Kind: proto.KindNotice, Text: "Background command finished", Wake: true,
			Task: &proto.TaskNote{Kind: "shell", Status: "completed", TaskID: "bx1", ToolUseID: "tu-user"}},
		{Role: "assistant", Kind: proto.KindText, Text: "后台测试全部通过。"}}, "后台测试全部通过。")
	if !e.mirrored() {
		t.Error("the result of the user's task, named in a patrol call's result, would be hidden from channels")
	}
}

// The user's turn ends and, before its finished push is decided, the next patrol's input is already in the transcript
// while the session list has not moved: the answer above that input is the user's and is pushed.
func TestQuietPatrolInputWrittenAfterUsersTurn(t *testing.T) {
	e := newPatrolEnv(t)
	e.working()
	e.done([]proto.Item{{Role: "user", Kind: proto.KindText, Text: "帮我看下 MR"},
		{Role: "assistant", Kind: proto.KindText, Text: "MR 已看完。"}, e.patrolInput()}, "MR 已看完。")
	got := e.ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
	if len(got) != 1 || got[0].Body != "MR 已看完。" {
		t.Errorf("the user's answer with a patrol input written after it: %d notifications, want 1 with its answer", len(got))
	}
	if !e.mirrored() {
		t.Error("the user's answer with a patrol input written after it would be hidden from channels")
	}
}
