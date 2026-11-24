package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Use the wire contract rather than the new Go field so removing production changes still compiles.
func reviewWakeStatus(t *testing.T, source string, submitted, checked time.Time) TodoPRStatus {
	t.Helper()
	fields := map[string]any{
		"url": source, "state": "open", "head_oid": "head-current", "review": "changes_requested",
		"checks": "success", "checked_at": checked,
	}
	if !submitted.IsZero() {
		fields["changes_requested_at"] = submitted
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var status TodoPRStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestPRReviewWakeBoundWait(t *testing.T) {
	const source = "https://github.com/example/app/pull/12"
	for _, name := range []string{"current PR", "canonical recorded source", "earlier PR while another is open"} {
		t.Run(name, func(t *testing.T) {
			s, f, task := newSupervisedService(t)
			holdDigest(s)
			ctx := context.Background()
			s.setTaskStage(ctx, task.ID, "review", source)
			if name == "earlier PR while another is open" {
				s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/13")
			}
			bound := source
			if name == "canonical recorded source" {
				bound = "https://GitHub.com/Example/App/pull/12/?tab=checks#discussion"
			}
			if _, err := reviewErr(s, task.ID, mergePark(bound)); err != nil {
				t.Fatal(err)
			}
			before := todoNow(s, task.ID)
			at := before.Supervision.parking().At
			sends := len(sentToAgent(f))
			s.deploy.forge = &statusForge{status: reviewWakeStatus(t, source, at.Add(time.Second), at.Add(2*time.Second))}
			s.sweepPRStatuses(ctx, at.Add(2*time.Second))
			wokeBy := wakeBound(at.Add(2 * time.Second))
			cur := todoNow(s, task.ID)
			if cur.DoneAt != nil || cur.Supervision.State != reviewPending || cur.Supervision.Parked != nil ||
				!slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
				t.Fatalf("the bound change request did not return to review: %+v; signals %v", cur, pendingKinds(s, task.ID))
			}
			reviews := requireParkEnded(t, before.Supervision, cur.Supervision.Reviews, wokeBy)
			if cur.Supervision.Supervisor != before.Supervision.Supervisor ||
				!reflect.DeepEqual(reviews, before.Supervision.Reviews) || len(sentToAgent(f)) != sends {
				t.Fatal("the observation changed history, supervisor or sent an Agent instruction")
			}
			if name == "earlier PR while another is open" && (cur.PRURL == source || cur.PRStatus.State != "open") {
				t.Fatal("a change request waited for or replaced the other open PR")
			}
			s.sweepPRStatuses(ctx, at.Add(2*deployRecheck))
			if !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
				t.Fatalf("the same review created another logical wake: %v", pendingKinds(s, task.ID))
			}
		})
	}
}

