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

// A direction's lead that a hand-over of the owner's quoted words went into (#11699) waits for the owner's Start before
// writable work it marks user_requested, whatever opens the turn, until the owner writes there. Read-only tasks and
// kinds the owner trusts start as before; so does everything that is no such hand-over: an unquoted message to a lead,
// quoted words to a conversation that is no lead, a hand-over still queued or past its 14 days, and the main
// conversation.
func TestALeadAHandoverWentIntoWaitsForStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string // the planning conversation: lead, side or main
		before func(d *dispatchPolicyHarness, t *testing.T)
		over   map[string]any
		trust  bool
		starts bool
	}{
		{"the turn the hand-over opened", "lead", (*dispatchPolicyHarness).handOver, nil, false, false},
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
		}, nil, false, false},
		{"after the owner writes there", "lead", func(d *dispatchPolicyHarness, t *testing.T) {
			d.handOver(t)
			d.s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: d.leaderID})
			d.f.setItems(ownerItem("m-lead", "yes, fix the export there"))
		}, nil, false, true},
		{"read-only, after a hand-over", "lead", (*dispatchPolicyHarness).handOver,
			map[string]any{"readonly": true, "permission": "plan", "goal": "find why the export drops it"}, false, true},
		{"a trusted kind, after a hand-over", "lead", (*dispatchPolicyHarness).handOver, nil, true, true},
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

// A direct send of a hand-over into a lead that ends in doubt has opened the lead's turn: a writable plan in that turn
// waits, although nothing said yet that the message went in.
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
	if d.planIn(t, d.leaderID, nil) {
		t.Fatal("a writable plan in the turn a hand-over in doubt opened started without the owner")
	}
}

// A try the lead definitely did not take leaves no mark when the lead had none: its plans start as before. One that had
// a mark from an earlier hand-over keeps it.
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
			d.f.setItems(taskReport())
			if got := d.planIn(t, d.leaderID, nil); got == before {
				t.Fatalf("started %v after a refused try (%s)", got, name)
			}
		})
	}
}

// The mark is saved before the send leaves: a manager that restarts while the send is on its way finds it, and a
// writable plan in the lead waits.
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
	restarted.f.setItems(taskReport())
	if restarted.planIn(t, restarted.leaderID, nil) {
		t.Fatal("after a restart during the send a writable plan in the lead started without the owner")
	}
}

// The lead's wait is kept with the relay state: after a manager restart a writable plan there still waits.
func TestALeadsWaitAfterAHandoverSurvivesARestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	d := newStartRuleHarness(t, st)
	stopHolds(t, d.s)
	d.handOver(t)
	restarted := newStartRuleHarness(t, st)
	stopHolds(t, restarted.s)
	restarted.f.setItems(taskReport())
	if restarted.planIn(t, restarted.leaderID, nil) {
		t.Fatal("after a restart a writable plan in the lead started without the owner")
	}
}

// The plan that waits still shows the owner's words its message carried: the card's brief takes them, so the owner
// sees what they asked for before Start.
func TestARelayedPlanThatWaitsShowsTheOwnersWords(t *testing.T) {
	d := newStartRuleHarness(t, nil)
	stopHolds(t, d.s)
	d.handOver(t)
	res := d.tool(t, d.leaderID, "task_plan", relayedPlan(nil))
	if res.IsError || !waitsForStart(res.Content[0].Text) {
		t.Fatalf("task_plan: %s", res.Content[0].Text)
	}
	p, err := d.s.loadPlan(context.Background(), planIDPattern.FindString(res.Content[0].Text))
	if err != nil {
		t.Fatal(err)
	}
	if b := p.Tasks[0].Brief; b == nil || b.Request == nil || b.Request.Text != quotedWords || b.Trigger != "conversation" || p.Tasks[0].UserRequested {
		t.Fatalf("the waiting plan's task: %+v", p.Tasks[0])
	}
}
