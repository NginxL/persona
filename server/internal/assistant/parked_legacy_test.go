package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func legacyOwnerPark(t *testing.T, s *Service, id string) TaskReview {
	t.Helper()
	seedLegacyPark(t, s, id, "Whether to publish the prepared update", parkOwnerPriority, "7d")
	return *todoNow(s, id).Supervision.parking()
}

func requireLegacyNeedsYou(t *testing.T, s *Service, id string) Todo {
	t.Helper()
	cur := todoNow(s, id)
	if cur.DoneAt != nil || cur.Supervision.State != reviewNeedsUser || cur.Supervision.Parked != nil {
		t.Fatalf("legacy owner choice is still hidden or was closed: %+v", cur)
	}
	if tr := triageTodo(listedTodo{Todo: cur}, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section != triageWaiting {
		t.Fatalf("legacy owner choice is not in Needs you: %+v", tr)
	}
	return cur
}

func legacyPastQuestion(t *testing.T, s *Service, id string, reversible bool) acceptAsk {
	t.Helper()
	ctx := context.Background()
	args := map[string]any{
		"verdict": reviewNeedsUser, "why_owner": "only_owner_knows", "reason": "Which rollout should be used?",
		"recommend": "Option A", "cost_of_waiting": "The rollout stays pending",
		"options": []map[string]string{
			{"label": "Option A", "leads_to": "Use the first prepared version"},
			{"label": "Option B", "leads_to": "Use the second prepared version"},
		},
	}
	if !reversible {
		args["why_owner"], args["deadline"], args["reversible"] = whyVisible, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339), false
	}
	if _, err := reviewErr(s, id, args); err != nil {
		t.Fatal(err)
	}
	// Seed a historical round that is overdue by the default path's real-time check, not just its sweep clock.
	asked := time.Now().Add(-48 * time.Hour).UTC()
	s.accept.mu.Lock()
	s.accept.asks[id].AskedAt = asked
	s.accept.asks[id].Deadline = asked.Add(24 * time.Hour)
	err := s.saveAcceptErrLocked(ctx)
	s.accept.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		r := &v.Reviews[len(v.Reviews)-1]
		r.At = asked
		if r.Deadline != nil {
			at := asked.Add(24 * time.Hour)
			r.Deadline = &at
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	a, ok := s.askCopy(id)
	if !ok || !a.Deadline.Before(time.Now()) || a.Reversible != reversible || !a.AcceptedAt.IsZero() {
		t.Fatalf("fixture has no unhandled expired question: %+v", a)
	}
	return a
}

func TestLegacyOwnerPriorityMovesToNeedsYouAndRestates(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task := newSupervisedServiceOn(t, database)
	// The restatement is immediate, so holdDigest alone does not keep it in: quiet while the test reads what is
	// pending, short again for releaseDigest to send it (#14125).
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	before := legacyOwnerPark(t, s, task.ID)
	waits := &OwnerWait{At: before.At, Work: before.At.Add(-time.Hour), Reason: "The owner must choose whether to publish"}
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.Waits = waits
		return true
	}); err != nil {
		t.Fatal(err)
	}
	pushes, sends := len(f.pushed()), len(sentToAgent(f))
	parked := todoNow(s, task.ID).Supervision
	watched := time.Now()
	s.superviseWatch(ctx, watched)
	wokeBy := wakeBound(watched)
	cur := requireLegacyNeedsYou(t, s, task.ID)
	if cur.Supervision.Supervisor != task.Supervision.Supervisor || !reflect.DeepEqual(cur.Supervision.Waits, waits) {
		t.Fatal("migration changed the supervisor or the independent owner wait")
	}
	if len(cur.Supervision.Reviews) != 1 || cur.Supervision.Reviews[0].RestateAt.IsZero() {
		t.Fatalf("missing persisted restatement or invented another review: %+v", cur.Supervision.Reviews)
	}
	// Moving the park to Needs you ends it: the only history change besides the restatement is park_ended_at.
	after := requireParkEnded(t, parked, cur.Supervision.Reviews, wokeBy)[0]
	after.RestateAt = before.RestateAt
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration rewrote the original wait/history: before %+v, after %+v", before, after)
	}
	if ask := listedAsk(t, s, task.ID); ask != nil || len(f.pushed()) != pushes || len(sentToAgent(f)) != sends {
		t.Fatal("migration invented an owner question, push, default or Agent instruction")
	}
	if got := pendingKinds(s, task.ID); !slices.Equal(got, []string{sigUndecidable}) {
		t.Fatalf("not handed to the supervisor for a real question: %v", got)
	}
	n := len(f.notices())
	setSuperviseTimings(s, shortSuperviseTimings)
	releaseDigest(s)
	waitFor(t, "the legacy question restatement", func() bool { return len(f.notices()) > n })
	again, _ := restartedOn(t, database)
	quietDigest(again) // a reissued restatement stays pending, where the check below sees it
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	again.superviseWatch(ctx, time.Now().Add(time.Hour))
	requireLegacyNeedsYou(t, again, task.ID)
	if slices.Contains(pendingKinds(again, task.ID), sigUndecidable) {
		t.Fatal("restarting reissued the same legacy restatement")
	}
}

