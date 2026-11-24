package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// #6483: a to-do handed over from an Agent that went offline keeps that earlier link. Supervision follows the Agent
// that took over: the old one neither keeps the task from settling nor raises stalled from its last update.

func handedOver(now time.Time) Todo {
	return Todo{ID: "t", Supervision: &Supervision{Supervisor: "main", ExpectMinutes: 40}, Agents: []TodoAgent{
		{MachineID: "box", SessionID: "old", LinkedAt: now.Add(-20 * time.Hour)},
		{MachineID: "box", SessionID: "new", LinkedAt: now.Add(-2 * time.Hour)},
	}}
}

func TestSupersededAgents(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	todo := handedOver(now)
	offline := proto.Session{MachineID: "box", SessionID: "old", Status: proto.StatusOffline, State: proto.StateOffline}
	idle := proto.Session{MachineID: "box", SessionID: "new", Status: proto.StatusIdle, State: proto.StateIdle}
	newOffline := idle
	newOffline.Status, newOffline.State = proto.StatusOffline, proto.StateOffline
	oldIdle := offline
	oldIdle.Status, oldIdle.State = proto.StatusIdle, proto.StateIdle
	released := handedOver(now)
	at := now
	released.Agents[1].ReleasedAt = &at
	for _, c := range []struct {
		name   string
		todo   Todo
		agents []proto.Session
		want   string
	}{
		{"old offline, new listed", todo, []proto.Session{offline, idle}, "old"},
		{"old no longer listed, new listed", todo, []proto.Session{idle}, "old"},
		{"both offline: no takeover to follow", todo, []proto.Session{offline, newOffline}, ""},
		{"old still live: two agents", todo, []proto.Session{oldIdle, idle}, ""},
		{"the later one released", released, []proto.Session{offline, idle}, ""},
		{"a lone offline agent", Todo{ID: "t", Agents: todo.Agents[:1]}, []proto.Session{offline}, ""},
	} {
		var got []string
		for k := range supersededAgents(c.todo, c.agents) {
			got = append(got, k[1])
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("%s: superseded %v, want %q", c.name, got, c.want)
		}
	}
}

func TestHandedOverAgentDoesNotHoldTheTaskOrStall(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	todo := handedOver(now)
	old := proto.Session{MachineID: "box", SessionID: "old", Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: now.Add(-15 * time.Hour)}
	stopped := proto.Session{MachineID: "box", SessionID: "new", Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: now.Add(-time.Minute)}
	if !taskAgentsSettled(todo, []proto.Session{old, stopped}) {
		t.Fatal("the current agent stopped, but the to-do does not settle because of the agent it was handed over from")
	}
	working := stopped
	working.Status, working.State = proto.StatusWorking, proto.StateWorking
	if taskAgentsSettled(todo, []proto.Session{old, working}) {
		t.Fatal("settled while the current agent works")
	}
	if got := watchSignals(todo, []proto.Session{old, stopped}, now); len(got) != 0 {
		t.Fatalf("signals from the agent it was handed over from: %+v", got)
	}
	// Unchanged for a lone offline agent: it is still the task's agent, and its silence is told.
	lone := Todo{ID: "t", Supervision: todo.Supervision, Agents: todo.Agents[:1]}
	if got := watchSignals(lone, []proto.Session{old}, now); len(got) != 1 || got[0].Kind != sigStalled {
		t.Fatalf("a lone offline agent: %+v", got)
	}
	if taskAgentsSettled(lone, []proto.Session{old}) {
		t.Fatal("a lone offline agent settles")
	}
}

// End to end: the agent that took the task over stops with the work unfinished; the supervisor hears it stopped short,
// although the offline agent it took over from is still linked.
func TestTakeoverStopTellsTheSupervisor(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	f.judge = `{"verdict":"not_done","reason":"还剩两项没查"}`
	ctx := context.Background()
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s2", "接手", "/code/app", "codex"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second link", func() bool { return len(todoNow(s, todo.ID).Agents) == 2 })
	old := proto.Session{MachineID: "box", SessionID: "s1", Cwd: "/code/app", Status: proto.StatusOffline, State: proto.StateOffline,
		UpdatedAt: time.Now().Add(-15 * time.Hour)}
	turn := func(status, state, last string) {
		f.setSessions("box", old, proto.Session{MachineID: "box", SessionID: "s2", Cwd: "/code/app", Agent: "codex", Status: status,
			State: state, LastText: last, UpdatedAt: time.Now()})
	}
	f.setItems(text("assistant", "查了一半，没有全部解决。"))
	turn(proto.StatusWorking, proto.StateWorking, "")
	s.taskWorking(ctx, "box", "s2", startRow{})
	turn(proto.StatusDone, proto.StateUnread, "查了一半，没有全部解决。")
	s.taskTurnEnded(ctx, "box", "s2")
	waitFor(t, "the stopped-short message", func() bool { return len(f.notices()) == 1 })
	if n := entryLine(f.notices()[0]); !strings.Contains(n, "没做完") {
		t.Fatalf("message:\n%s", n)
	}
}
