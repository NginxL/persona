package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The linked progress check's misjudgements of #11587: a working Agent called stuck on the first run and whenever it
// only ran tools, a reason that was the run's own summary, a signal that named the check's computer and read as the
// Agent's report, and an owner's answer that went to the Agent at work instead of its supervisor.

// toolCall is an Agent's tool call at at, as a node pages it.
func toolCall(id, name, input string, at time.Time) proto.Item {
	return proto.Item{ID: id, Role: "assistant", Kind: proto.KindToolUse, ToolName: name, ToolInput: json.RawMessage(input), TS: at}
}

// checkFixture is a supervised to-do with one Agent (box/worker) whose latest turn is items, and a check linked to it.
func checkFixture(t *testing.T, lang i18n.Lang) (*Service, *fakeHub, Todo, Task, *[]proto.Item) {
	t.Helper()
	s, f, todo := scheduledTodoFixture(t)
	f.mu.Lock()
	f.hello.Locale = string(lang)
	f.mu.Unlock()
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
	putTodo(s, todo)
	items := &[]proto.Item{{Role: "user", Kind: proto.KindText, Text: "Find where the limit is decided", TS: time.Now().Add(-time.Hour)}}
	f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
		if machine == "box" {
			return map[string]any{"items": *items}, nil
		}
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant",
			Text: "All three Agents are working normally."}}}, nil // the check run's own summary
	})
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	return s, f, todo, task, items
}

// addCalls appends calls to the Agent's latest turn, as it makes them.
func addCalls(f *fakeHub, items *[]proto.Item, calls ...proto.Item) {
	f.mu.Lock()
	*items = append(*items, calls...)
	f.mu.Unlock()
}

func pendingFor(s *Service, todo string) []superviseSignal {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	var out []superviseSignal
	for _, g := range s.supervise.pending {
		if g.Todo == todo {
			out = append(out, g)
		}
	}
	return out
}

// An Agent busy with tools when its check is linked: the first run only sets the baseline. Before #11587 it compared
// the run's own few seconds with the snapshot taken just before it went out, and told the supervisor the Agent was stuck.
func TestScheduledTodoFirstRunIsOnlyABaseline(t *testing.T) {
	s, f, todo, task, items := checkFixture(t, i18n.En)
	addCalls(f, items, toolCall("c1", "Bash", `{"command":"rg -n limit src/"}`, time.Now().Add(-time.Minute)))
	_, result := assessTodoRun(t, s, task.ID, func() {
		addCalls(f, items, toolCall("c2", "Read", `{"file_path":"/src/limit.go"}`, time.Now()))
	})
	if result.State != "baseline" || result.NoProgress != 0 || result.Reason != i18n.T(i18n.En, "assistant-schedule.todo_baseline") {
		t.Fatalf("the first linked run judged progress: %+v", result)
	}
	if got := pendingFor(s, todo.ID); len(got) != 0 {
		t.Fatalf("the first linked run signaled the supervisor: %+v", got)
	}
	cur, _ := taskByID(s, task.ID)
	if p := cur.TodoProgress; p == nil || p.Snapshot.At.IsZero() || len(p.Snapshot.Calls) != 1 || p.Signaled || p.NoProgress != 0 {
		t.Fatalf("the baseline did not keep what it read: %+v", cur.TodoProgress)
	}
}