func TestLegacyOwnerPriorityRestatementBuildsARealDecision(t *testing.T) {
	s, _, task := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	holdDigest(s)
	ctx := context.Background()
	before := legacyOwnerPark(t, s, task.ID)
	s.superviseWatch(ctx, time.Now())
	requireLegacyNeedsYou(t, s, task.ID)
	if _, err := reviewErr(s, task.ID, map[string]any{"verdict": reviewNeedsUser, "reason": before.Reason}); err == nil {
		t.Fatal("migration weakened admission to let an incomplete new decision through")
	}
	args := map[string]any{
		"verdict": reviewNeedsUser, "why_owner": whyVisible, "reason": before.Reason,
		"recommend": "Keep private", "cost_of_waiting": "The prepared update stays unpublished",
		"options": []map[string]string{
			{"label": "Keep private", "leads_to": "Leave the update unpublished"},
			{"label": "Publish", "leads_to": "Publish the prepared update after the owner's explicit choice"},
		},
	}
	if _, err := reviewErr(s, task.ID, args); err != nil {
		t.Fatal(err)
	}
	a, ok := s.askCopy(task.ID)
	if !ok || !decideAsk(&a) || a.Reason != before.Reason || a.Recommend != "Keep private" || len(a.Options) != 2 || !a.Deadline.IsZero() {
		t.Fatalf("the ordinary validated decision flow was not used: %+v", a)
	}
	cur := todoNow(s, task.ID)
	if len(cur.Supervision.Reviews) != 2 || !cur.Supervision.Reviews[0].At.Equal(before.At) || cur.Supervision.Waits != nil {
		t.Fatal("restating lost the historical park or failed to answer its independent owner wait")
	}
	if result, err := s.acceptTryAs(ctx, task.ID, "", false); err != nil || !result.taken {
		t.Fatalf("a fresh validated question was not answerable: %+v, %v", result, err)
	}
}

