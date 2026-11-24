package assistant

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func transferEnv(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := newQuietService(t)
	f.known["lead0001"] = true
	f.known["lead0002"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "lead0001", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "lead0002", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "side0001", MachineID: "server", Assistant: true})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead0001", "lead0002"} })
	working := boxAgent("work0001")
	working.Status, working.State = proto.StatusWorking, proto.StateWorking
	leadWork := boxAgent("leadw001")
	leadWork.Status, leadWork.State = proto.StatusWorking, proto.StateWorking
	otherLead := boxAgent("lead2001")
	otherLead.Status, otherLead.State = proto.StatusWorking, proto.StateWorking
	sideWork := boxAgent("sidew001")
	sideWork.Status, sideWork.State = proto.StatusWorking, proto.StateWorking
	taskLeaderWork := boxAgent("tldr0001")
	taskLeaderWork.Status, taskLeaderWork.State = proto.StatusWorking, proto.StateWorking
	f.setSessions("box", working, leadWork, otherLead, sideWork, taskLeaderWork,
		boxAgent("free0001"), boxAgent("ownr0001"), boxAgent("closed01"))
	return s, f
}

func transferArgs(machine, sid, supervisor string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"machine": machine, "session_id": sid, "supervisor": supervisor})
	return raw
}

func assertNoAgentControlCalls(t *testing.T, f *fakeHub, callsBefore int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls[callsBefore:] {
		if slices.Contains([]string{"session.send", "session.start", "session.interrupt", "session.close", "session.resume"}, c.Method) {
			t.Fatalf("transfer controlled the Agent: %s", c.Method)
		}
	}
}

func TestAgentTransferPreservesRunningWorkAndRoutesSupervision(t *testing.T) {
	s, f := transferEnv(t)
	ctx := t.Context()
	task := onBoxTask(t, s, "work0001", "conv1", false, "Checks pass")
	s.briefFor(ctx, s.callerOrigin(ctx, "conv1"), task, []string{"Checks pass"}, 30)
	before, _ := s.todoNow(task.ID)
	brief, ok := s.briefOfTodo(task.ID)
	if !ok {
		t.Fatal("missing original task brief")
	}
	f.mu.Lock()
	callsBefore := len(f.calls)
	f.mu.Unlock()

	for _, destination := range []string{"lead0001", "lead0001", "conv1"} {
		out, err := s.runTool(ctx, "server", "conv1", "agent_transfer", transferArgs("box", "work0001", destination))
		if err != nil || !strings.Contains(out, task.ID) || !strings.Contains(out, "Use agent_transfer with the previous supervisor to move it back.") {
			t.Fatalf("transfer to %s: %s %v", destination, out, err)
		}
		after, _ := s.todoNow(task.ID)
		if after.Supervision == nil || after.Supervision.Supervisor != destination || after.Origin != before.Origin ||
			after.Title != before.Title || after.Notes != before.Notes || after.DoneAt != nil ||
			!reflect.DeepEqual(after.Agents, before.Agents) || after.Supervision.ExpectMinutes != before.Supervision.ExpectMinutes ||
			!slices.Equal(after.Supervision.DoneWhen, before.Supervision.DoneWhen) || after.Supervision.State != before.Supervision.State {
			t.Fatalf("the running task changed beyond its supervisor: %+v", after)
		}
		if got, exists := s.briefOfTodo(task.ID); !exists || !reflect.DeepEqual(got, brief) {
			t.Fatalf("transfer rewrote its brief: %+v", got)
		}
		if to := s.digestTo(ctx, s.settings(ctx), superviseSignal{Todo: task.ID, Kind: sigStalled}); to != destination {
			t.Fatalf("supervision goes to %s, want %s", to, destination)
		}
		if taskCardWake(s, destination, task.ID) != wakeActive {
			t.Fatal("the destination does not show the existing task card")
		}
	}
	items, _, err := s.feed(ctx, "lead0001", time.Time{})
	if err != nil || len(items) != 1 {
		t.Fatalf("an idempotent transfer duplicated the direction's card: %d %v", len(items), err)
	}
	s.todos.mu.RLock()
	count := len(s.todos.todos)
	s.todos.mu.RUnlock()
	if count != 1 {
		t.Fatal("transfer made another task")
	}
	assertNoAgentControlCalls(t, f, callsBefore)
}

