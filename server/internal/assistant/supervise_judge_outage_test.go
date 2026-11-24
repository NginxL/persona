package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/proto"
)

// While the judge is down, every stopped task is told unjudged with the same reason. Those stops are not urgent: they
// wait for the ordinary spacing and go out together, instead of each waking the main conversation two minutes after
// the last. An unjudged stop the judge did read (unsure) stays urgent.
func TestJudgeOutageStopsAreNotUrgent(t *testing.T) {
	down := superviseSignal{Todo: "todo_1", Kind: sigUnjudged, Key: "k1", Detail: "判定模型未配置", JudgeDown: true}
	unsure := superviseSignal{Todo: "todo_2", Kind: sigUnjudged, Key: "k2", Detail: "看不出做没做完"}
	if down.urgent() || down.waitsOnDecision() || !unsure.urgent() {
		t.Fatalf("urgent: down %v (decision %v), unsure %v", down.urgent(), down.waitsOnDecision(), unsure.urgent())
	}
	p := superviseTimings{gather: time.Second, every: 10 * time.Minute, urgent: 2 * time.Minute}
	now := time.Now()
	down.At, unsure.At = now, now
	if wait, ok := digestWait(now, now, []superviseSignal{down}, p); !ok || wait < 9*time.Minute {
		t.Fatalf("a judge-outage stop is due in %s after a message", wait)
	}
	if wait, ok := digestWait(now, now, []superviseSignal{unsure}, p); !ok || wait > 3*time.Minute {
		t.Fatalf("an unsure stop is due in %s after a message", wait)
	}
	// Several stops during the outage are due together, with the first.
	other := down
	other.Todo, other.Key, other.At = "todo_3", "k3", now.Add(time.Minute)
	if a, _ := digestWait(now, now, []superviseSignal{down, other}, p); a > 10*time.Minute {
		t.Fatalf("two outage stops are due in %s", a)
	}
}

// End to end with no model set up for the judge: the stop still reaches the supervisor, marked as a judge outage on
// the signal and on the turn's ledger (so a replay after a restart is batched as the first telling was).
func TestJudgeWithoutModelMarksTheStopAsOutage(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	f.mu.Lock()
	f.hello.Models.AI, f.hello.Models.TriageAI = ai.Config{}, nil
	f.mu.Unlock()
	s.settingsChanged(context.Background())
	f.setItems(text("assistant", fullReport))
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, fullReport, 0)
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) == 1 })
	e, ok := s.turnOf(context.Background(), todo.ID)
	if !ok || e.Signal == nil || e.Signal.Kind != sigUnjudged || !e.Signal.JudgeDown {
		t.Fatalf("turn ledger: %+v", e.Signal)
	}
}
