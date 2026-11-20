package assistant

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

func TestPRReviewWakeMessageDoesNotCompleteCriteria(t *testing.T) {
	const source = "https://github.com/example/app/pull/12"
	for _, state := range []string{"merged", "closed", "changes requested"} {
		t.Run(state, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			ctx := context.Background()
			criteria := []string{
				"Revalidate and merge the original delivery PR after the prerequisite repair",
				"Deploy and verify the delivery in production",
			}
			if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
				v.DoneWhen = criteria
				return true
			}); err != nil {
				t.Fatal(err)
			}
			s.setTaskStage(ctx, task.ID, "review", source)
			if _, err := reviewErr(s, task.ID, mergePark(source)); err != nil {
				t.Fatal(err)
			}
			before := todoNow(s, task.ID)
			at := before.Supervision.parking().At
			status := TodoPRStatus{State: state}
			if state == "changes requested" {
				status = reviewWakeStatus(t, source, at.Add(time.Second), at.Add(2*time.Second))
			}
			s.deploy.forge = &statusForge{status: status}
			s.sweepPRStatuses(ctx, at.Add(2*time.Second))
			cur := todoNow(s, task.ID)
			if cur.DoneAt != nil || cur.Supervision.State != reviewPending ||
				!slices.Equal(cur.Supervision.DoneWhen, criteria) ||
				!reflect.DeepEqual(cur.Supervision.Criteria, before.Supervision.Criteria) ||
				!reflect.DeepEqual(cur.Supervision.Reviews, before.Supervision.Reviews) {
				t.Fatal("a PR review wake completed the task or changed its criteria/history")
			}
			signal, ok := reviewSignal(s, task.ID)
			if !ok || signal.Detail != "PR "+state+": "+source {
				t.Fatalf("review signal lost the actual PR event: %+v, queued=%v", signal, ok)
			}
			entry, ok := s.digestEntry(ctx, signal, "")
			if !ok {
				t.Fatal("the PR event no longer reaches the supervisor")
			}
			text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{entry}})
			for _, want := range append([]string{signal.Detail}, criteria...) {
				if !strings.Contains(text, want) {
					t.Errorf("supervisor message lost %q:\n%s", want, text)
				}
			}
			// Match the legacy Chinese completion claim without adding user-facing copy to Go.
			if strings.Contains(text, "hub \u5224\u65ad\u505a\u5b8c\u4e86") {
				t.Errorf("a PR review wake still claims completion:\n%s", text)
			}
			if _, err := reviewErr(s, task.ID, map[string]any{
				"verdict": "accepted", "reason": "The prerequisite PR changed",
			}); err == nil || !strings.Contains(err.Error(), "has no check") {
				t.Fatalf("want a missing-criterion-check refusal, got %v", err)
			}
			if got := todoNow(s, task.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
				t.Fatal("rejected acceptance changed the open review candidate")
			}
		})
	}
}