func TestPRReviewWakeRejectsUnboundAndOldEvidence(t *testing.T) {
	const source = "https://github.com/example/app/pull/12"
	for _, name := range []string{
		"different recorded PR", "person reply", "external event", "date", "legacy prose",
		"missing provider time", "missing park time", "pre-park review", "equal park time", "ambiguous provider second",
		"future provider time", "missing observation time", "missing head", "approved", "review pending", "closed task",
	} {
		t.Run(name, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			ctx := context.Background()
			s.setTaskStage(ctx, task.ID, "review", source)
			park := mergePark(source)
			switch name {
			case "different recorded PR":
				park = mergePark("https://github.com/example/other/pull/12")
			case "person reply":
				park = waitingPark()
				park["reason"] = "Waiting for a person to answer on " + source
			case "external event":
				park["waiting_on"] = parkExternalEvent
				park["evidence"] = testParkEvidence("external_event", "An external result", source, "The external result arrives")
			case "date":
				park["waiting_on"], park["until"] = parkDate, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)
				park["evidence"] = testParkEvidence("date", "The scheduled date", "", "The scheduled date arrives")
			}
			if name == "legacy prose" {
				seedLegacyPark(t, s, task.ID, "Wait for PR #12: "+source, parkSomeoneElse, "3d")
			} else if _, err := reviewErr(s, task.ID, park); err != nil {
				t.Fatal(err)
			}
			if name == "missing park time" {
				if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
					v.Reviews[len(v.Reviews)-1].At = time.Time{}
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := todoNow(s, task.ID)
			at := before.Supervision.parking().At
			if at.IsZero() {
				at = time.Now().UTC()
			}
			submitted, checked := at.Add(time.Second), at.Add(2*time.Second)
			switch name {
			case "missing provider time":
				submitted = time.Time{}
			case "pre-park review":
				submitted = at.Add(-time.Second)
			case "equal park time":
				submitted = at
			case "ambiguous provider second":
				submitted = at.Truncate(time.Second)
			case "future provider time":
				submitted = checked.Add(time.Second)
			case "missing observation time":
				checked = time.Time{}
			case "closed task":
				s.todos.mu.Lock()
				closed := time.Now()
				s.todos.todos[task.ID].DoneAt = &closed
				s.todos.mu.Unlock()
			}
			status := reviewWakeStatus(t, source, submitted, checked)
			switch name {
			case "missing head":
				status.HeadOID = ""
			case "approved":
				status.Review = "approved"
			case "review pending":
				status.Review = "pending"
			}
			if err := s.applyPRStatus(ctx, before, status); err != nil {
				t.Fatal(err)
			}
			cur := todoNow(s, task.ID)
			if cur.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) ||
				!reflect.DeepEqual(cur.Supervision.Reviews, before.Supervision.Reviews) {
				t.Fatalf("%s woke or rewrote an unrelated/current wait: %+v", name, cur.Supervision)
			}
		})
	}
}

type reviewWakeResponseForge struct {
	fakeForge
	status         TodoPRStatus
	beforeResponse func()
}

func (f *reviewWakeResponseForge) PRStatus(context.Context, string, int) (TodoPRStatus, error) {
	if f.beforeResponse != nil {
		f.beforeResponse()
		f.beforeResponse = nil
	}
	return f.status, nil
}

func TestPRReviewWakeRejectsReplacedWait(t *testing.T) {
	const source = "https://github.com/example/app/pull/12"
	for _, repark := range []bool{false, true} {
		t.Run(map[bool]string{false: "first park during request", true: "same source parked again during request"}[repark], func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			ctx := context.Background()
			s.setTaskStage(ctx, task.ID, "review", source)
			park := func() {
				t.Helper()
				if _, err := reviewErr(s, task.ID, mergePark(source)); err != nil {
					t.Fatal(err)
				}
			}
			if repark {
				park()
			}
			now := time.Now().Add(3 * time.Second)
			s.deploy.forge = &reviewWakeResponseForge{
				status: reviewWakeStatus(t, source, now.Add(-time.Second), now), beforeResponse: park,
			}
			s.sweepPRStatuses(ctx, now)
			before := todoNow(s, task.ID)
			if before.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
				t.Fatal("a request captured before this park resolved it")
			}
			s.sweepPRStatuses(ctx, now.Add(deployRecheck))
			wokeBy := wakeBound(now.Add(deployRecheck))
			cur := todoNow(s, task.ID)
			if cur.Supervision.State != reviewPending || !pendingReviewSignal(s, task.ID) {
				t.Fatal("a fresh observation failed to resolve the same recorded wait")
			}
			if !reflect.DeepEqual(requireParkEnded(t, before.Supervision, cur.Supervision.Reviews, wokeBy), before.Supervision.Reviews) {
				t.Fatal("a fresh observation rewrote the recorded wait's history")
			}
		})
	}
}

