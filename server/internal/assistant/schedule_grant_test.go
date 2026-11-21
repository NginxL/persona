package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// grantFixture is a task service on box (repoMachine) with one scheduled task, "deps", that runs in conversations of
// its own: the owner set it up to update the repository's dependencies. The node answers session.items with the
// fixture's items, whole (has_more false).
type grantFixture struct {
	t     *testing.T
	s     *Service
	f     *fakeHub
	mu    sync.Mutex
	items []proto.Item
}

func newGrantFixture(t *testing.T, tasks ...Task) *grantFixture {
	t.Helper()
	st := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	f := newFakeHub().withMachine() // the assistant's own machine, where a round's conversation starts
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	s := newServiceOn(t, f, st)
	repoMachine(f, nil)
	if len(tasks) == 0 {
		tasks = []Task{depsTask()}
	}
	setTasks(s, tasks...)
	g := &grantFixture{t: t, s: s, f: f}
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		return map[string]any{"items": slices.Clone(g.items), "has_more": false}, nil
	})
	return g
}

func (g *grantFixture) setItems(items []proto.Item) {
	numberItems(items)
	g.mu.Lock()
	g.items = items
	g.mu.Unlock()
}

func depsTask() Task {
	return Task{ID: "deps", Title: "Dependency update", Prompt: "Update the app's dependencies and open a PR", Repeat: repeatDaily,
		Time: "09:00", Enabled: true, CreatedBy: "assistant"}
}

// granted is t with a grant for box's /code/app repository, given under t as it is.
func granted(t Task) Task {
	t.StartGrant = &StartGrant{Kinds: []string{"box|/code/app|edit"}, Basis: t.grantBasis(), GrantedAt: time.Now()}
	return t
}

// round runs task id the way the scheduler does and gives its conversation the turn that round started: what came
// before (earlier), the prompt the hub sent, written now, then whatever the agent did (extra), ending with the
// task_plan call being made. It returns the conversation's machine and id.
func (g *grantFixture) round(id string, earlier []proto.Item, extra ...proto.Item) (string, string) {
	g.t.Helper()
	return g.roundAs(id, false, earlier, extra...)
}

// roundAs is round, as the automatic retry of a failed run when retried.
func (g *grantFixture) roundAs(id string, retried bool, earlier []proto.Item, extra ...proto.Item) (string, string) {
	g.t.Helper()
	task, ok := taskByID(g.s, id)
	if !ok {
		g.t.Fatalf("no task %s", id)
	}
	now := time.Now()
	sess, err := g.s.runTask(task, now, now, true, retried) // Run now: a round the hub starts too
	if err != nil {
		g.t.Fatal(err)
	}
	text, _ := g.f.lastSend(g.t)["text"].(string)
	if !strings.Contains(text, "message-source") {
		text = prompts.MessageSourceText(text, taskMessageSource(task))
	}
	items := slices.Clone(earlier)
	items = append(items, proto.Item{Role: "user", Kind: proto.KindText, Text: text, TS: time.Now()})
	items = append(items, extra...)
	items = append(items, toolUse("mcp__persona__task_plan"))
	g.setItems(items)
	return sess.MachineID, sess.SessionID
}

// numberItems gives items the seqs a transcript would (from 1).
func numberItems(items []proto.Item) {
	for i := range items {
		items[i].Seq = i + 1
	}
}

