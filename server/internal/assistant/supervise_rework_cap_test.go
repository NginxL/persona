package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskReviewReworkKindValidation(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "PR ready")

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "invalid rework_kind",
			args: map[string]any{"todo_id": todo.ID, "verdict": "rework", "rework_kind": "bogus", "note": "fix it"},
			want: "rework_kind must be",
		},
		{
			name: "rework_kind on accepted",
			args: map[string]any{"todo_id": todo.ID, "verdict": "accepted", "rework_kind": "external_input", "checks": completeReviewChecks(todoNow(s, todo.ID))},
			want: "rework_kind applies to rework only",
		},
		{
			name: "rework_kind on cancelled",
			args: map[string]any{"todo_id": todo.ID, "verdict": "cancelled", "rework_kind": "low_roi_edge", "note": "drop"},
			want: "rework_kind applies to rework only",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.args)
			if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestTaskReviewStalledReworkCapKindsAndLegacyDefault(t *testing.T) {
	cases := []struct {
		name  string
		kinds [3]string // 1st, 2nd allowed; 3rd refused
	}{
		{name: "legacy unspecified default", kinds: [3]string{"", "", ""}},
		{name: "repeated_error", kinds: [3]string{reworkRepeatedError, reworkRepeatedError, reworkRepeatedError}},
		{name: "low_roi_edge", kinds: [3]string{reworkLowROIEdge, reworkLowROIEdge, reworkLowROIEdge}},
		{name: "mixed repeated_error and low_roi_edge", kinds: [3]string{reworkRepeatedError, reworkLowROIEdge, reworkRepeatedError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()

			for i := 0; i < 2; i++ {
				waitsForReview(t, s, f, todo.ID, "still working")
				args := map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "missing item"}
				if tc.kinds[i] != "" {
					args["rework_kind"] = tc.kinds[i]
				}
				res := reviewTask(t, s, todo.ID, args)
				if !strings.Contains(res, "Rework round") {
					t.Fatalf("round %d: unexpected result %q", i+1, res)
				}
				v := todoNow(s, todo.ID).Supervision
				if v.Round != i+1 || v.StalledReworkRounds != i+1 || v.stalledReworkRounds() != i+1 {
					t.Fatalf("round %d: round=%d stalled=%d derived=%d", i+1, v.Round, v.StalledReworkRounds, v.stalledReworkRounds())
				}
				last := v.Reviews[len(v.Reviews)-1]
				if last.ReworkKind != tc.kinds[i] || last.StalledReworkRounds != i+1 {
					t.Fatalf("round %d review: %+v", i+1, last)
				}
			}

			waitsForReview(t, s, f, todo.ID, "still missing item")
			args3 := map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "still failing"}
			if tc.kinds[2] != "" {
				args3["rework_kind"] = tc.kinds[2]
			}
			raw, _ := json.Marshal(args3)
			if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil ||
				!strings.Contains(err.Error(), "sent back 2 times") || !strings.Contains(err.Error(), "why_owner") {
				t.Fatalf("3rd stalled rework without decision: %v", err)
			}
			if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || v.Round != 2 || v.stalledReworkRounds() != 2 {
				t.Fatalf("refused rework mutated supervision: %+v", v)
			}

			// With a full owner decision, the 3rd stalled rework escalates to needs_user.
			args3["why_owner"] = "priority"
			args3["reason"] = "Two stalled rounds still failed; how should we proceed?"
			args3["recommend"] = "Accept as-is"
			args3["cost_of_waiting"] = "PR remains open"
			args3["options"] = []map[string]string{
				{"label": "Accept as-is", "leads_to": "Close with note"},
				{"label": "Try once more", "leads_to": "Spend another round"},
			}
			res := reviewTask(t, s, todo.ID, args3)
			if !strings.Contains(res, "sent back 2 times") || !strings.Contains(res, "waits for the user") {
				t.Fatalf("3rd stalled rework with decision: %q", res)
			}
			if v := todoNow(s, todo.ID).Supervision; v.State != reviewNeedsUser || v.Round != 2 {
				t.Fatalf("after escalated 3rd rework: %+v", v)
			}
		})
	}
}

