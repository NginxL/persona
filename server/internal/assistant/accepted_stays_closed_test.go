package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// #7539: once a task is accepted (or cancelled), a later turn of its agent that nobody sent for the task — a wakeup
// it set itself, a turn already running at the acceptance — neither opens it again nor tells its supervisor anything.
// Only what its conversation sends the agent (agent_send, the owner's message on its card), the owner's Reopen or
// todo_reopen opens it again. A task the judge closed on its own still opens again on its agent's next turn (#5892).

// supervisionSeen is what the supervisor was told, or is about to be told, of one task: the kinds of its signals
// waiting, the key last told for each kind, and how many supervision messages named it.
type supervisionSeen struct {
	pending []string
	told    map[string]string
	notices int
}

func supervisionOf(s *Service, f *fakeHub, id string) supervisionSeen {
	seen := supervisionSeen{pending: pendingKinds(s, id), told: map[string]string{}}
	b := &s.supervise
	b.mu.Lock()
	for k, v := range b.sent {
		if strings.HasPrefix(k, id+"|") {
			seen.told[k] = v
		}
	}
	b.mu.Unlock()
	for _, n := range f.notices() {
		if strings.Contains(n, id) {
			seen.notices++
		}
	}
	return seen
}

// nothingToldAfter runs the supervision pass now, and again at a time past every wait the pass measures (an answer
// waited for, a stall, a silent transcript), and fails when anything about task id was raised or told since before.
// Nothing has to be waited out: turns and passes raise their signals as they run, a signal waits in the book until a
// supervision message takes it, and the message marks it told as it takes it, so a signal raised by now shows as
// waiting or as told.
func nothingToldAfter(t *testing.T, s *Service, f *fakeHub, id string, before supervisionSeen) {
	t.Helper()
	ctx, now := context.Background(), time.Now()
	expect := 0
	if v := todoNow(s, id).Supervision; v != nil {
		expect = v.ExpectMinutes
	}
	s.superviseWatch(ctx, now)
	s.superviseWatch(ctx, now.Add(max(superviseNeedsAfter, stallAfter(expect), silentAfter(expect))+time.Minute))
	after := supervisionOf(s, f, id)
	if len(after.pending) != 0 || !maps.Equal(after.told, before.told) || after.notices != before.notices {
		t.Fatalf("the supervisor heard of a closed task: waiting %v, told %v (before %v), %d messages (before %d):\n%s",
			after.pending, after.told, before.told, after.notices, before.notices, strings.Join(f.notices(), "\n---\n"))
	}
}

func judgeCalls(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.judged
}

func receiptCount(t *testing.T, s *Service) int {
	t.Helper()
	items, _, err := s.feed(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range items {
		if it.Kind == feedReceipt {
			n++
		}
	}
	return n
}

// reviewAndWait runs task_review on id from its supervisor conv1, and waits until the supervision messages it leaves
// due (what the review itself tells, and a digest still armed for the signals it answered) have gone out, so that a
// snapshot taken next counts them.
func reviewAndWait(t *testing.T, s *Service, id, verdict string) Todo {
	t.Helper()
	args := map[string]any{"todo_id": id, "verdict": verdict, "note": "Checked the PR and the screenshots"}
	if verdict == reviewAccepted { // an acceptance proves every current criterion (#7830)
		args["checks"] = completeReviewChecks(todoNow(s, id))
	}
	raw, _ := json.Marshal(args)
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatalf("%s: %v", verdict, err)
	}
	settleService(t, s)
	got := todoNow(s, id)
	if got.DoneAt == nil {
		t.Fatalf("%s did not close it: %+v", verdict, got)
	}
	return got
}

// closedUnchanged fails when closed task was's state, stage, PR or verdict moved, or it was opened again.
func closedUnchanged(t *testing.T, s *Service, was Todo) {
	t.Helper()
	got := todoNow(s, was.ID)
	if got.DoneAt == nil || !got.DoneAt.Equal(*was.DoneAt) || got.DoneBy != was.DoneBy || got.DoneReason != was.DoneReason ||
		got.ReopenedAt != nil || got.Stage != was.Stage || got.PRURL != was.PRURL || got.Supervision == nil ||
		got.Supervision.State != was.Supervision.State || len(got.Supervision.Reviews) != len(was.Supervision.Reviews) {
		t.Fatalf("the closed task changed:\n got %+v (supervision %+v)\nwant %+v (supervision %+v)", got, got.Supervision, was, was.Supervision)
	}
}