// Tool calls since the last check are work unless they wait, poll or repeat: the incident's Agent ran 118 commands and
// reads without a word and was escalated twice; one that only sleeps, polls CI or runs the same command again is not
// working, whatever the number of its calls.
func TestScheduledTodoToolCallsAreWorkWaitsAreNot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		calls   func(now time.Time) []proto.Item
		state   string
		reason  string // a part of the English reason
		streak0 bool   // a no-progress run came before: work resets it
	}{
		{name: "reads and commands", streak0: true, state: "working", reason: "the Agent is working: 3 actions since",
			calls: func(now time.Time) []proto.Item {
				return []proto.Item{
					toolCall("w1", "Bash", `{"command":"rg -n quota src/"}`, now),
					toolCall("w2", "Read", `{"file_path":"/src/quota.go"}`, now),
					toolCall("w3", "Bash", `{"command":"cat docs/limits.md"}`, now),
				}
			}},
		{name: "an edit", state: "working", reason: "1 action since",
			calls: func(now time.Time) []proto.Item {
				return []proto.Item{toolCall("e1", "Edit", `{"file_path":"/src/quota.go"}`, now)}
			}},
		{name: "waits and polls", streak0: true, state: "no_progress", reason: "3 actions, all waits or repeats",
			calls: func(now time.Time) []proto.Item {
				return []proto.Item{
					toolCall("p1", "Bash", `{"command":"sleep 120"}`, now),
					toolCall("p2", "ScheduleWakeup", `{"delaySeconds":600}`, now),
					toolCall("p3", "Bash", `{"command":"gh pr checks 42"}`, now),
				}
			}},
		{name: "the same command again", state: "no_progress", reason: "2 actions, all waits or repeats",
			calls: func(now time.Time) []proto.Item {
				return []proto.Item{
					toolCall("r1", "Bash", `{"command":"rg -n limit src/"}`, now), // in the baseline's turn already
					toolCall("r2", "Bash", `{"command":"rg -n limit src/"}`, now),
				}
			}},
		{name: "calls from before the baseline", state: "no_progress", reason: "No reply, report, criteria change or other activity since",
			calls: func(now time.Time) []proto.Item {
				return []proto.Item{toolCall("o1", "Bash", `{"command":"rg -n other src/"}`, now.Add(-2*time.Hour))}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, task, items := checkFixture(t, i18n.En)
			addCalls(f, items, toolCall("c1", "Bash", `{"command":"rg -n limit src/"}`, time.Now().Add(-time.Minute)))
			assessTodoBaseline(t, s, task.ID)
			want := 0
			if tc.streak0 {
				if _, quiet := assessTodoRun(t, s, task.ID, nil); quiet.State != "no_progress" || quiet.NoProgress != 1 {
					t.Fatalf("a quiet window was not a no-progress run: %+v", quiet)
				}
			}
			if tc.state == "no_progress" {
				want = 1
				if tc.streak0 {
					want = 2
				}
			}
			_, result := assessTodoRun(t, s, task.ID, func() { addCalls(f, items, tc.calls(time.Now())...) })
			if result.State != tc.state || result.NoProgress != want || !strings.Contains(result.Reason, tc.reason) {
				t.Fatalf("%s: got %+v, want %s (%d) with %q", tc.name, result, tc.state, want, tc.reason)
			}
			cur, _ := taskByID(s, task.ID)
			if tc.state == "working" && (cur.TodoProgress.NoProgress != 0 || cur.TodoProgress.Signaled || !cur.TodoProgress.Since.IsZero()) {
				t.Fatalf("work did not restart the streak: %+v", cur.TodoProgress)
			}
		})
	}
}

// A no-progress run's reason is what the hub saw, and so is the owner's question: never the check run's own summary,
// which said the work was going fine (#11587). The English question fits the 120 characters a decision card keeps.
func TestScheduledTodoNoProgressReasonIsEvidence(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			s, f, todo, task, items := checkFixture(t, lang)
			assessTodoBaseline(t, s, task.ID)
			_, first := assessTodoRun(t, s, task.ID, nil)
			run, second := assessTodoRun(t, s, task.ID, func() {
				now := time.Now()
				addCalls(f, items, toolCall("p1", "Bash", `{"command":"sleep 300"}`, now),
					toolCall("p2", "Bash", `{"command":"sleep 300"}`, now))
			})
			for _, r := range []ScheduleTodoAssessment{first, second} {
				if r.State != "no_progress" || strings.Contains(r.Reason, "working normally") {
					t.Fatalf("the reason is not the hub's evidence: %+v", r)
				}
			}
			current, _ := taskByID(s, task.ID)
			if want := scheduleEvidence(lang, s.location(t.Context()), current.TodoProgress.Since, run.At, 2); second.Reason != want {
				t.Fatalf("the streak's waits are not its reason: %q, want %q", second.Reason, want)
			}
			cur, _ := s.todoNow(todo.ID)
			review := cur.Supervision.Reviews[len(cur.Supervision.Reviews)-1]
			if review.Verdict != reviewNeedsUser || !strings.HasPrefix(review.Reason, second.Reason) ||
				strings.Contains(review.Note, "working normally") || !strings.Contains(review.Note, second.Reason) {
				t.Fatalf("the owner's question or its note is not the evidence: %+v", review)
			}
			a := askOf(s, todo.ID)
			if a == nil || strings.HasSuffix(a.Reason, "…") || len([]rune(a.Reason)) > 120 {
				t.Fatalf("the owner's question was cut: %q", a.Reason)
			}
		})
	}
}

