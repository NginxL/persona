package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPlanningLeadOwnsStartupRemediesRetryAndCancellation(t *testing.T) {
	s, f := newQuietService(t)
	ctx := t.Context()
	f.known["lead1"], f.known["lead2"] = true, true
	f.agents, f.failStart = 999999, keptRefusal(proto.CodeShellWaiting)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "lead2", MachineID: "server", Assistant: true})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1", "lead2"} })
	if out, err := s.toolTaskPlan(ctx, "lead1", planArgs(true)); err != nil {
		t.Fatalf("lead plan: %s %v", out, err)
	}
	task := onlyTodo(s)
	waitFor(t, "lead's failed-start notice", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("lead1"), "\n"), "signal=failed")
	})
	task = todoNow(s, task.ID)
	if task.Supervision.Supervisor != "lead1" || task.Supervision.SupervisorSource != "plan" || len(f.noticesTo("conv1")) != 0 {
		t.Fatalf("failed start did not belong to its planning lead: %+v", task)
	}
	var typed int
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{
		"session.screen": func(_ string, _ json.RawMessage) (any, error) {
			return map[string]any{"text": "A startup question", "status": proto.StatusIdle}, nil
		},
		"start.keys": func(_ string, raw json.RawMessage) (any, error) {
			var a struct {
				Pane string   `json:"pane"`
				Keys []string `json:"keys"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if a.Pane != "w1:p2" || len(a.Keys) != 1 || a.Keys[0] != "n" {
				t.Errorf("wrong startup keys: %+v", a)
			}
			typed++
			return map[string]any{}, nil
		},
	}
	f.mu.Unlock()
	args := func(values map[string]any) json.RawMessage { raw, _ := json.Marshal(values); return raw }
	for _, tool := range []string{"task_start_screen", "task_start_keys", "task_retry"} {
		if _, err := s.runTool(ctx, "server", "lead2", tool, args(map[string]any{"todo_id": task.ID, "keys": []string{"n"}, "reason": "Check startup"})); err == nil {
			t.Fatalf("unrelated lead used %s", tool)
		}
	}
	if out, err := s.runTool(ctx, "server", "lead1", "task_start_screen", args(map[string]any{"todo_id": task.ID})); err != nil || !strings.Contains(out, "A startup question") {
		t.Fatalf("lead could not inspect startup: %s %v", out, err)
	}
	if _, err := s.runTool(ctx, "server", "lead1", "task_start_keys", args(map[string]any{"todo_id": task.ID, "keys": []string{"n"}, "reason": "Continue the existing startup"})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if typed != 1 {
		t.Errorf("startup answer delivered %d times", typed)
	}
	f.failStart = nil
	f.mu.Unlock()
	if out, err := s.runTool(ctx, "server", "lead1", "task_retry", args(map[string]any{"todo_id": task.ID, "reason": "Startup question answered"})); err != nil || !strings.Contains(out, "the same tab") {
		t.Fatalf("lead retry: %s %v", out, err)
	}
	waitFor(t, "lead's successful retry notice", func() bool {
		return len(todoNow(s, task.ID).Agents) == 1 && strings.Contains(strings.Join(f.noticesTo("lead1"), "\n"), "signal=started")
	})
	if len(f.noticesTo("conv1")) != 0 || todoNow(s, task.ID).Supervision.Supervisor != "lead1" {
		t.Fatal("retry moved supervision back to main")
	}
	if _, err := runJSON(t, s, "lead1", "task_review", map[string]any{"todo_id": task.ID, "verdict": "cancelled", "note": "Stop this task"}); err != nil {
		t.Fatalf("lead could not cancel its planned task: %v", err)
	}
	if got := todoNow(s, task.ID); got.DoneAt == nil || got.Supervision.State != reviewCancelled || got.Supervision.Supervisor != "lead1" {
		t.Fatalf("cancelled task lost ownership: %+v", got)
	}
}

func TestPlanningLeadReceivesItsCardsWithoutMain(t *testing.T) {
	for _, kind := range []string{proto.IxQuestion, proto.IxPermission, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			s, f, task := newConversationPlanServiceOn(t, nil, "lead1", 999999)
			settleService(t, s)
			ix := askQuestion("lead-card", task.Agents[0].SessionID)
			if kind != proto.IxQuestion {
				ix = askPermission("lead-card", task.Agents[0].SessionID)
				ix.Kind = kind
			}
			ix.MachineID = task.Agents[0].MachineID
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ended := make(chan struct{})
			go func() { _, _ = s.waitInteraction(ctx, ix); close(ended) }()
			waitFor(t, "lead's card", func() bool {
				return strings.Contains(strings.Join(f.noticesTo("lead1"), "\n"), "interaction_id=lead-card")
			})
			settleService(t, s)
			if len(f.noticesTo("conv1")) != 0 {
				t.Fatal("main received its lead's card")
			}
			s.fleet.mu.Lock()
			p := s.fleet.interactions[ix.ID]
			ok := p != nil && p.route != nil && p.route.at() == "lead1" && len(p.route.ladder) == 1
			s.fleet.mu.Unlock()
			if !ok {
				t.Fatal("the card can silently hand worker supervision to main")
			}
			s.signalAsk(task.ID, "lead1", ix, 0)
			settleService(t, s)
			if n := strings.Count(strings.Join(f.noticesTo("lead1"), "\n"), "interaction_id=lead-card"); n != 1 {
				t.Fatalf("card delivered %d times", n)
			}
			cancel()
			<-ended
		})
	}
}

func TestPlanningLeadOnlyEscalatesOwnerDecisionToMain(t *testing.T) {
	s, f, task := newLeaderService(t)
	waitsForReview(t, s, f, task.ID, "Ready for review")
	settleService(t, s)
	if len(f.noticesTo("conv1")) != 0 {
		t.Fatal("main received ordinary lead supervision")
	}
	if _, err := runJSON(t, s, "lead1", "task_review", map[string]any{
		"todo_id": task.ID, "verdict": "needs_user", "reason": "Choose the release priority", "recommend": "Ship the verified fix",
		"why_owner": "priority", "cost_of_waiting": "The release waits", "options": []map[string]string{
			{"label": "Ship the verified fix", "leads_to": "The fix ships"}, {"label": "Wait", "leads_to": "Keep the release pending"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owner-decision escalation", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if n := f.noticesTo("conv1")[0]; !strings.Contains(n, "signal=escalated") || strings.Contains(n, "signal=review") {
		t.Fatalf("main received worker instructions: %s", n)
	}
	if got := todoNow(s, task.ID).Supervision; got.State != reviewNeedsUser || got.Supervisor != "lead1" {
		t.Fatalf("escalation took ownership: %+v", got)
	}
	settleService(t, s)
}

func TestPlanningMainRetainsItsOwnTask(t *testing.T) {
	s, f, task := newConversationPlanServiceOn(t, todoTestDB(t), "conv1", 999999)
	if task.Supervision.Supervisor != "conv1" {
		t.Fatalf("main's plan changed owner: %+v", task)
	}
	s.signal(superviseSignal{Todo: task.ID, Kind: sigReport, Report: upReportQuestion, Key: "main-question", Detail: "A main-owned question", At: time.Now()})
	waitFor(t, "main's question", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if len(f.noticesTo("lead1")) != 0 {
		t.Fatal("a main-owned task leaked to a direction")
	}
	settleService(t, s)
}

func TestPlanningLeadExplicitSelfAssignment(t *testing.T) {
	s, f := newQuietService(t)
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true}, proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	var args map[string]any
	if err := json.Unmarshal(planArgs(true), &args); err != nil {
		t.Fatal(err)
	}
	args["supervisor"] = "lead1"
	raw, _ := json.Marshal(args)
	if out, err := s.toolTaskPlan(t.Context(), "lead1", raw); err != nil || !strings.Contains(out, "You supervise them") {
		t.Fatalf("explicit self supervision: %s %v", out, err)
	}
	if v := onlyTodo(s).Supervision; v == nil || v.Supervisor != "lead1" || v.SupervisorSource != "plan" {
		t.Fatalf("explicit self assignment lost: %+v", v)
	}
	settleService(t, s)
}

func TestIncompatibleDirectionLeadCannotSilentlyHandWorkToMain(t *testing.T) {
	s, f := newQuietService(t)
	lead := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "dsh"}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true}, lead)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{lead.SessionID} })
	if _, err := s.toolTaskPlan(t.Context(), lead.SessionID, planArgs(true)); err == nil {
		t.Fatal("incompatible planning lead handed its supervision to main")
	}
	if todoCount(s) != 0 || len(f.started()) != 0 || len(f.noticesTo("conv1")) != 0 {
		t.Fatal("refused plan dispatched or woke main")
	}
	settleService(t, s)
}

func TestAssignedLeadLosingRuntimePolicyKeepsSignalsAwayFromMain(t *testing.T) {
	s, f := newQuietService(t)
	cfg := s.settings(t.Context())
	cfg.Leaders = []string{"lead1"}
	setSettings(t, s, func(v *Settings) { v.Leaders = cfg.Leaders })
	f.setSessions("server", proto.Session{SessionID: cfg.Main, MachineID: "server", Assistant: true}, proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "dsh"})
	task, err := s.createTodo(t.Context(), "Assigned work", "", "lead1", "")
	if err != nil {
		t.Fatal(err)
	}
	// Represents a lead whose runtime capabilities changed after an accepted assignment.
	if err := s.superviseTodo(t.Context(), task.ID, "lead1", nil, 0, "plan"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{sigEstimateDue, sigReport, sigIdle, sigReview, sigUndecidable, sigAllStopped, sigOrphaned, sigFailed, sigStarted, sigAsk} {
		g := superviseSignal{Todo: task.ID, Kind: kind, To: "lead1"}
		if got := s.digestTarget(t.Context(), cfg, g); got != "" {
			t.Errorf("%s incompatible runtime target: %s", kind, got)
		}
		if got := s.routeTo(t.Context(), cfg, g, func(string) bool { return false }); got != "" {
			t.Errorf("%s fell back to %s", kind, got)
		}
		if got := s.recheckDigestTarget(t.Context(), cfg, g); got != "" {
			t.Errorf("%s queued fallback: %s", kind, got)
		}
	}
	settleService(t, s)
}
