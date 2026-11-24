package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// relayedPlan is task_plan's arguments for one task on box the model marks user_requested: writable unless over says
// otherwise.
func relayedPlan(over map[string]any) string {
	task := map[string]any{"title": "Fix the export", "prompt": "The export drops the old format: fix it and add a test",
		"role": "worker", "machine": "box", "cwd": "/code/app", "readonly": false, "permission": "bypassPermissions", "user_requested": true,
		"why": "the repository is on box", "done_when": []string{"the export keeps the old format"}, "expect_minutes": 30}
	for k, v := range over {
		task[k] = v
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
	return string(raw)
}

// newStartRuleHarness is the dispatch harness on st, a database that keeps and starts task plans (nil: a new one),
// with the computer box for tasks.
func newStartRuleHarness(t *testing.T, st Store) *dispatchPolicyHarness {
	t.Helper()
	if st == nil {
		st = planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	}
	d := newDispatchPolicyHarnessOn(t, st)
	d.f.setSessions("box")
	return d
}

// startsSoFar counts the agents started so far.
func (d *dispatchPolicyHarness) startsSoFar() int {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	return len(d.f.starts)
}

// handOver hands the owner's quoted words from the main conversation to the lead, which takes them in at once; the
// lead's transcript then ends with them.
func (d *dispatchPolicyHarness) handOver(t *testing.T) {
	t.Helper()
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"please fix the export","quote":"latest"`)
	d.f.setItems(received(t, d.f, d.mainID))
}

// startedPlanOf keeps a started plan of conversation conv with one writable task in cwd on box: a kind the owner
// let that conversation start.
func (d *dispatchPolicyHarness) startedPlanOf(t *testing.T, conv, cwd string) {
	t.Helper()
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: conv, State: planStarted, CreatedAt: now, UpdatedAt: now,
		Tasks: []PlanTask{{Title: "Earlier work", Prompt: "earlier", Machine: "box", Cwd: cwd}}}
	if err := d.s.savePlan(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

// taskReport is the item a task's report leaves in a conversation: Persona's own input, not the owner's.
func taskReport() proto.Item {
	return proto.Item{ID: "n1", Seq: 3, Role: "user", Kind: proto.KindText, Text: "🔔 task done: the export audit",
		Source: &proto.MessageSource{Kind: proto.SourceSupervision}}
}

// planIn plans relayedPlan(over) in conversation id and says whether it started at once (the agent started) or waits
// for Start (none did).
func (d *dispatchPolicyHarness) planIn(t *testing.T, id string, over map[string]any) bool {
	t.Helper()
	before := d.startsSoFar()
	res := d.tool(t, id, "task_plan", relayedPlan(over))
	out := res.Content[0].Text
	if res.IsError {
		t.Fatalf("task_plan: %s", out)
	}
	if waitsForStart(out) {
		waitFor(t, "the start reservation to settle", func() bool { return d.s.taskStarting.Load() == 0 })
		if n := d.startsSoFar() - before; n != 0 {
			t.Fatalf("%d agents started on a plan that waits", n)
		}
		return false
	}
	waitFor(t, "the agent to start", func() bool { return d.startsSoFar() == before+1 })
	return true
}

// A direction's lead that a hand-over of the owner's quoted words went into (#11699) used to wait for the owner's Start
// before writable work it planned outside its own kinds. Since decision rights a lead's own plan is its release and
// starts at once in every turn, a place new to it included; so does everything that is no such hand-over.
func TestALeadAHandoverWentIntoStartsItsPlans(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string // the planning conversation: lead, side or main
		before func(d *dispatchPolicyHarness, t *testing.T)
		over   map[string]any
		trust  bool
		starts bool
	}{
		// The owner's verified words, delivered into this lead, start its writable task as the main conversation's (#12953).
		{"the turn the hand-over opened", "lead", (*dispatchPolicyHarness).handOver, nil, false, true},
		{"a later turn a task's report opened", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.handOver(t)
			// What goes on in the lead meanwhile: a turn runs and ends, an unquoted message and a task's report go in.
			d.setStatus(d.leaderID, proto.StatusWorking)
			d.setStatus("", "")
			d.s.drainRelays("server", []proto.Session{d.idle(d.leaderID)})
			d.send(t, d.mainID, d.leaderID, `"text":"how far is it?"`)
			sent := d.f.sentTo(d.leaderID)
			d.s.relayOrQueue(context.Background(), d.leaderID, relayMsg{From: "todo_audit", FromTitle: "Task", Tool: noticeTool,
				State: "done", Text: "🔔 task done: the export audit", At: time.Now().UTC()})
			waitFor(t, "the task's report to go in", func() bool { return d.f.sentTo(d.leaderID) == sent+1 })
			d.f.setItems(taskReport())
		}, nil, false, true},
		{"a later turn, not asked for in so many words", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.handOver(t)
			d.f.setItems(taskReport())
		}, map[string]any{"user_requested": false}, false, true},
		{"after the owner writes there", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.handOver(t)
			d.s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: d.leaderID})
			d.f.setItems(ownerItem("m-lead", "yes, fix the export there"))
		}, nil, false, true},
		{"read-only, after a hand-over", "lead", func(d *dispatchPolicyHarness, t *testing.T) { d.handOver(t); d.f.setItems(taskReport()) },
			map[string]any{"readonly": true, "permission": "plan", "goal": "find why the export drops it"}, false, true},
		{"a trusted kind, after a hand-over", "lead", func(d *dispatchPolicyHarness, t *testing.T) { d.handOver(t); d.f.setItems(taskReport()) }, nil, true, true},
		// The lead's own ground (#12953): a kind a started plan of this lead ran starts; a place new to it still waits.
		// In a later turn (no verified words): a kind a started plan of this lead ran starts; a place new to it waits.
		{"a kind the lead already started, in a later turn", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.startedPlanOf(t, d.leaderID, "/code/app")
			d.handOver(t)
			d.f.setItems(taskReport())
		}, nil, false, true},
		{"a place new to the lead, in a later turn", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.startedPlanOf(t, d.leaderID, "/code/other")
			d.startedPlanOf(t, d.mainID, "/code/app") // another conversation's ground is not the lead's
			d.handOver(t)
			d.f.setItems(taskReport())
		}, nil, false, true},
		{"an unquoted message to a lead", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.send(t, d.mainID, d.leaderID, `"text":"please fix the export"`)
			d.f.setItems(received(t, d.f, d.mainID))
		}, nil, false, true},
		{"a hand-over still queued", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.setStatus(d.leaderID, proto.StatusWorking)
			d.f.setItems(ownerItem("m-owner", quotedWords))
			d.send(t, d.mainID, d.leaderID, `"text":"please fix the export","quote":"latest"`)
		}, nil, false, true},
		{"a hand-over past its 14 days going in", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.setStatus(d.leaderID, proto.StatusWorking)
			d.f.setItems(ownerItem("m-owner", quotedWords))
			d.send(t, d.mainID, d.leaderID, `"text":"please fix the export","quote":"latest"`)
			h := &d.s.handovers
			h.mu.Lock()
			for id, e := range h.m {
				e.At = e.At.Add(-handoverMemory - time.Hour)
				h.m[id] = e
			}
			h.mu.Unlock()
			d.setStatus("", "")
			drainRelayTest(t, d.s, d.idle(d.leaderID))
			d.f.setItems(received(t, d.f, d.mainID))
		}, nil, false, true},
		{"quoted words to a conversation that is no lead", "side", func(d *dispatchPolicyHarness, t *testing.T) {
			d.f.setItems(ownerItem("m-owner", quotedWords))
			d.send(t, d.mainID, d.sideID, `"text":"please fix the export","quote":"latest"`)
			d.f.setItems(received(t, d.f, d.mainID))
		}, nil, false, true},
		{"the main conversation, after the lead handed it the words", "main", func(d *dispatchPolicyHarness, t *testing.T) {
			d.handOver(t)
			d.send(t, d.leaderID, d.mainID, `"text":"done; and the import needs the same","quote":"latest"`)
			d.f.setItems(received(t, d.f, d.leaderID))
		}, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newStartRuleHarness(t, nil)
			stopHolds(t, d.s)
			if tc.trust {
				if err := d.s.store().PutSetting(context.Background(), trustSetting, []string{trustKey(PlanTask{Machine: "box", Cwd: "/code/app"})}); err != nil {
					t.Fatal(err)
				}
			}
			tc.before(d, t)
			planner := map[string]string{"lead": d.leaderID, "side": d.sideID, "main": d.mainID}[tc.in]
			if got := d.planIn(t, planner, tc.over); got != tc.starts {
				t.Fatalf("started %v, want %v", got, tc.starts)
			}
		})
	}
}

// A direct send of a hand-over into a lead that ends in doubt has opened the lead's turn: the lead is marked, although
// nothing said yet that the message went in (a writable plan there starts all the same: decision rights).
func TestAHandoverInDoubtHoldsTheTurnItOpened(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	var sent string
	d.f.mu.Lock()
	d.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if p["session_id"] == d.leaderID {
			sent, _ = p["text"].(string)
		}
	}
	d.f.mu.Unlock()
	refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded) // written, then no answer in time
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"please fix the export","quote":"latest"`)
	if e := onlyHandover(t, d.s); e.State != handoverQueued {
		t.Fatalf("in doubt: %+v", e)
	}
	d.f.setItems(proto.Item{ID: "r1", Seq: 2, Role: "user", Kind: proto.KindText, Text: sent,
		Source: &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: d.mainID}})
	refuseSendsTo(d.f, d.leaderID, nil)
	if !d.s.handedOver(d.leaderID) {
		t.Fatal("a hand-over in doubt left no mark on the lead")
	}
	if !d.planIn(t, d.leaderID, nil) {
		t.Fatal("a writable plan in the lead waits for Start")
	}
}

