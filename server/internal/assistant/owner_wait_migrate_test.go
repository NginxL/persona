package assistant

import (
	"context"
	"slices"
	"testing"
	"time"
)

// Owner waits earlier versions pushed straight to the owner move to the supervisor once; the supervisor's own
// needs_user and a legacy owner_priority park stay as they are, and a second pass moves nothing.
func TestMigrateOwnerWaitsMovesOnlyTheJudgesToTheSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	wait := &OwnerWait{Work: at.Add(-time.Minute), At: at, Reason: "需要你本人操作", PushPending: false}
	judge := Todo{ID: "judge-wait", Title: "judge", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Waits: wait}}
	judgeRework := Todo{ID: "judge-wait-rework", Title: "judge rework", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Round: 2, Waits: &OwnerWait{Work: at, At: at, Reason: "选哪版"},
			Reviews: []TaskReview{{At: at.Add(-time.Hour), Verdict: reviewRework}}}}
	supervisor := Todo{ID: "supervisor-decision", Title: "supervisor", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Reviews: []TaskReview{{At: at, Verdict: reviewNeedsUser}}}}
	supervisorAfterWait := Todo{ID: "supervisor-after-wait", Title: "supervisor after the wait", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Waits: &OwnerWait{Work: at, At: at},
			Reviews: []TaskReview{{At: at.Add(time.Minute), Verdict: reviewNeedsUser}}}}
	legacyPark := Todo{ID: "legacy-park", Title: "legacy park", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Waits: &OwnerWait{Work: at, At: at},
			Reviews: []TaskReview{{At: at.Add(-time.Minute), Verdict: reviewParked}}}}
	for _, td := range []Todo{judge, judgeRework, supervisor, supervisorAfterWait, legacyPark} {
		putTodo(s, td)
	}

	moved := s.migrateOwnerWaits(ctx)
	slices.Sort(moved)
	if !slices.Equal(moved, []string{"judge-wait", "judge-wait-rework"}) {
		t.Fatalf("moved %v", moved)
	}
	if v := todoNow(s, "judge-wait").Supervision; v.State != "" || v.Waits == nil {
		t.Fatalf("judge wait after the move: %+v", v)
	}
	if v := todoNow(s, "judge-wait-rework").Supervision; v.State != reviewRework || v.Waits == nil {
		t.Fatalf("judge wait in rework after the move: %+v", v)
	}
	for _, id := range []string{"supervisor-decision", "supervisor-after-wait", "legacy-park"} {
		if st := todoNow(s, id).Supervision.State; st != reviewNeedsUser {
			t.Errorf("%s: the supervisor's needs_user was moved: %q", id, st)
		}
	}
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("the move pushed: %+v", got)
	}
	if again := s.migrateOwnerWaits(ctx); len(again) != 0 {
		t.Fatalf("a second pass moved %v", again)
	}
}
