package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the leaders report up (leader_reports.go, #3485): reached, decision and stuck, each told once from the ledger;
// a replay or a restart tells nothing again; stuck at most once per 24 hours. All ids are placeholders.

func (e *todoEventEnv) reports(at time.Time, stuckToo bool) []assistantwire.LeaderReportEvent {
	e.t.Helper()
	e.s.leaderReportsSync(context.Background(), at, stuckToo)
	var out []assistantwire.LeaderReportEvent
	for _, ev := range e.told() {
		if ev.name != assistantwire.EventLeaderReport {
			continue
		}
		var r assistantwire.LeaderReportEvent
		if err := json.Unmarshal(ev.raw, &r); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// leaderTask is a task of leader lead, created now.
func (e *todoEventEnv) leaderTask(title, lead string) string {
	e.t.Helper()
	id := e.create(title).ID
	e.s.superviseTodo(context.Background(), id, lead, []string{"it works"}, 0)
	return id
}

// worked gives todo id an agent that worked on it and was let go: it has started, so it can be stuck (taskStuck).
func (e *todoEventEnv) worked(id string) {
	e.t.Helper()
	gone := time.Now().UTC().Add(-time.Hour)
	e.s.todos.mu.Lock()
	t := e.s.todos.todos[id]
	t.Agents = append(t.Agents, TodoAgent{MachineID: "mac", SessionID: "s-worked", LinkedAt: gone.Add(-time.Hour), ReleasedAt: &gone})
	e.s.todos.mu.Unlock()
}

func (e *todoEventEnv) review(id, verdict, note string) {
	e.t.Helper()
	_, _, err := e.s.updateSupervision(context.Background(), id, func(v *Supervision) bool {
		v.State = verdict
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: verdict, Note: note})
		return true
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestLeaderReportsReachedAndDecisionOncePerReview(t *testing.T) {
	st := &memStore{}
	e := directionEnv(t, st)
	e.leaders("conv_lead_a")
	e.s.leaderReportsSync(context.Background(), time.Now(), false) // reports start now: earlier reviews are history

	a := e.leaderTask("Android sidebar groups", "conv_lead_a")
	b := e.leaderTask("Fix push after redeploy", "conv_lead_a")
	mainTask := e.leaderTask("A task of the main conversation", e.s.settings(context.Background()).Main)
	e.s.todos.mu.Lock()
	e.s.todos.todos[a].PRURL = "https://example.com/pull/7"
	e.s.todos.mu.Unlock()
	e.review(a, reviewAccepted, "")
	e.review(b, reviewNeedsUser, "Ship behind a flag, or wait for the SDK?")
	e.review(mainTask, reviewAccepted, "")

	got := e.reports(time.Now(), false)
	if len(got) != 2 {
		t.Fatalf("reports: %+v", got)
	}
	byTodo := map[string]assistantwire.LeaderReportEvent{got[0].Todo: got[0], got[1].Todo: got[1]}
	if r := byTodo[a]; r.Kind != "reached" || r.Leader != "conv_lead_a" || r.Link != "https://example.com/pull/7" ||
		!strings.Contains(r.Text, "Android sidebar groups") || !strings.HasPrefix(r.ID, "lrep_") || r.At.IsZero() {
		t.Fatalf("reached: %+v", r)
	}
	if r := byTodo[b]; r.Kind != "decision" || !strings.Contains(r.Text, "Ship behind a flag") || r.Link != "" {
		t.Fatalf("decision: %+v", r)
	}

	// Replayed: nothing again. A restart on the same store: nothing again.
	if got = e.reports(time.Now(), true); len(got) != 0 {
		t.Fatalf("replay: %+v", got)
	}
	e2 := directionEnv(t, st)
	e2.leaders("conv_lead_a")
	for _, id := range []string{a, b} { // the same ledger, as loaded again
		tt, _ := e.s.todoNow(id)
		e2.s.todos.mu.Lock()
		cp := tt
		e2.s.todos.todos[id] = &cp
		e2.s.todos.mu.Unlock()
	}
	e2.told()
	if got = e2.reports(time.Now(), false); len(got) != 0 {
		t.Fatalf("a restart told again: %+v", got)
	}

	// A new review is news: reworked, then accepted again.
	e.review(b, reviewRework, "")
	e.review(b, reviewAccepted, "")
	if got = e.reports(time.Now(), false); len(got) != 1 || got[0].Todo != b || got[0].Kind != "reached" {
		t.Fatalf("a new review: %+v", got)
	}
}

func TestLeaderReportsStuckOncePerDay(t *testing.T) {
	e := directionEnv(t, &memStore{})
	e.leaders("conv_lead_a")
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-idle", State: proto.StateIdle},
		proto.Session{MachineID: "mac", SessionID: "s-run", State: proto.StateWorking})
	idle := e.leaderTask("Stopped unfinished", "conv_lead_a")
	running := e.leaderTask("Running for days", "conv_lead_a")
	offline := e.leaderTask("On a machine that does not answer", "conv_lead_a")
	e.s.todos.mu.Lock()
	e.s.todos.todos[idle].Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-idle"}}
	e.s.todos.todos[running].Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-run"}}
	e.s.todos.todos[offline].Agents = []TodoAgent{{MachineID: "gone", SessionID: "s-x"}}
	e.s.todos.mu.Unlock()
	start := time.Now()

	if got := e.reports(start.Add(119*time.Minute), true); len(got) != 0 {
		t.Fatalf("under two hours: %+v", got)
	}
	got := e.reports(start.Add(121*time.Minute), true)
	if len(got) != 1 || got[0].Todo != idle || got[0].Kind != "stuck" || !strings.Contains(got[0].Text, "Stopped unfinished") {
		t.Fatalf("stuck: %+v", got)
	}
	first := got[0].ID
	if got = e.reports(start.Add(3*time.Hour), true); len(got) != 0 {
		t.Fatalf("stuck again within the day: %+v", got)
	}
	// Only the loop reads stuck tasks (it asks the machines): a change's read does not.
	if got = e.reports(start.Add(60*time.Hour), false); len(got) != 0 {
		t.Fatalf("a change's read told stuck: %+v", got)
	}
	got = e.reports(start.Add(50*time.Hour), true)
	if len(got) != 1 || got[0].Todo != idle || got[0].ID == first {
		t.Fatalf("the next day: %+v", got)
	}
}

// A task waiting on the owner (its review needs them) for a day is stuck too, in those words; once its leader ended,
// nothing of its tasks is reported.
func TestLeaderReportsStuckWaitingAndEndedLeader(t *testing.T) {
	e := directionEnv(t, &memStore{})
	e.leaders("conv_lead_a")
	e.s.leaderReportsSync(context.Background(), time.Now(), false)
	a := e.leaderTask("Pick the release branch", "conv_lead_a")
	e.review(a, reviewNeedsUser, "")
	if got := e.reports(time.Now(), false); len(got) != 1 || got[0].Kind != "decision" {
		t.Fatalf("decision: %+v", got)
	}
	got := e.reports(time.Now().Add(25*time.Hour), true)
	if len(got) != 1 || got[0].Kind != "stuck" || !strings.Contains(got[0].Text, "在等你") {
		t.Fatalf("stuck waiting: %+v", got)
	}

	e.leaders()
	b := e.leaderTask("Another", "conv_lead_a")
	e.review(b, reviewAccepted, "")
	if got = e.reports(time.Now().Add(80*time.Hour), true); len(got) != 0 {
		t.Fatalf("an ended leader's tasks: %+v", got)
	}
}

func TestTaskStuckEstimateBoundaries(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name        string
		supervision *Supervision
		after       time.Duration
	}{
		{"no supervision", nil, 2 * time.Hour},
		{"no estimate", &Supervision{}, 2 * time.Hour},
		{"short estimate", &Supervision{ExpectMinutes: 30}, 2 * time.Hour},
		{"long estimate", &Supervision{ExpectMinutes: 180}, 270 * time.Minute},
		{"maximum estimate", &Supervision{ExpectMinutes: 1440}, 36 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := Todo{Supervision: tc.supervision, Agents: []TodoAgent{{SessionID: "worked"}}}
			tr := todoTriage{Section: triageOther, Last: now.Add(-tc.after)}
			if taskStuck(task, tr, true, now.Add(-time.Nanosecond)) {
				t.Fatal("stuck before threshold")
			}
			if !taskStuck(task, tr, true, now) {
				t.Fatal("not stuck at threshold")
			}
			if taskStuck(task, tr, false, now) {
				t.Fatal("offline task reported stuck")
			}
			task.DoneAt = &now
			if taskStuck(task, tr, true, now) {
				t.Fatal("completed task reported stuck")
			}
			task.DoneAt, task.Agents = nil, nil
			if taskStuck(task, tr, true, now) {
				t.Fatal("not-started task reported stuck")
			}
			tr.Section = triageWaiting
			if !taskStuck(task, tr, true, now) {
				t.Fatal("waiting task not stuck at threshold")
			}
		})
	}
}