// The owner is asked once the task went without progress for the no_progress signal's floor (30 minutes, or 1.5×
// what it should take), not after two short windows; its supervisor hears of the first no-progress run at once.
func TestScheduledTodoAskWaitsForTheFloor(t *testing.T) {
	s, _, todo, task, _ := checkFixture(t, i18n.En)
	s.todoAskAfter = 0 // the real floor
	cur, _ := s.todoNow(todo.ID)
	cur.Supervision.ExpectMinutes = 90
	putTodo(s, cur)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	if got := pendingFor(s, todo.ID); len(got) != 1 || got[0].Kind != sigCheck {
		t.Fatalf("the first no-progress run did not tell the supervisor: %+v", got)
	}
	_, second := assessTodoRun(t, s, task.ID, nil)
	if cur, _ = s.todoNow(todo.ID); second.NoProgress != 2 || cur.Supervision.State == reviewNeedsUser {
		t.Fatalf("a 90-minute task went to the owner after two short windows: %+v %+v", second, cur.Supervision)
	}
	if spinAfter(90) != 135*time.Minute || s.scheduleAskAfter(cur) != 135*time.Minute {
		t.Fatalf("the floor is not the no_progress signal's: %v", s.scheduleAskAfter(cur))
	}
	// 136 minutes without progress.
	sc := s.loadSchedule(t.Context())
	sc.mu.Lock()
	sc.tasks[sc.indexLocked(task.ID)].TodoProgress.Since = time.Now().Add(-136 * time.Minute)
	sc.mu.Unlock()
	_, third := assessTodoRun(t, s, task.ID, nil)
	if cur, _ = s.todoNow(todo.ID); third.NoProgress != 3 || cur.Supervision.State != reviewNeedsUser {
		t.Fatalf("a stall past the floor did not reach the owner: %+v %+v", third, cur.Supervision)
	}
}

// The check's signal names the task Agent's computer and session, says it is the hub's check, and never reads as the
// Agent's own report (#11587: "the Agent reports it is blocked … on <the supervising conversation's machine>").
func TestScheduledTodoCheckSignalNamesTheAgent(t *testing.T) {
	s, f, todo, task, _ := checkFixture(t, i18n.En)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "worker", State: proto.StateWorking,
		LastText: "Reading the limit rules"})
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	got := pendingFor(s, todo.ID)
	if len(got) != 1 {
		t.Fatalf("pending signals: %+v", got)
	}
	g := got[0]
	if g.Kind != sigCheck || g.Report != checkNoProgressReport || g.Machine != "box" || g.Session != "worker" {
		t.Fatalf("the check's signal is not about the task's Agent: %+v", g)
	}
	if g.urgent() || g.waitsOnDecision() || supervisionBatchImmediate(g) || g.id() != todo.ID+"|check:no_progress" {
		t.Fatalf("a no-progress check jumped the supervision gap or shared a report's slot: %+v", g)
	}
	e, ok := s.digestEntry(t.Context(), g, "")
	if !ok || e.Machine != "box" || e.Session != short("worker") || e.Report != checkNoProgressReport {
		t.Fatalf("the supervision entry lost the Agent: %+v", e)
	}
	text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}})
	if !strings.Contains(text, "signal=check") || !strings.Contains(text, "挂在它上面的定时检查没看到进展") || // i18n-ignore: the supervision message is Chinese
		!strings.Contains(text, "不是 Agent 的汇报") || strings.Contains(text, "Agent 主动向你汇报") || // i18n-ignore
		!strings.Contains(text, "；在 box session_id="+short("worker")) || strings.Contains(text, "在 server") { // i18n-ignore
		t.Fatalf("the supervision message misreads the check:\n%s", text)
	}
	worker := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{e})
	if !strings.Contains(worker, "a scheduled check linked to it saw no progress") || strings.Contains(worker, "the worker reports") {
		t.Fatalf("a task leader misreads the check:\n%s", worker)
	}
}

// agentAtWork lists todo's Agent as working, as the incident's was: an answer relayed to it would be delivered.
func agentAtWork(f *fakeHub, todo Todo) {
	c := idleAgent(time.Now())
	c.MachineID, c.SessionID = todo.Agents[0].MachineID, todo.Agents[0].SessionID
	c.Status, c.State = proto.StatusWorking, proto.StateWorking
	f.setSessions(c.MachineID, c)
}

