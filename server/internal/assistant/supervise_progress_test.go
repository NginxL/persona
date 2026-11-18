package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// #10089: an agent that only sleeps and checks a status keeps its session moving, so `stalled` never comes; the
// no_progress signal reads what moved it.

// spinning is a working agent's session at minute i of a loop: its transcript written just now, its latest tool call
// a sleep (even minutes) or the same status check (odd minutes), started just now.
func spinning(base time.Time, i int, activity ...string) proto.Session {
	at := base.Add(time.Duration(i) * time.Minute)
	line := "在等待" // i18n-ignore: the node's activity line for a sleep (transcript.Activity)
	if i%2 == 1 {
		line = "在跑 ./scripts/status.sh" // i18n-ignore: the node's activity line for a shell command
	}
	if len(activity) > 0 {
		line = activity[0]
	}
	return proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking,
		State: proto.StateWorking, UpdatedAt: at, Activity: line, ActivityAt: &at}
}

func kindsOf(list []superviseSignal) []string {
	var out []string
	for _, g := range list {
		out = append(out, g.Kind)
	}
	return out
}

// The regression: a session that keeps writing while its agent only sleeps and checks a status is no_progress once
// max(30 minutes, 1.5 × expect) passed, never stalled; the same stretch keeps one key.
func TestNoProgressWhileOnlySleepingAndPolling(t *testing.T) {
	s := &Service{}
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	task := Todo{ID: "t", Supervision: &Supervision{Supervisor: "main", ExpectMinutes: 10}} // 30 minutes
	var first superviseSignal
	for i := 0; i <= 40; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		c := spinning(base, i)
		if got := kindsOf(watchSignals(task, []proto.Session{c}, now)); len(got) != 0 {
			t.Fatalf("minute %d: watchSignals = %v; the session moves, so it is not stalled", i, got)
		}
		g, ok := s.noProgressSignal(task, c, now)
		switch {
		case i < 30 && ok:
			t.Fatalf("minute %d: no_progress before 30 minutes: %+v", i, g)
		case i >= 30 && !ok:
			t.Fatalf("minute %d: no no_progress after %d minutes of sleeping and polling", i, i)
		case ok && first.Key == "":
			first = g
		case ok && g.Key != first.Key:
			t.Fatalf("minute %d: the same stretch changed its key: %q, then %q", i, first.Key, g.Key)
		}
	}
	if first.Kind != sigNoProgress || first.Minutes != 30 || first.Calls != 31 || first.Waits != 30 ||
		!strings.Contains(first.Detail, "./scripts/status.sh") || !strings.Contains(first.Detail, "在等待") {
		t.Fatalf("the signal = %+v", first)
	}
	if !s.noProgressHolds(first) {
		t.Fatal("the stretch it names should still hold")
	}
	s.progressMade("box", "s1", base.Add(41*time.Minute)) // its supervisor told it something
	if s.noProgressHolds(first) {
		t.Fatal("a message to the agent should end the stretch the signal names: it is dropped before delivery")
	}
}