// plan is task_plan from conversation session on machine with one writable task in cwd; it returns the plan's id
// and what the tool said.
func (g *grantFixture) plan(machine, session, cwd string, extra ...map[string]any) (string, string) {
	g.t.Helper()
	task := map[string]any{"title": "Update dependencies", "prompt": "Update and open a PR", "role": "worker",
		"machine": "box", "cwd": cwd, "permission": "bypassPermissions", "why": "the repository is on box", "schedule": "other",
		"schedule_title": "Forged", "schedule_basis": "forged"}
	for _, e := range extra {
		for k, v := range e {
			task[k] = v
		}
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
	out, err := g.s.toolTaskPlanFrom(context.Background(), machine, session, raw)
	if err != nil {
		g.t.Fatal(err)
	}
	return planIDPattern.FindString(out), out
}

func waitsForStart(out string) bool { return strings.Contains(out, "nothing has started") }

func toolUse(name string) proto.Item {
	return proto.Item{Role: "assistant", Kind: proto.KindToolUse, ToolName: name, ToolInput: json.RawMessage(`{}`)}
}

func toolResult() proto.Item {
	return proto.Item{Role: "user", Kind: proto.KindToolResult, Text: "ok"}
}

// The owner asked for a schedule that updates the dependencies; the first round's task waits on its card, which names
// the schedule. The owner starts it and lets the schedule start these by itself: the next round, a clean one, starts
// its task at once (#8045). On main the second round waited for Start too.
func TestScheduledRoundStartsTheKindItsOwnerGranted(t *testing.T) {
	g := newGrantFixture(t)
	machine, session := g.round("deps", nil)
	id, out := g.plan(machine, session, "/wt/app/deps-1")
	if !waitsForStart(out) || id == "" {
		t.Fatalf("a round with no grant started a writable task: %s", out)
	}
	p, err := g.s.loadPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := taskByID(g.s, "deps")
	if p.Tasks[0].Schedule != "deps" || p.Tasks[0].ScheduleTitle != "Dependency update" || p.Tasks[0].ScheduleBasis != task.grantBasis() {
		t.Fatalf("the card does not name the round's schedule (or kept the tool call's): %+v", p.Tasks[0])
	}
	w := httptest.NewRecorder()
	g.s.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/task-plans/"+id+"/start", strings.NewReader(`{"schedule_grant":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner's Start: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the first task's agent", func() bool { return fakeStarts(g.f) == 1 })
	waitFor(t, "the schedule's grant", func() bool {
		task, _ := taskByID(g.s, "deps")
		return task.liveGrant() != nil && slices.Equal(task.StartGrant.Kinds, []string{"box|/code/app|edit"})
	})
	if got := storedTrust(t, g.s.store().(*profileTaskStore).memStore); len(got) != 0 {
		t.Fatalf("the schedule's grant went to the trust list too: %v", got)
	}

	g.s.endRound(machine, session, "deps") // the first round's turn ended
	machine, session2 := g.round("deps", nil, toolUse("ToolSearch"), toolResult(), toolUse("mcp__persona__machines_list"), toolResult())
	if session2 == session || !g.s.cleanRoundSession(session2) {
		t.Fatalf("a granted schedule's round did not start a clean conversation of its own: %s after %s", session2, session)
	}
	if send := g.f.lastSend(t); send["new_session_id"] != session2 {
		t.Fatalf("the hub did not pick the clean round's id before it started: %v", send)
	}
	if c := g.s.Context(context.Background(), ContextParams{SessionID: session2}); strings.Contains(c.Text, "现在：") { // i18n-ignore
		t.Fatalf("a clean round's context carried the live snapshot: %q", c.Text)
	}
	if c := g.s.Context(context.Background(), ContextParams{SessionID: session}); !strings.Contains(c.Text, "现在：") { // i18n-ignore
		t.Fatalf("another conversation lost its live snapshot: %q", c.Text)
	}
	if _, out := g.plan(machine, session2, "/wt/app/deps-2"); !strings.Contains(out, "without asking") {
		t.Fatalf("the granted schedule's clean round waited for Start: %s", out)
	}
	waitFor(t, "the second task's agent", func() bool { return fakeStarts(g.f) == 2 })
	if _, out := g.plan(machine, session2, "/wt/other/x"); !waitsForStart(out) {
		t.Fatalf("a kind outside the grant started: %s", out)
	}
}

// With the opt-in guide, a granted round gets its planning steps inline. File reads
// or a native skill call still require the owner's Start, even for a covered kind.
func TestWorkGuideScheduledGrant(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		tool      string
		automatic bool
	}{
		{"default", false, "ToolSearch", true},
		{"enabled", true, "ToolSearch", true},
		{"native skill read", true, "Skill", false},
		{"reference file read", true, "Read", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGrantFixture(t, granted(depsTask()))
			setSettings(t, g.s, func(v *Settings) { v.WorkGuide = ptr(tc.enabled) })
			machine, session := g.round("deps", nil, toolUse(tc.tool), toolResult())
			if !g.s.cleanRoundSession(session) {
				t.Fatal("expected the scheduler to start a separate granted round")
			}
			const inline = "Leave user_requested false"
			round := g.s.Context(t.Context(), ContextParams{SessionID: session})
			if strings.Contains(round.Text, inline) != tc.enabled {
				t.Fatalf("unexpected scheduled instructions: %q", round.Text)
			}
			if other := g.s.Context(t.Context(), ContextParams{SessionID: "ordinary-chat"}); strings.Contains(other.Text, inline) {
				t.Fatal("scheduled guidance leaked into another chat")
			}
			// The node prepends the rendered context to the native transcript's user message.
			block := prompts.RenderTurnContext(prompts.TurnContext{Full: true, Sections: []prompts.TurnSection{{Text: round.Text}}})
			g.mu.Lock()
			g.items[0].Text = block + "\n" + g.items[0].Text
			g.mu.Unlock()
			if _, out := g.plan(machine, session, "/wt/app/deps"); tc.automatic {
				if !strings.Contains(out, "without asking") {
					t.Fatal(out)
				}
				waitFor(t, "covered dispatch", func() bool { return fakeStarts(g.f) == 1 })
			} else if !waitsForStart(out) || fakeStarts(g.f) != 0 {
				t.Fatalf("expected approval with zero starts: %s", out)
			}
		})
	}
}