// A decision the check raised is about the supervisor's approach: the owner's option, recommended or not, goes to the
// task's supervising conversation with what it leads to, and the Agent at work hears nothing from the hub (#11587:
// "Change approach" alone reached the Agent, which wrapped up early).
func TestScheduledTodoCheckDecisionGoesToSupervisor(t *testing.T) {
	for _, pick := range []string{"recommended", "other"} {
		t.Run(pick, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			s.todoAskAfter = time.Nanosecond
			s.superviseTimes.gather = time.Hour
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			assessTodoRun(t, s, task.ID, nil)
			assessTodoRun(t, s, task.ID, nil)
			a := askOf(s, todo.ID)
			if a == nil || !decideAsk(a) || a.Check != task.ID || len(a.Options) != 2 {
				t.Fatalf("the check's decision is not marked as the check's: %+v", a)
			}
			agentAtWork(f, todo)
			lang := s.botLang(ctx)
			label, leadsTo := a.Options[0].Label, a.Options[0].LeadsTo
			if pick == "other" {
				label, leadsTo = a.Options[1].Label, a.Options[1].LeadsTo
			}
			sent := len(sentToAgent(f))
			var result acceptResult
			var err error
			if pick == "recommended" {
				result, err = s.acceptTry(ctx, todo.ID, "")
			} else {
				result, err = s.chooseOption(ctx, todo.ID, "", label)
			}
			if err != nil || !result.taken || result.undelivered != nil {
				t.Fatalf("the owner's answer was not taken: %+v %v", result, err)
			}
			if got := sentToAgent(f)[sent:]; len(got) != 0 {
				t.Fatalf("the owner's answer to the check went to the Agent at work: %q", got)
			}
			if st := todoNow(s, todo.ID).Supervision.State; st == reviewNeedsUser {
				t.Fatal("the to-do still waits on the owner")
			}
			s.supervise.mu.Lock()
			g, ok := s.supervise.pending[todo.ID+"|"+sigCheck+":"+checkDecidedReport]
			s.supervise.mu.Unlock()
			if !ok || g.Detail != label || g.Decision == nil || len(g.Decision.Options) != 2 || !g.urgent() || !supervisionBatchImmediate(g) ||
				g.Machine != todo.Agents[0].MachineID || g.Session != todo.Agents[0].SessionID {
				t.Fatalf("the supervisor was not handed the owner's option: %+v", g)
			}
			// Where the task's other supervision messages go: its supervisor, now that it no longer waits on the owner.
			cfg := s.settings(ctx)
			if to, want := s.digestTo(cfg, g), s.digestTo(cfg, superviseSignal{Todo: todo.ID, Kind: sigIdle}); to == "" || to != want {
				t.Fatalf("the owner's option went to %q, not the task's supervisor %q", to, want)
			}
			e, ok := s.digestEntry(ctx, g, "")
			text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}})
			if !ok || !strings.Contains(text, "选了「"+label+"」") || !strings.Contains(text, "没有转给 Agent") || // i18n-ignore: the supervision message is Chinese
				!strings.Contains(text, leadsTo) {
				t.Fatalf("the supervision message lacks the option or what it leads to:\n%s", text)
			}
			note := acceptedNote(lang, askOf(s, todo.ID))
			want := i18n.T(lang, "assistant-reports.verdict_taken_supervisor")
			if pick == "other" {
				want = i18n.T(lang, "assistant-reports.decision_chosen_supervisor", i18n.Args{"option": label})
			}
			if note != want {
				t.Fatalf("the card says %q, want %q", note, want)
			}
		})
	}
}

// The decision row the owner's answer leaves on the to-do, with a database.
func TestScheduledTodoCheckDecisionIsRecorded(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	s.todoAskAfter = time.Nanosecond
	s.superviseTimes.gather = time.Hour
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	assessTodoRun(t, s, task.ID, nil)
	a := askOf(s, todo.ID)
	agentAtWork(f, todo)
	sent := len(sentToAgent(f))
	if result, err := s.acceptTry(context.Background(), todo.ID, ""); err != nil || !result.taken || result.undelivered != nil {
		t.Fatalf("accept: %+v %v", result, err)
	}
	decisions, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Choice != a.Recommend || decisions[0].Question != a.Reason ||
		len(sentToAgent(f)) != sent {
		t.Fatalf("decisions %+v, %d sends to the Agent", decisions, len(sentToAgent(f))-sent)
	}
}

// A worker's decision goes to the conversation above its task leader, which reviewed it, not to the leader Agent.
func TestScheduledTodoWorkerCheckDecisionGoesAboveTheLeader(t *testing.T) {
	s, _, worker := scheduledTodoFixture(t)
	ctx := t.Context()
	leader := Todo{ID: "task-leader", Origin: "conv1",
		Agents:      []TodoAgent{{MachineID: "box", SessionID: "leader", Source: sourceLeader}},
		Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, leader)
	worker.Origin = leaderRef("box", "leader")
	worker.Supervision.Supervisor = worker.Origin
	putTodo(s, worker)
	task := todoProgressTask(worker.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	assessTodoRun(t, s, task.ID, nil)
	a := askOf(s, worker.ID)
	if a == nil || a.Check != task.ID {
		t.Fatalf("the worker's check decision: %+v", a)
	}
	if result, err := s.chooseOption(ctx, worker.ID, "", a.Options[1].Label); err != nil || !result.taken {
		t.Fatalf("choose: %+v %v", result, err)
	}
	s.supervise.mu.Lock()
	g, ok := s.supervise.pending[worker.ID+"|"+sigCheck+":"+checkDecidedReport]
	s.supervise.mu.Unlock()
	if !ok || g.To != "conv1" {
		t.Fatalf("the worker's decision did not go to the conversation above its leader: %+v", g)
	}
	if _, handled, _ := s.leaderRoute(g); handled {
		t.Fatal("the leader Agent took the owner's answer to a decision it did not raise")
	}
}
