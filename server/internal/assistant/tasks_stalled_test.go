package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// stalledTaskFixture is a supervised task whose Persona-started terminal Agent s1 begins a no-progress stretch at
// base. The explicit supervision timestamp keeps supervisor action and session activity on the same fake clock.
func stalledTaskFixture(t *testing.T, expect int) (*Service, *fakeHub, Todo, proto.Session, time.Time) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.ExpectMinutes = expect
		return true
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.UpdatedAt = base
	s.todos.mu.Unlock()
	todo = todoNow(s, todo.ID)
	agent := proto.Session{
		SessionID: todo.Agents[0].SessionID, MachineID: "box", Host: proto.EngineHerdr, PaneID: "w1:p1",
		Cwd: "/code/app", WorkspaceCwd: "/code/app", WorkspaceID: "workspace-1", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: base, StartedByPersona: true,
	}
	f.setSessions("box", agent)
	return s, f, todo, agent, base
}

// stalledPolling is agent at minute i of a stretch of sleeps and repeated status checks. Its transcript keeps moving,
// but none of this is real progress.
func stalledPolling(agent proto.Session, base time.Time, i int) proto.Session {
	at := base.Add(time.Duration(i) * time.Minute)
	agent.UpdatedAt, agent.ActivityAt = at, &at
	agent.Activity = "在等待" // i18n-ignore: the node's activity line for a sleep (transcript.Activity)
	if i%2 == 1 {
		agent.Activity = "在跑 ./scripts/status.sh" // i18n-ignore: the node's activity line for a shell command
	}
	return agent
}

func stalledTimeoutNotices(f *fakeHub, id string) (out []string) {
	for _, notice := range f.notices() {
		if strings.Contains(notice, id) && strings.Contains(notice, "signal=timed_out") {
			out = append(out, notice)
		}
	}
	return out
}

func stalledStopIDs(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stopped)
}

// A terminal Agent stops once at max(60 minutes, 3 × expect), never just before it. Its open task, rather than a
// completed one, carries the public timed_out state, and the same stretch cannot stop it again on a later pass.
func TestTaskStalledClosesTerminalAtThreshold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect int
		after  time.Duration
	}{
		{"one-hour floor", 0, time.Hour},
		{"estimate longer than floor", 30, 90 * time.Minute},
		{"long estimate", 90, 270 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, agent, base := stalledTaskFixture(t, tc.expect)
			ctx := context.Background()
			s.superviseWatch(ctx, base)
			s.superviseWatch(ctx, base.Add(tc.after-time.Second))
			if calls := retireCalls(f); len(calls) != 0 {
				t.Fatalf("stopped before %v: %v", tc.after, calls)
			}

			s.superviseWatch(ctx, base.Add(tc.after))
			calls := retireCalls(f)
			if len(calls) != 1 || !strings.HasPrefix(calls[0], deviceSessionStop+" ") || strings.Contains(calls[0], "retire=true") {
				t.Fatalf("at %v: stop calls = %v, want one terminal-retaining session.stop", tc.after, calls)
			}
			if stopped := stalledStopIDs(f); len(stopped) != 1 || stopped[0] != agent.SessionID {
				t.Fatalf("stopped Agents = %v, want only %s", stopped, agent.SessionID)
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt != nil || got.Supervision == nil || got.Supervision.state(got) != "timed_out" || taskState(got, nil) != "timed_out" {
				t.Fatalf("the open task is not marked timed_out: %+v; supervision: %+v; public state: %q", got, got.Supervision, taskState(got, nil))
			}

			s.superviseWatch(ctx, base.Add(tc.after+time.Hour))
			if calls := retireCalls(f); len(calls) != 1 {
				t.Fatalf("a later pass stopped the same stretch again: %v", calls)
			}
		})
	}
}

