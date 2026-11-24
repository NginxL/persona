package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// A decision recorded on a parked task (#8044): the owner's own call ends a parking that waits for them or for someone
// else and goes to the task's supervisor; anything else leaves the parking alone.

// parkFor parks the supervised task of newSupervisedService for what, waiting on on, until until.
func parkFor(t *testing.T, s *Service, id, what, on, until string) {
	t.Helper()
	if on == parkOwnerPriority {
		seedLegacyPark(t, s, id, what, on, until)
	} else {
		kind, target, sourceURL, wake := "person_reply", "the design team", "", "The design team replies with the requested decision"
		switch on {
		case parkExternalEvent:
			kind, target, sourceURL, wake = "external_event", "the upstream release", "https://example.com/releases/next", "The upstream release is published"
		case parkDate:
			kind, target, wake = "date", "the agreed date", "The recorded date arrives"
			at, err := parseParkUntil(until, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			until = at.UTC().Format(time.RFC3339)
		}
		args := map[string]any{"verdict": "parked", "reason": what, "waiting_on": on, "until": until,
			"evidence": testParkEvidence(kind, target, sourceURL, wake)}
		if _, err := reviewErr(s, id, args); err != nil {
			t.Fatal(err)
		}
	}
	if cur := todoNow(s, id); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("not parked: %+v", cur.Supervision)
	}
}

// ownerAnswersCard has the owner answer a question card of the task's agent (s1 on box) with answer.
func ownerAnswersCard(s *Service, question, answer string) {
	s.InteractionResolved(context.Background(), InteractionResolved{
		Interaction: proto.Interaction{ID: "ix_" + randomHex(4), MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: question}}},
		Resolution: proto.Resolution{Answers: map[string]string{question: answer}}, Status: "resolved"})
}

// writeDecision has the owner write a decision on to-do id by hand, as the apps do.
func writeDecision(t *testing.T, s *Service, id, question, choice string) {
	t.Helper()
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"question": question, "choice": choice})
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+id+"/decisions", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST decisions: %d %s", rec.Code, rec.Body.String())
	}
}

// holdDigest keeps the next supervision message back, as after any message that just went: what is raised waits.
func holdDigest(s *Service) {
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.last = time.Now().Add(time.Hour)
	s.supervise.mu.Unlock()
}

// The owner answers the agent's question while the task is parked for their call: the parking ends, and the task's
// supervisor gets one message that says so and carries the decision. A restart tells it no second time.
func TestOwnersDecisionEndsAParkingThatWaitsForThem(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, todoTestDB(t))
	ctx := context.Background()
	parkFor(t, s, todo.ID, "Whether this goes ahead of the billing work", "owner_priority", "3d")
	before := len(f.notices())

	ownerAnswersCard(s, "Do this now or after the billing work?", "Leave it until someone runs into it")

	cur := todoNow(s, todo.ID)
	if v := cur.Supervision; cur.DoneAt != nil || v.State != "" || v.Parked != nil || v.state(cur) == reviewParked {
		t.Fatalf("still parked after the owner's decision: todo = %+v, ledger = %+v", cur, v)
	}
	if sup, _ := listedTodos(t, s)[todo.ID]["supervision"].(map[string]any); sup["state"] != "" || sup["parked"] != nil {
		t.Fatalf("the apps still get it as parked: %v", sup)
	}
	if s.parkedSession("box", "s1") {
		t.Fatal("the manager's own follow-ups are still held")
	}
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"搁置期间用户在任务上记下了新的决定，搁置已经结束", "搁置时在等：Whether this goes ahead of the billing work",
		"这个任务已经定下的事", "Do this now or after the billing work?", "Leave it until someone runs into it", "signal=park_decided"} {
		if !strings.Contains(n, want) {
			t.Errorf("the message lacks %q:\n%s", want, n)
		}
	}
	// Handling belongs to the role-scoped system policy, not every decision event.
	for _, manual := range []string{"搁置期间记下决定的：", "agent_send", "task_review"} {
		if strings.Contains(n, manual) {
			t.Errorf("the event repeats handling policy %q:\n%s", manual, n)
		}
	}
	waitFor(t, "the mark on the review", func() bool {
		r := todoNow(s, todo.ID).Supervision.decidedPark()
		return r != nil && !r.DecidedTold.IsZero()
	})
	// Told: a manager that forgot what it told (a restart) does not tell it again, however often it looks.
	forgetSignals(t, s)
	for _, d := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour} {
		s.superviseWatch(ctx, time.Now().Add(d))
	}
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigParkDecided) || slices.Contains(got, sigParkedDue) {
		t.Fatalf("told a second time, or still due: %v", got)
	}
	// The count of parks in a row starts again, as after an agent's turn: the owner's answer is news.
	parkFor(t, s, todo.ID, "The owner said to leave it: until the next report of it", "external_event", "7d")
	if p := todoNow(s, todo.ID).Supervision.Parked; p == nil || p.Count != 1 {
		t.Fatalf("parked again = %+v, want a first park", p)
	}
}

