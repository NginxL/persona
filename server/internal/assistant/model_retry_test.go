package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTemporaryModelContinuation(t *testing.T) {
	for _, main := range []bool{false, true} {
		name := "agent"
		if main {
			name = "assistant"
		}
		t.Run(name, func(t *testing.T) {
			s, f := transientService(t)
			for i := 0; i < 12; i++ {
				c := apiErrorTurn("s1", "API Error: 503 Service Unavailable")
				c.Assistant = main
				c.UpdatedAt = turnEnd.Add(time.Duration(i) * time.Minute)
				f.setSessions("box", c)
				triage(t, s, "s1")
				e := entryOf(t, s, "s1")
				if e.Work.On != proto.WaitTransient || e.WakeAt == nil || e.Work.Max != 0 {
					t.Fatalf("attempt %d did not remain scheduled without a limit: %+v", i, e)
				}
				s.workTick(context.Background(), *e.WakeAt)
				s.workTick(context.Background(), *e.WakeAt)
				if len(f.sentToAgents()) != i+1 {
					t.Fatalf("duplicate or missing continuation: %v", f.sentToAgents())
				}
				s.workStarted(context.Background(), "box", "s1", time.Now())
			}
		})
	}
}

func TestModelRetryPendingDeliveryAfterRestart(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	st := &workMemStore{}
	s := newServiceOn(t, f, st)
	s.work.switches = &TriageSwitches{}
	c := apiErrorTurn("s1", "503 Service Unavailable")
	f.setSessions("box", c)
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	s.workTick(ctx, *e.WakeAt)
	if len(f.sentToAgents()) != 1 {
		t.Fatal("first continuation was not delivered")
	}
	// Model the manager crashing after the hub accepted delivery, before the
	// manager cleared its due row. The old persisted row uses the same send id.
	st.wmu.Lock()
	st.rows[[2]string{"box", "s1"}] = e
	st.wmu.Unlock()
	restarted := newServiceOn(t, f, st)
	restarted.work.switches = &TriageSwitches{}
	restarted.workTick(ctx, *e.WakeAt)
	if len(f.sentToAgents()) != 1 || entryOf(t, restarted, "s1").WakeAt != nil {
		t.Fatal("restart repeated an acknowledged continuation")
	}
}

func TestModelRetryRestartAndConcurrentWake(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	st := &workMemStore{}
	s := newServiceOn(t, f, st)
	s.work.switches = &TriageSwitches{}
	c := apiErrorTurn("s1", "503 Service Unavailable")
	c.UpdatedAt = time.Now().Add(-72 * time.Hour)
	f.setSessions("box", c)
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	restarted := newServiceOn(t, f, st)
	restarted.work.switches = &TriageSwitches{}
	if !restarted.loadWork(ctx) || entryOf(t, restarted, "s1").WakeAt == nil {
		t.Fatal("pending retry lost after restart")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			restarted.workWake(ctx, restarted.newWorkView(ctx), [2]string{"box", "s1"}, *e.WakeAt)
		}()
	}
	wg.Wait()
	if len(f.sentToAgents()) != 1 {
		t.Fatalf("duplicate wake: %v", f.sentToAgents())
	}
	restarted.UserStopped(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
	again := newServiceOn(t, f, st)
	again.work.switches = &TriageSwitches{}
	again.workTick(ctx, e.WakeAt.Add(time.Hour))
	triage(t, again, "s1")
	if len(f.sentToAgents()) != 1 {
		t.Fatal("restart resurrected a cancelled retry")
	}
}

func TestUserStoppedCancelsModelRetry(t *testing.T) {
	s, f := transientService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 Service Unavailable"))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	s.UserStopped(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"})
	s.workTick(context.Background(), *e.WakeAt)
	triage(t, s, "s1")
	if len(f.sentToAgents()) != 0 || entryOf(t, s, "s1").WakeAt != nil {
		t.Fatal("Stop was bypassed by a retry or a repeated turn-end")
	}
}
