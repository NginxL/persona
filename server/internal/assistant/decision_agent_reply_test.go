package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

func decisionAgentReplyService(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	return s, f, todo
}

func TestUserSpokePausesPendingAgentDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	sent := len(sentToAgent(f))
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	if got := askOf(s, todo.ID); got.AgentReply == nil || got.DueAt.IsZero() {
		t.Fatalf("the owner message left the automatic default enabled: %+v", got)
	}
	waitFor(t, "the supervisor handoff", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && a.AgentReply != nil && !a.AgentReply.ToldAt.IsZero()
	})
	got := askOf(s, todo.ID)
	if got == nil || got.AgentReply == nil || !got.DueAt.After(got.AskedAt) || got.AgentReply.MachineID != a.MachineID ||
		got.AgentReply.SessionID != a.SessionID || got.AgentReply.At.IsZero() || got.AgentReply.ToldAt.IsZero() {
		t.Fatalf("reply was not recorded and paused: %+v", got)
	}
	if !got.AcceptedAt.IsZero() || got.ByDefault || got.Chosen != "" || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("the answer was guessed or the ask ended: ask %+v state %q", got, todoNow(s, todo.ID).Supervision.State)
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
	if result, err := s.acceptTryAs(context.Background(), todo.ID, "", true); err != nil || result.taken {
		t.Fatalf("an already-collected deadline took the default: %+v %v", result, err)
	}
	if got := sentToAgent(f)[sent:]; len(got) != 0 || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("sweep chose an option or ended the ask: sent %q, state %q", got, todoNow(s, todo.ID).Supervision.State)
	}
	waitFor(t, "the rendered decision directive", func() bool {
		return strings.Contains(strings.Join(f.notices(), "\n"), "task_decided")
	})
}

func TestUserPromptSubmitPausesPendingAgentDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	waitFor(t, "the decision hook to pause", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && got.AgentReply.MachineID == a.MachineID && got.AgentReply.SessionID == a.SessionID && !got.DueAt.IsZero()
	})
	waitFor(t, "the decision reply signal", func() bool { return signalled(s, f, todo.ID, sigDecisionReply, "task_decided") })
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser || askOf(s, todo.ID).Chosen != "" {
		t.Fatal("hook guessed an option or ended the pending decision")
	}
}

