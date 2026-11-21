package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestProtectedQuotaWaitContinuesOnConfiguredModel(t *testing.T) {
	for _, leader := range []bool{false, true} {
		name := "main"
		if leader {
			name = "supervisor"
		}
		for _, triageEnabled := range []bool{false, true} {
			variant := name + "/triage-off"
			if triageEnabled {
				variant = name + "/triage-on"
			}
			t.Run(variant, func(t *testing.T) {
				s, f := workService(t)
				s.work.switches.Enabled, s.work.switches.AutoContinue = triageEnabled, triageEnabled
				f.withMachine()
				setSettings(t, s, func(v *Settings) {
					v.MachineID = "server"
					if leader {
						v.Leaders = []string{"s1"}
					} else {
						v.Main = "s1"
					}
				})
				setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "claude-opus-5-5"})
				reset := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
				failure := "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at " + reset.Format("15:04") + " UTC (in 60m)"
				c := apiErrorTurn("s1", failure)
				c.Assistant, c.Launcher, c.Model, c.UpdatedAt = true, "claude", "claude-haiku-5-5", time.Now()
				decorateLimit(&c)
				f.setSessions("server", c)
				ctx := context.Background()
				s.triageTurn(ctx, s.newWorkView(ctx), "server", c)
				e, ok := s.workEntry([2]string{"server", "s1"})
				if !ok || e.Work.On != proto.WaitExternal || e.WakeAt == nil || e.WakeAt.Before(reset) {
					t.Fatalf("quota did not wait for reset: %+v", e)
				}
				s.workTick(ctx, reset.Add(-time.Second))
				if len(f.sentToAgents()) != 0 {
					t.Fatal("continued before quota reset")
				}
				s.workTick(ctx, *e.WakeAt)
				s.workTick(ctx, e.WakeAt.Add(time.Second))
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.agentSent) != 1 {
					t.Fatalf("continuations: %v", f.agentSent)
				}
				p := f.agentSent[0]
				if p["model"] != "claude-opus-5-5" || p["launcher"] != "claude" || (leader && p["follows_setting"] != true) || (!leader && p["is_main"] != true) {
					t.Fatalf("quota continuation reused observed fallback: %v", p)
				}
			})
		}
	}
}

func TestProtectedQuotaRuntimeResumeIsLeftAlone(t *testing.T) {
	s, f := workService(t)
	f.withMachine()
	setSettings(t, s, func(v *Settings) { v.MachineID, v.MainMachineID, v.Main = "server", "server", "s1" })
	reset := time.Now().Add(time.Hour)
	c := limitTurn("s1", reset, true)
	c.Assistant, c.UpdatedAt = true, time.Now()
	f.setSessions("server", c)
	ctx := context.Background()
	if !s.protectedQuotaWait(ctx, "server", c) {
		t.Fatal("fixture is not a protected quota wait")
	}
	s.triageTurn(ctx, s.newWorkView(ctx), "server", c)
	e, tracked := s.workEntry([2]string{"server", "s1"})
	if !tracked || e.Work.On != proto.WaitExternal || e.WakeAt != nil {
		t.Fatalf("runtime-owned quota wait: %+v", e)
	}
	s.workTick(ctx, reset.Add(time.Hour))
	if len(f.sentToAgents()) != 0 {
		t.Fatal("interrupted the runtime's own quota continuation")
	}
}

func TestProtectedOrdinaryManagerSendKeepsExistingParams(t *testing.T) {
	s, f := workService(t)
	f.withMachine()
	setSettings(t, s, func(v *Settings) { v.MachineID, v.MainMachineID, v.Main = "server", "server", "s1" })
	if !s.protectedModelConversation(context.Background(), "server", "s1") {
		t.Fatal("fixture is not the protected main conversation")
	}
	if err := s.sendToSession(context.Background(), "server", "s1", "ordinary manager message"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agentSent) != 1 || f.agentSent[0]["text"] != "ordinary manager message" {
		t.Fatalf("ordinary manager send changed: %v", f.agentSent)
	}
	// Service.call already attests is_main and supervision metadata independently of model selection.
	for _, field := range []string{"model", "launcher"} {
		if _, added := f.agentSent[0][field]; added {
			t.Fatalf("ordinary send resolved model field %s", field)
		}
	}
}
