package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	recoveryFirstError = "API Error: 529 Overloaded"
	recoveryNewError   = "API Error: 503 no eligible upstream"
)

// retryDecisionFixture reaches the real exhausted-retry decision after one automatic continuation. The work tick uses
// a due timestamp; no test waits for backoff.
func retryDecisionFixture(t *testing.T, st *workMemStore) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.hello.Agents.Triage.MaxRetries = 1
	s := newServiceOn(t, f, st)
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Decide: true, Wake: true, AutoContinue: true}); err != nil {
		t.Fatal(err)
	}
	failed := apiErrorTurn("s1", recoveryFirstError)
	failed.UpdatedAt = time.Now().UTC().Add(-2 * time.Minute)
	f.setSessions("box", failed)
	triage(t, s, "s1")
	s.workTick(context.Background(), time.Now().Add(16*time.Second))
	retryFailureAt(t, s, f, recoveryFirstError, time.Now().UTC().Add(-time.Minute))
	cards := s.workCards()
	if len(cards) != 1 || cards[0].ToolName != workCardTool || !strings.Contains(cards[0].Questions[0].Question, recoveryFirstError) {
		t.Fatalf("setup: retry decision %+v", cards)
	}
	return s, f
}

func retryFailureAt(t *testing.T, s *Service, f *fakeHub, text string, at time.Time) {
	t.Helper()
	s.workStarted(context.Background(), "box", "s1", time.Now())
	failed := apiErrorTurn("s1", text)
	failed.UpdatedAt = at
	f.setSessions("box", failed)
	triage(t, s, "s1")
}

func TestRetiredRetryEvidence(t *testing.T) {
	at := time.Now().UTC()
	wake := at.Add(time.Minute)
	for _, c := range []struct {
		name    string
		on      string
		retries int
		status  string
		last    string
		later   time.Duration
		want    bool
	}{
		{name: "a pending automatic retry recovers", on: proto.WaitTransient, status: proto.StatusDone, later: time.Second, want: true},
		{name: "an exhausted retry recovers", on: proto.WaitUser, retries: 1, status: proto.StatusDone, later: time.Second, want: true},
		{name: "the same failure stays", on: proto.WaitTransient, status: proto.StatusDone, last: recoveryFirstError, later: time.Second},
		{name: "process exit is not recovery", on: proto.WaitTransient, status: proto.StatusOffline, later: time.Second},
		{name: "a running turn is not recovery", on: proto.WaitTransient, status: proto.StatusWorking, later: time.Second},
		{name: "the same row is not newer", on: proto.WaitTransient, status: proto.StatusDone},
		{name: "another user decision is untouched", on: proto.WaitUser, status: proto.StatusDone, later: time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := db.SessionWork{TurnAt: at, WakeAt: &wake, Retries: c.retries, InteractionID: "work_old",
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: c.on, WakeAt: &wake, InteractionID: "work_old"}}
			row := proto.Session{Status: c.status, UpdatedAt: at.Add(c.later), LastText: c.last, State: proto.StateUnread}
			got, ok := retiredRetry(row, e)
			if ok != c.want {
				t.Fatalf("recovered = %v, want %v; work %+v", ok, c.want, got)
			}
			if ok && (got.Work.Phase == proto.WorkBlocked || got.WakeAt != nil || got.InteractionID != "" || got.Work.WakeAt != nil || got.Work.InteractionID != "") {
				t.Fatalf("retry attention survived retirement: %+v", got)
			}
		})
	}
}

func TestPendingRetryDecisionStaysAnswerable(t *testing.T) {
	s, f := retryDecisionFixture(t, &workMemStore{})
	defer s.waitWorkEvents()
	card := s.workCards()[0]
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[0].Label}})
	if got := f.sentToAgents(); len(got) != 2 || got[1] != "s1: 继续" {
		t.Fatalf("the pending retry was not answerable: %v", got)
	}
	if entry := entryOf(t, s, "s1"); entry.InteractionID != "" || entry.Retries != 0 {
		t.Fatalf("the answered retry did not settle: %+v", entry)
	}
}

func TestSuccessfulTurnRetiresRetryDecisionAcrossRestart(t *testing.T) {
	st := &workMemStore{}
	s, f := retryDecisionFixture(t, st)
	oldCard := s.workCards()[0].ID
	f.triage = `{"phase":"done","line":"Recovered"}`
	recovered := stopped("s1", "")
	recovered.UpdatedAt = time.Now().UTC()
	f.setSessions("box", recovered)
	triage(t, s, "s1")
	entry := entryOf(t, s, "s1")
	if entry.Work.Phase == proto.WorkBlocked || entry.InteractionID != "" || entry.WakeAt != nil || len(s.workCards()) != 0 {
		t.Fatalf("the newer success kept retry attention: %+v, cards %+v", entry, s.workCards())
	}
	if handled, err := s.workAnswer(context.Background(), oldCard, proto.Resolution{}); !handled || err == nil {
		t.Fatalf("the retired retry still accepted an answer: handled %v, err %v", handled, err)
	}
	s.waitWorkEvents()

	s = newServiceOn(t, f, st)
	defer s.waitWorkEvents()
	s.workTick(context.Background(), time.Now())
	entry = entryOf(t, s, "s1")
	if entry.Work.Phase == proto.WorkBlocked || entry.InteractionID != "" || entry.WakeAt != nil || len(s.workCards()) != 0 {
		t.Fatalf("the retired retry returned after restart: %+v, cards %+v", entry, s.workCards())
	}
}