func TestPRReviewWakeRejectsOlderFacts(t *testing.T) {
	s, _, task := newSupervisedService(t)
	holdDigest(s)
	ctx := context.Background()
	const source = "https://github.com/example/app/pull/12"
	s.setTaskStage(ctx, task.ID, "review", source)
	if _, err := reviewErr(s, task.ID, mergePark(source)); err != nil {
		t.Fatal(err)
	}
	at := todoNow(s, task.ID).Supervision.parking().At
	newer := reviewWakeStatus(t, source, time.Time{}, at.Add(3*time.Second))
	newer.Review, newer.HeadOID = "approved", "newer-head"
	if err := s.applyPRStatus(ctx, todoNow(s, task.ID), newer); err != nil {
		t.Fatal(err)
	}
	stale := reviewWakeStatus(t, source, at.Add(time.Second), at.Add(2*time.Second))
	if err := s.applyPRStatus(ctx, todoNow(s, task.ID), stale); err != nil {
		t.Fatal(err)
	}
	cur := todoNow(s, task.ID)
	if cur.PRStatus.Review != "approved" || cur.PRStatus.HeadOID != "newer-head" ||
		cur.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
		t.Fatalf("an old change request replaced newer head/review facts or woke the wait: %+v", cur)
	}
}

func TestPRReviewWakePersistsAndRecovers(t *testing.T) {
	database := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	const source = "https://github.com/example/app/pull/12"
	s.setTaskStage(ctx, task.ID, "review", source)
	if _, err := reviewErr(s, task.ID, mergePark(source)); err != nil {
		t.Fatal(err)
	}
	before := todoNow(s, task.ID)
	at := before.Supervision.parking().At
	status := reviewWakeStatus(t, source, at.Add(time.Second), at.Add(2*time.Second))
	sends := len(sentToAgent(f))
	database.down.Store(true)
	if err := s.applyPRStatus(ctx, before, status); err == nil {
		t.Fatal("expected the supervision write to fail")
	}
	if cur := todoNow(s, task.ID); cur.PRStatus.Review != "changes_requested" ||
		cur.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) || len(sentToAgent(f)) != sends {
		t.Fatal("failed ledger write lost the saved observation or published a wake")
	}
	database.down.Store(false)
	again, _ := restartedOn(t, database)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	stored := todoNow(again, task.ID)
	raw, err := json.Marshal(stored.PRStatus)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields["changes_requested_at"] != at.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("provider time did not survive a database reload: %s, %v", raw, err)
	}
	again.deploy.forge = &statusForge{status: status}
	again.sweepPRStatuses(ctx, at.Add(2*deployRecheck))
	wokeBy := wakeBound(at.Add(2 * deployRecheck))
	cur := todoNow(again, task.ID)
	beforeHistory, err := json.Marshal(before.Supervision.Reviews)
	if err != nil {
		t.Fatal(err)
	}
	afterHistory, err := json.Marshal(cur.Supervision.Reviews)
	if err != nil {
		t.Fatal(err)
	}
	if cur.DoneAt != nil || cur.Supervision.State != reviewPending ||
		cur.Supervision.Supervisor != before.Supervision.Supervisor ||
		!slices.Equal(pendingKinds(again, task.ID), []string{sigReview}) {
		t.Fatalf("the saved review did not recover through the same wait: state=%s closed=%v supervisor=%q/%q signals=%v; history=%s/%s",
			cur.Supervision.State, cur.DoneAt != nil, cur.Supervision.Supervisor, before.Supervision.Supervisor,
			pendingKinds(again, task.ID), afterHistory, beforeHistory)
	}
	if afterHistory, err = json.Marshal(requireParkEnded(t, before.Supervision, cur.Supervision.Reviews, wokeBy)); err != nil {
		t.Fatal(err)
	}
	if string(afterHistory) != string(beforeHistory) {
		t.Fatalf("the recovered wake rewrote the saved review's history: %s/%s", afterHistory, beforeHistory)
	}
	again.sweepPRStatuses(ctx, at.Add(3*deployRecheck))
	if !slices.Equal(pendingKinds(again, task.ID), []string{sigReview}) {
		t.Fatal("the same recorded review queued another logical wake")
	}
	rows, err := database.ListTodoSupervision(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != reviewPending {
		t.Fatalf("recovered state was not persisted: %+v, %v", rows, err)
	}
	decisions, err := database.ListTodoDecisions(ctx, task.ID, false)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("a forge observation manufactured an owner decision: %+v, %v", decisions, err)
	}
}