// The decision is recorded and the manager restarts before its supervision message goes out: the ledger still says a
// decision ended the park and nobody was told, so the signal is raised again. A park's time that was waiting is moot.
func TestDecisionThatEndedAParkingIsToldAfterARestart(t *testing.T) {
	s, _, todo := newSupervisedServiceOn(t, todoTestDB(t))
	// Both signals are immediate. Keep delivery stopped while the decision
	// replaces parked_due and while the watch recovers park_decided.
	stopSupervisionTimers(s)
	ctx := context.Background()
	parkFor(t, s, todo.ID, "Ada has to say which region goes first", "someone_else", "2h")
	s.superviseWatch(ctx, time.Now().Add(3*time.Hour))
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkedDue}) {
		t.Fatalf("signals at its time: %v", got)
	}

	writeDecision(t, s, todo.ID, "Which region goes first?", "Ada says the EU region")

	cur := todoNow(s, todo.ID)
	if v := cur.Supervision; v.State != "" || v.Parked != nil {
		t.Fatalf("still parked: %+v", v)
	}
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals after the decision: %v", got)
	}
	if r := cur.Supervision.decidedPark(); r == nil || !r.DecidedTold.IsZero() {
		t.Fatalf("marked told before a message carried it: %+v", r)
	}
	forgetUntold(s)
	s.superviseWatch(ctx, time.Now())
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals after the restart: %v", got)
	}
	// Its supervisor reviews it: what waited is answered.
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "cancelled", "note": "Ada took it over"}); err != nil {
		t.Fatal(err)
	}
	s.superviseWatch(ctx, time.Now())
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("signals after the review: %v", got)
	}
}

// A task parked for something outside or for a date does not wait for a decision, and a row that is not the owner's own
// call (an agent's note, an outcome) is not what a task parked for the owner waits for: the parking stays.
func TestOtherDecisionsLeaveAParkingAlone(t *testing.T) {
	for _, on := range []string{parkExternalEvent, parkDate} {
		t.Run(on, func(t *testing.T) {
			s, _, todo := newSupervisedServiceOn(t, todoTestDB(t))
			parkFor(t, s, todo.ID, "The upstream release with the fix", on, "3d")
			holdDigest(s)
			ownerAnswersCard(s, "Wait for the upstream release?", "Yes, wait for it")
			writeDecision(t, s, todo.ID, "Scope", "Only the login page")
			cur := todoNow(s, todo.ID)
			if v := cur.Supervision; v.state(cur) != reviewParked || v.Parked == nil || v.decidedPark() != nil {
				t.Fatalf("a decision ended a parking that waits on %s: %+v", on, v)
			}
			if got := pendingKinds(s, todo.ID); len(got) != 0 {
				t.Fatalf("signals: %v", got)
			}
		})
	}
	t.Run("not the owner's call", func(t *testing.T) {
		database := todoTestDB(t)
		s, _, todo := newSupervisedServiceOn(t, database)
		parkFor(t, s, todo.ID, "Whether this goes ahead of the billing work", "owner_priority", "3d")
		holdDigest(s)
		for i, source := range []string{db.DecisionAgent, db.DecisionOutcome} {
			at := decisionNow().Add(time.Duration(i) * time.Microsecond)
			if ok, err := s.insertDecision(context.Background(), database, db.TodoDecisionRow{ID: "dec_" + source, TodoID: todo.ID,
				Question: "Order", Choice: "After the billing work", Source: source, CreatedAt: at}); err != nil || !ok {
				t.Fatalf("%s: %v, %v", source, ok, err)
			}
		}
		cur := todoNow(s, todo.ID)
		if v := cur.Supervision; v.state(cur) != reviewParked || v.decidedPark() != nil {
			t.Fatalf("a row that is not the owner's ended the parking: %+v", v)
		}
		if got := pendingKinds(s, todo.ID); len(got) != 0 {
			t.Fatalf("signals: %v", got)
		}
	})
}

// A parked task's time comes: the message that hands it back carries the decisions recorded on the task, so its
// supervisor sees an answer that came meanwhile before parking it again.
func TestParkedDueCarriesTheRecordedDecisions(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, todoTestDB(t))
	parkFor(t, s, todo.ID, "The upstream release with the fix", "external_event", "2h")
	writeDecision(t, s, todo.ID, "What if upstream slips?", "Ship our own patch after Friday")
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("no longer parked: %+v", cur.Supervision)
	}
	before := len(f.notices())
	s.superviseWatch(context.Background(), time.Now().Add(3*time.Hour))
	waitFor(t, "the parked_due message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"搁置到期了", "The upstream release with the fix", "这个任务已经定下的事", "What if upstream slips?",
		"Ship our own patch after Friday"} {
		if !strings.Contains(n, want) {
			t.Errorf("the message lacks %q:\n%s", want, n)
		}
	}
}