func TestTaskReviewExternalInputAndSubstantiveNewReset(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()

	// 1st stalled rework -> round=1, stalled=1.
	waitsForReview(t, s, f, todo.ID, "first attempt")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkRepeatedError, "note": "same test failure"})
	if v := todoNow(s, todo.ID).Supervision; v.Round != 1 || v.stalledReworkRounds() != 1 {
		t.Fatalf("after 1 repeated_error: %+v", v)
	}

	// Three external_input reworks (PR comments / merge conflicts): round climbs to 4, stalled stays at 1.
	for i := 1; i <= 3; i++ {
		waitsForReview(t, s, f, todo.ID, "addressed external review")
		res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkExternalInput, "note": "new PR comment"})
		if !strings.Contains(res, reworkExternalInput) || !strings.Contains(res, "stalled/low-ROI 1 of 2") {
			t.Fatalf("external_input #%d: unexpected reply %q", i, res)
		}
		v := todoNow(s, todo.ID).Supervision
		if v.Round != 1+i || v.stalledReworkRounds() != 1 {
			t.Fatalf("external_input #%d: round=%d stalled=%d", i, v.Round, v.stalledReworkRounds())
		}
	}
	// Agent message for round > 2 must display maxTotalReworkRounds (10), not max 2.
	sent := sentToAgent(f)
	if last := sent[len(sent)-1]; !strings.Contains(last, "第 4 轮返工，最多 10 轮") {
		t.Fatalf("expected agent prompt to show 4 of 10, got %q", last)
	}

	// 2nd stalled rework (low_roi_edge) -> round=5, stalled=2.
	waitsForReview(t, s, f, todo.ID, "done")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkLowROIEdge, "note": "minor edge case"})
	if !strings.Contains(res, "stalled/low-ROI 2 of 2") {
		t.Fatalf("2nd stalled rework reply: %q", res)
	}
	if v := todoNow(s, todo.ID).Supervision; v.Round != 5 || v.stalledReworkRounds() != 2 {
		t.Fatalf("after 2nd stalled rework: %+v", v)
	}

	// Now stalled==2: another stalled rework is REFUSED.
	waitsForReview(t, s, f, todo.ID, "done")
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "rework_kind": reworkRepeatedError, "note": "still broken"})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil || !strings.Contains(err.Error(), "sent back 2 times") {
		t.Fatalf("expected stalled refusal at stalled=2, got %v", err)
	}

	// But external_input is STILL allowed even when stalled==2 (keeps stalled at 2).
	res = reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkExternalInput, "note": "upstream merge conflict"})
	if !strings.Contains(res, "Rework round 6 of 10 (external_input; stalled/low-ROI 2 of 2)") {
		t.Fatalf("external_input at stalled=2 reply: %q", res)
	}
	if v := todoNow(s, todo.ID).Supervision; v.Round != 6 || v.stalledReworkRounds() != 2 {
		t.Fatalf("after external_input at stalled=2: %+v", v)
	}

	// substantive_new resets consecutive stalled_rework_rounds back to 0!
	waitsForReview(t, s, f, todo.ID, "rebased cleanly")
	res = reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkSubstantiveNew, "note": "prior fix landed; new auth bypass found"})
	if !strings.Contains(res, "Rework round 7 of 10 (substantive_new; stalled/low-ROI 0 of 2)") {
		t.Fatalf("substantive_new reply: %q", res)
	}
	if v := todoNow(s, todo.ID).Supervision; v.Round != 7 || v.StalledReworkRounds != 0 || v.stalledReworkRounds() != 0 {
		t.Fatalf("substantive_new did not reset stalled counter: %+v", v)
	}

	// After the reset, 2 new stalled reworks are allowed (rounds 8 and 9, stalled 1 and 2), and a 3rd is refused again.
	for i := 1; i <= 2; i++ {
		waitsForReview(t, s, f, todo.ID, "fixed")
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": reworkRepeatedError, "note": "new failure"})
		if v := todoNow(s, todo.ID).Supervision; v.Round != 7+i || v.stalledReworkRounds() != i {
			t.Fatalf("post-reset stalled #%d: round=%d stalled=%d", i, v.Round, v.stalledReworkRounds())
		}
	}
	waitsForReview(t, s, f, todo.ID, "still failing")
	raw, _ = json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "rework_kind": reworkLowROIEdge, "note": "nitpick"})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil || !strings.Contains(err.Error(), "sent back 2 times") {
		t.Fatalf("expected 3rd post-reset stalled rework refusal, got %v", err)
	}
}