// doneAndReviewed is a plan-started supervised task whose agent finished, told as waiting for review.
func doneAndReviewed(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Fixed it: PR https://github.com/o/r/pull/77 is merged", 0)
	waitFor(t, "the review message", func() bool { return supervisionOf(s, f, todo.ID).notices == 1 })
	return s, f, todo
}

// The case of #7539: accepted, then its agent's own wakeup starts a turn that only confirms nothing is left and ends.
func TestAcceptedPlanTaskStaysClosedOnALateDoneTurn(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	accepted := reviewAndWait(t, s, todo.ID, reviewAccepted)
	if accepted.DoneBy != db.DoneByReview || accepted.PlanID == "" {
		t.Fatalf("not a plan task accepted by its review: %+v", accepted)
	}
	before, judged, receipted := supervisionOf(s, f, todo.ID), judgeCalls(f), receiptCount(t, s)

	waitFor(t, "the clock to pass the acceptance", func() bool { return time.Now().After(*accepted.DoneAt) })
	agentTurn(s, f, proto.StatusWorking, "", 0) // nothing was sent for the task
	closedUnchanged(t, s, accepted)
	agentTurn(s, f, proto.StatusDone, "Checked again: nothing is left. PR https://github.com/o/r/pull/78", 0)
	closedUnchanged(t, s, accepted)
	if n := judgeCalls(f); n != judged {
		t.Fatalf("the late turn was judged: %d judgments, %d before", n, judged)
	}
	if n := receiptCount(t, s); n != receipted {
		t.Fatalf("the late turn got a receipt: %d, %d before", n, receipted)
	}
	nothingToldAfter(t, s, f, todo.ID, before)
}

// A turn already running when the task was accepted ends after it: the same.
func TestAcceptedTaskStaysClosedWhenARunningTurnEndsAfterIt(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	agentTurn(s, f, proto.StatusWorking, "", 0) // it goes on for a moment, still the task's
	accepted := reviewAndWait(t, s, todo.ID, reviewAccepted)
	before, judged, receipted := supervisionOf(s, f, todo.ID), judgeCalls(f), receiptCount(t, s)

	agentTurn(s, f, proto.StatusDone, "Also tidied the notes: PR https://github.com/o/r/pull/78 is merged", 0)
	closedUnchanged(t, s, accepted)
	if n := judgeCalls(f); n != judged {
		t.Fatalf("the turn was judged: %d judgments, %d before", n, judged)
	}
	if n := receiptCount(t, s); n != receipted {
		t.Fatalf("the turn got a receipt: %d, %d before", n, receipted)
	}
	nothingToldAfter(t, s, f, todo.ID, before)
}

// Accepted while its agent's turn end is being read: the receipt still says how the turn ended (as for a task closed
// by hand meanwhile), and that is all: no stage or PR from that reply, no judgment, no signal.
func TestAcceptedWhileItsTurnEndIsRead(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	var accepted Todo
	var before supervisionSeen
	var judged, receipted int
	f.hookReads(receiptHooks{before: func(read int) {
		if read == 1 && accepted.ID == "" { // the receipt's read of the turn: the review comes in now
			accepted = reviewAndWait(t, s, todo.ID, reviewAccepted)
			before, judged, receipted = supervisionOf(s, f, todo.ID), judgeCalls(f), receiptCount(t, s)
		}
	}})
	agentTurn(s, f, proto.StatusDone, "Also tidied the notes: PR https://github.com/o/r/pull/78 is merged", 0)
	f.hookReads(receiptHooks{})
	if accepted.ID == "" {
		t.Fatal("the turn's end was never read")
	}
	closedUnchanged(t, s, accepted)
	if n := receiptCount(t, s); n != receipted+1 {
		t.Fatalf("%d receipts, %d before: the turn's end has none", n, receipted)
	}
	if n := judgeCalls(f); n != judged {
		t.Fatalf("the turn was judged: %d judgments, %d before", n, judged)
	}
	nothingToldAfter(t, s, f, todo.ID, before)
}