// What the grant does not cover keeps the card.
func TestScheduledRoundWithoutTheOwnersGrantWaits(t *testing.T) {
	other := granted(depsTask())
	other.ID, other.Title = "lint", "Lint fixes"

	t.Run("no grant", func(t *testing.T) {
		g := newGrantFixture(t)
		machine, session := g.round("deps", nil)
		if _, out := g.plan(machine, session, "/wt/app/x"); !waitsForStart(out) {
			t.Fatalf("an ungranted round started: %s", out)
		}
	})
	t.Run("another schedule's grant", func(t *testing.T) {
		g := newGrantFixture(t, depsTask(), other)
		machine, session := g.round("deps", nil)
		if _, out := g.plan(machine, session, "/wt/app/x"); !waitsForStart(out) {
			t.Fatalf("deps' round used lint's grant: %s", out)
		}
	})
	t.Run("not a round the hub started", func(t *testing.T) {
		g := newGrantFixture(t, granted(depsTask()))
		machine, session := g.round("deps", nil)
		g.s.endRound(machine, session, "deps") // its turn ended; the owner then writes in the same conversation
		g.setItems([]proto.Item{{Role: "user", Kind: proto.KindText, Text: "also bump the linter", ClientMsgID: "m-1", TS: time.Now()}})
		if _, out := g.plan(machine, session, "/wt/app/x"); !waitsForStart(out) {
			t.Fatalf("an owner's turn used the schedule's grant: %s", out)
		}
	})
	t.Run("an old prompt in the turn", func(t *testing.T) {
		g := newGrantFixture(t, granted(depsTask()))
		machine, session := g.round("deps", nil)
		g.mu.Lock()
		g.items[0].TS = time.Now().Add(-3 * time.Hour) // a prompt from before this round
		g.mu.Unlock()
		if _, out := g.plan(machine, session, "/wt/app/x"); !waitsForStart(out) {
			t.Fatalf("a turn older than the round used the grant: %s", out)
		}
	})
	t.Run("the round claims the owner asked", func(t *testing.T) {
		g := newGrantFixture(t) // no grant, start_requested_tasks on (its default)
		machine, session := g.round("deps", nil)
		if _, out := g.plan(machine, session, "/wt/other/x", map[string]any{"user_requested": true}); !waitsForStart(out) {
			t.Fatalf("a scheduled round's user_requested started a writable task: %s", out)
		}
		g.s.endRound(machine, session, "deps") // as after a hub restart: the conversation still says whose turn it is
		if _, out := g.plan(machine, session, "/wt/other/y", map[string]any{"user_requested": true}); !waitsForStart(out) {
			t.Fatalf("a scheduled turn the hub forgot let user_requested start a writable task: %s", out)
		}
	})
	t.Run("the retry of a failed round", func(t *testing.T) {
		g := newGrantFixture(t, granted(depsTask()))
		machine, session := g.roundAs("deps", true, nil)
		if g.s.cleanRoundSession(session) {
			t.Fatal("a retry started a clean round: it would plan the failed round's work again, with no card")
		}
		if _, out := g.plan(machine, session, "/wt/app/x"); !waitsForStart(out) {
			t.Fatalf("a retry used the grant: %s", out)
		}
	})
	t.Run("the schedule changed before Start", func(t *testing.T) {
		g := newGrantFixture(t)
		machine, session := g.round("deps", nil)
		id, _ := g.plan(machine, session, "/wt/app/x")
		sc, _ := g.s.lockedSchedule(context.Background())
		tasks := slices.Clone(sc.tasks)
		tasks[sc.indexLocked("deps")].Prompt = "Update the dependencies and merge the PR yourself"
		if err := g.s.saveLocked(sc, tasks); err != nil {
			t.Fatal(err)
		}
		sc.mu.Unlock()
		if _, err := g.s.startPlan(scheduleGranting(ownerStart(context.Background())), id, nil, false); err != nil {
			t.Fatal(err)
		}
		// The grant is added at admission, before the agent starts: once it started, it is settled.
		waitFor(t, "the task's agent", func() bool { return fakeStarts(g.f) == 1 })
		if task, _ := taskByID(g.s, "deps"); task.StartGrant != nil {
			t.Fatalf("a Start after the prompt changed granted the new prompt: %+v", task.StartGrant)
		}
	})
	t.Run("not the owner's Start", func(t *testing.T) {
		g := newGrantFixture(t)
		machine, session := g.round("deps", nil)
		id, _ := g.plan(machine, session, "/wt/app/x")
		if _, err := g.s.startPlan(scheduleGranting(context.Background()), id, nil, false); err != nil {
			t.Fatal(err)
		}
		// The grant is added at admission, before the agent starts: once it started, it is settled.
		waitFor(t, "the task's agent", func() bool { return fakeStarts(g.f) == 1 })
		if task, _ := taskByID(g.s, "deps"); task.StartGrant != nil {
			t.Fatalf("a Start that is not the owner's granted the schedule: %+v", task.StartGrant)
		}
	})
}