// Real progress and a supervisor message each begin a fresh full timeout stretch. Merely sleeping and polling keeps
// the old one running even though UpdatedAt and ActivityAt advance every minute.
func TestTaskStalledProgressAndSupervisorActionResetClock(t *testing.T) {
	for _, tc := range []struct {
		name string
		at20 func(*Service, *proto.Session, time.Time)
		stop int
	}{
		{"real progress", func(_ *Service, agent *proto.Session, _ time.Time) {
			agent.Activity = "在改 login.swift" // i18n-ignore: node activity for an edit
		}, 80},
		{"supervisor action", func(s *Service, agent *proto.Session, now time.Time) {
			s.progressMade(agent.MachineID, agent.SessionID, now)
		}, 80},
		{"only waits and polls", nil, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, agent, base := stalledTaskFixture(t, 0)
			ctx := context.Background()
			for i := 0; i <= tc.stop+1; i++ {
				now := base.Add(time.Duration(i) * time.Minute)
				row := stalledPolling(agent, base, i)
				if i == 20 && tc.at20 != nil {
					tc.at20(s, &row, now)
				}
				f.setSessions("box", row)
				s.superviseWatch(ctx, now)
				calls := retireCalls(f)
				switch {
				case i < tc.stop && len(calls) != 0:
					t.Fatalf("minute %d: stopped before a fresh full threshold: %v", i, calls)
				case i == tc.stop && len(calls) != 1:
					t.Fatalf("minute %d: stop calls = %v, want the first stop at the threshold", i, calls)
				case i > tc.stop && len(calls) != 1:
					t.Fatalf("minute %d: repeated stop: %v", i, calls)
				}
			}
		})
	}
}

// Timeout keeps the task's only Agent link and the node's transcript/workspace. Retry continues that same session;
// it does not release the link, start a replacement Agent, archive the session, or remove its workspace.
func TestTaskStalledContinuationResumesOriginalSession(t *testing.T) {
	s, f, todo, agent, base := stalledTaskFixture(t, 0)
	// The production transcript reader requires a full session ID, unlike the short IDs sufficient for node-call fakes.
	s.todos.mu.Lock()
	delete(s.todos.activeBySession, [2]string{"box", agent.SessionID})
	agent.SessionID = "agent-0001"
	s.todos.todos[todo.ID].Agents[0].SessionID = agent.SessionID
	s.todos.activeBySession[[2]string{"box", agent.SessionID}] = todo.ID
	s.todos.mu.Unlock()
	todo = todoNow(s, todo.ID)
	f.setSessions("box", agent)
	ctx := context.Background()
	s.superviseWatch(ctx, base)
	s.superviseWatch(ctx, base.Add(time.Hour))
	if calls := retireCalls(f); len(calls) != 1 {
		t.Fatalf("timeout did not stop the original Agent: %v", calls)
	}
	original := todoNow(s, todo.ID)
	if len(original.Agents) != 1 || original.Agents[0].SessionID != agent.SessionID || original.Agents[0].Cwd != agent.Cwd ||
		original.Agents[0].ReleasedAt != nil || original.Agents[0].DismissedAt != nil {
		t.Fatalf("timeout released or replaced the original Agent link: %+v", original.Agents)
	}

	stopped := agent
	stopped.Status, stopped.State = proto.StatusOffline, proto.StateIdle
	stopped.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: proto.StopUser}
	stopped.ReadOnlyReason = "stopped"
	f.setSessions("box", stopped)
	f.setItems(proto.Item{Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "the original transcript and diagnosis"})
	resumingNode(f)
	before := len(startCalls(f))
	if _, err := s.retryTask(ctx, todo.ID); err != nil {
		t.Fatalf("continue timed-out task: %v", err)
	}
	waitFor(t, "the original session's resume", func() bool { return len(startCalls(f)) > before })
	starts := startCalls(f)
	if len(starts) != before+1 || starts[before]["resume_session_id"] != agent.SessionID || starts[before]["resume_automatic"] == true {
		t.Fatalf("continuation did not resume %s exactly once through the user's Resume: %v", agent.SessionID, starts[before:])
	}

	got := todoNow(s, todo.ID)
	if todoCount(s) != 1 || len(got.Agents) != 1 || got.Agents[0].SessionID != agent.SessionID || got.Agents[0].Cwd != agent.Cwd ||
		got.Agents[0].ReleasedAt != nil || got.Agents[0].DismissedAt != nil {
		t.Fatalf("continuation replaced the task or its Agent: count=%d task=%+v", todoCount(s), got)
	}
	read, err := s.toolAgentRead(ctx, "box", agent.SessionID, 1, s.agentCallerSpace(ctx, "box", "conv1"))
	if err != nil || !strings.Contains(read, "the original transcript and diagnosis") {
		t.Fatalf("the original transcript was not retained: %q, %v", read, err)
	}
	f.mu.Lock()
	calls := append([]DeviceCall(nil), f.calls...)
	f.mu.Unlock()
	for _, call := range calls {
		switch call.Method {
		case deviceSessionRetire, "session.close", deviceSessionArchive, deviceWorkspaceClose, "worktree.remove":
			t.Fatalf("timeout or continuation discarded retained state with %s", call.Method)
		}
	}
}