// A judgment that lands after the acceptance is of work from before it: dropped, whether the to-do is closed already
// or task_review has written its ledger and not closed the to-do yet. Work after an explicit reopen is judged as ever.
func TestLateJudgmentOfAnAcceptedTaskIsDropped(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	work := time.Now()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	judgedCopy := todoNow(s, todo.ID) // the task as the judge was handed it
	waitFor(t, "the clock to pass the work's start", func() bool { return time.Now().After(work) })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { // task_review, before it closes the to-do
		v.State = reviewAccepted
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	verdicts := []taskDoneCheck{{Verdict: verdictDone, Reason: "merged"}, {Verdict: verdictNotDone, Reason: "one case left"},
		{Verdict: verdictUnsure, Reason: "cannot tell"}}
	for _, c := range verdicts {
		s.superviseJudged(ctx, judgedCopy, work, c, nil)
		if v := todoNow(s, todo.ID).Supervision; v.State != reviewAccepted || len(pendingKinds(s, todo.ID)) != 0 {
			t.Fatalf("judged %s while accepted: ledger %s, waiting %v", c.Verdict, v.State, pendingKinds(s, todo.ID))
		}
	}
	if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Checked", true); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	for _, c := range verdicts {
		s.superviseJudged(ctx, judgedCopy, work, c, nil)
		if v := todoNow(s, todo.ID).Supervision; v.State != reviewAccepted || len(pendingKinds(s, todo.ID)) != 0 {
			t.Fatalf("judged %s once closed: ledger %s, waiting %v", c.Verdict, v.State, pendingKinds(s, todo.ID))
		}
	}

	if err := s.reopenTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	s.superviseJudged(ctx, todoNow(s, todo.ID), time.Now(), verdicts[0], nil)
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || !strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigReview) {
		t.Fatalf("work after the reopen judged done: ledger %s, waiting %v", v.State, pendingKinds(s, todo.ID))
	}
}

// A late judgment that reaches applyTaskDone (the judge answering after task_review began) writes nothing on the task
// either: not the check its card shows, not where its delivery is held, not its stage. Both while task_review has
// written its ledger and not closed the to-do yet, and once it is closed.
func TestLateJudgmentLeavesAnAcceptedTaskAsItWas(t *testing.T) {
	for _, closed := range []bool{false, true} {
		name := "ledger accepted"
		if closed {
			name = "closed"
		}
		t.Run(name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			agentTurn(s, f, proto.StatusWorking, "", 0)
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID] // the stretch the judge was asked about
			s.tasks.mu.Unlock()
			judgedCopy := todoNow(s, todo.ID)
			waitFor(t, "the clock to pass the work's start", func() bool { return time.Now().After(work) })
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { // task_review, before it closes the to-do
				v.State = reviewAccepted
				v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted})
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if closed {
				if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Checked", true); err != nil || !ok {
					t.Fatalf("close: %v %v", ok, err)
				}
			}
			was := todoNow(s, todo.ID)
			if was.Check != nil || was.Delivery != nil {
				t.Fatalf("the task has a check or delivery before the late judgment: %+v %+v", was.Check, was.Delivery)
			}
			for _, c := range []taskDoneCheck{{Verdict: verdictNotDone, Reason: "one case left"},
				{Verdict: verdictDone, Reason: "live", Delivery: &TodoDelivery{State: deliveryLive, At: time.Now()}}} {
				s.applyTaskDone(ctx, judgedCopy, work, true, c, nil)
				got := todoNow(s, todo.ID)
				if got.Check != nil || got.Delivery != nil || got.Stage != was.Stage || got.Supervision.State != reviewAccepted ||
					len(pendingKinds(s, todo.ID)) != 0 {
					t.Fatalf("judged %s late: check %+v, delivery %+v, stage %q (was %q), ledger %s, waiting %v", c.Verdict,
						got.Check, got.Delivery, got.Stage, was.Stage, got.Supervision.State, pendingKinds(s, todo.ID))
				}
			}
		})
	}
}