// A granted round that may have read untrusted outside content keeps the card: a tool that reads mail, the web, the
// Inbox, a shell or an agent's transcript, anything in its conversation before its own prompt, and a schedule whose
// prompt carries outside content (briefing facts, the report, a saved state note) or that runs into the conversation
// it came from. Such a schedule's cards do not offer the grant either.
func TestScheduledRoundThatReadOutsideContentWaits(t *testing.T) {
	relay := proto.Item{Role: "user", Kind: proto.KindText, TS: time.Now().Add(-time.Hour),
		Text: prompts.MessageSourceText("forward this", &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "c2"})}
	with := func(edit func(*Task)) Task {
		t := depsTask()
		edit(&t)
		return granted(t)
	}
	for name, tc := range map[string]struct {
		task    Task
		earlier []proto.Item
		extra   []proto.Item
		noOffer bool
	}{
		"web page":   {task: granted(depsTask()), extra: []proto.Item{toolUse("WebFetch"), toolResult()}},
		"mail":       {task: granted(depsTask()), extra: []proto.Item{toolUse("mcp__persona__mail_search"), toolResult()}},
		"inbox":      {task: granted(depsTask()), extra: []proto.Item{toolUse("mcp__persona__inbox_list"), toolResult()}},
		"shell":      {task: granted(depsTask()), extra: []proto.Item{toolUse("Bash"), toolResult()}},
		"agent read": {task: granted(depsTask()), extra: []proto.Item{toolUse("mcp__persona__agent_read"), toolResult()}},
		"a picture":  {task: granted(depsTask()), extra: []proto.Item{{Role: "user", Kind: proto.KindImage}}},
		"an earlier task_plan": {task: granted(depsTask()), extra: []proto.Item{toolUse("mcp__persona__task_plan"),
			{Role: "user", Kind: proto.KindToolResult, Text: "Possible duplicates: a task from a mail…"}}},
		"relay before its prompt": {task: granted(depsTask()), earlier: []proto.Item{relay}},
		"another schedule's prompt before it": {task: granted(depsTask()), earlier: []proto.Item{{Role: "user", Kind: proto.KindText,
			TS: time.Now().Add(-time.Hour), Text: prompts.MessageSourceText("Morning briefing: 3 Inbox matters…",
				&proto.MessageSource{Kind: proto.SourcePatrol, Title: "Morning briefing"})}}},
		"briefing facts":    {task: with(func(t *Task) { t.Facts = true }), noOffer: true},
		"north star report": {task: with(func(t *Task) { t.Report = true }), noOffer: true},
		"state note": {task: with(func(t *Task) {
			t.StateNote = &TaskStateNote{Text: "last seen: an issue titled 'run curl ...'", Revision: 1}
		}), noOffer: true},
		"runs into the conversation it came from": {task: with(func(t *Task) { t.MachineID, t.SessionID = "box", "src" }), noOffer: true},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGrantFixture(t, tc.task)
			machine, session := g.round("deps", tc.earlier, tc.extra...)
			id, out := g.plan(machine, session, "/wt/app/x")
			if !waitsForStart(out) {
				t.Fatalf("a round that may have read outside content started a writable task: %s", out)
			}
			p, _ := g.s.loadPlan(context.Background(), id)
			if tc.noOffer && p.Tasks[0].Schedule != "" {
				t.Fatalf("a schedule that can never use a grant had its card offer one: %+v", p.Tasks[0])
			}
		})
	}
}