// A successful stop produces one timeout entry through the ordinary supervision digest, never one per watch pass.
func TestTaskStalledTimeoutNotifiesSupervisorOnce(t *testing.T) {
	s, f, todo, _, base := stalledTaskFixture(t, 0)
	ctx := context.Background()
	s.superviseWatch(ctx, base)
	s.superviseWatch(ctx, base.Add(time.Hour))
	if calls := retireCalls(f); len(calls) != 1 {
		t.Fatalf("timeout did not stop the Agent: %v", calls)
	}
	waitFor(t, "the supervisor's timeout notification", func() bool { return len(stalledTimeoutNotices(f, todo.ID)) == 1 })

	s.superviseWatch(ctx, base.Add(2*time.Hour))
	seen := supervisionOf(s, f, todo.ID)
	if slices.Contains(seen.pending, "timed_out") || len(stalledTimeoutNotices(f, todo.ID)) != 1 {
		t.Fatalf("timeout notification was repeated: pending=%v notices=%v", seen.pending, stalledTimeoutNotices(f, todo.ID))
	}
}

func TestTaskStalledTimeoutRendered(t *testing.T) {
	e := prompts.SuperviseEntry{ID: "todo_1", Title: "Fix the stall", Kind: sigTimedOut}
	if got := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}}); !strings.Contains(got, "Agent 已停止") ||
		!strings.Contains(got, "原会话和工作区保留") {
		t.Fatalf("supervise:\n%s", got)
	}
	if got := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{e}); !strings.Contains(got, "the Agent was stopped") ||
		!strings.Contains(got, "session and workspace are retained") {
		t.Fatalf("worker supervise:\n%s", got)
	}
}

// A node refusal leaves the task open and actively supervised: no timed_out marker and no notification that claims a
// successful closure. The attempted stop itself is visible in the fake hub's calls.
func TestTaskStalledStopFailureDoesNotMarkTimedOut(t *testing.T) {
	s, f, todo, _, base := stalledTaskFixture(t, 0)
	f.retireErr = errors.New("node refused stop")
	ctx := context.Background()
	s.superviseWatch(ctx, base)
	s.superviseWatch(ctx, base.Add(time.Hour))
	if calls := retireCalls(f); len(calls) != 1 {
		t.Fatalf("the threshold did not attempt one stop: %v", calls)
	}
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision == nil || got.Supervision.state(got) == "timed_out" || taskState(got, nil) == "timed_out" {
		t.Fatalf("failed stop marked the task timed out: %+v", got)
	}
	seen := supervisionOf(s, f, todo.ID)
	if slices.Contains(seen.pending, "timed_out") || seen.told[todo.ID+"|timed_out"] != "" || len(stalledTimeoutNotices(f, todo.ID)) != 0 {
		t.Fatalf("failed stop sent a successful timeout notice: pending=%v told=%v notices=%v", seen.pending, seen.told, stalledTimeoutNotices(f, todo.ID))
	}
}