func TestLegacyOwnerPriorityDoesNotReplayOldQuestionOrDefault(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	old := legacyPastQuestion(t, s, task.ID, true)
	oldJSON, _ := json.Marshal(old)
	// Simulate an old park whose question retirement missed its write. Do not manufacture a fresh question from it.
	legacyOwnerPark(t, s, task.ID)
	pushes, sends := len(f.pushed()), len(sentToAgent(f))
	s.superviseWatch(ctx, time.Now())
	requireLegacyNeedsYou(t, s, task.ID)
	if !slices.Contains(pendingKinds(s, task.ID), sigUndecidable) {
		t.Fatal("an old recommendation suppressed the request for a current, complete question")
	}
	s.sweepTaskDeadlines(time.Now().Add(48 * time.Hour))
	writeDecision(t, s, task.ID, "Should the prepared update be published?", "Keep it private")
	s.sweepTaskDeadlines(time.Now().Add(72 * time.Hour))
	if len(f.pushed()) != pushes || len(sentToAgent(f)) != sends {
		t.Fatal("an old card/deadline was replayed, including after the owner answered the legacy wait")
	}
	now, _ := s.askCopy(task.ID)
	nowJSON, _ := json.Marshal(now)
	if string(nowJSON) != string(oldJSON) {
		t.Fatal("migration rewrote or revived the prior question/consent/deadline")
	}
	decisions, err := database.ListTodoDecisions(ctx, task.ID, false)
	if err != nil || len(decisions) != 1 || decisions[0].Source != db.DecisionManual {
		t.Fatalf("migration/default created an owner decision: %+v, %v", decisions, err)
	}
}

func TestLegacyOwnerPriorityDoesNotReplayOldReminder(t *testing.T) {
	s, f, task := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	holdDigest(s)
	old := legacyPastQuestion(t, s, task.ID, false)
	legacyOwnerPark(t, s, task.ID)
	s.superviseWatch(context.Background(), time.Now())
	requireLegacyNeedsYou(t, s, task.ID)
	pushes := len(f.pushed())
	s.sweepTaskDeadlines(time.Now())
	after, _ := s.askCopy(task.ID)
	if !reflect.DeepEqual(old, after) || len(f.pushed()) != pushes {
		t.Fatal("the old irreversible deadline was handled or reminded as if it were a current question")
	}
}

func TestLegacyOwnerPriorityCannotAnswerSupersededCard(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	old := legacyPastQuestion(t, s, task.ID, true)
	legacyOwnerPark(t, s, task.ID)
	s.superviseWatch(ctx, time.Now())
	cur := requireLegacyNeedsYou(t, s, task.ID)
	sends := len(sentToAgent(f))
	result, err := s.acceptTryAs(ctx, task.ID, "", false)
	if err != nil || result.taken || len(sentToAgent(f)) != sends {
		t.Fatalf("an old card acted as an answer to the current legacy choice: %+v, %v", result, err)
	}
	if decisionOpen(&old, cur) {
		t.Fatal("a superseded question became open again merely because the legacy state changed")
	}
	decisions, err := database.ListTodoDecisions(ctx, task.ID, false)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("a superseded card created a new owner decision: %+v, %v", decisions, err)
	}
}

func TestLegacyOwnerVerificationStillEligible(t *testing.T) {
	task := Todo{Supervision: &Supervision{State: reviewNeedsUser, Reviews: []TaskReview{{
		Verdict: reviewParked, WaitingOn: parkOwnerPriority,
	}}}}
	for _, conclusion := range []bool{false, true} {
		a := acceptAsk{AskedAt: time.Now(), Conclusion: conclusion, Outcome: outcomeDone}
		if acceptAskStale(&a, task) {
			t.Fatal("the legacy decision fence also blocked an unrelated verification ask")
		}
	}
}

