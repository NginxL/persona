package assistant

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskScopeInvalidatesTerminalReview(t *testing.T) {
	for _, state := range []string{reviewAccepted, reviewCancelled} {
		for _, c := range []struct {
			name       string
			change     string
			round      int
			verdict    string
			judgeError error
			wantSignal string
		}{
			{name: "added criterion", change: "add", verdict: verdictNotDone, wantSignal: sigIdle},
			{name: "replaced criteria", change: "replace", verdict: verdictUnsure, wantSignal: sigUnjudged},
			{name: "removed criterion", change: "remove", verdict: verdictNotDone, wantSignal: sigIdle},
			{name: "judge unavailable", change: "add", judgeError: errors.New("judge unavailable"), wantSignal: sigUnjudged},
			{name: "completed follow-up", change: "add", verdict: verdictDone, wantSignal: sigReview},
			{name: "existing rework round", change: "add", round: 1, verdict: verdictNotDone, wantSignal: sigIdle},
			{name: "same criteria", change: "same", verdict: verdictNotDone},
			{name: "reordered criteria", change: "reorder", verdict: verdictNotDone},
			{name: "expected time only", change: "time", verdict: verdictNotDone},
			{name: "description only", change: "description", verdict: verdictNotDone},
		} {
			t.Run(state+"/"+c.name, func(t *testing.T) {
				s, _, todo := newSupervisedService(t)
				ctx := context.Background()
				brief, err := s.ensureTaskBrief(ctx, todo)
				if err != nil {
					t.Fatal(err)
				}
				prior := TaskReview{At: time.Now().Add(-time.Hour), Verdict: state, Note: "The earlier scope was reviewed"}
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.State, v.Round = state, c.round
					v.Reviews = append(v.Reviews, prior)
					v.Criteria[0].State, v.Criteria[0].Evidence = "met", "Earlier evidence"
					return true
				}); err != nil {
					t.Fatal(err)
				}
				todo = todoNow(s, todo.ID)
				before := todo.Supervision.clone()
				next := slices.Clone(brief.Criteria)
				change := scopeChange{By: "conv1", Change: "Follow-up work", Why: "Update the task requirements"}
				changed := true
				switch c.change {
				case "add":
					next = append(next, "An additional regression test passes")
				case "replace":
					next = []string{"An additional regression test passes"}
				case "remove":
					next = next[:1]
				case "same":
					changed = false
				case "reorder":
					slices.Reverse(next)
					changed = false
				case "time":
					next, change.Expect, changed = nil, brief.ExpectNow+15, false
				case "description":
					next, changed = nil, false
				}
				change.DoneWhen = next
				if _, err := s.applyScope(ctx, todo, change); err != nil {
					t.Fatal(err)
				}
				current := todoNow(s, todo.ID)
				wantState := state
				if changed {
					wantState = ""
					if c.round > 0 {
						wantState = reviewRework
					}
				}
				if got := current.Supervision.State; got != wantState {
					t.Fatalf("scope left review state %q; want %q", got, wantState)
				}
				if !reflect.DeepEqual(current.Supervision.Reviews, before.Reviews) ||
					current.Supervision.Supervisor != before.Supervisor || current.Supervision.Round != before.Round {
					t.Fatalf("scope discarded review history or ownership: before %+v, after %+v", before, current.Supervision)
				}
				for _, criterion := range current.Supervision.Criteria {
					if criterion.Criterion == before.Criteria[0].Criterion &&
						(criterion.State != "met" || criterion.Evidence != "Earlier evidence") {
						t.Fatalf("unchanged criterion lost its evidence: %+v", criterion)
					}
				}
				s.superviseJudged(ctx, current, time.Now(), taskDoneCheck{Verdict: c.verdict, Reason: "Follow-up result"}, c.judgeError)
				s.supervise.mu.Lock()
				defer s.supervise.mu.Unlock()
				for _, kind := range []string{sigIdle, sigUnjudged, sigReview} {
					_, pending := s.supervise.pending[todo.ID+"|"+kind]
					_, sent := s.supervise.sent[todo.ID+"|"+kind]
					if got, want := pending || sent, kind == c.wantSignal; got != want {
						t.Errorf("signal %s = %v; want %v", kind, got, want)
					}
				}
			})
		}
	}
}