// What the task's conversation sends its agent after the acceptance opens a plan-started task again, as it does one
// with no plan (#6726): a turn nobody sent and another conversation's send leave it closed, its own send opens it,
// whether it queues the text (agent_send) or cuts in with it (agent_interrupt with then).
func TestAcceptedPlanTaskReopensWhenItsConversationSendsMore(t *testing.T) {
	for _, tool := range []string{"agent_send", "agent_interrupt"} {
		t.Run(tool, func(t *testing.T) {
			s, f := newQuietService(t)
			ctx := context.Background()
			const sid = "s1abcdef"
			turn := func(state string) {
				status := proto.StatusIdle
				if state == proto.StateWorking {
					status = proto.StatusWorking
				}
				f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: status,
					State: state, UpdatedAt: time.Now()})
				if state == proto.StateWorking {
					s.taskWorking(ctx, "box", sid, startRow{})
				}
			}
			done := time.Now()
			putTodo(s, Todo{ID: "todo_a", Title: "Write the plan", Origin: "conv1", PlanID: "plan_1", CreatedAt: done.Add(-time.Hour),
				DoneAt: &done, DoneBy: db.DoneByReview, Supervision: &Supervision{Supervisor: "conv1", State: reviewAccepted},
				Agents: []TodoAgent{{MachineID: "box", SessionID: sid}}})
			s.todos.mu.Lock()
			delete(s.todos.activeBySession, [2]string{"box", sid}) // closed: as setTodoRow does
			s.todos.mu.Unlock()
			turn(proto.StateIdle)
			waitFor(t, "the clock to pass the acceptance", func() bool { return time.Now().After(done) })
			open := func() bool { return todoNow(s, "todo_a").DoneAt == nil }
			send := func(from, text string) {
				t.Helper()
				args := map[string]any{"machine": "box", "session_id": sid, "text": text}
				if tool == "agent_interrupt" {
					args = map[string]any{"machine": "box", "session_id": sid, "then": text}
				}
				if _, err := runJSON(t, s, from, tool, args); err != nil {
					t.Fatalf("%s from %s: %v", tool, from, err)
				}
			}

			turn(proto.StateWorking) // its own wakeup, or the owner typing in its session
			if open() {
				t.Fatal("a turn nobody sent for the task reopened it")
			}
			turn(proto.StateIdle)
			send("conv7", "Also look at the other dashboard")
			turn(proto.StateWorking)
			if open() {
				t.Fatalf("another conversation's %s reopened it", tool)
			}
			turn(proto.StateIdle)
			send("conv1", "Add a table to section three")
			turn(proto.StateWorking)
			if got := todoNow(s, "todo_a"); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("its own conversation's %s did not reopen it: %+v", tool, got)
			}
		})
	}
}