// releaseDigest lets the supervision message holdDigest kept back go now.
func releaseDigest(s *Service) {
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.last = time.Time{}
	s.scheduleDigestLocked(time.Now())
	b.mu.Unlock()
}

// ledgerDown is a database whose supervision ledger cannot be written while down is set.
type ledgerDown struct {
	*db.DB
	down atomic.Bool
}

func (d *ledgerDown) PutTodoSupervision(ctx context.Context, r db.TodoSupervisionRow) error {
	if d.down.Load() {
		return errors.New("the database did not answer")
	}
	return d.DB.PutTodoSupervision(ctx, r)
}

// The decision is stored but the ledger cannot be written as it ends the parking: the request that recorded it
// succeeds all the same, and the watch ends the parking from the stored decision once the ledger can be written, and
// tells the supervisor.
func TestDecisionStoredWhileTheLedgerFailsStillEndsTheParking(t *testing.T) {
	database := &ledgerDown{DB: todoTestDB(t)}
	s, f, todo := newSupervisedServiceOn(t, database)
	ctx := context.Background()
	parkFor(t, s, todo.ID, "Whether this goes ahead of the billing work", "owner_priority", "3d")
	holdDigest(s)

	database.down.Store(true)
	writeDecision(t, s, todo.ID, "Do this now or after the billing work?", "After the billing work") // a 200, or it fails
	stored, err := database.ListTodoDecisions(ctx, todo.ID, false)
	if err != nil || len(stored) != 1 {
		t.Fatalf("decisions stored = %+v, %v", stored, err)
	}
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("the parking ended with no ledger write: %+v", cur.Supervision)
	}
	s.superviseWatch(ctx, time.Now()) // the ledger is still down: tried, and left for the next look
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("the parking ended with no ledger write: %+v", cur.Supervision)
	}

	database.down.Store(false)
	s.superviseWatch(ctx, time.Now())
	cur := todoNow(s, todo.ID)
	if v := cur.Supervision; v.state(cur) == reviewParked || v.Parked != nil {
		t.Fatalf("still parked after the ledger came back: %+v", v)
	}
	if r := cur.Supervision.decidedPark(); r == nil || r.Decided != stored[0].ID {
		t.Fatalf("the park the decision ended = %+v, want decision %s", r, stored[0].ID)
	}
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals: %v", got)
	}
	before := len(f.notices())
	releaseDigest(s)
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) > before })
	if n := f.notices()[before]; !strings.Contains(n, "搁置期间用户在任务上记下了新的决定") || !strings.Contains(n, "After the billing work") {
		t.Fatalf("the message lacks the decision:\n%s", n)
	}
}

// The decision ends the parking, and its supervisor parks the task again before the message that tells it went out:
// the decision still reaches it, saying the task was parked again, and the new park is not ended by the old decision.
func TestDecisionReachesTheSupervisorThatParkedAgainBeforeItWasTold(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, todoTestDB(t))
	stopSupervisionTimers(s)
	ctx := context.Background()
	parkFor(t, s, todo.ID, "Whether this goes ahead of the billing work", "owner_priority", "3d")
	writeDecision(t, s, todo.ID, "Do this now or after the billing work?", "After the billing work")
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals after the decision: %v", got)
	}

	parkFor(t, s, todo.ID, "The billing work lands first", "someone_else", "5d")
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals after parking again: %v", got)
	}
	forgetUntold(s) // a restart in between: the ledger still knows nobody was told
	s.superviseWatch(ctx, time.Now())
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkDecided}) {
		t.Fatalf("signals after a restart: %v", got)
	}
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("the decision from before ended the new parking: %+v", cur.Supervision)
	}

	before := len(f.notices())
	s.sendDigest(ctx)
	notices := f.notices()[before:]
	if len(notices) != 1 {
		t.Fatalf("notices after digest = %v, want one", notices)
	}
	n := notices[0]
	for _, want := range []string{"搁置期间用户在任务上记下了新的决定", "这条消息到之前你又搁置了它", "The billing work lands first",
		"Do this now or after the billing work?", "After the billing work"} {
		if !strings.Contains(n, want) {
			t.Errorf("the message lacks %q:\n%s", want, n)
		}
	}
	if r := todoNow(s, todo.ID).Supervision.decidedPark(); r == nil || r.DecidedTold.IsZero() {
		t.Fatalf("decision delivery left no receipt: %+v", r)
	}
	forgetSignals(t, s)
	s.superviseWatch(ctx, time.Now())
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("told a second time: %v", got)
	}
	if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewParked {
		t.Fatalf("no longer parked: %+v", cur.Supervision)
	}
}
