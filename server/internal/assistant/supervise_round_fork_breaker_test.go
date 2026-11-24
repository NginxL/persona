package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Forked background supervisor round sessions attested by the manager (via deliverRelay's returned session_id, persisted
// across restarts) inherit full assistant authorization and supervision tools (task_review, agent_send,
// supervision_finish, notify_user), even when their listed cwd drifted or when the background supervisor runs on a
// separate BackgroundSupervisorMachineID. Unrelated sessions on BackgroundSupervisorMachineID, headless sessions with
// spoofed ParentSessionID/RootSessionID linkage, host-pane sessions, and work machines remain strictly denied.
func TestBackgroundForkedRoundInheritsAuthAndSupervisionTools(t *testing.T) {
	s, d, f, _ := privacyService(t)
	st := &memStore{}
	s.store = func() Store { return st }
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	// Use "other" (neither MachineID nor InboxAgentMachine) as BackgroundSupervisorMachineID to verify that moving the
	// background supervisor there does not grant blanket machine authority to unrelated sessions on "other".
	setSettings(t, s, func(v *Settings) {
		v.MachineID = "server"
		v.Main = "conv1"
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "other"
		v.BackgroundSupervisorAt = &at
	})
	// Seed a supervised task owned by conv1 so task_review can be called by the background supervisor's forked round.
	todo := Todo{
		ID:     "todo-fork-1",
		Title:  "Investigate flaky check",
		Origin: "conv1",
		Agents: []TodoAgent{{MachineID: "twin", SessionID: "agent1"}},
		Supervision: &Supervision{
			Supervisor: "conv1",
			State:      "running",
			DoneWhen:   []string{"Root cause documented"},
		},
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[todo.ID] = &todo
	s.todos.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.hold["bg1"] = time.Now()
	s.supervise.mu.Unlock()

	// Manager-attested forked round id returned by the node on deliverRelay (claude --fork-session / round branch).
	const forkFromRelay = "bg1-round-fork-1"
	f.setSessions("other")
	f.mu.Lock()
	f.known["bg1"] = true
	f.assistantAnswer = func(a map[string]any) map[string]any {
		return map[string]any{"session_id": forkFromRelay, "context": "fresh"}
	}
	f.mu.Unlock()
	if err := s.deliverRelay(ctx, "other", "bg1", relayMsg{
		From: superviseTool,
		Tool: superviseTool,
		Text: prompts.SuperviseRoundMarker + "\n📋 督办：检查任务",
		At:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	const (
		spoofedParentOnOther  = "spoofed-parent-other"
		spoofedRootOnOther    = "spoofed-root-other"
		unrelatedOnOther      = "unrelated-agent-other"
		unrelatedAsstDirOther = "unrelated-asst-dir-other"
		paneOnOther           = "bg1-pane-spoof"
		spoofedParentOnServer = "spoofed-parent-server"
		spoofedRootOnServer   = "spoofed-root-server"
	)
	d.setSessions("other", []proto.Session{
		{SessionID: "bg1", MachineID: "other", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle},
		{SessionID: forkFromRelay, MachineID: "other", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: spoofedParentOnOther, MachineID: "other", ParentSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: spoofedRootOnOther, MachineID: "other", RootSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: unrelatedOnOther, MachineID: "other", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: unrelatedAsstDirOther, MachineID: "other", Cwd: "/home/u/.persona/assistant", Status: proto.StatusWorking},
		{SessionID: paneOnOther, MachineID: "other", ParentSessionID: "bg1", Cwd: "/home/u/.persona/assistant", PaneID: "pane-1", Status: proto.StatusWorking},
	})
	d.setSessions("server", []proto.Session{
		{SessionID: spoofedParentOnServer, MachineID: "server", ParentSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: spoofedRootOnServer, MachineID: "server", RootSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
	})
	d.setSessions("work", []proto.Session{
		{SessionID: forkFromRelay, MachineID: "work", ParentSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
	})
	s.setSessions("other", d.machines["other"].sessions)
	s.setSessions("server", d.machines["server"].sessions)

	// Verify both the exact background supervisor and the manager-attested forked round are authorized on "other".
	for _, sid := range []string{"bg1", forkFromRelay} {
		t.Run("authorized/"+sid, func(t *testing.T) {
			if !s.assistantCaller(ctx, "other", sid) {
				t.Fatalf("assistantCaller(other, %s) = false, want true", sid)
			}
			if !s.assistantDiscovery(ctx, "other", sid) {
				t.Fatalf("assistantDiscovery(other, %s) = false, want true", sid)
			}
			out := privacyRequest(t, s, "other", "mcp.tools", map[string]any{
				"session_id": sid,
				"assistant":  true,
				"tool_set":   toolSetSupervise,
			})
			var names []string
			for _, tool := range out.(map[string]any)["tools"].([]proto.Tool) {
				names = append(names, tool.Name)
			}
			for _, want := range []string{"task_review", "agent_send", "supervision_finish", "notify_user", "todo_get"} {
				if !slices.Contains(names, want) {
					t.Fatalf("supervise tool list for %s missing %q: %v", sid, want, names)
				}
			}
			for _, unwanted := range []string{"report_to_supervisor", "todo_decisions", "todo_decision_add", "todo_bind_thread"} {
				if slices.Contains(names, unwanted) {
					t.Fatalf("supervise tool list for %s downgraded to ordinary agent tool %q: %v", sid, unwanted, names)
				}
			}
		})
	}

	// Restart recovery: wiping in-memory fork state reloads the persisted manager-attested record from store.
	r := &s.background.rounds
	r.mu.Lock()
	r.forks = nil
	r.forksLoaded = false
	r.mu.Unlock()
	if base, ok := s.backgroundForkBase(ctx, "other", forkFromRelay); !ok || base != "bg1" {
		t.Fatalf("after simulated restart, backgroundForkBase(other, %s) = (%q, %v), want (bg1, true)", forkFromRelay, base, ok)
	}
	if !s.assistantCaller(ctx, "other", forkFromRelay) {
		t.Fatalf("after simulated restart, assistantCaller(other, %s) = false, want true", forkFromRelay)
	}

	// Regression (P1): BackgroundSupervisorMachineID does NOT grant blanket machine authority to unrelated or unknown
	// sessions, and unverified ParentSessionID/RootSessionID linkage NEVER grants assistant authorization (headless or
	// pane, on BackgroundSupervisorMachineID or MachineID).
	deniedCases := []struct {
		machine string
		session string
		desc    string
	}{
		{"other", unrelatedOnOther, "unrelated headless agent on BackgroundSupervisorMachineID"},
		{"other", unrelatedAsstDirOther, "unrelated session in assistant dir on BackgroundSupervisorMachineID"},
		{"other", "unknown-session-on-other", "unknown session on BackgroundSupervisorMachineID"},
		{"other", "", "empty session on BackgroundSupervisorMachineID"},
		{"other", paneOnOther, "host-pane session on BackgroundSupervisorMachineID"},
		{"other", spoofedParentOnOther, "headless session with spoofed ParentSessionID on BackgroundSupervisorMachineID"},
		{"other", spoofedRootOnOther, "headless session with spoofed RootSessionID on BackgroundSupervisorMachineID"},
		{"server", spoofedParentOnServer, "headless session with spoofed ParentSessionID on MachineID"},
		{"server", spoofedRootOnServer, "headless session with spoofed RootSessionID on MachineID"},
		{"work", forkFromRelay, "work machine claiming forked round id"},
	}
	for _, tc := range deniedCases {
		t.Run("denied/"+tc.desc, func(t *testing.T) {
			if s.assistantCaller(ctx, tc.machine, tc.session) {
				t.Fatalf("assistantCaller(%s, %q) = true, want false (%s)", tc.machine, tc.session, tc.desc)
			}
			if tc.session != "" {
				if _, ok := s.backgroundForkBase(ctx, tc.machine, tc.session); ok && tc.machine != "work" {
					t.Fatalf("backgroundForkBase(%s, %q) = true, want false (%s)", tc.machine, tc.session, tc.desc)
				}
				res := privacyCall(t, s, tc.machine, tc.session, "todo_list", true)
				if !res.IsError {
					t.Fatalf("mcp.call from %s (%s/%s) succeeded unexpectedly: %+v", tc.desc, tc.machine, tc.session, res)
				}
			}
		})
	}

	// Calling supervision_finish (silent before promotion) and task_review through mcp.call from the verified forked
	// round succeeds (no assistant_denied) and resolves the base supervisor's supervision hold.
	resFinish := privacyRequest(t, s, "other", "mcp.call", map[string]any{
		"session_id": forkFromRelay,
		"assistant":  true,
		"name":       "supervision_finish",
		"args":       map[string]any{"visibility": "silent", "summary": "Round finished"},
	}).(proto.ToolResult)
	if resFinish.IsError {
		t.Fatalf("supervision_finish from forked round failed: %+v", resFinish)
	}
	if len(resFinish.Content) == 0 || !strings.Contains(resFinish.Content[0].Text, `"level":"triage"`) {
		t.Fatalf("supervision_finish unexpected result: %+v", resFinish)
	}

	// Seed the durable evidence a delivered legacy notice records. A fork inherits only that base evidence; merely being
	// a new main-supervised task is not enough (#5732).
	s.rememberLegacyBackground("bg1", []superviseSignal{{Todo: todo.ID, Kind: sigReview, To: "bg1"}})
	resReview := privacyRequest(t, s, "other", "mcp.call", map[string]any{
		"session_id": forkFromRelay,
		"assistant":  true,
		"name":       "task_review",
		"args": map[string]any{
			"todo_id": todo.ID,
			"verdict": "accepted",
			"note":    "Verified",
			"checks":  completeReviewChecks(todoNow(s, todo.ID)),
		},
	}).(proto.ToolResult)
	if resReview.IsError {
		t.Fatalf("task_review from forked round failed: %+v", resReview)
	}
	if done := todoNow(s, todo.ID); done.DoneAt == nil {
		t.Fatalf("task_review from forked round did not accept todo: %+v", done)
	}
}

// Manager-attested forked background supervisor round branches stay hidden from the published session list and
// agents_list, while ordinary agents that merely carry ParentSessionID/RootSessionID pointing at the background
// supervisor are NOT promoted to Assistant=true and NOT hidden.
func TestBackgroundForkedRoundsExcludedFromAgentsList(t *testing.T) {
	s, d, f, _ := privacyService(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.MachineID = "server"
		v.Main = "conv1"
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "server"
		v.BackgroundSupervisorAt = &at
	})
	s.registerBackgroundRoundFork(ctx, "server", "bg1-fork-a", "bg1")
	now := time.Now()
	list := []proto.Session{
		{SessionID: "conv1", MachineID: "server", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle, Assistant: true, UpdatedAt: now},
		{SessionID: "bg1", MachineID: "server", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle, Assistant: true, UpdatedAt: now},
		{SessionID: "bg1-fork-a", MachineID: "server", Cwd: "/code/example", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: now},
		{SessionID: "agent-parent-linked", MachineID: "server", ParentSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking, State: proto.StateWorking, Title: "Parent Linked Agent", UpdatedAt: now},
		{SessionID: "agent-root-linked", MachineID: "server", RootSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking, State: proto.StateWorking, Title: "Root Linked Agent", UpdatedAt: now},
		{SessionID: "agent-real", MachineID: "server", Cwd: "/code/app", Status: proto.StatusWorking, State: proto.StateWorking, Title: "Real Agent", UpdatedAt: now},
	}
	d.setSessions("server", list)
	f.setSessions("server", list...)
	s.setSessions("server", list)

	// setSessions must NOT auto-register arbitrary parent/root-linked sessions as round forks.
	for _, ordinary := range []string{"agent-parent-linked", "agent-root-linked"} {
		if _, ok := s.backgroundForkBase(ctx, "server", ordinary); ok {
			t.Fatalf("setSessions promoted unverified parent/root-linked agent %q into round fork table", ordinary)
		}
	}

	published := s.publishList("server")
	byID := map[string]proto.Session{}
	for _, row := range published {
		byID[row.SessionID] = row
	}
	if _, leaked := byID["bg1-fork-a"]; leaked {
		t.Fatalf("publishList leaked verified forked round bg1-fork-a")
	}
	for _, kept := range []string{"conv1", "bg1", "agent-real", "agent-parent-linked", "agent-root-linked"} {
		row, ok := byID[kept]
		if !ok {
			t.Fatalf("publishList dropped %q", kept)
		}
		if (kept == "agent-parent-linked" || kept == "agent-root-linked") && row.Assistant {
			t.Fatalf("ordinary parent/root-linked agent %q was promoted to Assistant=true", kept)
		}
	}
	for _, ordinary := range []string{"agent-parent-linked", "agent-root-linked"} {
		row, ok := s.decoratedSession("server", ordinary)
		if !ok || row.Assistant {
			t.Fatalf("decoratedSession(%q) = (ok=%v, Assistant=%v), want (true, false)", ordinary, ok, row.Assistant)
		}
	}

	out, err := runJSON(t, s, "conv1", "agents_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "bg1-fork-a") {
		t.Fatalf("agents_list leaked verified forked round bg1-fork-a:\n%s", out)
	}
	if !strings.Contains(out, "agent-real") {
		t.Fatalf("agents_list missing real agent:\n%s", out)
	}
}

// setupAllStoppedBackground creates a background-supervisor test service with a task whose agent has been stopped for
// superviseAllStoppedAfter so sendDigest emits an all_stopped signal. Calling nextSignal shifts the agent's stop
// timestamp so allStoppedSignal produces a fresh valid key that passes checkedAllStopped.
func setupAllStoppedBackground(t *testing.T) (*Service, *fakeHub, Todo, string, func(step int) superviseSignal) {
	t.Helper()
	s, f, todo, _ := newRoundService(t)
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	bg := withBackgroundBG1(t, s, f)
	tNow := todoNow(s, todo.ID)
	tNow.Supervision.ExpectMinutes = 1440
	s.todos.mu.Lock()
	s.todos.todos[todo.ID] = &tNow
	s.todos.mu.Unlock()
	baseStop := time.Now().Add(-superviseAllStoppedAfter - 10*time.Minute)
	nextSignal := func(step int) superviseSignal {
		t.Helper()
		stoppedAt := baseStop.Add(time.Duration(step) * time.Minute)
		agent := proto.Session{
			MachineID:  "box",
			SessionID:  "s1",
			Status:     proto.StatusDone,
			State:      proto.StateUnread,
			StateSince: stoppedAt,
			UpdatedAt:  stoppedAt,
			LastText:   "Stopped waiting for review",
		}
		f.setSessions("box", agent)
		g, ok := allStoppedSignal(todoNow(s, todo.ID), nil, []proto.Session{agent}, time.Now())
		if !ok {
			t.Fatalf("step %d: fixture did not produce all_stopped signal", step)
		}
		return g
	}
	return s, f, tNow, bg, nextSignal
}

// If the background supervisor or a verified round fork hits assistant_denied, the circuit breaker trips immediately:
// the next all_stopped wake falls back once to the main conversation with a clear note instead of waking the background
// supervisor, and subsequent wakes within the backoff window are suppressed. An unrelated/unrecognized headless session
// denied during an active round must NOT trip the background breaker.
func TestBackgroundBreakerTripsOnAssistantDeniedAndFallsBackOnce(t *testing.T) {
	s, f, _, bg, nextSignal := setupAllStoppedBackground(t)
	ctx := context.Background()

	// Round 1 wakes the background supervisor on all_stopped.
	wakeRound(s, nextSignal(1))
	if got := len(assistantSendsTo(f, bg)); got != 1 {
		t.Fatalf("round 1 sends to bg = %d, want 1", got)
	}

	// Regression (P2): an unrelated/unrecognized session hitting assistant_denied on the same machine while a round is
	// active must NOT trip the global background breaker.
	s.noteAssistantDenied(ctx, "server", "unrelated-headless-agent", "task_review")
	r := &s.background.rounds
	r.mu.Lock()
	trippedByUnrelated := !r.breaker.authUntil.IsZero()
	r.mu.Unlock()
	if trippedByUnrelated {
		t.Fatal("unrelated headless session tripped background supervisor breaker")
	}

	// Simulate an assistant_denied failure on a verified round fork of the background supervisor.
	const verifiedFork = "bg1-verified-fork-denied"
	s.registerBackgroundRoundFork(ctx, "server", verifiedFork, bg)
	s.noteAssistantDenied(ctx, "server", verifiedFork, "task_review")

	// Round 2: background supervisor is tripped -> falls back ONCE to main conversation conv1 with breaker note.
	wakeRound(s, nextSignal(2))
	if got := len(assistantSendsTo(f, bg)); got != 1 {
		t.Fatalf("bg was woken again after assistant_denied: %d sends", got)
	}
	mainSends := assistantSendsTo(f, "conv1")
	if len(mainSends) != 1 {
		t.Fatalf("expected 1 fallback send to main conv1, got %d", len(mainSends))
	}
	if text, _ := mainSends[0]["text"].(string); !strings.Contains(text, "assistant_denied") || !strings.Contains(text, "task_review") {
		t.Fatalf("fallback message to main lacks assistant_denied explanation:\n%s", text)
	}

	// Round 3 within the backoff window: suppressed completely (neither bg nor conv1 is spammed).
	wakeRound(s, nextSignal(3))
	if got := len(assistantSendsTo(f, bg)); got != 1 {
		t.Fatalf("bg woken during backoff: %d sends", got)
	}
	if got := len(assistantSendsTo(f, "conv1")); got != 1 {
		t.Fatalf("main spammed during backoff: %d sends", got)
	}
}

// Two consecutive no-op background supervisor rounds on the same task's all_stopped signal trip the circuit breaker,
// falling back once to the main conversation and suppressing subsequent wakes during backoff. Real supervision
// progress (task_review / supervision_finish / supervisedWorking) resets the breaker.
func TestBackgroundBreakerTripsOnRepeatedNoOpAllStoppedAndResetsOnProgress(t *testing.T) {
	s, f, todo, bg, nextSignal := setupAllStoppedBackground(t)
	ctx := context.Background()

	// Round 1: wakes bg, finishes with zero progress tools called -> noOps=1 (not tripped yet).
	wakeRound(s, nextSignal(1))
	if got := len(assistantSendsTo(f, bg)); got != 1 {
		t.Fatalf("round 1 sends to bg = %d, want 1", got)
	}
	s.backgroundRoundTurnEnded(ctx, bg, time.Now())

	// Round 2: wakes bg again, finishes with zero progress -> noOps=2 -> trips breaker.
	wakeRound(s, nextSignal(2))
	if got := len(assistantSendsTo(f, bg)); got != 2 {
		t.Fatalf("round 2 sends to bg = %d, want 2", got)
	}
	s.backgroundRoundTurnEnded(ctx, bg, time.Now())

	// Round 3: breaker is open -> falls back ONCE to main conversation with a clear explanation; bg is NOT woken.
	wakeRound(s, nextSignal(3))
	if got := len(assistantSendsTo(f, bg)); got != 2 {
		t.Fatalf("bg woken after repeated no-op breaker tripped: %d sends", got)
	}
	mainSends := assistantSendsTo(f, "conv1")
	if len(mainSends) != 1 {
		t.Fatalf("expected 1 fallback send to main conv1, got %d", len(mainSends))
	}
	if text, _ := mainSends[0]["text"].(string); !strings.Contains(text, "repeated_noop") || !strings.Contains(text, "连续 2 轮无进展") {
		t.Fatalf("fallback message to main lacks repeated_noop explanation:\n%s", text)
	}

	// Round 4: still within backoff -> suppressed for both bg and conv1.
	wakeRound(s, nextSignal(4))
	if got := len(assistantSendsTo(f, bg)); got != 2 || len(assistantSendsTo(f, "conv1")) != 1 {
		t.Fatalf("woke during backoff: bg=%d conv1=%d", len(assistantSendsTo(f, bg)), len(assistantSendsTo(f, "conv1")))
	}

	// Progress resets the breaker: once the agent resumes work (supervisedWorking) or task_review succeeds,
	// a new all_stopped signal can wake bg again.
	s.resetBackgroundBreakerForTodo(todo.ID)
	wakeRound(s, nextSignal(5))
	if got := len(assistantSendsTo(f, bg)); got != 3 {
		t.Fatalf("after breaker reset, bg sends = %d, want 3", got)
	}

	// A round that calls a progress tool (supervision_finish) does NOT count as a no-op.
	s.noteBackgroundToolProgress(ctx, bg, "supervision_finish", json.RawMessage(`{}`))
	s.backgroundRoundTurnEnded(ctx, bg, time.Now())
	wakeRound(s, nextSignal(6))
	if got := len(assistantSendsTo(f, bg)); got != 4 {
		t.Fatalf("round after progress tool did not wake bg: %d", got)
	}
}

// If mcp.tools itself suffers an assistant-discovery denial for the current background supervisor or one of its
// verified round forks, the breaker trips even if the model never invokes an MCP tool and falls back to Bash.
// Unrelated terminal panes and unrelated headless task agents denied on the same machine during an active round must
// NOT trip the background supervisor's breaker.
func TestBackgroundBreakerTripsOnMCPToolsDiscoveryDowngrade(t *testing.T) {
	s, d, _, _ := privacyService(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.MachineID = "server"
		v.Main = "conv1"
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "server"
		v.BackgroundSupervisorAt = &at
	})
	const verifiedFork = "bg1-fork-verified"
	s.registerBackgroundRoundFork(ctx, "server", verifiedFork, "bg1")
	d.setSessions("server", []proto.Session{
		{SessionID: "bg1", MachineID: "server", Cwd: "/home/u/.persona/assistant", Status: proto.StatusWorking},
		{SessionID: verifiedFork, MachineID: "server", Cwd: "/code/example", Status: proto.StatusWorking},
		{SessionID: "pane-agent", MachineID: "server", Cwd: "/home/u/.persona/assistant", PaneID: "pane-1", Status: proto.StatusWorking},
		{SessionID: "unrelated-headless", MachineID: "server", ParentSessionID: "bg1", Cwd: "/code/example", Status: proto.StatusWorking},
	})
	s.backgroundRoundDelivered(ctx, "bg1", relayMsg{Tool: superviseTool}, time.Now())

	// Unrelated host panes and unrelated headless agents hitting mcp.tools denial must NOT trip the background breaker.
	for _, sid := range []string{"pane-agent", "unrelated-headless"} {
		_ = privacyTools(t, s, "server", sid, true)
		r := &s.background.rounds
		r.mu.Lock()
		tripped := !r.breaker.authUntil.IsZero()
		r.mu.Unlock()
		if tripped {
			t.Fatalf("unrelated session %q tripped background supervisor breaker", sid)
		}
	}

	// If the actual verified round fork hits mcp.tools discovery denial (e.g. its machine loses personal trust), the
	// breaker trips with lastDeniedTool == "mcp.tools".
	d.machines["server"].trust = "work"
	downgraded := privacyTools(t, s, "server", verifiedFork, true)
	if !slices.Contains(downgraded, "report_to_supervisor") {
		t.Fatalf("expected denied caller to receive agent tools, got %v", downgraded)
	}
	r := &s.background.rounds
	r.mu.Lock()
	tripped := !r.breaker.authUntil.IsZero() && r.breaker.lastDeniedTool == "mcp.tools"
	r.mu.Unlock()
	if !tripped {
		t.Fatal("mcp.tools discovery downgrade on verified background round fork did not trip breaker")
	}
}

// An outgrown legacy background supervisor moves its unread work to main without starting a replacement (#5732). Its
// attested fork and breaker state belong to the unchanged base and must survive both an unavailable start path and a
// later settings recovery; no new supervisor is created and no stale state is cleared merely to make a replacement clean.
func TestOutgrownBackgroundKeepsForkAndBreakerStateWithoutReplacement(t *testing.T) {
	s, f, _, bg, nextSignal := setupAllStoppedBackground(t)
	ctx := context.Background()

	const staleFork = "bg1-fork-stale"
	s.registerBackgroundRoundFork(ctx, "server", staleFork, bg)
	if !s.isBackgroundSession(ctx, staleFork) {
		t.Fatal("registered fork should be recognized before the legacy round outgrows its anchor")
	}

	wakeRound(s, nextSignal(1))
	s.backgroundRoundTurnEnded(ctx, bg, time.Now())
	wakeRound(s, nextSignal(2))
	s.backgroundRoundTurnEnded(ctx, bg, time.Now())
	s.backgroundRoundDelivered(ctx, bg, relayMsg{Tool: superviseTool}, time.Now())
	s.noteAssistantDenied(ctx, "server", staleFork, "task_review")
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})

	// An unavailable assistant start must not be used to start a replacement or wipe the unchanged base's state.
	f.mu.Lock()
	f.failAssistant = errors.New("start failed")
	f.mu.Unlock()
	if id, gone := s.ensureBackground(ctx); id != "" || gone != bg {
		t.Fatalf("outgrown legacy route returned (%q, %q), want no replacement and the old id to move", id, gone)
	}
	r := &s.background.rounds
	r.mu.Lock()
	keptForks := len(r.forks)
	keptByTodo := len(r.breaker.byTodo)
	keptAuthTrips := r.breaker.authTrips
	r.mu.Unlock()
	if keptForks != 1 || keptByTodo == 0 || keptAuthTrips == 0 {
		t.Fatalf("outgrown legacy route wiped state: forks=%d byTodo=%d authTrips=%d", keptForks, keptByTodo, keptAuthTrips)
	}

	// Recovery still does not create a replacement. The same base keeps its exact fork and breaker state.
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	if id, gone := s.ensureBackground(ctx); id != "" || gone != bg {
		t.Fatalf("recovered legacy route returned (%q, %q), want no replacement and the old id to move", id, gone)
	}
	r.mu.Lock()
	gotForks := len(r.forks)
	gotByTodo := len(r.breaker.byTodo)
	gotAuthTrips := r.breaker.authTrips
	r.mu.Unlock()
	if gotForks != keptForks || gotByTodo != keptByTodo || gotAuthTrips != keptAuthTrips {
		t.Fatalf("legacy base state changed without a replacement: forks=%d byTodo=%d authTrips=%d", gotForks, gotByTodo, gotAuthTrips)
	}
	r.mu.Lock()
	r.forks = nil
	r.forksLoaded = false
	r.mu.Unlock()
	if !s.isBackgroundSession(ctx, staleFork) {
		t.Fatal("the unchanged base lost its attested fork after reconstruction")
	}

	// The next wake follows the moved legacy work to main, not to a newly started background supervisor.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: bg, MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	beforeMain := len(assistantSendsTo(f, "conv1"))
	beforeBackground := len(assistantSendsTo(f, bg))
	wakeRound(s, nextSignal(3))
	if got := len(assistantSendsTo(f, "conv1")); got != beforeMain+1 {
		t.Fatalf("main sends after moving the outgrown legacy work = %d, want %d", got, beforeMain+1)
	}
	if got := len(assistantSendsTo(f, bg)); got != beforeBackground {
		t.Fatalf("outgrown background received %d new sends, want none", got-beforeBackground)
	}
}
