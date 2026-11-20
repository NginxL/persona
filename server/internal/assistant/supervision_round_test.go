package assistant

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// #11553: a plan's task that takes a to-do over (task_plan continues, or its todo_id) starts a new round of the
// to-do's supervision. The earlier round's terminal review stays in the ledger's history, but the ledger no longer
// says accepted or cancelled: no client shows the earlier acceptance on the new work, the to-do is not filed under
// Just finished or Done when its new Agent stops, and the new round's stop short reaches its supervisor. Before, the
// continued to-do sat in Working with the earlier round's green Accepted chip, and superviseJudged dropped its stops
// as more work sent after an acceptance.

// roundAgentOn waits for an Agent other than old to work on todo id, and returns its session.
func roundAgentOn(t *testing.T, s *Service, id, old string) string {
	t.Helper()
	var session string
	waitFor(t, "a new agent on "+id, func() bool {
		for _, a := range todoNow(s, id).Agents {
			if a.SessionID != old && a.active() {
				session = a.SessionID
				return true
			}
		}
		return false
	})
	return session
}

// roundSignalled says whether supervised task id has a signal of kind waiting for, or already told to, its supervisor.
func roundSignalled(s *Service, id, kind string) bool {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	_, pending := s.supervise.pending[id+"|"+kind]
	_, sent := s.supervise.sent[id+"|"+kind]
	return pending || sent
}

func TestContinuationStartsANewSupervisionRound(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		for _, backend := range []string{"memory", "database"} {
			t.Run(verdict+"/"+backend, func(t *testing.T) {
				var store *db.DB
				var st Store
				if backend == "database" {
					store = taskRecoveryDatabase(t)
					st = store
				}
				s, f, todo := plannedTask(t, st)
				ctx := context.Background()
				longTurn(s, f, "working", "")
				longTurn(s, f, "done", "Wrote the report: notes/coupon-risk.md")
				waitFor(t, "the review message", func() bool { return supervisionOf(s, f, todo.ID).notices == 1 })
				// The earlier round was sent back once before its last review.
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.Round = 1
					return true
				}); err != nil {
					t.Fatal(err)
				}
				closed := reviewAndWait(t, s, todo.ID, verdict)
				before := closed.Supervision.clone()
				if before.State != verdict || before.Round != 1 || before.Reviews[len(before.Reviews)-1].Verdict != verdict {
					t.Fatalf("not %s: %+v", verdict, before)
				}
				if _, err := s.ensureTaskBrief(ctx, todoNow(s, todo.ID)); err != nil {
					t.Fatal(err)
				}

				if _, err := s.toolTaskPlan(ctx, "conv1", continuesArgs(todo.ID)); err != nil {
					t.Fatal(err)
				}
				roundAgentOn(t, s, todo.ID, longAgent)

				got := todoNow(s, todo.ID)
				if got.DoneAt != nil {
					t.Fatalf("the continuation did not reopen the to-do: %+v", got)
				}
				v := got.Supervision
				if v.State != "" || v.Round != 0 || v.Waits != nil || v.PendingSince != nil {
					t.Fatalf("the earlier round's %s still decides the continued to-do: state %q, round %d, waits %+v",
						verdict, v.State, v.Round, v.Waits)
				}
				if !reflect.DeepEqual(v.Reviews, before.Reviews) || v.Supervisor != before.Supervisor {
					t.Fatalf("the earlier round's reviews or supervisor were lost:\nbefore %+v\n after %+v", before, v)
				}
				// What every client reads (web acceptanceStands and reviewState, Apple, Android, the hub's triage).
				if acceptanceStands(got) || triageReviewState(got) != "" {
					t.Fatalf("the earlier acceptance still stands for the new round: %+v", v)
				}
				// The new round stops short: its supervisor hears of it.
				s.superviseJudged(ctx, got, time.Now(), taskDoneCheck{Verdict: verdictNotDone, Reason: "The report is half written"}, nil)
				if !roundSignalled(s, todo.ID, sigIdle) {
					t.Fatal("the continuation's stop short did not reach its supervisor")
				}

				if store != nil {
					rows, err := store.ListTodoSupervision(ctx)
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						if row.TodoID != todo.ID {
							continue
						}
						if kept := supervisionFromRow(row); kept.State != "" || kept.Round != 0 || len(kept.Reviews) != len(before.Reviews) {
							t.Fatalf("the stored ledger still has the earlier round: %+v", kept)
						}
						return
					}
					t.Fatal("the to-do's ledger was not stored")
				}
			})
		}
	}
}

// A plan's todo_id takeover of an open to-do whose acceptance stood (its Agent was sent more after the acceptance,
// #3156, and then stopped) is a new round too.
func TestTodoIDTakeoverStartsANewSupervisionRound(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.stoppedAgent01()
	accepted := TaskReview{At: time.Now().Add(-time.Hour), Verdict: reviewAccepted, Note: "Released 1.2"}
	putTodo(e.s, Todo{ID: "todo_x", Title: "发版", Origin: "m1",
		Supervision: &Supervision{Supervisor: "m1", State: reviewAccepted, Reviews: []TaskReview{accepted}},
		Agents:      []TodoAgent{{MachineID: "box", SessionID: "agent01"}}})
	if !acceptanceStands(todoNow(e.s, "todo_x")) {
		t.Fatal("the reopened to-do's acceptance does not stand to begin with")
	}
	if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_x")); err != nil {
		t.Fatal(err)
	}
	newAgentOn(t, e, "todo_x")
	got := todoNow(e.s, "todo_x")
	if got.Supervision.State != "" || acceptanceStands(got) {
		t.Fatalf("the takeover's new Agent works under the earlier acceptance: %+v", got.Supervision)
	}
	if len(got.Supervision.Reviews) != 1 || got.Supervision.Reviews[0].Verdict != reviewAccepted {
		t.Fatalf("the earlier acceptance left the history: %+v", got.Supervision.Reviews)
	}
}

// The rule stays for what it was made for: more work sent to an accepted task's Agent reopens it while its acceptance
// stands (#3156, #7539), and so does a Reopen by hand; neither is a plan's new Agent.
func TestMoreWorkForAnAcceptedTaskKeepsItsAcceptance(t *testing.T) {
	s, f, accepted := acceptedPlanTask(t, nil)
	if err := sendTo(t, s, "agent_send", "conv1", "Also attach the dark screenshot"); err != nil {
		t.Fatal(err)
	}
	longTurn(s, f, "working", "")
	waitFor(t, "the reopen", func() bool { return todoNow(s, accepted.ID).DoneAt == nil })
	got := todoNow(s, accepted.ID)
	if got.Supervision.State != reviewAccepted || !acceptanceStands(got) {
		t.Fatalf("more work for the accepted task dropped its acceptance: %+v", got.Supervision)
	}
}
