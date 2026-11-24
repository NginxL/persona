package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func autoContinueTextForTest(text string) string {
	return prompts.MessageSourceText(text, &proto.MessageSource{Kind: "auto-continue"})
}

func requireAutoContinueSource(t *testing.T, params map[string]any) {
	t.Helper()
	source, _ := params["message_source"].(map[string]any)
	if source["kind"] != "auto-continue" {
		t.Fatalf("message_source = %v, want kind auto-continue", params["message_source"])
	}
}

func TestAccountExhaustionDoesNotAutoContinueByDefault(t *testing.T) {
	for _, errText := range []string{
		"API Error: 503 no other eligible account",
		"API Error: 503 no eligible upstream account",
		"API Error: 503 the upstream request failed and no other eligible upstream account",
		"API Error: 503 upstream attempt budget is exhausted",
		"API Error: 503 no eligible upstream",
	} {
		t.Run(errText, func(t *testing.T) {
			s, f := transientService(t)
			f.setSessions("box", apiErrorTurn("s1", errText))
			triage(t, s, "s1")

			if e, ok := s.workEntry([2]string{"box", "s1"}); ok && (e.WakeAt != nil || e.Work.WakeAt != nil) {
				t.Fatalf("account exhaustion scheduled an automatic continuation: %+v", e)
			}
			s.workTick(context.Background(), time.Now().Add(time.Hour))
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("account exhaustion automatically continued: %v", got)
			}
		})
	}
}

func TestAccountExhaustionRetryBudgetIsOptInAndBounded(t *testing.T) {
	s, f := transientService(t)
	f.hello.Agents.Triage.AccountExhaustionMaxRetries = 2
	const errText = "API Error: 503 no eligible upstream account"
	f.setSessions("box", apiErrorTurn("s1", errText))
	triage(t, s, "s1")

	for continuation := 1; continuation <= 2; continuation++ {
		e := entryOf(t, s, "s1")
		if e.WakeAt == nil || e.Work.On != proto.WaitTransient {
			t.Fatalf("opt-in continuation %d was not scheduled: %+v", continuation, e)
		}
		s.workTick(context.Background(), *e.WakeAt)
		if got := f.sentToAgents(); len(got) != continuation {
			t.Fatalf("after opt-in continuation %d: sent %v", continuation, got)
		}
		failAgain(t, s, f, continuation, errText)
	}

	if e := entryOf(t, s, "s1"); e.WakeAt != nil || e.Work.WakeAt != nil {
		t.Fatalf("account exhaustion exceeded its configured budget: %+v", e)
	}
	s.workTick(context.Background(), time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 2 {
		t.Fatalf("bounded opt-in policy sent another continuation: %v", got)
	}
}

func TestAccountExhaustionRetryBudgetCanBeDisabledWhilePending(t *testing.T) {
	s, f := transientService(t)
	f.hello.Agents.Triage.AccountExhaustionMaxRetries = 1
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 no other eligible account"))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil {
		t.Fatalf("opt-in continuation was not scheduled: %+v", e)
	}

	f.hello.Agents.Triage.AccountExhaustionMaxRetries = 0
	s.workTick(context.Background(), *e.WakeAt)
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("disabled account-exhaustion policy still sent: %v", got)
	}
	if current := entryOf(t, s, "s1"); current.WakeAt != nil || current.Work.WakeAt != nil {
		t.Fatalf("disabled account-exhaustion policy kept its wake: %+v", current)
	}
}

func TestAccountExhaustionCancelsAlreadyQueuedContinuation(t *testing.T) {
	s, f := transientService(t)
	c := apiErrorTurn("s1", "API Error: 503 Service Unavailable")
	f.setSessions("box", c)
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitTransient {
		t.Fatalf("ordinary transient control was not scheduled: %+v", e)
	}

	// A restored session can reveal the concrete cause after an older version
	// already queued the retry. The due send must classify the current error again.
	c.TurnError = "API Error: 503 no eligible upstream account"
	c.LastText = c.TurnError
	f.setSessions("box", c)
	s.workTick(context.Background(), *e.WakeAt)

	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("queued account-exhaustion continuation was sent: %v", got)
	}
	if current := entryOf(t, s, "s1"); current.WakeAt != nil || current.Work.WakeAt != nil {
		t.Fatalf("account-exhaustion wake survived the due recheck: %+v", current)
	}
}

// Assistant is the shared classification for the main chat, leader and
// background-supervision conversations, side chats, scheduled-task and health
// chats, and meeting rooms. They all use the same account-exhaustion budget as
// coding Agents: selecting another launcher cannot help when no account is eligible.
func TestAssistantAccountExhaustionDoesNotAutoContinue(t *testing.T) {
	for _, errText := range []string{
		"API Error: 503 no eligible upstream account",
		"API Error: 503 the upstream request failed and no other eligible upstream account",
	} {
		t.Run(errText, func(t *testing.T) {
			s, f := transientService(t)
			c := apiErrorTurn("s1", errText)
			c.Assistant = true
			f.setSessions("box", c)
			triage(t, s, "s1")

			if e, ok := s.workEntry([2]string{"box", "s1"}); ok && (e.WakeAt != nil || e.Work.WakeAt != nil) {
				t.Fatalf("assistant account exhaustion scheduled an automatic continuation: %+v", e)
			}
			s.workTick(context.Background(), time.Now().Add(time.Hour))
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("assistant account exhaustion automatically continued: %v", got)
			}
		})
	}
}

func TestAutomaticContinuationHasSourceMarker(t *testing.T) {
	s, f := transientService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 Service Unavailable"))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	s.workTick(context.Background(), *e.WakeAt)

	got := f.sentToAgents()
	want := "s1: " + autoContinueTextForTest(continueText)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("automatic continuation lacks structured provenance: sent %v, want %q", got, want)
	}
	f.mu.Lock()
	params := f.agentSent[0]
	f.mu.Unlock()
	requireAutoContinueSource(t, params)
}

func TestOrdinaryTransientErrorsStillAutoContinue(t *testing.T) {
	for _, errText := range []string{
		"API Error: 503 Service Unavailable",
		"API Error: 529 overloaded",
		"API Error: Connection lost mid-response",
		"API Error: 429 Too Many Requests",
	} {
		t.Run(errText, func(t *testing.T) {
			s, f := transientService(t)
			f.setSessions("box", apiErrorTurn("s1", errText))
			triage(t, s, "s1")
			e := entryOf(t, s, "s1")
			if e.WakeAt == nil || e.Work.On != proto.WaitTransient {
				t.Fatalf("ordinary transient error was not scheduled: %+v", e)
			}
			s.workTick(context.Background(), *e.WakeAt)
			if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: "+autoContinueTextForTest(continueText) {
				t.Fatalf("ordinary transient error did not continue exactly once: %v", got)
			}
		})
	}
}
