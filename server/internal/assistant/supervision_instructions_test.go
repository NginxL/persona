package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestGoldenSupervisionInstructions(t *testing.T) {
	text := prompts.RenderSupervisionInstructions()
	golden(t, "supervision_instructions", text)
	raw, err := os.ReadFile("../prompts/assistant/supervision_instructions.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "{{") || strings.TrimSuffix(string(raw), "\n") != text {
		t.Fatal("supervision policy is not the exact static template bytes")
	}
}

func TestSupervisionInstructionsDispatchIsolation(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	yes, no := true, false
	setSettings(t, s, func(v *Settings) {
		v.MainSupervisesOwnTasks = &no
		v.Leaders = []string{"lead1"}
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "background", "server"
	})
	f.setSessions("server",
		proto.Session{SessionID: "conv1", Assistant: true},
		proto.Session{SessionID: "lead1", Assistant: true},
		proto.Session{SessionID: "background", Assistant: true},
		proto.Session{SessionID: "side", Assistant: true},
		proto.Session{SessionID: "ordinary", Assistant: true},
		proto.Session{SessionID: "worker"},
		proto.Session{SessionID: "health", Assistant: true, Space: SpaceHealth})
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.Supervisor = "side"
	s.todos.mu.Unlock()
	policy := prompts.RenderSupervisionInstructions()
	for _, tc := range []struct {
		machine, id string
		want        bool
	}{
		{"server", "conv1", false}, {"server", "lead1", true},
		{"server", "background", true}, {"server", "side", true},
		{"server", "ordinary", false}, {"server", "worker", false},
		{"server", "health", false}, {"server", "gone", false},
		{"box", "lead1", false}, {"box", "background", false},
		{"box", "side", false}, {"box", "conv1", false},
	} {
		t.Run(tc.machine+"/"+tc.id, func(t *testing.T) {
			want := ""
			if tc.want {
				want = policy
			}
			for _, method := range []string{DeviceAssistantSend, DeviceSessionSend} {
				p := map[string]any{"session_id": tc.id, "text": "New task facts", "supervision_instructions": "caller policy"}
				if err := s.call(ctx, tc.machine, method, p, nil); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				sends := f.sends
				if method == DeviceSessionSend {
					sends = f.agentSent
				}
				got := sends[len(sends)-1]["supervision_instructions"]
				f.mu.Unlock()
				if got != want {
					t.Fatalf("%s policy: got %d bytes, want %d", method, len(got.(string)), len(want))
				}
				if tc.id == "conv1" && tc.machine == "server" && p["is_main"] != true {
					t.Fatalf("%s lost the main conversation's execution identity", method)
				}
			}
			resolved, err := s.SendParams(ctx, SendParams{MachineID: tc.machine, SessionID: tc.id, Params: map[string]any{"supervision_instructions": "untrusted"}})
			if err != nil {
				t.Fatal(err)
			}
			if resolved["supervision_instructions"] != want {
				t.Fatal("app/node model resolution disagrees with manager dispatch")
			}
		})
	}
	setSettings(t, s, func(v *Settings) { v.MainSupervisesOwnTasks = &yes })
	if mustSupervisionInstructions(t, s, ctx, "server", "conv1") != policy {
		t.Fatal("self-supervising main lacks policy")
	}
	if mustSupervisionInstructions(t, s, ctx, "box", "conv1") != "" {
		t.Fatal("main identity leaked to another machine")
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = nil })
	if mustSupervisionInstructions(t, s, ctx, "server", "lead1") != "" {
		t.Fatal("former direction without tasks retained policy")
	}
	now := time.Now()
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].DoneAt = &now
	s.todos.mu.Unlock()
	if mustSupervisionInstructions(t, s, ctx, "server", "side") != "" {
		t.Fatal("side chat retained policy after its last task closed")
	}
}