func TestAgentTransferLeadHandsOffOwnTask(t *testing.T) {
	s, f := transferEnv(t)
	ctx := t.Context()
	task := onBoxTask(t, s, "leadw001", "lead0001", false, "Checks pass")
	s.briefFor(ctx, s.callerOrigin(ctx, "lead0001"), task, []string{"Checks pass"}, 30)
	before, _ := s.todoNow(task.ID)
	brief, ok := s.briefOfTodo(task.ID)
	if !ok {
		t.Fatal("missing original task brief")
	}
	f.mu.Lock()
	callsBefore := len(f.calls)
	f.mu.Unlock()

	checkPreserved := func(destination string) {
		t.Helper()
		after, _ := s.todoNow(task.ID)
		if after.Supervision == nil || after.Supervision.Supervisor != destination || after.Origin != before.Origin ||
			after.Title != before.Title || after.Notes != before.Notes || after.DoneAt != nil ||
			!reflect.DeepEqual(after.Agents, before.Agents) || after.Supervision.ExpectMinutes != before.Supervision.ExpectMinutes ||
			!slices.Equal(after.Supervision.DoneWhen, before.Supervision.DoneWhen) || after.Supervision.State != before.Supervision.State {
			t.Fatalf("the running task changed beyond its supervisor: %+v", after)
		}
		if got, exists := s.briefOfTodo(task.ID); !exists || !reflect.DeepEqual(got, brief) {
			t.Fatalf("transfer rewrote its brief: %+v", got)
		}
		if to := s.digestTo(ctx, s.settings(ctx), superviseSignal{Todo: task.ID, Kind: sigStalled}); to != destination {
			t.Fatalf("supervision goes to %s, want %s", to, destination)
		}
		if taskCardWake(s, destination, task.ID) != wakeActive {
			t.Fatalf("destination %s does not show the existing task card", destination)
		}
	}

	// Idempotent self-transfer keeps state and creates no extra card.
	if out, err := s.runTool(ctx, "server", "lead0001", "agent_transfer", transferArgs("box", "leadw001", "lead0001")); err != nil || !strings.Contains(out, task.ID) {
		t.Fatalf("idempotent lead transfer: %s %v", out, err)
	}

	// lead0001 hands its own open task to another current lead (lead0002).
	out, err := s.runTool(ctx, "server", "lead0001", "agent_transfer", transferArgs("box", "leadw001", "lead0002"))
	if err != nil || !strings.Contains(out, task.ID) || !strings.Contains(out, "Only the new supervisor, the main conversation, or the owner can move it back.") {
		t.Fatalf("lead0001 -> lead0002: %s %v", out, err)
	}
	checkPreserved("lead0002")

	// Even as the task's origin, lead0001 cannot pull it back once lead0002 supervises it.
	ledgerBefore := taskLedger(t, s, "conv1", "lead0001", "lead0002")
	if _, err := s.runTool(ctx, "server", "lead0001", "agent_transfer", transferArgs("box", "leadw001", "lead0001")); err == nil ||
		!strings.Contains(err.Error(), "lead0002") || !strings.Contains(err.Error(), "main conversation") {
		t.Fatalf("origin lead pulled back transferred task: %v", err)
	}
	if got := taskLedger(t, s, "conv1", "lead0001", "lead0002"); got != ledgerBefore {
		t.Fatal("refused pull-back changed the ledger or task cards")
	}

	// lead0002 (current supervisor, different from Origin) hands the task to the main conversation.
	out, err = s.runTool(ctx, "server", "lead0002", "agent_transfer", transferArgs("box", "leadw001", "conv1"))
	if err != nil || !strings.Contains(out, task.ID) || !strings.Contains(out, "Only the new supervisor, the main conversation, or the owner can move it back.") {
		t.Fatalf("lead0002 -> conv1: %s %v", out, err)
	}
	checkPreserved("conv1")

	// After handing off to main, lead0002 cannot move it back; refusal names main.
	ledgerBefore = taskLedger(t, s, "conv1", "lead0001", "lead0002")
	if _, err := s.runTool(ctx, "server", "lead0002", "agent_transfer", transferArgs("box", "leadw001", "lead0002")); err == nil ||
		!strings.Contains(err.Error(), "main conversation (conv1)") || !strings.Contains(err.Error(), "only the main conversation or the owner") {
		t.Fatalf("former supervisor moved main's task: %v", err)
	}
	if got := taskLedger(t, s, "conv1", "lead0001", "lead0002"); got != ledgerBefore {
		t.Fatal("refused transfer from main changed the ledger or task cards")
	}

	// Main can still move the task back to a lead as before.
	if out, err := s.runTool(ctx, "server", "conv1", "agent_transfer", transferArgs("box", "leadw001", "lead0001")); err != nil || !strings.Contains(out, task.ID) {
		t.Fatalf("main -> lead0001: %s %v", out, err)
	}
	checkPreserved("lead0001")

	s.todos.mu.RLock()
	count := len(s.todos.todos)
	s.todos.mu.RUnlock()
	if count != 1 {
		t.Fatal("transfer made another task")
	}
	assertNoAgentControlCalls(t, f, callsBefore)
}

