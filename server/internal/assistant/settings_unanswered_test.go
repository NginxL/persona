package assistant

import (
	"context"
	"encoding/json"
	approvalops "github.com/nwverse/persona/server/internal/approval"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A persona_settings change is the user's to approve (#3722): only their own allow applies it and only their own deny
// is a decline. A card that expires unanswered, is closed, or that someone else answers for them changes nothing and
// says nobody answered, so the conversation never reports that the user refused.

const interjectKey = "assistant.interject_background"

// setFrom has conversation session ask to turn interject_background on; the tool's result comes on the channel.
func setFrom(s *Service, session string) chan string {
	done := make(chan string, 1)
	go func() {
		r := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "mac", SessionID: session,
			Arguments: json.RawMessage(`{"action":"set","key":"` + interjectKey + `","value":true}`)})
		done <- r.Content[0].Text
	}()
	return done
}

func interjectOn(t *testing.T, s *Service) bool {
	t.Helper()
	r := s.Tool(context.Background(), ToolCall{Name: "persona_settings", Arguments: json.RawMessage(`{"action":"get","key":"` + interjectKey + `"}`)})
	return r.Content[0].Text == "true"
}

func resultOf(t *testing.T, done chan string, wait time.Duration) string {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(wait):
		t.Fatal("persona_settings did not return")
		return ""
	}
}

// The card a leader conversation raised goes through the manager's queue like any card. Unanswered in its decision
// window it is no decline: the tool says the card is still waiting for the owner (it stays on the apps), and once the
// waiting window ends too it expires without changing the setting.
func TestSettingCardExpiringUnansweredIsNoDecline(t *testing.T) {
	old, oldWaiting := settingCardWait, approvalWaitingWindow
	settingCardWait, approvalWaitingWindow = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { settingCardWait, approvalWaitingWindow = old, oldWaiting })
	a := newAskRig(t)
	setSettings(t, a.s, func(v *Settings) { v.Main, v.Leaders = "main-1", []string{"s1"} })

	done := setFrom(a.s, "s1")
	var ix proto.Interaction
	waitFor(t, "the settings card on the queue", func() bool {
		qs := a.s.pendingQuestions()
		if len(qs) != 1 {
			return false
		}
		ix = qs[0]
		p, up := a.s.pendingIx(ix.ID)
		return up && !p.Delegating // the user's from the start: not held by a supervisor
	})
	out := resultOf(t, done, 5*time.Second)
	if out != approvalops.WaitingText || strings.Contains(out, "declined") {
		t.Fatalf("a card unanswered in its window: %q", out)
	}
	waitFor(t, "the waiting card to expire", func() bool {
		status, _, _ := a.mem.InteractionOutcome(context.Background(), ix.ID)
		return status == "expired"
	})
	if interjectOn(t, a.s) {
		t.Error("the setting changed without the user's approval")
	}
}

// Only what the user answered counts: an allow the assistant (or a supervisor) gave for them applies nothing, a dismissed
// or cancelled card is no decline, the user's deny is one, and only the user's allow saves the change.
func TestSettingCardOnlyTheUsersAnswerCounts(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name       string
		r          proto.Resolution
		want       string // the tool's result; "" for an applied change
		applied    bool
		wantPrefix string
	}{
		{name: "allowed for the user", r: proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant}, want: prompts.RenderSettingUnanswered()},
		{name: "denied for the user", r: proto.Resolution{Allow: &no, AnsweredBy: proto.AnsweredByAssistant}, want: prompts.RenderSettingUnanswered()},
		{name: "dismissed", r: proto.Resolution{Dismiss: true}, want: prompts.RenderSettingUnanswered()},
		{name: "expired", r: proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}, want: prompts.RenderSettingUnanswered()},
		{name: "the user denies", r: proto.Resolution{Allow: &no}, want: "The user declined the change: nothing changed."},
		{name: "the user allows", r: proto.Resolution{Allow: &yes}, applied: true, wantPrefix: interjectKey + " is true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newTestService(t, newFakeHub().withMachine())
			setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", []string{"lead-1"} })
			done := setFrom(s, "lead-1")
			q, ok := awaitCard(s)
			if !ok {
				t.Fatal("no card")
			}
			raw, _ := json.Marshal(c.r)
			if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
				t.Fatal(err)
			}
			out := resultOf(t, done, 5*time.Second)
			if c.wantPrefix != "" && !strings.HasPrefix(out, c.wantPrefix) || c.wantPrefix == "" && out != c.want {
				t.Fatalf("result %q, want %q", out, c.want+c.wantPrefix)
			}
			if got := interjectOn(t, s); got != c.applied {
				t.Fatalf("applied = %v, want %v", got, c.applied)
			}
		})
	}
}
