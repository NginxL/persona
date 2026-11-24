package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

// An owner hold an Agent reported on a supervised task is its supervisor's until a review says otherwise (#13191):
// not the owner's Needs you, nor an approval in the snapshot. The supervisor's needs_user makes it the owner's, a park
// keeps today's other, and an unsupervised task keeps its owner hold (#10787).
func TestSupervisedDeliveryHoldWaitsForItsSupervisor(t *testing.T) {
	at := time.Date(2026, 11, 22, 16, 40, 49, 0, time.UTC)
	hold := func() *TodoDelivery {
		return &TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser, At: at}
	}
	supervised := func(state string, reviews ...TaskReview) *Supervision {
		return &Supervision{Supervisor: "conv1", State: state, Reviews: reviews}
	}
	cases := []struct {
		name  string
		todo  Todo
		want  string
		owner bool
	}{
		{"supervised, no review", Todo{Delivery: hold(), Supervision: supervised("")}, unblockSupervisor, false},
		{"supervised, review before the hold", Todo{Delivery: hold(), Supervision: supervised("",
			TaskReview{At: at.Add(-time.Hour), Verdict: reviewRework, Note: "补测试"})}, unblockSupervisor, false},
		{"the supervisor's needs_user after it", Todo{Delivery: hold(), Supervision: supervised(reviewNeedsUser,
			TaskReview{At: at.Add(time.Minute), Verdict: reviewNeedsUser, Reason: "批群 A 实发"})}, unblockUser, true},
		{"the supervisor's needs_user before it", Todo{Delivery: hold(), Supervision: supervised(reviewNeedsUser,
			TaskReview{At: at.Add(-time.Minute), Verdict: reviewNeedsUser, Reason: "批群 A 实发"})}, unblockUser, true},
		{"parked on a date after it", Todo{Delivery: hold(), Supervision: supervised(reviewParked,
			TaskReview{At: at.Add(100 * time.Second), Verdict: reviewParked, WaitingOn: parkDate, Reason: "03:30 再看"})}, unblockOther, false},
		{"unsupervised", Todo{Delivery: hold()}, unblockUser, true},
		{"no supervisor named", Todo{Delivery: hold(), Supervision: &Supervision{}}, unblockUser, true},
	}
	for _, tc := range cases {
		got := currentDelivery(tc.todo)
		if got.Unblock != tc.want || got.waitsForOwner() != tc.owner {
			t.Errorf("%s: unblock %q (owner %v), want %q (owner %v)", tc.name, got.Unblock, got.waitsForOwner(), tc.want, tc.owner)
		}
		again := tc.todo
		again.Delivery = got
		if twice := currentDelivery(again); twice.Unblock != got.Unblock || twice.Gate != got.Gate {
			t.Errorf("%s: read twice %+v, once %+v", tc.name, twice, got)
		}
	}
}

// The supervisor hears of an unreviewed owner hold once, after the settle: kept with the hold, so the next sweep (or a
// restart reading the same setting) does not tell it again, and a new hold after a review is told anew.
func TestDeliveryHoldGoesToItsSupervisorOnce(t *testing.T) {
	st := &memStore{}
	s, f, todo := newSupervisedServiceOn(t, planUpdateStore{&planMemStore{memStore: st, plans: map[string]db.TaskPlanRow{}}})
	ctx := context.Background()
	at := time.Now().Add(-2 * deliveryHoldSettle).UTC()
	if !s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser, At: at}) {
		t.Fatal("hold not kept")
	}
	if got := formatTodo(todoNow(s, todo.ID)).Delivery; got.Unblock != unblockSupervisor || !got.SupervisorTold.IsZero() {
		t.Fatalf("the apps read %+v", got)
	}
	if got := s.snapshotApprovals(ctx, ""); len(got) != 0 {
		t.Fatalf("the snapshot lists an approval nobody asked for: %+v", got)
	}
	for _, task := range s.snapshotTasks(ctx, "") {
		if task.ID == todo.ID && task.State == taskStateLabel("owner_wait") {
			t.Fatalf("the snapshot's task list says it waits for the owner: %+v", task)
		}
	}
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now())
	waitFor(t, "the delivery hold message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"signal=delivery_hold", "等用户批授权卡实发验收", "没有能回答的卡片"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}
	if todoNow(s, todo.ID).Delivery.SupervisorTold.IsZero() {
		t.Fatal("not kept on the hold")
	}
	var stages map[string]todoStage
	if raw, ok := st.vals[reportStagesKey]; !ok || json.Unmarshal(raw, &stages) != nil || stages[todo.ID].Delivery.SupervisorTold.IsZero() {
		t.Fatalf("not stored: %s", st.vals[reportStagesKey])
	}
	forgetSignals(t, s)
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigDeliveryHold) {
		t.Fatalf("told twice: %v", got)
	}
	// The same hold reported again with no review since: still told.
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser, At: at.Add(time.Minute)})
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigDeliveryHold) {
		t.Fatalf("the same hold told again: %v", got)
	}
	// A review, then a new owner hold: told anew.
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewRework, Note: "先把切换做完"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser,
		At: time.Now().Add(time.Second).UTC()})
	if got := todoNow(s, todo.ID).Delivery; !got.SupervisorTold.IsZero() {
		t.Fatalf("a hold after a review kept the old mark: %+v", got)
	}
	before = len(f.notices())
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	waitFor(t, "the new hold's message", func() bool { return len(f.notices()) > before })
}

// The supervisor's needs_user makes the hold the owner's: nothing is handed over, and the snapshot lists it.
func TestDeliveryHoldAfterNeedsUserIsTheOwners(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	ctx := context.Background()
	at := time.Now().Add(-2 * deliveryHoldSettle).UTC()
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser, At: at})
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewNeedsUser, Reason: "批群 A 实发"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if got := formatTodo(todoNow(s, todo.ID)).Delivery; got.Unblock != unblockUser {
		t.Fatalf("after needs_user the apps read %+v", got)
	}
	if got := s.snapshotApprovals(ctx, ""); len(got) != 1 {
		t.Fatalf("snapshot approvals %+v", got)
	}
	if !slices.ContainsFunc(s.snapshotTasks(ctx, ""), func(task prompts.SnapshotTask) bool {
		return task.ID == todo.ID && task.State == taskStateLabel("owner_wait")
	}) {
		t.Fatal("the snapshot's task list does not say it waits for the owner")
	}
	s.superviseWatch(ctx, time.Now())
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigDeliveryHold) {
		t.Fatalf("an owner's hold went to the supervisor: %v", got)
	}
}

// A hold that comes due while its conversation is stopped (/stop, #3308) is not marked told, since nothing is queued
// then: once the user speaks again it reaches the supervisor.
func TestDeliveryHoldUnderStopWaitsForTheUser(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	s.markReporter(ctx, "box", todo.Origin)
	s.setReporterStopped(ctx, todo.Origin, "app")
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等用户批授权卡实发验收", Unblock: unblockUser,
		At: time.Now().Add(-2 * deliveryHoldSettle).UTC()})
	s.superviseWatch(ctx, time.Now())
	if !todoNow(s, todo.ID).Delivery.SupervisorTold.IsZero() {
		t.Fatal("marked told while stopped, though nothing was queued")
	}
	s.setReporterStopped(ctx, todo.Origin, "")
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	waitFor(t, "the delivery hold message after /stop is lifted", func() bool {
		for _, n := range f.notices()[before:] {
			if strings.Contains(n, "signal=delivery_hold") {
				return true
			}
		}
		return false
	})
}