func TestSuccessfulTurnWithinClockToleranceRetiresRetryDecision(t *testing.T) {
	s, f := retryDecisionFixture(t, &workMemStore{})
	defer s.waitWorkEvents()
	recovered := stopped("s1", "")
	recovered.UpdatedAt = entryOf(t, s, "s1").TurnAt.Add(time.Second)
	f.setSessions("box", recovered)
	triage(t, s, "s1")
	entry := entryOf(t, s, "s1")
	if entry.Work.Phase == proto.WorkBlocked || entry.InteractionID != "" || entry.WakeAt != nil || len(s.workCards()) != 0 {
		t.Fatalf("the newer success inside the timestamp tolerance kept retry attention: %+v, cards %+v", entry, s.workCards())
	}
}

func TestSuccessfulTurnRejectsRestoredRetryAnswer(t *testing.T) {
	st := &workMemStore{}
	s, f := retryDecisionFixture(t, st)
	card := s.workCards()[0]
	f.hubCards = []proto.Interaction{card}
	s.waitWorkEvents()

	recovered := stopped("s1", "")
	recovered.UpdatedAt = time.Now().UTC()
	f.setSessions("box", recovered)
	s = newServiceOn(t, f, st)
	defer s.waitWorkEvents()
	handled, err := s.workAnswer(context.Background(), card.ID, proto.Resolution{Answers: map[string]string{
		card.Questions[0].Question: card.Questions[0].Options[0].Label,
	}})
	if !handled || err == nil {
		t.Fatalf("the restored retry accepted an answer after recovery: handled %v, err %v", handled, err)
	}
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("the retired retry sent another continuation: %v", got)
	}
}

func TestNewerFailureReplacesRetryDecision(t *testing.T) {
	s, f := retryDecisionFixture(t, &workMemStore{})
	defer s.waitWorkEvents()
	retryFailureAt(t, s, f, recoveryNewError, time.Now().UTC())
	entry := entryOf(t, s, "s1")
	cards := s.workCards()
	if entry.InteractionID != "" || entry.Work.Wait != recoveryNewError || entry.WakeAt == nil || len(cards) != 0 {
		t.Fatalf("the newer failure did not replace the old decision: %+v, cards %+v", entry, cards)
	}
}

func TestDueTransientRetryRetiresBeforeWake(t *testing.T) {
	for _, c := range []struct {
		name  string
		later time.Duration
	}{
		{name: "inside the timestamp tolerance", later: time.Second},
		{name: "outside the timestamp tolerance", later: 3 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeHub()
			f.sessions["box"] = nil
			f.hello.Agents.Triage.MaxRetries = 1
			s := newServiceOn(t, f, &workMemStore{})
			defer s.waitWorkEvents()
			if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Decide: true, Wake: true, AutoContinue: true}); err != nil {
				t.Fatal(err)
			}
			failed := apiErrorTurn("s1", recoveryFirstError)
			failed.UpdatedAt = time.Now().UTC().Add(-2 * time.Minute)
			f.setSessions("box", failed)
			triage(t, s, "s1")
			pending := entryOf(t, s, "s1")
			if pending.Work.On != proto.WaitTransient || pending.WakeAt == nil {
				t.Fatalf("setup did not schedule a transient retry: %+v", pending)
			}

			recovered := stopped("s1", "")
			recovered.UpdatedAt = pending.TurnAt.Add(c.later)
			f.setSessions("box", recovered)
			before := len(f.sentToAgents())
			s.workTick(context.Background(), *pending.WakeAt)
			if got := f.sentToAgents(); len(got) != before {
				t.Fatalf("the recovered retry sent another continuation: %v", got[before:])
			}
			entry := entryOf(t, s, "s1")
			if entry.Work.On == proto.WaitTransient || entry.WakeAt != nil || entry.Work.WakeAt != nil || entry.Retries != 0 {
				t.Fatalf("the due retry was not retired: %+v", entry)
			}
		})
	}
}

func TestCardTickRetiresRetryDecisionOnSuccessfulRow(t *testing.T) {
	s, f := retryDecisionFixture(t, &workMemStore{})
	defer s.waitWorkEvents()
	pending := entryOf(t, s, "s1")
	if pending.InteractionID == "" || len(s.workCards()) != 1 {
		t.Fatalf("setup did not leave a retry card: %+v", pending)
	}
	recovered := stopped("s1", "")
	recovered.UpdatedAt = pending.TurnAt.Add(time.Second)
	f.setSessions("box", recovered)

	ctx := context.Background()
	s.cardTick(ctx, s.newWorkView(ctx), [2]string{"box", "s1"}, time.Now())
	entry := entryOf(t, s, "s1")
	if entry.Work.On == proto.WaitUser || entry.InteractionID != "" || entry.Work.InteractionID != "" || entry.Retries != 0 || len(s.workCards()) != 0 {
		t.Fatalf("the card tick kept the recovered retry: %+v, cards %+v", entry, s.workCards())
	}
}