func TestTaskReviewOverallSafetyValve(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()

	// Alternating external_input and substantive_new up to maxTotalReworkRounds (10) all succeed.
	for i := 1; i <= maxTotalReworkRounds; i++ {
		waitsForReview(t, s, f, todo.ID, "iteration done")
		kind := reworkExternalInput
		if i%2 == 0 {
			kind = reworkSubstantiveNew
		}
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "rework_kind": kind, "note": "round"})
		v := todoNow(s, todo.ID).Supervision
		if v.Round != i || v.stalledReworkRounds() != 0 {
			t.Fatalf("round %d (%s): round=%d stalled=%d", i, kind, v.Round, v.stalledReworkRounds())
		}
	}

	// 11th rework is refused even for external_input and substantive_new without an owner decision.
	waitsForReview(t, s, f, todo.ID, "round 10 done")
	for _, kind := range []string{reworkExternalInput, reworkSubstantiveNew} {
		raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "rework_kind": kind, "note": "one more"})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil ||
			!strings.Contains(err.Error(), "overall rework safety cap 10") || !strings.Contains(err.Error(), "why_owner") {
			t.Fatalf("11th rework (%s) without decision: %v", kind, err)
		}
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || v.Round != maxTotalReworkRounds {
		t.Fatalf("refused safety-cap rework mutated supervision: %+v", v)
	}

	// With a full owner decision, the 11th rework escalates cleanly to needs_user naming the safety cap.
	res := reviewTask(t, s, todo.ID, map[string]any{
		"verdict":         "rework",
		"rework_kind":     reworkExternalInput,
		"note":            "yet another PR review cycle",
		"why_owner":       "priority",
		"reason":          "Reached 10 rework rounds on this PR; continue or close?",
		"recommend":       "Pause and review scope",
		"cost_of_waiting": "PR stays open",
		"options": []map[string]string{
			{"label": "Pause and review scope", "leads_to": "Owner resets direction"},
			{"label": "Drop PR", "leads_to": "Cancel task"},
		},
	})
	if !strings.Contains(res, "sent back 10 times already") || !strings.Contains(res, "overall rework safety cap 10") || !strings.Contains(res, "waits for the user") {
		t.Fatalf("11th rework with decision: %q", res)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewNeedsUser || v.Round != maxTotalReworkRounds {
		t.Fatalf("after safety-cap escalation: %+v", v)
	}
}

