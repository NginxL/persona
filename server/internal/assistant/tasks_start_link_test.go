package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// linkHub is reconcileHub whose machines the fake lists from f.sessions (so f.down takes them off line), with the
// wait for a link to come back shortened to wait.
func linkHub(t *testing.T, wait time.Duration) (*Service, *fakeHub) {
	t.Helper()
	s, f := reconcileHub(t, "box")
	f.mu.Lock()
	f.devices = nil
	f.slowStart = false
	f.down = map[string]bool{}
	f.mu.Unlock()
	f.downLists = map[string]int{}
	s.taskRetry = wait
	oldPoll, oldBudget := taskReconnectPoll, nodeStartBudget
	taskReconnectPoll, nodeStartBudget = 10*time.Millisecond, 0
	t.Cleanup(func() { taskReconnectPoll, nodeStartBudget = oldPoll, oldBudget })
	return s, f
}

// dropFirstStart makes the first session.start come back as a dropped link with box off line, back after back; then
// (and on any later start) returns the session the fake normally answers. A later start runs as usual.
func dropFirstStart(f *fakeHub, back time.Duration, then func(f *fakeHub, p map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	starts := 0
	f.onStart = func(f *fakeHub, p map[string]any) {
		if starts++; starts == 1 {
			f.down["box"] = true
			f.failStart = rpc.ErrDropped
			time.AfterFunc(back, func() {
				f.mu.Lock()
				f.down["box"] = false
				if then != nil {
					then(f, p)
				}
				f.mu.Unlock()
			})
			return
		}
		f.failStart = nil
	}
}

func noHealthRecord(t *testing.T, s *Service) {
	t.Helper()
	if r, ok := s.launcherHealth(context.Background(), "box", "claude"); ok && r.State != healthOK {
		t.Fatalf("a link failure was counted against the launcher: %+v", r)
	}
}

// The link drops with the start out and the machine, back a moment later, does not show the agent: the start runs
// once more as soon as the link is back, the task starts, and nothing counts against the launcher (#7511).
func TestTaskStartRetriesOnceTheLinkIsBack(t *testing.T) {
	s, f := linkHub(t, 5*time.Second)
	dropFirstStart(f, 200*time.Millisecond, nil)
	begun := time.Now()
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the retried start to link", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.StartError != "" {
		t.Fatalf("todo = %+v", got)
	}
	if starts := taskStartMachines(f); len(starts) != 2 {
		t.Fatalf("starts %v, want the first and one retry", starts)
	}
	if took := time.Since(begun); took > 4*time.Second {
		t.Fatalf("the retry waited %s: it should follow the link, not the whole wait", took)
	}
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatal("a start that made it on its retry left a failure receipt")
	}
	noHealthRecord(t, s)
}

// The node had the start after all: back, the machine lists the session under the start's own id, which is adopted;
// no second start (#7511, on top of #6540).
func TestTaskStartAdoptsTheSessionOnceTheLinkIsBack(t *testing.T) {
	s, f := linkHub(t, 5*time.Second)
	dropFirstStart(f, 1500*time.Millisecond, func(f *fakeHub, p map[string]any) { // after the first look found nothing
		id, _ := p["session_id"].(string)
		f.sessions["box"] = []proto.Session{{SessionID: id, MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: "claude",
			StartedByPersona: true, Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
	})
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the session to be adopted", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })
	if starts := taskStartMachines(f); len(starts) != 1 {
		t.Fatalf("starts %v, want only the first: its session was there", starts)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.StartError != "" {
		t.Fatalf("todo = %+v", got)
	}
	noHealthRecord(t, s)
}

// The link does not come back within the wait: the task fails once, saying the link dropped, with no second start
// and nothing against the launcher.
func TestTaskStartFailsAsLinkDownWhenItDoesNotComeBack(t *testing.T) {
	s, f := linkHub(t, 300*time.Millisecond)
	dropFirstStart(f, time.Hour, nil)
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the task to fail", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	got := todoNow(s, todo.ID)
	if !strings.Contains(got.StartError, "连接断了") || !strings.Contains(got.StartError, "没重连上") || !strings.Contains(got.StartError, "connection closed") {
		t.Fatalf("start error %q does not say the link dropped", got.StartError)
	}
	if starts := taskStartMachines(f); len(starts) != 1 {
		t.Fatalf("starts %v, want one", starts)
	}
	noHealthRecord(t, s)
}

// The wait runs out while the machine stays connected: a busy node, not the link. No retry, and the card says so.
func TestTaskStartWaitOnAConnectedMachineSaysBusy(t *testing.T) {
	s, f := reconcileHub(t, "box")
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the task to fail", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	got := todoNow(s, todo.ID)
	if !strings.Contains(got.StartError, "一直连着") || !strings.Contains(got.StartError, "deadline") {
		t.Fatalf("start error %q does not say the machine was busy", got.StartError)
	}
	if starts := taskStartMachines(f); len(starts) != 1 {
		t.Fatalf("starts %v, want one", starts)
	}
	noHealthRecord(t, s)
}

// The wait runs out and the machine is off line by then: the link, not a busy node. Back, it shows nothing, and the
// start runs once more.
func TestTaskStartWaitWithTheMachineGoneRetriesOnceItIsBack(t *testing.T) {
	s, f := linkHub(t, 5*time.Second)
	f.mu.Lock()
	f.slowStart = true
	starts := 0
	f.onStart = func(f *fakeHub, _ map[string]any) {
		if starts++; starts == 1 {
			f.downLists["box"] = 1 // off line when the hub looks after the wait, back when it looks again
			return
		}
		f.slowStart = false
	}
	f.mu.Unlock()
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the retried start to link", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })
	if starts := taskStartMachines(f); len(starts) != 2 {
		t.Fatalf("starts %v, want two", starts)
	}
	noHealthRecord(t, s)
}

// A start that may have reached the node is looked for until the node's own start would have run out of time before
// it is started again: a node still building the first session shows it in that time, and no second agent opens.
func TestTaskStartLinkDropWaitsOutTheNodesStartBeforeRetrying(t *testing.T) {
	s, f := linkHub(t, 5*time.Second)
	nodeStartBudget = 4 * time.Second // longer than both looks (1 s each) and the reconnect
	dropFirstStart(f, 100*time.Millisecond, nil)
	f.mu.Lock()
	var startID string
	inner := f.onStart
	f.onStart = func(f *fakeHub, p map[string]any) {
		if startID == "" {
			startID, _ = p["session_id"].(string)
			// The node finishes the first session 3 s in: after both 1 s looks, within its own budget.
			time.AfterFunc(3*time.Second, func() {
				f.mu.Lock()
				f.sessions["box"] = []proto.Session{{SessionID: startID, MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: "claude",
					StartedByPersona: true, Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
				f.mu.Unlock()
			})
		}
		inner(f, p)
	}
	f.mu.Unlock()
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the first session to be adopted", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })
	if starts := taskStartMachines(f); len(starts) != 1 {
		t.Fatalf("starts %v: a second agent was started beside one the node was still building", starts)
	}
	if got := todoNow(s, todo.ID).Agents[0].SessionID; got != startID {
		t.Fatalf("linked %s, want the first start's %s", got, startID)
	}
}
