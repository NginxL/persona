package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// Only existing entry points and JSON are used: restoring baseline production code still compiles these tests.
func TestPersonReplyPersistsAndRecovers(t *testing.T) {
	ctx := context.Background()
	database := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := reviewErr(s, task.ID, personReplyParkArgs(now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.Reviews[len(v.Reviews)-1].At = now.Add(-5 * time.Minute)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	before := todoNow(s, task.ID)
	beforeJSON, err := json.Marshal(before.Supervision)
	if err != nil {
		t.Fatal(err)
	}
	beforeRows, err := database.ListTodoSupervision(ctx)
	if err != nil || len(beforeRows) != 1 {
		t.Fatalf("initial ledger: %v, %v", beforeRows, err)
	}
	reply := personReplyObservation(now)
	calls := 0
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		calls++
		return reply, nil
	})
	sends := len(sentToAgent(f))
	database.down.Store(true)
	s.sweepDeploys(now)
	afterJSON, err := json.Marshal(todoNow(s, task.ID).Supervision)
	if err != nil || string(afterJSON) != string(beforeJSON) || pendingReviewSignal(s, task.ID) ||
		len(sentToAgent(f)) != sends || calls != 1 {
		t.Fatalf("failed write published a receipt/wake or skipped the actual read: calls=%d ledger=%s/%s err=%v",
			calls, afterJSON, beforeJSON, err)
	}
	rows, err := database.ListTodoSupervision(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != reviewParked ||
		string(rows[0].Reviews) != string(beforeRows[0].Reviews) {
		t.Fatalf("a failed ledger write partially committed the reply: %+v, %v", rows, err)
	}
	database.down.Store(false)
	again, hub := restartedOn(t, database)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := todoNow(again, task.ID)
	loadedJSON, _ := json.Marshal(loaded.Supervision.Reviews)
	var saved []map[string]any
	if err := json.Unmarshal(loadedJSON, &saved); err != nil || len(saved) != 1 {
		t.Fatalf("stored review: %s, %v", loadedJSON, err)
	}
	evidence, ok := saved[0]["evidence"].(map[string]any)
	if !ok || evidence["reply"] == nil || saved[0]["reply"] != nil {
		t.Fatalf("binding was lost or a failed observation was stored: %s", loadedJSON)
	}
	reads := 0
	hub.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		reads++
		return reply, nil
	})
	again.sweepDeploys(now.Add(time.Minute))
	wokeBy := wakeBound(now.Add(time.Minute))
	cur := todoNow(again, task.ID)
	if reads != 1 || cur.DoneAt != nil || cur.Supervision.State != reviewPending ||
		cur.Supervision.Supervisor != before.Supervision.Supervisor ||
		!slices.Equal(pendingKinds(again, task.ID), []string{sigReview}) {
		t.Fatalf("reconstruction did not retry the exact saved park: reads=%d state=%s supervisor=%q signals=%v",
			reads, cur.Supervision.State, cur.Supervision.Supervisor, pendingKinds(again, task.ID))
	}
	raw, _ := json.Marshal(cur.Supervision.Reviews)
	var reviews []map[string]any
	if err := json.Unmarshal(raw, &reviews); err != nil || len(reviews) != 1 {
		t.Fatalf("stored reply: %s, %v", raw, err)
	}
	receipt, ok := reviews[0]["reply"].(map[string]any)
	if !ok {
		t.Fatalf("the transition did not retain source evidence: %s", raw)
	}
	source, ok := receipt["reply"].(map[string]any)
	if !ok || source["id"] != reply["reply"].(map[string]any)["id"] || receipt["request"] == nil || receipt["watch"] == nil {
		t.Fatalf("receipt identity missing: %s", raw)
	}
	endedRaw, _ := json.Marshal(requireParkEnded(t, before.Supervision, cur.Supervision.Reviews, wokeBy))
	var rest []map[string]any
	if err := json.Unmarshal(endedRaw, &rest); err != nil || len(rest) != 1 {
		t.Fatalf("stored reply: %s, %v", endedRaw, err)
	}
	delete(rest[0], "reply")
	unchanged, _ := json.Marshal(rest)
	var old []map[string]any
	oldRaw, _ := json.Marshal(before.Supervision.Reviews)
	if err := json.Unmarshal(oldRaw, &old); err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(old)
	if string(unchanged) != string(oldJSON) {
		t.Fatalf("reply rewrote the supervisor's review: %s/%s", unchanged, oldJSON)
	}
	again.sweepDeploys(now.Add(2 * time.Minute))
	if reads != 1 || !slices.Equal(pendingKinds(again, task.ID), []string{sigReview}) {
		t.Fatal("the same receipt was read or logically signaled again")
	}
	// Simulate loss of the in-memory signal after commit: the ordinary watch must recover the review from the
	// persisted state, without another source read or an extra owner decision.
	third, _ := restartedOn(t, database)
	holdDigest(third)
	if err := third.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	stored := todoNow(third, task.ID)
	storedRaw, _ := json.Marshal(stored.Supervision.Reviews)
	if stored.Supervision.State != reviewPending || string(storedRaw) != string(raw) {
		t.Fatalf("the atomic receipt/transition did not survive restart: %s", storedRaw)
	}
	signals := watchSignals(stored, nil, now.Add(3*time.Minute))
	if len(signals) != 1 || signals[0].Kind != sigReview {
		t.Fatalf("post-commit reconstruction did not recover review: %+v", signals)
	}
	third.signal(signals[0])
	if !slices.Equal(pendingKinds(third, task.ID), []string{sigReview}) {
		t.Fatal("the persisted review was not recoverable by the existing signal path")
	}
	decisions, err := database.ListTodoDecisions(ctx, task.ID, false)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("a third-party reply manufactured owner consent: %+v, %v", decisions, err)
	}
}