// The owner's message on the task's card opens it again too, and the work it starts is followed as ever: judged done,
// the task waits for review again.
func TestAcceptedPlanTaskReopensForTheOwnersCardMessage(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	reviewAndWait(t, s, todo.ID, reviewAccepted)
	if _, err := s.routeToTask(context.Background(), "conv1", todo.ID, "Please cover the landscape case too"); err != nil {
		t.Fatalf("the owner's message on the card: %v", err)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("the owner's card message did not reopen it: %+v", got)
	}
	agentTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged", 0)
	waitFor(t, "the review again", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	waitFor(t, "the second review message", func() bool { return supervisionOf(s, f, todo.ID).notices == 2 })
}

// The owner's message on the card of an accepted task whose agents have all stopped (the hub retires a done task's
// agents a few minutes after it closes) starts a new agent for it (tasks_successor.go). The task is open again before
// that agent starts, so its work is the task's: followed, judged, and waiting for review again.
func TestAcceptedPlanTaskReopensForACardMessageAfterItsAgentStopped(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	ctx := context.Background()
	reviewAndWait(t, s, todo.ID, reviewAccepted)
	stopped := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		LastText: "Fixed it: PR https://github.com/o/r/pull/77 is merged", Status: proto.StatusOffline, State: proto.StateOffline,
		UpdatedAt: time.Now(), Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"}
	f.setSessions("box", stopped)
	item, err := s.routeToTask(ctx, "conv1", todo.ID, "Please cover the landscape case too")
	if err != nil {
		t.Fatalf("the owner's message on the card: %v", err)
	}
	var body struct {
		NewAgent string `json:"new_agent"`
	}
	_ = json.Unmarshal(item.Body, &body)
	if body.NewAgent == "" {
		t.Fatalf("no new agent started: %s", item.Body)
	}
	successor := func(status, text string) {
		state := proto.StateUnread
		if status == proto.StatusWorking {
			state = proto.StateWorking
		}
		f.setSessions("box", stopped, proto.Session{SessionID: body.NewAgent, MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: status, State: state, LastText: text, UpdatedAt: time.Now()})
		if status == proto.StatusWorking {
			s.taskWorking(ctx, "box", body.NewAgent, startRow{})
		} else {
			s.taskTurnEnded(ctx, "box", body.NewAgent)
		}
	}
	successor(proto.StatusWorking, "")
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("the owner's card message to a stopped task did not reopen it: %+v", got)
	}
	successor(proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
	waitFor(t, "the review again", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	waitFor(t, "the second review message", func() bool { return supervisionOf(s, f, todo.ID).notices == 2 })
}

// Accepted while the end of a turn the model service cut off is read: the interrupted receipt says how it ended, and
// that is all, as for a done end: no notice, no interrupted signal, no report line.
func TestAcceptedWhileACutOffTurnEndIsRead(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	var accepted Todo
	var before supervisionSeen
	var receipted int
	f.hookReads(receiptHooks{before: func(read int) {
		if read == 1 && accepted.ID == "" {
			accepted = reviewAndWait(t, s, todo.ID, reviewAccepted)
			before, receipted = supervisionOf(s, f, todo.ID), receiptCount(t, s)
		}
	}})
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle,
		State: proto.StateUnread, TurnError: "API Error: 529 overloaded_error", TurnErrorKind: proto.TurnErrorTransient,
		UpdatedAt: time.Now()})
	s.taskTurnEnded(context.Background(), "box", "s1")
	f.hookReads(receiptHooks{})
	if accepted.ID == "" {
		t.Fatal("the turn's end was never read")
	}
	closedUnchanged(t, s, accepted)
	if n := receiptCount(t, s); n != receipted+1 {
		t.Fatalf("%d receipts, %d before: the cut-off turn has none", n, receipted)
	}
	s.reports.mu.Lock()
	cuts := s.reports.cuts[todo.ID]
	s.reports.mu.Unlock()
	if cuts != 0 {
		t.Fatalf("an interrupted report line was queued for a closed task (%d cuts)", cuts)
	}
	nothingToldAfter(t, s, f, todo.ID, before)
}

// A cancelled task stays cancelled when its agent ends one more turn "done" after it (#1851), with nothing told.
func TestCancelledTaskStaysClosedOnALateDoneTurn(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	cancelled := reviewAndWait(t, s, todo.ID, reviewCancelled)
	if !cancelled.Cancelled() {
		t.Fatalf("not cancelled: %+v", cancelled)
	}
	before, judged := supervisionOf(s, f, todo.ID), judgeCalls(f)

	agentTurn(s, f, proto.StatusDone, "Finished anyway: PR https://github.com/o/r/pull/78 is merged", 0)
	ended := time.Now()
	waitFor(t, "the clock to pass the turn's end", func() bool { return time.Now().After(ended) })
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Checked again: all done", 0)
	closedUnchanged(t, s, cancelled)
	if !todoNow(s, todo.ID).Cancelled() || judgeCalls(f) != judged {
		t.Fatalf("cancelled task judged or reopened: %+v, %d judgments (%d before)", todoNow(s, todo.ID), judgeCalls(f), judged)
	}
	nothingToldAfter(t, s, f, todo.ID, before)
}

// Unchanged: a task the hub's judge closed on its own (nobody accepted it) opens again on its agent's next turn, plan
// or not, with nothing sent for it (#5892).
func TestJudgeClosedTaskStillReopensOnTheNextTurn(t *testing.T) {
	s, f := newTaskService(t)
	f.judge = `{"verdict":"not_done","reason":"The overview is still missing"}`
	todo := startedTask(t, s, f)
	if todo.PlanID == "" || todo.Supervision != nil {
		t.Fatalf("not an unsupervised plan task: %+v", todo)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "The draft is written", 0)
	if ok, err := s.autoDoneTodo(context.Background(), todo.ID, "Done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	closed := todoNow(s, todo.ID)
	if closed.DoneAt == nil || closed.DoneBy != db.DoneByAuto {
		t.Fatalf("not closed by the judge: %+v", closed)
	}
	waitFor(t, "the clock to pass the judge's close", func() bool { return time.Now().After(*closed.DoneAt) })
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("the judge's close did not give way to its agent's next turn: %+v", got)
	}
}
