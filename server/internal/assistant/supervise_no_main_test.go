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

	"github.com/nwverse/persona/server/internal/proto"
)

// noMainLog keeps s's log lines and answers how many contain part.
func noMainLog(s *Service) func(part string) int {
	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	return func(part string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(l, part) {
				n++
			}
		}
		return n
	}
}

// supervisionTo are the supervision messages sent into conversation id that carry signal kind.
func supervisionTo(f *fakeHub, id, kind string) []string {
	var out []string
	for _, n := range f.noticesTo(id) {
		if strings.Contains(n, "📋 督办") && strings.Contains(n, "signal="+kind) {
			out = append(out, n)
		}
	}
	return out
}

// With no main conversation set, a task a side conversation dispatched with task_plan whose start fails (here at its
// workspace) is told to that conversation within the normal digest delay, instead of waiting for a main conversation
// that may never come (#10126). Nothing waits, so the log never says anything waits.
func TestFailedStartWithoutAMainConversationGoesToTheDispatchingConversation(t *testing.T) {
	s, f := newQuietService(t)
	logged := noMainLog(s)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	f.known["side1"] = true
	f.setSessions("server", proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.mu.Lock()
	f.failStart = errors.New("the new task's workspace was not created; no Agent was started")
	f.mu.Unlock()
	raw, err := json.Marshal(briefTask(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlan(ctx, "side1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed start", func() bool { return onlyTodo(s).StartError != "" })
	todo := onlyTodo(s)
	if v := todo.Supervision; v == nil || v.Supervisor != "side1" || todo.Origin != "side1" {
		t.Fatalf("ledger = %+v (origin %q)", todo.Supervision, todo.Origin)
	}

	waitFor(t, "the failed start told to the dispatching conversation", func() bool { return len(supervisionTo(f, "side1", sigFailed)) == 1 })
	if n := supervisionTo(f, "side1", sigFailed)[0]; !strings.Contains(n, todo.ID) || !strings.Contains(n, "workspace was not created") {
		t.Fatalf("message:\n%s", n)
	}
	b := &s.supervise
	b.mu.Lock()
	pending := len(b.pending)
	told := b.sent[superviseSignal{Todo: todo.ID, Kind: sigFailed}.id()] != ""
	b.mu.Unlock()
	if pending != 0 || !told {
		t.Fatalf("after the message: %d signal(s) still pending, failed start marked told %v", pending, told)
	}
	if n := logged("no main conversation"); n != 0 {
		t.Fatalf("logged %d line(s) that signals wait for a main conversation, though the message went out", n)
	}

	// And it is told once: later passes have nothing to send.
	for range 5 {
		s.sendDigest(ctx)
	}
	if n := supervisionTo(f, "side1", sigFailed); len(n) != 1 {
		t.Fatalf("%d supervision messages about the failed start, want 1", len(n))
	}
}

// With no main conversation set, what no conversation can be told waits, as before (#5420): here a task whose
// dispatching conversation is gone. Another task's signal, whose conversation is there, goes out meanwhile, and the
// log counts only what waits. Once the conversation is back, what waited goes to it, and nothing says it still waits.
func TestWithoutAMainConversationOnlyWhatNobodyCanBeToldWaits(t *testing.T) {
	s, f := newQuietService(t)
	logged := noMainLog(s)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	f.known["side1"] = true
	f.setSessions("server", proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	task := func(title, conversation string) Todo {
		todo, err := s.createTodo(ctx, title, "做"+title, conversation, "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, todo.ID, conversation, []string{"PR 合并"}, 0)
		return todo
	}
	here, away := task("甲", "side1"), task("乙", "side2") // side2 is listed nowhere
	at := time.Now().Add(-time.Minute)
	s.signal(superviseSignal{Todo: here.ID, Kind: sigFailed, Key: "1", Detail: "工作区未创建成功", At: at})
	s.signal(superviseSignal{Todo: away.ID, Kind: sigFailed, Key: "1", Detail: "工作区未创建成功", At: at})

	waitFor(t, "the reachable task's message", func() bool { return len(supervisionTo(f, "side1", sigFailed)) == 1 })
	if n := supervisionTo(f, "side1", sigFailed)[0]; !strings.Contains(n, here.ID) || strings.Contains(n, away.ID) {
		t.Fatalf("side1's message:\n%s", n)
	}
	for range 10 { // the watch's passes
		s.sendDigest(ctx)
	}
	if n := logged("no main conversation; 1 signal(s) wait for one"); n != 1 {
		t.Fatalf("logged that one waits %d times, want once", n)
	}
	b := &s.supervise
	b.mu.Lock()
	_, waiting := b.pending[superviseSignal{Todo: away.ID, Kind: sigFailed}.id()]
	told := b.sent[superviseSignal{Todo: away.ID, Kind: sigFailed}.id()] != ""
	b.mu.Unlock()
	if !waiting || told {
		t.Fatalf("the unreachable task's signal: pending %v, marked told %v; want it waiting, untold", waiting, told)
	}
	if n := f.noticesTo("side2"); len(n) != 0 {
		t.Fatalf("sent into a conversation that is not there: %q", n)
	}

	// side2 is back: what waited goes to it, with no line about a main conversation.
	f.known["side2"] = true
	f.setSessions("server", proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	waitFor(t, "the waiting task's message", func() bool { return len(supervisionTo(f, "side2", sigFailed)) == 1 })
	if n := supervisionTo(f, "side2", sigFailed)[0]; !strings.Contains(n, away.ID) {
		t.Fatalf("side2's message:\n%s", n)
	}
	b.mu.Lock()
	b.noMainLogged = b.noMainLogged.Add(-superviseNoMainLogEvery) // an hour on
	b.mu.Unlock()
	for range 3 {
		s.sendDigest(ctx)
	}
	if n := logged("still no main conversation"); n != 0 {
		t.Fatalf("logged %d line(s) that signals still wait, after they went out", n)
	}
	if n := logged("no main conversation"); n != 1 {
		t.Fatalf("no-main lines: %d, want only the first", n)
	}
}

// With no main conversation set, a worker's signal whose task leader cannot be reached, with no conversation above the
// leader either, waits (#5420) instead of being taken from the pending ones and dropped (review of #10133). Once a main
// conversation is set it goes up there as the leader's decision.
func TestUnreachableLeaderWithNobodyAboveWaitsWithoutAMainConversation(t *testing.T) {
	s, f := newQuietService(t)
	logged := noMainLog(s)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	// The leader's agent row is released (it was replaced): there is no leader agent to reach.
	s.todos.mu.Lock()
	for i := range s.todos.todos[todo.ID].Agents {
		if a := &s.todos.todos[todo.ID].Agents[i]; a.SessionID == "s1" {
			now := time.Now().UTC()
			a.ReleasedAt = &now
		}
	}
	s.todos.mu.Unlock()
	if lead, handled, ok := s.leaderSupervisor(todoNow(s, w.ID)); !handled || ok {
		t.Fatalf("setup: the leader is reachable or not the worker's supervisor: %+v %v %v", lead, handled, ok)
	}
	if up, _ := s.supervisorAboveTask(ctx, todoNow(s, w.ID)); up != "" {
		t.Fatalf("setup: a conversation above the leader: %q", up)
	}
	f.setSessions("box", proto.Session{SessionID: w.Agents[0].SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了", UpdatedAt: time.Now()})
	s.signal(superviseSignal{Todo: w.ID, Kind: sigReview, Key: "review@1", Detail: "做完了，等你验收", At: time.Now().Add(-time.Minute)})
	for range 5 { // the watch's passes
		s.sendDigest(ctx)
	}
	id := superviseSignal{Todo: w.ID, Kind: sigReview}.id()
	b := &s.supervise
	b.mu.Lock()
	_, waiting := b.pending[id]
	told := b.sent[id] != ""
	b.mu.Unlock()
	if !waiting || told {
		t.Fatalf("the worker's signal: pending %v, marked told %v; want it waiting, untold", waiting, told)
	}
	if n := logged("no main conversation; 1 signal(s) wait for one"); n != 1 {
		t.Fatalf("logged that one waits %d times, want once", n)
	}
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("told while nobody could be: %q", n)
	}

	// A main conversation is set: the decision goes up to it.
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	waitFor(t, "the decision to go up to the main conversation", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "cannot be reached") {
		t.Fatalf("the main conversation's escalation:\n%s", n)
	}
}