func TestSupervisionInstructionsFirstStartAndWarm(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	yes := true
	setSettings(t, s, func(v *Settings) { v.MainSupervisesOwnTasks = &yes })
	policy := prompts.RenderSupervisionInstructions()
	for _, tc := range []struct {
		name, purpose string
		main, want    bool
	}{
		{"main", "", true, true},
		{"side", "", false, false},
		{"purpose alone is not authority", purposeSupervise, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.startOn(ctx, "server", StartParams{Text: "Hello", Purpose: tc.purpose}, tc.main); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			got := f.sends[len(f.sends)-1]["supervision_instructions"]
			f.mu.Unlock()
			if (got == policy) != tc.want || !tc.want && got != "" {
				t.Fatal("first start has the wrong policy")
			}
		})
	}
	s.noBackground = false
	if _, err := s.startBackground(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.sends[len(f.sends)-1]["supervision_instructions"]
	f.mu.Unlock()
	if got != policy {
		t.Fatal("background supervisor's first start lacks exact static policy")
	}
	s.warm("")
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, call := range f.calls {
		if call.Method != DeviceAssistantWarm {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(call.Params, &p); err != nil {
			t.Fatal(err)
		}
		found = true
		if p["supervision_instructions"] != policy || p["is_main"] != true {
			t.Fatal("main warm did not use its startup role")
		}
	}
	if !found {
		t.Fatal("no warm dispatch")
	}
}

func TestSupervisionEventsContainFactsNotPolicy(t *testing.T) {
	policy := prompts.RenderSupervisionInstructions()
	for _, name := range []string{"overdue", "estimate_due", "parked_due", "park_decided", "inbox", "ask", "escalated", "leader_stuck", "recheck", "undecidable", "owner_cannot_answer", "decide", "all_stopped", "pr_conflict"} {
		t.Run(name, func(t *testing.T) {
			p := prompts.Supervise{Leader: true, Side: true, Background: true, Escalated: true, BackgroundEscalated: true,
				LeaderStuck: true, Overdue: true, ParkedDue: true, ParkDecided: true, Asked: true, AskMinutes: 5, LowDistraction: true, Decisions: true, Inbox: true,
				Entries: []prompts.SuperviseEntry{{ID: "todo_example", Title: "Example task", Kind: name, Last: "Task evidence"}}}
			text := prompts.RenderSupervise(p)
			for _, banned := range []string{"怎么处理", "task_review", "agent_send", "ask_escalate", "interaction_answer", "supervision_finish", "task_scope", "验收以它", "按任务本身", "Check each participant's stated wait", "Waiting on another stopped participant"} {
				if strings.Contains(text, banned) {
					t.Fatalf("event contains handling policy %q", banned)
				}
			}
			if !strings.Contains(text, "todo_example") || !strings.Contains(text, "Task evidence") || !strings.Contains(text, "signal="+name) {
				t.Fatal("event lost its task, signal or Agent facts")
			}
			if prompts.RenderSupervisionInstructions() != policy {
				t.Fatal("policy bytes vary with task/event/role data")
			}
		})
	}
	for _, background := range []bool{false, true} {
		common := prompts.RenderAssistantClaudeMD(prompts.AssistantClaudeMD{BackgroundSupervisor: background})
		if common != prompts.RenderAssistantClaudeMD(prompts.AssistantClaudeMD{}) {
			t.Fatal("supervisor-mode changes rewrote the directory shared by ordinary chats")
		}
		for _, banned := range []string{"## Task supervision", "visibility: \"silent\"", "cost_of_waiting", "user_check", "独立评审"} {
			if strings.Contains(common, banned) {
				t.Fatalf("shared instructions contain detailed policy %q", banned)
			}
		}
	}
	for _, role := range []prompts.AssistantContext{
		{Leader: true, Owner: "Example user", LeaderName: "Example direction"},
		{Background: true, Owner: "Example user"},
	} {
		text := prompts.RenderAssistantContext(role)
		for _, banned := range []string{"task_review", "agent_send", "leader_progress", "supervision_finish", "todo_create"} {
			if strings.Contains(text, banned) {
				t.Fatalf("role facts repeated handling policy %q", banned)
			}
		}
	}
	start := prompts.RenderSupervisorStart(prompts.SupervisorStart{Owner: "Example user"})
	if strings.Contains(start, "task_review") || strings.Contains(start, "不要调用工具") {
		t.Fatal("background startup repeated handling policy instead of role facts")
	}
}

func mustSupervisionInstructions(t *testing.T, s *Service, ctx context.Context, machine, id string) string {
	t.Helper()
	text, err := s.supervisionInstructions(ctx, machine, id)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

type supervisionUnavailableStore struct {
	Store
	todoStore
}

func (supervisionUnavailableStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	return nil, errors.New("task ownership unavailable")
}

func TestSupervisionInstructionsUnknownOwnershipDoesNotDemote(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, supervisionUnavailableStore{Store: &memStore{}})
	f.setSessions("server", proto.Session{SessionID: "side", Assistant: true})
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()
	p := map[string]any{"session_id": "side", "text": "New facts"}
	f.mu.Lock()
	before := len(f.sends)
	f.mu.Unlock()
	err := s.call(context.Background(), "server", DeviceAssistantSend, p, nil)
	if err == nil || !strings.Contains(err.Error(), "task ownership unavailable") {
		t.Fatalf("unknown ownership dispatched without supervisor policy: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != before {
		t.Fatal("an ownership lookup failure reached the node")
	}
}

func TestSupervisionInstructionsGuestsNeverBecomeSupervisors(t *testing.T) {
	f, s, _ := guestService(t)
	w := postConversation(t, s, `{"prompt":"Read a connector","purpose":"lark.agent","guest":{"tools":["lark_agent_reply"]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("guest start: %d", w.Code)
	}
	var result struct {
		Session proto.Session `json:"session"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{result.Session.SessionID} })
	p := map[string]any{"session_id": result.Session.SessionID, "text": "Read again", "supervision_instructions": "untrusted caller policy"}
	if err := s.call(context.Background(), result.Session.MachineID, DeviceAssistantSend, p, nil); err != nil {
		t.Fatal(err)
	}
	if p["supervision_instructions"] != "" {
		t.Fatal("guest inherited supervision instructions")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, send := range f.sends {
		if send["supervision_instructions"] != "" {
			t.Fatal("guest first start or resumed dispatch received supervisor policy")
		}
	}
}

func TestSupervisionPolicySizeBudget(t *testing.T) {
	raw, err := os.ReadFile(promptSizeBaseline)
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]int
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	pieces := promptSizePieces(t)
	if pieces["supervision_instructions"] != len(prompts.RenderSupervisionInstructions()) {
		t.Fatal("the new static fragment is not covered by the size ratchet")
	}
	verdict := decidePromptSizeUpdate(baseline, pieces)
	t.Logf("instruction budget: %d -> %d bytes including the static fragment", verdict.baselineTotal, verdict.currentTotal)
	if !verdict.accept {
		t.Fatal("moving the policy raised the total instruction budget")
	}
}
