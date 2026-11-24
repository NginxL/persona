package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// An ended direction's open tasks go to the main chat's supervision, as the Unpin confirmation says (#11394). Both ways
// a direction ends hand them over with #13346's transfer: the owner's Unpin and an approved assistant.leaders.

// unpinEnv is main conv1 with directions lead1 and lead2 on a database, and lead1 supervising: a task it planned, with
// a question card delivered to it and unanswered; a task main planned and moved to it, with a report waiting in its
// queue while it works; a start that failed and still waits; and a task it cancelled. Not lead1's: lead2's plan, main's
// own and a task leader's worker.
type unpinEnv struct {
	t                                 *testing.T
	s                                 *Service
	f                                 *fakeHub
	database                          Store
	planned, transferred, failed      Todo // lead1's open tasks
	cancelled, other, mainOwn, worker Todo // not lead1's open tasks
	leadNotices                       int  // what lead1 was sent before its direction ended
}

const (
	unpinCard   = "unpin-card"
	unpinReport = "A question queued for the lead"
)

// planTitled has conversation plan one read-only task titled title (at most 20 characters: a plan clips a longer one), in
// a directory of its own, and waits for its Agent.
func planTitled(t *testing.T, s *Service, conversation, title string) Todo {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": title, "prompt": title + ": fix it and add a test", "machine": "box", "cwd": "/code/" + strings.ToLower(strings.ReplaceAll(title, " ", "-")), "readonly": true,
		"why": "the repository is on box", "done_when": []string{"PR merged to main"}, "expect_minutes": 30,
	}}})
	out, err := s.toolTaskPlan(t.Context(), conversation, raw)
	if err != nil {
		t.Fatalf("%s plans %q: %s %v", conversation, title, out, err)
	}
	var todo Todo
	waitFor(t, title+"'s Agent", func() bool {
		var ok bool
		todo, ok = todoByTitle(s, title)
		return ok && len(todo.Agents) == 1
	})
	return todoNow(s, todo.ID)
}

func newUnpinEnv(t *testing.T) *unpinEnv {
	t.Helper()
	database := todoTestDB(t) // ending and making a direction need a database
	s, f := newQuietServiceOn(t, database)
	f.agents = 999999 // agent_transfer needs eight characters of a session id
	f.judge = `{"verdict":"done","reason":"PR merged"}`
	f.known["lead1"], f.known["lead2"] = true, true
	e := &unpinEnv{t: t, s: s, f: f, database: database}
	e.conversations(proto.StatusIdle)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1", "lead2"} })
	ctx := t.Context()

	e.planned = planTitled(t, s, "lead1", "Planned by the lead")
	e.transferred = planTitled(t, s, "conv1", "Moved to the lead")
	e.cancelled = planTitled(t, s, "lead1", "Cancelled by lead")
	e.other = planTitled(t, s, "lead2", "The other lead's")
	e.mainOwn = planTitled(t, s, "conv1", "Main's own")
	var agents []proto.Session
	for _, task := range []Todo{e.planned, e.transferred, e.cancelled, e.other, e.mainOwn} {
		a := task.Agents[0]
		agents = append(agents, proto.Session{SessionID: a.SessionID, MachineID: a.MachineID, Status: proto.StatusWorking})
	}
	f.setSessions("box", agents...)
	a := e.transferred.Agents[0]
	if _, err := s.toolAgentTransfer(ctx, "conv1", transferArgs(a.MachineID, a.SessionID, "lead1")); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSON(t, s, "lead1", "task_review", map[string]any{"todo_id": e.cancelled.ID, "verdict": "cancelled", "note": "Not needed"}); err != nil {
		t.Fatal(err)
	}
	failed, err := s.createTodo(ctx, "A start that failed", "", "lead1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, failed.ID, "lead1", []string{"PR merged to main"}, 0, "plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.failTodo(ctx, failed.ID, "the shell waits for an answer"); err != nil {
		t.Fatal(err)
	}
	leader := e.planned.Agents[0]
	worker, err := s.createTodo(ctx, "A task leader worker", "", leaderRef(leader.MachineID, leader.SessionID), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, worker.ID, leaderRef(leader.MachineID, leader.SessionID), []string{"Tests pass"}, 0); err != nil {
		t.Fatal(err)
	}
	settleService(t, s)

	// The planned task's question reaches lead1, which has not answered it.
	ix := askQuestion(unpinCard, e.planned.Agents[0].SessionID)
	ix.MachineID = e.planned.Agents[0].MachineID
	waitCtx, cancel := context.WithCancel(context.Background())
	waited := make(chan struct{})
	go func() { _, _ = s.waitInteraction(waitCtx, ix); close(waited) }()
	t.Cleanup(func() { cancel(); <-waited })
	waitFor(t, "lead1's card", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("lead1"), "\n"), "interaction_id="+unpinCard)
	})
	settleService(t, s)
	// lead1 is at work: the moved task's report waits in its queue.
	e.conversations(proto.StatusWorking)
	s.signal(superviseSignal{Todo: e.transferred.ID, Kind: sigReport, Report: upReportQuestion, Key: "unpin-report", Detail: unpinReport})
	waitFor(t, "lead1's queued report", func() bool { return s.queuedFor("lead1") == 1 })

	e.failed, e.cancelled, e.worker = todoNow(s, failed.ID), todoNow(s, e.cancelled.ID), todoNow(s, worker.ID)
	e.planned, e.transferred = todoNow(s, e.planned.ID), todoNow(s, e.transferred.ID)
	for _, task := range []Todo{e.planned, e.transferred, e.failed, e.cancelled} {
		if task.Supervision.Supervisor != "lead1" {
			t.Fatalf("%q is not lead1's to begin with: %+v", task.Title, task.Supervision)
		}
	}
	if !e.failed.failedStart() || !e.cancelled.closedForGood() {
		t.Fatal("the failed start and the cancelled task are not what they should be")
	}
	e.leadNotices = len(f.noticesTo("lead1"))
	return e
}

