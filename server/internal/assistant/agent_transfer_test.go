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
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "lead0001", MachineID: "server", Assistant: true})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead0001"} })
	working := boxAgent("work0001")
	working.Status, working.State = proto.StatusWorking, proto.StateWorking
	f.setSessions("box", working, boxAgent("free0001"), boxAgent("ownr0001"), boxAgent("closed01"))
	return s, f
}

func transferArgs(machine, sid, supervisor string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"machine": machine, "session_id": sid, "supervisor": supervisor})
	return raw
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
		if err != nil || !strings.Contains(out, task.ID) {
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
		if to := s.digestTo(s.settings(ctx), superviseSignal{Todo: task.ID, Kind: sigStalled}); to != destination {
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
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls[callsBefore:] {
		if slices.Contains([]string{"session.send", "session.start", "session.interrupt", "session.close", "session.resume"}, c.Method) {
			t.Fatalf("transfer controlled the Agent: %s", c.Method)
		}
	}
}

func TestAgentTransferRefusesWithoutChangingTheLedger(t *testing.T) {
	for _, tc := range []struct{ name, self, machine, session, supervisor, want string }{
		{"side conversation", "side0001", "box", "work0001", "lead0001", "only the main conversation"},
		{"leader conversation", "lead0001", "box", "work0001", "conv1", "only the main conversation"},
		{"not a direction", "conv1", "box", "work0001", "side0001", "not a leader conversation"},
		{"missing destination", "conv1", "box", "work0001", "", "supervisor is required"},
		{"owner task", "conv1", "box", "ownr0001", "lead0001", "owner's own to-do"},
		{"untracked Agent", "conv1", "box", "free0001", "lead0001", "no open task"},
		{"closed task", "conv1", "box", "closed01", "lead0001", "no open task"},
		{"assistant session", "conv1", "server", "lead0001", "conv1", "not a coding agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := transferEnv(t)
			onBoxTask(t, s, "work0001", "conv1", false, "Checks pass")
			onBoxTask(t, s, "ownr0001", "", false)
			onBoxTask(t, s, "closed01", "conv1", true, "Checks pass")
			before := taskLedger(t, s, "conv1", "lead0001")
			_, err := s.runTool(t.Context(), "server", tc.self, "agent_transfer", transferArgs(tc.machine, tc.session, tc.supervisor))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal: %v, want %s", err, tc.want)
			}
			if got := taskLedger(t, s, "conv1", "lead0001"); got != before {
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
	if !has(s.Tools(t.Context())) || has(s.AgentTools()) {
		t.Fatal("agent_transfer must be listed for assistant conversations only")
	}
}