func TestTaskScopePersistsInvalidatedReview(t *testing.T) {
	database := todoTestDB(t)
	s, _, todo := newSupervisedServiceOn(t, database)
	ctx := context.Background()
	brief, err := s.ensureTaskBrief(ctx, todo)
	if err != nil {
		t.Fatal(err)
	}
	prior := TaskReview{At: time.Now().Add(-time.Hour), Verdict: reviewAccepted, Note: "Earlier report accepted"}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State, v.Reviews = reviewAccepted, []TaskReview{prior}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	if _, err := s.applyScope(ctx, todo, scopeChange{
		By: "conv1", Change: "Add a follow-up check", Why: "The task now includes implementation",
		DoneWhen: append(slices.Clone(brief.Criteria), "An additional regression test passes"),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListTodoSupervision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.TodoID != todo.ID {
			continue
		}
		v := supervisionFromRow(row)
		if v.State != "" || len(v.Reviews) != 1 || v.Reviews[0].Verdict != reviewAccepted ||
			len(v.DoneWhen) != len(brief.Criteria)+1 {
			t.Fatalf("persisted supervision still uses the old acceptance: %+v", v)
		}
		return
	}
	t.Fatal("the task's supervision row was not persisted")
}

func TestAgentSendRescopeRestoresMainSupervision(t *testing.T) {
	s, f := newQuietServiceOn(t, todoTestDB(t))
	ctx := context.Background()
	s.noBackground = false
	on := true
	setSettings(t, s, func(v *Settings) { v.MainSupervisesOwnTasks = &on })
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{
		"machine": "box", "session_id": "sec00001", "title": "Investigate a regression",
		"done_when": []string{"The investigation report is delivered"}, "expect_minutes": 30,
	}); err != nil {
		t.Fatal(err)
	}
	todo := onlyTodo(s)
	agent := todo.Agents[0]
	turn := func(status, reply string) {
		state := proto.StateUnread
		if status == proto.StatusWorking {
			state = proto.StateWorking
		}
		f.setSessions(agent.MachineID, proto.Session{
			SessionID: agent.SessionID, MachineID: agent.MachineID, Cwd: "/code/desk", Agent: "claude",
			Status: status, State: state, LastText: reply, UpdatedAt: time.Now(),
		})
		if status == proto.StatusWorking {
			s.taskWorking(ctx, agent.MachineID, agent.SessionID, startRow{})
		} else {
			s.taskTurnEnded(ctx, agent.MachineID, agent.SessionID)
		}
	}
	f.pageTurns = true
	f.judge = `{"verdict":"done","reason":"Investigation complete"}`
	f.setItems(text("assistant", "The investigation report is complete"))
	turn(proto.StatusWorking, "")
	turn(proto.StatusDone, "The investigation report is complete")
	waitFor(t, "the original review message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": todo.ID, "verdict": reviewAccepted, "note": "The original work is accepted",
		"checks": []ReviewCheck{{
			Criterion: "The investigation report is delivered", Met: true,
			Evidence: "The investigation report is complete",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatal("the original task was not accepted and closed")
	}
	if err := s.reopenTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
		"machine": todo.Agents[0].MachineID, "session_id": todo.Agents[0].SessionID,
		"text": "Implement the follow-up and add its regression test",
		"task": map[string]any{
			"title": todo.Title, "expect_minutes": 30,
			"done_when": append(slices.Clone(todo.Supervision.DoneWhen), "An additional regression test passes"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != "" || len(v.Reviews) != 1 {
		t.Fatalf("agent_send retained the old terminal state or discarded history: %+v", v)
	}
	f.judge = `{"verdict":"not_done","reason":"The new regression test is pending"}`
	f.setItems(text("user", "Implement the follow-up"), text("assistant", "The regression test is still pending"))
	turn(proto.StatusWorking, "")
	turn(proto.StatusDone, "The regression test is still pending")
	waitFor(t, "the new main supervision message", func() bool { return len(f.noticesTo("conv1")) == 2 })
	if got := f.noticesTo("conv1")[1]; !strings.Contains(got, todo.ID) ||
		!strings.Contains(got, "The new regression test is pending") {
		t.Fatalf("main did not receive the new unfinished work: %s", got)
	}
	if bg := s.settings(ctx).BackgroundSupervisor; bg != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("the follow-up was rerouted to a background conversation: %q", bg)
	}
}
