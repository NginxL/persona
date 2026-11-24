package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Decision rights: a supervisor (a direction lead, the main conversation) handing work or words to the owner's own
// Agents and sessions is none of the six kinds of thing only the owner decides, so no phone card is raised for it,
// whatever its track (health included) and whatever the words carry. An ordinary conversation is still asked.
func TestSupervisorsHandOffWithoutAPhoneCard(t *testing.T) {
	ctx := context.Background()
	s, f, _ := infoUseService(t)
	setSettings(t, s, func(v *Settings) {
		v.Main, v.Leaders, v.MachineID = "qa-main-chat", []string{"qa-main", "qa-health-1"}, "qa-machine"
	})
	f.setSessions("qa-machine", proto.Session{SessionID: "qa-main", MachineID: "qa-machine", Assistant: true},
		proto.Session{SessionID: "qa-health-1", MachineID: "qa-machine", Assistant: true, Space: SpaceHealth},
		proto.Session{SessionID: "qa-main-chat", MachineID: "qa-machine", Assistant: true},
		proto.Session{SessionID: "qa-side", MachineID: "qa-machine", Assistant: true})
	f.mu.Lock()
	f.infoTag = `{"categories":["legal_id","health"]}`
	f.mu.Unlock()
	for _, self := range []string{"qa-main", "qa-health-1", "qa-main-chat"} {
		for _, tool := range []string{"agent_start", "agent_send", "agent_interrupt", "a2a_send", "todo_create"} {
			done := make(chan bool, 1)
			go func() {
				ok, _, err := s.confirmOutbound(ctx, "qa-machine", self, tool, "qa: a placeholder passport number", "summary", nil)
				done <- ok && err == nil
			}()
			select {
			case ok := <-done:
				if !ok {
					t.Fatalf("%s %s: the supervisor's hand-off was refused", self, tool)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s %s: the supervisor's hand-off waits on a phone card", self, tool)
			}
			if qs := s.pendingQuestions(); len(qs) != 0 {
				t.Fatalf("%s %s: a phone card was raised: %+v", self, tool, qs)
			}
		}
	}
	answerOutboundIn(t, s, "qa-side", "agent_send", "summary", "", false)
	if ok, _, err := s.confirmOutbound(ctx, "qa-machine", "qa-side", "agent_send", "qa: a placeholder passport number", "summary", nil); err != nil || ok {
		t.Fatalf("an ordinary conversation's hand-off was not asked about: %v %v", ok, err)
	}
}

// A direction lead's plan starts at once, writable work in a folder the owner never let start by itself included: no
// card, no push. An ordinary conversation's same plan still waits on its card.
func TestLeadPlanStartsAtOnce(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.known["lead1"] = true
	res, err := s.toolTaskPlan(ctx, "lead1", planArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "without asking") {
		t.Fatalf("the lead's plan waits on a card: %s", res)
	}
	waitFor(t, "the lead's task to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("the lead's plan was pushed: %+v", got)
	}

	s2, f2 := newQuietService(t)
	if res, err := s2.toolTaskPlan(ctx, "side1", planArgs(false)); err != nil || !strings.Contains(res, "card") {
		t.Fatalf("an ordinary conversation's writable plan did not wait on its card: %s %v", res, err)
	}
	if got := f2.pushed(); len(got) != 1 {
		t.Fatalf("an ordinary conversation's plan card, with the owner away, pushes once: %+v", got)
	}
}