func TestLegacyOwnerPriorityPreservesOwnerDecisions(t *testing.T) {
	for _, when := range []string{"before reconciliation", "after reconciliation", "stored while ledger fails"} {
		t.Run(when, func(t *testing.T) {
			database := &ledgerDown{DB: taskRecoveryDatabase(t)}
			s, _, task := newSupervisedServiceOn(t, database)
			// Nothing is told before the restart: the settle loop below counts only the restarted manager's messages.
			// park_decided is immediate, so holdDigest alone does not keep it in (#13987).
			quietDigest(s)
			holdDigest(s)
			ctx := context.Background()
			legacyOwnerPark(t, s, task.ID)
			if when != "before reconciliation" {
				s.superviseWatch(ctx, time.Now())
				requireLegacyNeedsYou(t, s, task.ID)
			}
			database.down.Store(when == "stored while ledger fails")
			writeDecision(t, s, task.ID, "Should the update be published?", "Do not publish")
			database.down.Store(false)
			again, hub := restartedOn(t, database)
			holdDigest(again)
			if err := again.ensureTodosLoaded(ctx); err != nil {
				t.Fatal(err)
			}
			again.superviseWatch(ctx, time.Now())
			cur := todoNow(again, task.ID)
			if cur.Supervision.State == reviewNeedsUser || cur.Supervision.State == reviewParked || cur.DoneAt != nil {
				t.Fatalf("a real stored owner answer was lost or re-asked: %+v", cur.Supervision)
			}
			decisions, err := database.ListTodoDecisions(ctx, task.ID, false)
			r := cur.Supervision.decidedPark()
			if err != nil || len(decisions) != 1 || r == nil || r.Decided != decisions[0].ID {
				t.Fatalf("missing or duplicated owner decision: %+v, review %+v, %v", decisions, r, err)
			}
			// Immediate delivery can move the pending signal between two reads. Wait for one settled form in a single loop:
			// the recovered signal is still pending, or exactly one real message carried it; never both nor neither.
			var pending []string
			var delivered int
			waitFor(t, "the recovered park decision to settle", func() bool {
				pending = pendingKinds(again, task.ID)
				delivered = strings.Count(strings.Join(hub.notices(), "\n"), "signal=park_decided")
				return slices.Equal(pending, []string{sigParkDecided}) && delivered == 0 || len(pending) == 0 && delivered == 1
			})
			if len(pending) > 0 && (!slices.Equal(pending, []string{sigParkDecided}) || delivered != 0) {
				t.Fatalf("an answered legacy choice was restated or duplicated: pending %v, messages %q", pending, hub.notices())
			} else if len(pending) == 0 && delivered != 1 {
				t.Fatalf("an answered legacy choice was not told exactly once: pending %v, messages %q", pending, hub.notices())
			}
		})
	}
}

func TestLegacyOwnerPriorityRetriesWritesAndReloads(t *testing.T) {
	database := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, _, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	before := legacyOwnerPark(t, s, task.ID)
	database.down.Store(true)
	s.superviseWatch(ctx, time.Now())
	cur := todoNow(s, task.ID)
	if cur.Supervision.State != reviewParked || !cur.Supervision.Reviews[0].RestateAt.IsZero() || len(pendingKinds(s, task.ID)) != 0 {
		t.Fatal("a failed migration published its state or consumed its restatement")
	}
	database.down.Store(false)
	again, _ := restartedOn(t, database)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	again.superviseWatch(ctx, time.Now())
	cur = requireLegacyNeedsYou(t, again, task.ID)
	if !cur.Supervision.Reviews[0].At.Equal(before.At) || !slices.Contains(pendingKinds(again, task.ID), sigUndecidable) {
		t.Fatal("the failed persisted transition did not recover on the next watch")
	}
}

type legacyDecisionReadDown struct {
	*db.DB
	down atomic.Bool
}

func (d *legacyDecisionReadDown) ListTodoDecisions(ctx context.Context, id string, all bool) ([]db.TodoDecisionRow, error) {
	if d.down.Load() {
		return nil, errors.New("synthetic decision lookup failure")
	}
	return d.DB.ListTodoDecisions(ctx, id, all)
}

func TestLegacyOwnerPriorityWaitsForDecisionLookupRecovery(t *testing.T) {
	database := &legacyDecisionReadDown{DB: taskRecoveryDatabase(t)}
	s, _, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	legacyOwnerPark(t, s, task.ID)
	database.down.Store(true)
	s.superviseWatch(ctx, time.Now())
	if cur := todoNow(s, task.ID); cur.Supervision.State != reviewParked || !cur.Supervision.Reviews[0].RestateAt.IsZero() {
		t.Fatal("migration guessed that no owner answer existed after its lookup failed")
	}
	database.down.Store(false)
	s.superviseWatch(ctx, time.Now())
	requireLegacyNeedsYou(t, s, task.ID)
}