func TestTaskStalledPollingStopFailureRetriesWithoutReset(t *testing.T) {
	s, f, todo, agent, base := stalledTaskFixture(t, 0)
	ctx := context.Background()
	for i := 0; i <= 61; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		f.setSessions("box", stalledPolling(agent, base, i))
		if i == 60 {
			f.retireErr = errors.New("transient stop failure")
		} else if i == 61 {
			f.retireErr = nil
		}
		s.superviseWatch(ctx, now)
		calls := retireCalls(f)
		switch i {
		case 60:
			if len(calls) != 1 {
				t.Fatalf("minute %d: first stop calls = %v, want one", i, calls)
			}
			if got := todoNow(s, todo.ID); got.Supervision.state(got) != "timeout_closing" {
				t.Fatalf("failed stop did not retain its close intent: %+v", got.Supervision)
			}
		case 61:
			if len(calls) != 2 {
				t.Fatalf("minute %d: stop calls = %v, polling restarted the full timeout", i, calls)
			}
			if got := todoNow(s, todo.ID); got.Supervision.state(got) != "timed_out" {
				t.Fatalf("successful retry did not mark the task timed out: %+v", got.Supervision)
			}
		}
	}
}

// Runtime-owned sessions retain the existing stalled reminder, but this mechanism neither stops them nor marks their
// tasks timed out.
func TestTaskStalledRuntimeOnlyReminds(t *testing.T) {
	s, f, todo, agent, base := stalledTaskFixture(t, 0)
	agent.Host, agent.SourceKind, agent.SourceInstanceID = proto.EnginePaseo, proto.SourceRuntime, "paseo"
	f.setSessions("box", agent)
	ctx := context.Background()
	s.superviseWatch(ctx, base)
	s.superviseWatch(ctx, base.Add(time.Hour))
	if calls := retireCalls(f); len(calls) != 0 {
		t.Fatalf("runtime-owned session was stopped: %v", calls)
	}
	got := todoNow(s, todo.ID)
	if got.Supervision == nil || got.Supervision.state(got) == "timed_out" || taskState(got, nil) == "timed_out" {
		t.Fatalf("runtime-owned session's task was marked timed out: %+v", got)
	}
	waitFor(t, "the runtime session's existing stalled reminder", func() bool {
		for _, notice := range f.notices() {
			if strings.Contains(notice, todo.ID) && strings.Contains(notice, "signal=stalled") {
				return true
			}
		}
		return false
	})
}

// Review, owner input and intentional parking hold closure. Sessions Persona does not own, assistant conversations,
// and sessions outside a Persona terminal are never this mechanism's stop targets.
func TestTaskStalledRegressionExclusions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		edit  func(*proto.Session)
	}{
		{"waiting for review", reviewPending, nil},
		{"waiting for owner input", reviewNeedsUser, nil},
		{"waiting for personal verification", reviewUserCheck, nil},
		{"intentionally parked", reviewParked, nil},
		{"assistant conversation", "", func(agent *proto.Session) { agent.Assistant = true }},
		{"not started by Persona", "", func(agent *proto.Session) { agent.StartedByPersona = false }},
		{"outside a terminal", "", func(agent *proto.Session) { agent.PaneID = "" }},
		{"another tool's session", "", func(agent *proto.Session) { agent.SourceKind = "codex_app_server" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, agent, base := stalledTaskFixture(t, 0)
			ctx := context.Background()
			switch tc.state {
			case reviewPending, reviewNeedsUser, reviewUserCheck:
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = tc.state; return true }); err != nil {
					t.Fatal(err)
				}
			case reviewParked:
				seedLegacyPark(t, s, todo.ID, "wait for the vendor's scheduled answer", parkSomeoneElse, "3d")
			}
			if tc.edit != nil {
				tc.edit(&agent)
				f.setSessions("box", agent)
			}
			s.superviseWatch(ctx, base)
			s.superviseWatch(ctx, base.Add(2*time.Hour))
			if calls := retireCalls(f); len(calls) != 0 {
				t.Fatalf("excluded session was stopped: %v", calls)
			}
			got := todoNow(s, todo.ID)
			if got.Supervision == nil || got.Supervision.state(got) == "timed_out" || taskState(got, nil) == "timed_out" {
				t.Fatalf("excluded task was marked timed out: %+v", got)
			}
		})
	}
}