func TestSupervisionReloadReconstructsStalledReworkRounds(t *testing.T) {
	now := time.Now().UTC()

	// 1. Persisted external_input after 1 stalled round restores stalled=1.
	v1 := &Supervision{
		Supervisor:          "conv1",
		Round:               2,
		StalledReworkRounds: 1,
		Reviews: []TaskReview{
			{At: now.Add(-2 * time.Minute), Verdict: reviewRework, Round: 1, ReworkKind: reworkRepeatedError, StalledReworkRounds: 1},
			{At: now.Add(-time.Minute), Verdict: reviewRework, Round: 2, ReworkKind: reworkExternalInput, StalledReworkRounds: 1},
		},
	}
	if got := supervisionFromRow(v1.row("todo_1")); got.StalledReworkRounds != 1 || got.stalledReworkRounds() != 1 {
		t.Fatalf("reload external_input: got %+v", got)
	}

	// 2. Persisted substantive_new restores stalled=0.
	v2 := &Supervision{
		Supervisor:          "conv1",
		Round:               3,
		StalledReworkRounds: 0,
		Reviews: []TaskReview{
			{At: now.Add(-3 * time.Minute), Verdict: reviewRework, Round: 1, ReworkKind: reworkRepeatedError, StalledReworkRounds: 1},
			{At: now.Add(-2 * time.Minute), Verdict: reviewRework, Round: 2, ReworkKind: reworkLowROIEdge, StalledReworkRounds: 2},
			{At: now.Add(-time.Minute), Verdict: reviewRework, Round: 3, ReworkKind: reworkSubstantiveNew, StalledReworkRounds: 0},
		},
	}
	if got := supervisionFromRow(v2.row("todo_2")); got.StalledReworkRounds != 0 || got.stalledReworkRounds() != 0 {
		t.Fatalf("reload substantive_new: got %+v", got)
	}

	// 3. Legacy row written before two-track accounting (empty ReworkKind and 0 StalledReworkRounds) restores stalled=Round.
	legacy := &Supervision{
		Supervisor: "conv1",
		Round:      2,
		Reviews: []TaskReview{
			{At: now.Add(-2 * time.Minute), Verdict: reviewRework, Round: 1, Note: "legacy 1"},
			{At: now.Add(-time.Minute), Verdict: reviewRework, Round: 2, Note: "legacy 2"},
		},
	}
	if got := supervisionFromRow(legacy.row("todo_3")); got.StalledReworkRounds != 2 || got.stalledReworkRounds() != 2 {
		t.Fatalf("reload legacy reviews: got %+v", got)
	}

	// 4. Legacy row with Round>0 and no rework reviews restores stalled=Round.
	synthetic := &Supervision{Supervisor: "conv1", Round: 2}
	if got := supervisionFromRow(synthetic.row("todo_4")); got.StalledReworkRounds != 2 || got.stalledReworkRounds() != 2 {
		t.Fatalf("reload synthetic round: got %+v", got)
	}

	// 5. After a terminal accepted review and startSupervisionRound reset, counters are 0.
	s, _, todo := newSupervisedService(t)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State, v.Round, v.StalledReworkRounds = reviewAccepted, 2, 2
		v.Reviews = append(v.Reviews, TaskReview{At: now, Verdict: reviewAccepted, Note: "done"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.startSupervisionRound(ctx, todo.ID)
	if v := todoNow(s, todo.ID).Supervision; v.Round != 0 || v.StalledReworkRounds != 0 || v.stalledReworkRounds() != 0 {
		t.Fatalf("startSupervisionRound did not reset stalled counter: %+v", v)
	}
}

func TestTaskLeaderReworkKindTwoTrackCap(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, map[string]any{"title": "multi-round worker"})
	review := func(args map[string]any) proto.ToolResult {
		t.Helper()
		raw, _ := json.Marshal(args)
		return s.mcpCall(context.Background(), "box", "s1", "task_review", raw, false)
	}

	// Invalid rework_kind is rejected.
	if res := review(map[string]any{"todo_id": w.ID, "verdict": "rework", "rework_kind": "invalid", "note": "x"}); !res.IsError {
		t.Fatalf("expected error for invalid rework_kind: %+v", res)
	}

	// 3 external_input reworks succeed without hitting the 2-round stalled cap.
	for i := 1; i <= 3; i++ {
		if res := review(map[string]any{"todo_id": w.ID, "verdict": "rework", "rework_kind": reworkExternalInput, "note": "PR feedback"}); res.IsError {
			t.Fatalf("leader external_input #%d failed: %+v", i, res)
		}
	}
	if v := todoNow(s, w.ID).Supervision; v.Round != 3 || v.stalledReworkRounds() != 0 {
		t.Fatalf("leader after 3 external_input: %+v", v)
	}

	// 2 stalled reworks succeed, then 3rd stalled rework escalates to needs_user.
	for i := 1; i <= 2; i++ {
		if res := review(map[string]any{"todo_id": w.ID, "verdict": "rework", "rework_kind": reworkRepeatedError, "note": "same error"}); res.IsError {
			t.Fatalf("leader repeated_error #%d failed: %+v", i, res)
		}
	}
	res := review(map[string]any{"todo_id": w.ID, "verdict": "rework", "rework_kind": reworkLowROIEdge, "note": "nitpick"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "sent back 2 times already") {
		t.Fatalf("leader 3rd stalled rework: %+v", res)
	}
	if v := todoNow(s, w.ID).Supervision; v.State != reviewNeedsUser || v.Round != 5 {
		t.Fatalf("leader after 3rd stalled rework: %+v", v)
	}
}