func TestLegacyOwnerPriorityDoesNotRestateUnknownStoredAnswer(t *testing.T) {
	database := &legacyDecisionReadDown{DB: taskRecoveryDatabase(t)}
	s, _, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	legacyOwnerPark(t, s, task.ID)
	// A restart between migration and restatement; the owner row committed before its park-end write.
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: task.ID,
		Question: "Should the update be published?", Choice: "Keep it private", Source: db.DecisionManual, CreatedAt: time.Now().UTC()}
	if inserted, err := database.DB.InsertTodoDecision(ctx, row); err != nil || !inserted {
		t.Fatalf("store the pending owner answer: %v, %v", inserted, err)
	}
	database.down.Store(true)
	s.superviseWatch(ctx, time.Now().Add(11*time.Minute))
	cur := todoNow(s, task.ID)
	if !cur.Supervision.Reviews[0].RestateAt.IsZero() || slices.Contains(pendingKinds(s, task.ID), sigUndecidable) {
		t.Fatal("an unreadable stored owner answer was replaced with another question")
	}
	database.down.Store(false)
	s.superviseWatch(ctx, time.Now().Add(12*time.Minute))
	cur = todoNow(s, task.ID)
	if cur.Supervision.State == reviewNeedsUser || cur.Supervision.decidedPark() == nil ||
		!slices.Equal(pendingKinds(s, task.ID), []string{sigParkDecided}) {
		t.Fatalf("the stored answer did not win after lookup recovery: %+v, signals %v", cur.Supervision, pendingKinds(s, task.ID))
	}
}

func TestLegacyOwnerPrioritySourceAuthority(t *testing.T) {
	for _, source := range []string{db.DecisionManual, db.DecisionCard, db.DecisionThread, db.DecisionAgent, db.DecisionOutcome} {
		t.Run(source, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, _, task := newSupervisedServiceOn(t, database)
			holdDigest(s)
			ctx := context.Background()
			legacyOwnerPark(t, s, task.ID)
			s.superviseWatch(ctx, time.Now())
			requireLegacyNeedsYou(t, s, task.ID)
			row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: task.ID,
				Question: "Should the update be published?", Choice: "Keep it private", Source: source, CreatedAt: time.Now().UTC()}
			if ok, err := s.insertDecision(ctx, database, row); err != nil || !ok {
				t.Fatalf("store source %s: %v, %v", source, ok, err)
			}
			s.superviseWatch(ctx, time.Now())
			cur := todoNow(s, task.ID)
			owner := source == db.DecisionManual || source == db.DecisionCard || source == db.DecisionThread
			if (cur.Supervision.State != reviewNeedsUser) != owner {
				t.Fatalf("source %s changed the owner-wait authority: %+v", source, cur.Supervision)
			}
		})
	}
}

func TestLegacyOwnerOtherWaitsAndClosedTasksStayUnchanged(t *testing.T) {
	for _, kind := range []string{parkSomeoneElse, parkExternalEvent, parkDate, "closed", "newer review"} {
		t.Run(kind, func(t *testing.T) {
			s, _, task := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
			holdDigest(s)
			ctx := context.Background()
			switch kind {
			case "closed":
				legacyOwnerPark(t, s, task.ID)
				s.todos.mu.Lock()
				at := time.Now()
				s.todos.todos[task.ID].DoneAt = &at
				s.todos.mu.Unlock()
			case "newer review":
				legacyOwnerPark(t, s, task.ID)
				if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
					t.Fatal(err)
				}
			default:
				parkFor(t, s, task.ID, "An external dependency remains", kind, "7d")
			}
			before, _ := json.Marshal(todoNow(s, task.ID).Supervision)
			s.superviseWatch(ctx, time.Now())
			after, _ := json.Marshal(todoNow(s, task.ID).Supervision)
			if string(before) != string(after) {
				t.Fatalf("migration changed a closed, replaced or non-owner wait: %s -> %s", before, after)
			}
		})
	}
}
