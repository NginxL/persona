package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A dedup-hit member needs a receipt after a mixed send, but it was not in the failed message and must not escalate.
func TestLeaderFailedMixedDeliveryEscalatesOnlyTheSentSignals(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor, v.State = "box/s1", reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	putTodo(s, Todo{ID: "leader-task", Title: "Leader", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", Source: sourceLeader, Agent: "claude"}}})
	todo = todoNow(s, todo.ID)
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker", Source: sourceWorker, Via: "leader-task", Agent: "claude"}}
	putTodo(s, todo)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusInterrupted, Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, UpdatedAt: time.Now()},
		proto.Session{MachineID: "box", SessionID: "worker", Status: proto.StatusDone, State: proto.StateUnread, LastText: "已完成", UpdatedAt: time.Now()})
	other := Todo{ID: "other-worker", Title: "Other worker", Origin: "conv1", Supervision: &Supervision{Supervisor: "box/s1", State: reviewPending}}
	putTodo(s, other)
	old := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "leader-mixed-acknowledged", At: time.Now()}
	newer := superviseSignal{Todo: other.ID, Kind: sigReview, Key: "leader-mixed-new-send", At: time.Now()}
	s.signal(old)
	markLeaderTurnSignalled(t, s, old)
	reserveUndelivered(t, s, old)
	s.ackSupervisePending([]superviseSignal{old})
	s.signal(newer)
	markLeaderTurnSignalled(t, s, newer)
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: proto.CodeAgentStopped, Message: "这个 Agent 已经停了，这次没有发送", Data: json.RawMessage(`{"delivered":"none"}`)}
	f.mu.Unlock()
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if s.deliverToLeaderAgent(ctx, lead, []superviseSignal{old, newer}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("a stopped leader did not take the escalation path")
	}
	var oldEscalated, newEscalated bool
	for _, g := range storedSupervisePending(t, s) {
		if !strings.HasPrefix(g.Key, "leader-unreachable@") {
			continue
		}
		oldEscalated = oldEscalated || g.Todo == old.Todo
		newEscalated = newEscalated || g.Todo == newer.Todo
	}
	t.Logf("pending after the failed mixed send: %+v", storedSupervisePending(t, s))
	if oldEscalated || !newEscalated {
		t.Fatalf("escalations: old dedup-hit=%v, actually sent=%v; pending=%+v", oldEscalated, newEscalated, storedSupervisePending(t, s))
	}
}