// A try the lead definitely did not take leaves no mark when the lead had none. One that had a mark from an earlier
// hand-over keeps it.
func TestARefusedHandoverLeavesNoMark(t *testing.T) {
	for _, before := range []bool{false, true} {
		name := "no mark before"
		if before {
			name = "a mark before"
		}
		t.Run(name, func(t *testing.T) {
			d := newStartRuleHarness(t, nil)
			stopHolds(t, d.s)
			if before {
				d.handOver(t)
			}
			refuseSendsTo(d.f, d.leaderID, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update Persona on this computer"})
			d.f.setItems(ownerItem("m-owner-2", "and the import as well"))
			if res := d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.leaderID+`","text":"and the import","quote":"latest"}`); !res.IsError {
				t.Fatalf("the refused send went: %s", res.Content[0].Text)
			}
			refuseSendsTo(d.f, d.leaderID, nil)
			if got := d.s.handedOver(d.leaderID); got != before {
				t.Fatalf("marked %v after a refused try (%s)", got, name)
			}
		})
	}
}

// The mark is saved before the send leaves: a manager that restarts while the send is on its way finds it.
func TestTheLeadsMarkSurvivesARestartDuringTheSend(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	d := newStartRuleHarness(t, st)
	stopHolds(t, d.s)
	var during map[string][]byte // the database as a restart during the send would find it
	d.f.mu.Lock()
	d.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if p["session_id"] != d.leaderID {
			return
		}
		st.memStore.mu.Lock()
		during = map[string][]byte{}
		for k, v := range st.memStore.vals {
			during[k] = slices.Clone(v)
		}
		st.memStore.mu.Unlock()
	}
	d.f.mu.Unlock()
	refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded) // the manager stops before any answer comes
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"please fix the export","quote":"latest"`)
	if during == nil {
		t.Fatal("no send to the lead")
	}
	restarted := newStartRuleHarness(t, planUpdateStore{&planMemStore{memStore: &memStore{vals: during}, plans: map[string]db.TaskPlanRow{}}})
	stopHolds(t, restarted.s)
	if !restarted.s.handedOver(restarted.leaderID) {
		t.Fatal("after a restart during the send the lead's mark was gone")
	}
}

// The lead's mark is kept with the relay state: a manager restart finds it.
func TestALeadsMarkAfterAHandoverSurvivesARestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	d := newStartRuleHarness(t, st)
	stopHolds(t, d.s)
	d.handOver(t)
	restarted := newStartRuleHarness(t, st)
	stopHolds(t, restarted.s)
	if !restarted.s.handedOver(restarted.leaderID) {
		t.Fatal("after a restart the lead's mark was gone")
	}
}

// The acceptance case of #12953: a lead plans a writable task with user_requested on the owner's words the main
// conversation handed over, verified by the hub (bypassPermissions on the work computer). It starts at once, as the
// main conversation's would, with no Start card, and its brief keeps the owner's words.
func TestALeadStartsTheOwnersRelayedWordsAtOnce(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	d.handOver(t)
	if !d.planIn(t, d.leaderID, nil) {
		t.Fatal("the lead's plan on the owner's relayed words waits for Start")
	}
	var started *TaskPlan
	plans, err := d.s.conversationPlans(context.Background(), d.leaderID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.State == planStarted {
			started = p
		}
	}
	if started == nil || len(started.Tasks) != 1 {
		t.Fatalf("no started plan: %+v", started)
	}
	if b := started.Tasks[0].Brief; b == nil || b.Request == nil || b.Request.Text != quotedWords || b.Trigger != "conversation" ||
		!started.Tasks[0].UserRequested {
		t.Fatalf("the started task: %+v", started.Tasks[0])
	}
}

// A continuation keeps the original brief, but the request that authorizes this dispatch is the verified hand-over in
// the current conversation. The earlier plan uses another folder so its started kind cannot mask the request hold.
func TestAContinuationStartsTheCurrentRelayedWordsAtOnce(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-earlier", "Fix the earlier export"))
	if !d.planIn(t, d.leaderID, map[string]any{"cwd": "/code/earlier"}) {
		t.Fatal("the original plan on the owner's direct words waits for Start")
	}
	todo := onlyTodo(d.s)
	before, ok := d.s.briefOfTodo(todo.ID)
	if !ok || before.Request == nil || before.Request.MessageID != "m-earlier" {
		t.Fatalf("original brief = %+v", before)
	}

	d.handOver(t)
	if !d.planIn(t, d.leaderID, map[string]any{
		"continues": todo.ID, "continues_why": "Replace the earlier Agent", "cwd": "/code/app",
	}) {
		t.Fatal("a continuation on the current relayed words waits for Start")
	}
	plans, err := d.s.conversationPlans(context.Background(), d.leaderID)
	if err != nil {
		t.Fatal(err)
	}
	var continued *TaskPlan
	for _, p := range plans {
		if len(p.Tasks) == 1 && p.Tasks[0].Continues == todo.ID {
			continued = p
		}
	}
	if continued == nil || !continued.Tasks[0].UserRequested {
		t.Fatalf("continuation = %+v", continued)
	}
	after, ok := d.s.briefOfTodo(todo.ID)
	if !ok || after.ID != before.ID || after.Request == nil || after.Request.MessageID != before.Request.MessageID {
		t.Fatalf("continuation replaced the original brief: before %+v, after %+v", before, after)
	}
}

// The live case of decision rights (plan_8e309c4bf9d2): a lead marked by an earlier hand-over of the owner's words
// plans one writable task, not asked for in so many words, in a place new to it. It starts at once: no card, no push.
func TestAMarkedLeadsOwnWritablePlanStartsWithoutAPush(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	d.handOver(t)
	d.f.setItems(taskReport())
	if !d.s.handedOver(d.leaderID) {
		t.Fatal("the hand-over left no mark on the lead")
	}
	pushed := len(d.f.pushed())
	if !d.planIn(t, d.leaderID, map[string]any{"user_requested": false, "cwd": "/code/new-place"}) {
		t.Fatal("the marked lead's writable plan waits for Start")
	}
	if got := d.f.pushed(); len(got) != pushed {
		t.Fatalf("the lead's plan was pushed: %+v", got[pushed:])
	}
}

// markHandedOver puts the hand-over mark of #11699 on conversation id directly, as a hand-over sent into it would.
func (d *dispatchPolicyHarness) markHandedOver(t *testing.T, id string) {
	t.Helper()
	b := &d.s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !d.s.relayLocked(context.Background()) {
		t.Fatal("the relay state did not load")
	}
	if b.st.Handed == nil {
		b.st.Handed = map[string]time.Time{}
	}
	b.st.Handed[id] = time.Now().UTC()
}

// What still waits for the owner's Start with the hand-over mark on: an ordinary conversation's writable plan (it is not
// its supervisor's release), and a marked lead's task that spends money, speaks outward as the owner or takes an
// irreversible step they have not agreed to (owner_step).
func TestWhatStillWaitsWithTheHandOverMark(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	d.markHandedOver(t, d.sideID)
	d.f.setItems(taskReport())
	if d.planIn(t, d.sideID, map[string]any{"user_requested": false}) {
		t.Fatal("an ordinary conversation's writable plan started without the owner's Start")
	}
	for _, step := range []string{ownerStepMoney, ownerStepOutward, ownerStepIrreversible} {
		d := newStartRuleHarness(t, nil)
		stopHolds(t, d.s)
		d.handOver(t)
		d.f.setItems(taskReport())
		if d.planIn(t, d.leaderID, map[string]any{"user_requested": false, "owner_step": step}) {
			t.Fatalf("a marked lead's %s task started without the owner's Start", step)
		}
	}
}