// conversations lists conv1 and lead2 idle and lead1 as lead1Status.
func (e *unpinEnv) conversations(lead1Status string) {
	e.f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: lead1Status},
		proto.Session{SessionID: "lead2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
}

// handedBack checks, on s (the manager or the same one restarted), that lead1's open tasks are main's explicit
// assignments that route to main, and that everything else kept its supervisor.
func (e *unpinEnv) handedBack(s *Service) {
	e.t.Helper()
	cfg := s.settings(e.t.Context())
	for _, task := range []Todo{e.planned, e.transferred, e.failed} {
		v := todoNow(s, task.ID).Supervision
		if v == nil || v.Supervisor != "conv1" || v.SupervisorSource != "explicit" {
			e.t.Errorf("%q was not handed to main: %+v", task.Title, v)
		}
		for _, kind := range []string{sigReport, sigIdle, sigReview, sigFailed, sigStarted, sigAllStopped} {
			if got := s.digestTo(e.t.Context(), cfg, superviseSignal{Todo: task.ID, Kind: kind}); got != "conv1" {
				e.t.Errorf("%q's %s goes to %s", task.Title, kind, got)
			}
		}
	}
	for _, task := range []Todo{e.cancelled, e.other, e.mainOwn, e.worker} {
		got, want := todoNow(s, task.ID).Supervision, task.Supervision
		if got == nil || got.Supervisor != want.Supervisor || got.SupervisorSource != want.SupervisorSource {
			e.t.Errorf("%q changed supervisor: %+v, was %+v", task.Title, got, want)
		}
	}
	if got := s.digestTo(e.t.Context(), cfg, superviseSignal{Todo: e.other.ID, Kind: sigIdle}); got != "lead2" {
		e.t.Errorf("the other lead's task goes to %s", got)
	}
	if s.queuedFor("lead1") != 0 {
		e.t.Error("lead1's queue kept the moved task's report")
	}
}