// Real progress starts a new stretch; reading, searching and testing different things is not spinning; an agent that
// went silent is stalled's, not this signal's.
func TestNoProgressNotRaisedOnRealProgress(t *testing.T) {
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	task := Todo{ID: "t", Supervision: &Supervision{Supervisor: "main"}}
	type step func(s *Service, now time.Time, c *proto.Session)
	cases := []struct {
		name string
		at20 step // what happens at minute 20
		want bool // a signal at minute 40 (10 minutes past the 30 from the start, 20 from minute 20)
	}{
		{"nothing", nil, true},
		{"an edit", func(_ *Service, _ time.Time, c *proto.Session) { c.Activity = "在改 main.go" }, false}, // i18n-ignore: node activity
		{"a commit", func(_ *Service, _ time.Time, c *proto.Session) { c.Activity = "在提交代码" }, false},     // i18n-ignore: node activity
		{"a report with evidence", func(s *Service, now time.Time, _ *proto.Session) { s.progressReported("box", "s1", 1, 0, now) }, false},
		{"a report naming a later step", func(s *Service, now time.Time, _ *proto.Session) { s.progressReported("box", "s1", 0, 2, now) }, false},
		{"a report with nothing in it", func(s *Service, now time.Time, _ *proto.Session) { s.progressReported("box", "s1", 0, 0, now) }, true},
		{"a criterion reading changed", func(s *Service, now time.Time, _ *proto.Session) { s.progressMade("box", "s1", now) }, false},
		{"a plan step done", func(_ *Service, _ time.Time, c *proto.Session) { c.Plan = &proto.PlanInfo{Done: 2, Total: 4} }, false},
	}
	for _, tc := range cases {
		s := &Service{}
		raised := false
		for i := 0; i <= 40; i++ {
			now := base.Add(time.Duration(i) * time.Minute)
			c := spinning(base, i)
			c.Plan = &proto.PlanInfo{Done: 1, Total: 4}
			if i == 20 && tc.at20 != nil {
				tc.at20(s, now, &c)
			}
			if i > 20 && c.Plan != nil && tc.name == "a plan step done" {
				c.Plan = &proto.PlanInfo{Done: 2, Total: 4}
			}
			_, ok := s.noProgressSignal(task, c, now)
			raised = raised || ok
		}
		if raised != tc.want {
			t.Errorf("%s: raised = %v, want %v", tc.name, raised, tc.want)
		}
	}

	// Investigating: a different file or test every minute, for an hour.
	s := &Service{}
	for i := 0; i <= 60; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		lines := []string{"在读 f" + string(rune('a'+i%26)) + ".go", "在搜 handler" + string(rune('a'+i%26)), "在跑测试"} // i18n-ignore: node activity
		if g, ok := s.noProgressSignal(task, spinning(base, i, lines[i%3]), now); ok {
			t.Fatalf("minute %d: varied investigation raised %+v", i, g)
		}
	}

	// Silent: its last call was long ago; stalled says so if the transcript stops too.
	s = &Service{}
	for i := 0; i <= 10; i++ {
		s.noProgressSignal(task, spinning(base, i), base.Add(time.Duration(i)*time.Minute))
	}
	quiet := spinning(base, 10)
	if g, ok := s.noProgressSignal(task, quiet, base.Add(2*time.Hour)); ok {
		t.Fatalf("an agent whose last call is two hours old raised %+v", g)
	}

	// Not working (stopped, waiting for the owner): nothing.
	stopped := spinning(base, 0)
	stopped.State = proto.StateIdle
	if _, ok := (&Service{}).noProgressSignal(task, stopped, base.Add(time.Hour)); ok {
		t.Fatal("a stopped agent raised no_progress")
	}
}

// Through the hub's supervision loop: a supervised task's agent that only sleeps and polls reaches its supervisor as
// no_progress (not stalled) once the threshold passed, in one message; an edit midway puts it off.
func TestNoProgressReachesTheSupervisor(t *testing.T) {
	for _, edit := range []bool{false, true} {
		s, f, todo := newSupervisedService(t) // expect 30: max(30, 45) = 45 minutes
		ctx := context.Background()
		base := time.Now()
		raisedAt := -1
		for i := 0; i <= 60 && raisedAt < 0; i++ {
			now := base.Add(time.Duration(i) * time.Minute)
			c := spinning(base, i)
			if edit && i == 20 {
				c.Activity = "在改 login.swift" // i18n-ignore: node activity
			}
			f.setSessions("box", c)
			s.superviseWatch(ctx, now)
			seen := supervisionOf(s, f, todo.ID)
			if slices.Contains(seen.pending, sigStalled) || seen.told[todo.ID+"|"+sigStalled] != "" {
				t.Fatalf("edit=%v minute %d: stalled for a session that moves", edit, i)
			}
			if slices.Contains(seen.pending, sigNoProgress) || seen.told[todo.ID+"|"+sigNoProgress] != "" {
				raisedAt = i
			}
		}
		want := 45
		if edit {
			want = -1 // 20 + 45 = 65: past the hour watched
		}
		if raisedAt != want {
			t.Fatalf("edit=%v: no_progress raised at minute %d, want %d", edit, raisedAt, want)
		}
		if edit {
			continue
		}
		waitFor(t, "the supervision message", func() bool {
			for _, n := range f.notices() {
				if strings.Contains(n, todo.ID) && strings.Contains(n, "signal=no_progress") {
					return true
				}
			}
			return false
		})
	}
}

// The messages say what the hub saw: how long, how many calls, how many were waits, and what it kept doing.
func TestNoProgressRendered(t *testing.T) {
	e := prompts.SuperviseEntry{ID: "todo_1", Title: "等发版", Kind: sigNoProgress, Minutes: 45, Calls: 24, Waits: 23, Detail: "在等 CI; 在等待"}
	if got := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}}); !strings.Contains(got, "45 分钟没有真实进展") ||
		!strings.Contains(got, "24 次工具调用里有 23 次") || !strings.Contains(got, "（在等 CI; 在等待）") {
		t.Fatalf("supervise:\n%s", got)
	}
	if got := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{e}); !strings.Contains(got, "no real progress for 45 minutes") ||
		!strings.Contains(got, "23 of the 24 tool calls") {
		t.Fatalf("worker supervise:\n%s", got)
	}
}