func TestAgentTransferRefusesWithoutChangingTheLedger(t *testing.T) {
	for _, tc := range []struct {
		name, self, machine, session, supervisor string
		wants                                    []string
	}{
		{"side conversation", "side0001", "box", "work0001", "lead0001", []string{"only the main conversation"}},
		{"side conversation on its own task", "side0001", "box", "sidew001", "lead0001", []string{"only the main conversation"}},
		{"lead on main's task", "lead0001", "box", "work0001", "lead0002", []string{"main conversation (conv1)", "only the main conversation or the owner"}},
		{"lead on another lead's task", "lead0001", "box", "lead2001", "lead0001", []string{"direction lead lead0002", "only that supervisor, the main conversation, or the owner"}},
		{"lead on side conversation's task", "lead0001", "box", "sidew001", "lead0002", []string{"conversation side0001", "only the main conversation or the owner"}},
		{"lead on task leader's task", "lead0001", "box", "tldr0001", "lead0002", []string{"task leader box/leader01", "only the main conversation or the owner"}},
		{"lead to non-leader destination", "lead0001", "box", "leadw001", "side0001", []string{"not a leader conversation"}},
		{"lead missing destination", "lead0001", "box", "leadw001", "", []string{"supervisor is required"}},
		{"lead on owner task", "lead0001", "box", "ownr0001", "lead0002", []string{"owner's own to-do"}},
		{"lead on untracked Agent", "lead0001", "box", "free0001", "lead0002", []string{"no open task"}},
		{"lead on closed task", "lead0001", "box", "closed01", "lead0002", []string{"no open task"}},
		{"lead on assistant session", "lead0001", "server", "lead0002", "conv1", []string{"not a coding agent"}},
		{"not a direction", "conv1", "box", "work0001", "side0001", []string{"not a leader conversation"}},
		{"missing destination", "conv1", "box", "work0001", "", []string{"supervisor is required"}},
		{"owner task", "conv1", "box", "ownr0001", "lead0001", []string{"owner's own to-do"}},
		{"untracked Agent", "conv1", "box", "free0001", "lead0001", []string{"no open task"}},
		{"closed task", "conv1", "box", "closed01", "lead0001", []string{"no open task"}},
		{"assistant session", "conv1", "server", "lead0001", "conv1", []string{"not a coding agent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := transferEnv(t)
			onBoxTask(t, s, "work0001", "conv1", false, "Checks pass")
			onBoxTask(t, s, "leadw001", "lead0001", false, "Checks pass")
			onBoxTask(t, s, "lead2001", "lead0002", false, "Checks pass")
			onBoxTask(t, s, "sidew001", "side0001", false, "Checks pass")
			tldr := onBoxTask(t, s, "tldr0001", "conv1", false, "Checks pass")
			if _, _, err := s.updateSupervision(t.Context(), tldr.ID, func(v *Supervision) bool {
				v.Supervisor = leaderRef("box", "leader01")
				return true
			}); err != nil {
				t.Fatal(err)
			}
			onBoxTask(t, s, "ownr0001", "", false)
			onBoxTask(t, s, "closed01", "conv1", true, "Checks pass")
			before := taskLedger(t, s, "conv1", "lead0001", "lead0002", "side0001")
			_, err := s.runTool(t.Context(), "server", tc.self, "agent_transfer", transferArgs(tc.machine, tc.session, tc.supervisor))
			if err == nil {
				t.Fatalf("refusal: nil, want %v", tc.wants)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal: %v, want %q", err, want)
				}
			}
			if got := taskLedger(t, s, "conv1", "lead0001", "lead0002", "side0001"); got != before {
				t.Fatal("a refused transfer changed the ledger or task cards")
			}
			if len(sentToAgent(f)) != 0 {
				t.Fatal("a refused transfer sent the Agent a message")
			}
		})
	}
}

func TestAgentTransferIsAnAssistantTool(t *testing.T) {
	s, _ := transferEnv(t)
	has := func(list []proto.Tool) bool {
		return slices.ContainsFunc(list, func(tool proto.Tool) bool { return tool.Name == "agent_transfer" })
	}
	ctx := t.Context()
	if !has(s.Tools(ctx)) || has(s.AgentTools()) {
		t.Fatal("agent_transfer must be listed for assistant conversations only")
	}
	for _, conv := range []string{"conv1", "lead0001"} {
		if !has(s.sessionMCPTools(ctx, "server", conv, true, false)) {
			t.Fatalf("agent_transfer must be exposed to %s", conv)
		}
	}
}