// delivered checks that the card and the queued report reached main once each, and lead1 nothing more.
func (e *unpinEnv) delivered() {
	e.t.Helper()
	waitFor(e.t, "main's card and report", func() bool {
		all := strings.Join(e.f.noticesTo("conv1"), "\n")
		return strings.Contains(all, "interaction_id="+unpinCard) && strings.Contains(all, unpinReport)
	})
	settleService(e.t, e.s)
	if _, ok := e.s.routedTo(unpinCard, "conv1", false); !ok {
		e.t.Error("the card kept its route to lead1")
	}
	all := strings.Join(e.f.noticesTo("conv1"), "\n")
	if strings.Count(all, "interaction_id="+unpinCard) != 1 || strings.Count(all, unpinReport) != 1 {
		e.t.Errorf("main was told the card or the report more than once: %s", all)
	}
	if n := len(e.f.noticesTo("lead1")); n != e.leadNotices {
		e.t.Errorf("lead1 was sent %d more messages after its direction ended: %q", n-e.leadNotices, e.f.noticesTo("lead1")[e.leadNotices:])
	}
	for _, p := range e.f.pushed() {
		if p.Interaction != nil && p.Interaction.ID == unpinCard {
			e.t.Error("the hand-back pushed the card to the owner")
		}
	}
}

// restartAndRepin checks the hand-back after a manager restart, then that pinning lead1 again does not take its old
// tasks back.
func (e *unpinEnv) restartAndRepin() {
	e.t.Helper()
	again := newRestartedService(e.t, Options{Hub: e.f, Store: func() Store { return e.database }, Logf: e.t.Logf})
	shortSupervise(e.t, again)
	noDirectionsTimers(again)
	if err := again.ensureTodosLoaded(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
	e.handedBack(again)
	if _, err := again.makeDirection(e.t.Context(), "lead1", "", "", ""); err != nil {
		e.t.Fatal(err)
	}
	if !again.isLeader(e.t.Context(), "lead1") {
		e.t.Fatal("lead1 was not pinned again")
	}
	e.handedBack(again)
	settleService(e.t, again)
}

func TestUnpinHandsTheDirectionsOpenTasksToMain(t *testing.T) {
	e := newUnpinEnv(t)
	e.conversations(proto.StatusIdle) // main and lead1 can take messages: nothing waits for a turn
	if w := serve(e.s, "POST", "/api/v1/assistant/directions/lead1/end", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ended":true`) {
		t.Fatalf("unpin: %d %s", w.Code, w.Body)
	}
	if e.s.isLeader(t.Context(), "lead1") {
		t.Fatal("lead1 is still a lead")
	}
	e.handedBack(e.s)
	e.delivered()
	e.restartAndRepin()
}

func TestApprovedLeadersChangeHandsTheRemovedDirectionsOpenTasksToMain(t *testing.T) {
	e := newUnpinEnv(t)
	e.conversations(proto.StatusIdle)
	ctx := t.Context()
	raw, _ := json.Marshal(map[string]any{"action": "set", "key": "assistant.leaders", "value": []string{"lead2"}})
	out := make(chan string, 1)
	go func() {
		r := e.s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: raw, MachineID: "server", SessionID: "conv1"})
		out <- r.Content[0].Text
	}()
	q, ok := awaitCard(e.s)
	if !ok {
		t.Fatal("no approval card")
	}
	allow := true
	answer, _ := json.Marshal(proto.Resolution{Allow: &allow})
	if _, err := e.s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
		t.Fatal(err)
	}
	got := <-out
	if e.s.isLeader(ctx, "lead1") || !e.s.isLeader(ctx, "lead2") {
		t.Fatalf("leaders: %v (%s)", e.s.settings(ctx).Leaders, got)
	}
	e.handedBack(e.s)
	e.delivered()
	// The assistant is told what moved, and that pinning again does not move it back.
	if !strings.HasPrefix(got, "assistant.leaders is") || !strings.Contains(got, "The 3 open tasks of the leads taken out") ||
		!strings.Contains(got, "does not take them back") {
		t.Errorf("approved: %s", got)
	}
	e.restartAndRepin()
}