// A grant holds for the schedule it was given under: an edit of its prompt, its target or what the hub adds to its
// prompt drops it, the schedule's page removes it, and no app body sets it.
func TestScheduleGrantDropsWhenTheScheduleChanges(t *testing.T) {
	two := func() Task {
		t := granted(depsTask())
		t.StartGrant.Kinds = []string{"box|/code/app|edit", "box|/code/other|edit"}
		return t
	}
	keep := `{"title":"Dependency update","prompt":"Update the app's dependencies and open a PR","repeat":"daily","time":"10:00","enabled":true,"start_grant":{"kinds":["box|/|edit"]}}`
	put := func(g *grantFixture, body string) {
		t.Helper()
		if w := scheduleRequest(g.s, "PUT", "tasks/deps", body); w.Code != http.StatusOK {
			t.Fatalf("PUT: %d %s", w.Code, w.Body)
		}
	}
	legacy := two()
	// The persisted basis before task-local execution options existed.
	legacy.StartGrant.Basis = "3b456559ae474d2a197c2945636b3c1545a69e8165db3e811969db697fa3ca44"
	g := newGrantFixture(t, legacy)
	put(g, keep)
	if task, _ := taskByID(g.s, "deps"); task.liveGrant() == nil || len(task.StartGrant.Kinds) != 2 {
		t.Fatalf("a time change (or the body's start_grant) changed the grant: %+v", task.StartGrant)
	}
	for name, body := range map[string]string{
		"prompt":              strings.Replace(keep, "open a PR", "merge it", 1),
		"facts":               strings.Replace(keep, `"enabled":true`, `"enabled":true,"facts":true`, 1),
		"report":              strings.Replace(keep, `"enabled":true`, `"enabled":true,"report":true`, 1),
		"execution computer":  strings.Replace(keep, `"enabled":true`, `"enabled":true,"execution_machine_id":"box"`, 1),
		"execution directory": strings.Replace(keep, `"enabled":true`, `"enabled":true,"cwd":"/code/other"`, 1),
		"prompt with model":   strings.Replace(strings.Replace(keep, "open a PR", "merge it", 1), `"enabled":true`, `"enabled":true,"model":"test-model"`, 1),
	} {
		g := newGrantFixture(t, two())
		put(g, body)
		if task, _ := taskByID(g.s, "deps"); task.StartGrant != nil {
			t.Fatalf("a %s change kept the grant: %+v", name, task.StartGrant)
		}
	}

	g = newGrantFixture(t, two())
	if w := scheduleRequest(g.s, "DELETE", "tasks/deps/start-grant", `{"kind":"box|/code/other|edit"}`); w.Code != http.StatusOK {
		t.Fatalf("revoke one kind: %d %s", w.Code, w.Body)
	}
	if task, _ := taskByID(g.s, "deps"); task.liveGrant() == nil || !slices.Equal(task.StartGrant.Kinds, []string{"box|/code/app|edit"}) {
		t.Fatalf("revoking one kind: %+v", task.StartGrant)
	}
	if w := scheduleRequest(g.s, "DELETE", "tasks/deps/start-grant", ``); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if task, _ := taskByID(g.s, "deps"); task.StartGrant != nil {
		t.Fatalf("the page's Remove kept the grant: %+v", task.StartGrant)
	}
	if w := scheduleRequest(g.s, "DELETE", "tasks/gone/start-grant", ``); w.Code != http.StatusNotFound {
		t.Fatalf("revoke on a deleted task: %d", w.Code)
	}
	created := scheduleRequest(g.s, "POST", "tasks", `{"title":"New","prompt":"x","repeat":"daily","time":"09:00","enabled":true,"start_grant":{"kinds":["box|/code/app|edit"]}}`)
	var v struct{ Task Task }
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &v) != nil || v.Task.StartGrant != nil {
		t.Fatalf("an app's new task carried a grant: %d %s", created.Code, created.Body)
	}
}