// An accept holds its todo's action lock across session.send. Its own UserPromptSubmit must not wait for that lock on
// the manager's read loop: the event returns now and the ordered decision work runs once the send has completed.
func TestDecisionPromptDoesNotBlockOnAcceptActionLock(t *testing.T) {
	s, _, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	s.reviewDismissals.actions.acquire(todo.ID)
	locked := true
	defer func() {
		if locked {
			s.reviewDismissals.actions.release(todo.ID)
		}
	}()
	answered := make(chan bool, 1)
	go func() { answered <- s.NodeEvent(a.MachineID, "hook.event", params) }()
	select {
	case accepted := <-answered:
		if !accepted {
			t.Fatal("hook event was not accepted")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("NodeEvent waited for the accept action lock on the read loop")
	}
	s.reviewDismissals.actions.release(todo.ID)
	locked = false
	waitFor(t, "the ordered decision hook", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
}

func TestDecisionAgentReplyIgnoresUntrustedOrStaleEvents(t *testing.T) {
	for _, tc := range []struct {
		name, machine, session, event, source string
		spooledOld                            bool
	}{
		{name: "unrelated session", machine: "box", session: "another-session", event: "UserPromptSubmit", source: "user"},
		{name: "wrong machine", machine: "another-machine", session: "s1", event: "UserPromptSubmit", source: "user"},
		{name: "missing source", machine: "box", session: "s1", event: "UserPromptSubmit"},
		{name: "task source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "task"},
		{name: "system source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "system"},
		{name: "relay source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "relay"},
		{name: "stop event", machine: "box", session: "s1", event: "Stop", source: "user"},
		{name: "old replay", machine: "box", session: "s1", event: "UserPromptSubmit", source: "user", spooledOld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			a := askOf(s, todo.ID)
			if tc.machine == "box" && tc.session == "s1" {
				tc.machine, tc.session = a.MachineID, a.SessionID
			}
			p := map[string]any{"session_id": tc.session, "event": tc.event}
			if tc.source != "" {
				p["prompt_source"] = tc.source
			}
			if tc.spooledOld {
				p["spooled_at"] = a.AskedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
			}
			params, _ := json.Marshal(p)
			s.NodeEvent(tc.machine, "hook.event", params)
			if tc.name == "unrelated session" {
				s.UserSpoke(context.Background(), SessionRef{MachineID: tc.machine, SessionID: tc.session})
			}
			if tc.name == "wrong machine" {
				s.UserSpoke(context.Background(), SessionRef{MachineID: tc.machine, SessionID: a.SessionID})
			}
			if got := askOf(s, todo.ID); got == nil || !got.DueAt.IsZero() || got.AgentReply != nil ||
				strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) ||
				signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
				t.Fatalf("ignored event changed the decision: ask %+v", got)
			}
		})
	}
}

func TestDecisionAgentReplyDoesNotAffectAnsweredOrNextRound(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	sent := len(sentToAgent(f))
	if _, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": "Choose the full rollout.", "told_agent": true}); isErr {
		t.Fatal("explicit answer was rejected")
	}
	if got := askOf(s, todo.ID); got == nil || got.AgentReply == nil || got.AgentReply.At.IsZero() || got.AcceptedAt.IsZero() {
		t.Fatalf("answered decision was not retained: %+v", got)
	}
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("explicitly told answer was sent again: %q", got)
	}

	s2, f2, todo2 := newSupervisedService(t)
	waitsForReview(t, s2, f2, todo2.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s2, todo2.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	a2 := askOf(s2, todo2.ID)
	s2.UserSpoke(context.Background(), SessionRef{MachineID: a2.MachineID, SessionID: a2.SessionID})
	if _, err := reviewErr(s2, todo2.ID, map[string]any{"verdict": "rework", "note": "Revise the rollout plan."}); err != nil {
		t.Fatal(err)
	}
	waitsForReview(t, s2, f2, todo2.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s2, todo2.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if got := askOf(s2, todo2.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.AskedAt.Equal(a2.AskedAt) {
		t.Fatalf("previous round's pause affected the new round: %+v", got)
	}
	sent = len(sentToAgent(f2))
	pastDeadline(t, s2, todo2.ID)
	s2.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f2)[sent:]; len(got) != 1 || !askOf(s2, todo2.ID).ByDefault {
		t.Fatalf("new round lost its independent default: sent %q ask %+v", got, askOf(s2, todo2.ID))
	}
}

func TestGoldenDecisionAgentReply(t *testing.T) {
	for _, language := range []struct{ locale, name string }{
		{"en", "English"}, {"zh-Hans", ""}, {"ja", "Japanese"},
	} {
		t.Run(language.locale, func(t *testing.T) {
			golden(t, "supervise.agent_reply."+language.locale, prompts.RenderSupervise(prompts.Supervise{
				Entries: []prompts.SuperviseEntry{{ID: "todo_example", Title: "Example task", Kind: sigDecisionReply,
					AgentReply: &prompts.DecisionAgentReply{Language: language.name,
						Machine: "box", Session: "agent-session"}}},
			}))
		})
	}
}

func TestDecisionAgentReplyPauseSurvivesReload(t *testing.T) {
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	waitsForReview(t, s, f, todo.ID, "Choose a rollout option.")
	reviewTask(t, s, todo.ID, rolloutDecision())
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	a := askOf(s, todo.ID)
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	s.supervise.dedupMu.Lock()
	s.supervise.mu.Lock()
	if s.supervise.timer != nil {
		s.supervise.timer.Stop()
		s.supervise.timer, s.supervise.timerAt = nil, time.Time{}
	}
	s.supervise.pending = map[string]superviseSignal{}
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Unlock()
	var stored map[string]*acceptAsk
	if _, err := database.Setting(context.Background(), acceptanceKey, &stored); err != nil {
		t.Fatal(err)
	}
	paused := stored[todo.ID]
	if paused == nil || paused.AgentReply == nil || !paused.AgentReply.ToldAt.IsZero() || paused.DueAt.IsZero() || !paused.AcceptedAt.IsZero() {
		t.Fatalf("pause not persisted: %+v", paused)
	}
	reloaded, nextHub := newQuietServiceOn(t, database)
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
	waitFor(t, "the recovered supervisor handoff", func() bool {
		a, ok := reloaded.askCopy(todo.ID)
		return ok && a.AgentReply != nil && !a.AgentReply.ToldAt.IsZero()
	})
	again, ok := reloaded.askCopy(todo.ID)
	if !ok || again.AgentReply == nil || again.DueAt.IsZero() || !again.AcceptedAt.IsZero() || again.ByDefault {
		t.Fatalf("reloaded decision lost the pause: %+v", again)
	}
	if result, err := reloaded.acceptTryAs(context.Background(), todo.ID, "", true); err != nil || result.taken {
		t.Fatalf("reloaded default was taken: %+v %v", result, err)
	}
	if got := sentToAgent(nextHub); len(got) != 0 {
		t.Fatalf("reloaded decision sent a guessed answer: %q", got)
	}
	if decisions, err := database.ListTodoDecisions(context.Background(), todo.ID, true); err != nil || len(decisions) != 0 {
		t.Fatalf("free text was recorded as an inferred choice: %+v %v", decisions, err)
	}
}
