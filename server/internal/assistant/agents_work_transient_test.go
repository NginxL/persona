package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Transient model errors are continued by default, as a rule, also with the triage off (transient_retry, #3208).

// transientService is a manager whose user never turned the triage on: only transient_retry's default is in force.
func transientService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := workService(t)
	s.work.switches = &TriageSwitches{}
	// Any model call would make the turn the user's: none may happen with the triage off.
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	return s, f
}

// failAgain: s1 went on (the manager's "Continue", or the user) and its next turn, the i-th, ended on errText.
func failAgain(t *testing.T, s *Service, f *fakeHub, i int, errText string) {
	t.Helper()
	s.workStarted(context.Background(), "box", "s1", time.Now())
	c := apiErrorTurn("s1", errText)
	c.UpdatedAt = turnEnd.Add(time.Duration(i) * time.Minute)
	f.setSessions("box", c)
	triage(t, s, "s1")
}

func TestRateLimitError(t *testing.T) {
	for text, want := range map[string]bool{
		"API Error: 429 Too Many Requests":    true,
		"API Error: rate_limit_error":         true,
		"API Error: Rate limit reached":       true,
		"API Error: 503 no eligible upstream": false,
		"API Error: Connection error.":        false,
		"API Error: timed out after 4290ms":   false,
	} {
		if got := rateLimitError(text); got != want {
			t.Errorf("rateLimitError(%q) = %v, want %v", text, got, want)
		}
	}
}

// Unset switches: transient_retry on, everything else off; a PUT that leaves it out keeps it.
func TestTransientRetrySwitch(t *testing.T) {
	if sw := (TriageSwitches{}); !sw.transientOn() || !sw.active() || sw.Enabled {
		t.Fatalf("defaults %+v", sw)
	}
	if sw := (TriageSwitches{TransientRetry: new(false)}); sw.transientOn() || sw.active() {
		t.Fatalf("turned off %+v", sw)
	}
	off := false
	f := newFakeHub()
	f.hello.Agents.Triage.TransientRetry = &off
	if sw := newTestService(t, f).triageSwitches(context.Background()); sw.transientOn() {
		t.Fatalf("manager.json's initial value is not taken: %+v", sw)
	}
	transient := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient}
	limit := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, ResetsAt: &turnEnd}
	wait := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "CI"}
	for _, c := range []struct {
		sw                     TriageSwitches
		transient, limit, wait bool
	}{
		{TriageSwitches{}, true, false, false},
		{TriageSwitches{TransientRetry: new(false), Enabled: true, AutoContinue: true, Wake: true}, false, true, true},
		{TriageSwitches{Enabled: true}, true, false, false},
		{TriageSwitches{AutoContinue: true, Wake: true}, true, false, false}, // the main switch off
	} {
		if c.sw.wakes(transient) != c.transient || c.sw.wakes(limit) != c.limit || c.sw.wakes(wait) != c.wait {
			t.Errorf("%+v: wakes %v %v %v, want %v %v %v", c.sw, c.sw.wakes(transient), c.sw.wakes(limit), c.sw.wakes(wait),
				c.transient, c.limit, c.wait)
		}
	}

	s, _ := workService(t)
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"transient_retry":false}`); w.Code != 200 {
		t.Fatalf("PUT %d %s", w.Code, w.Body)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"wake":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"transient_retry":false`) {
		t.Fatalf("an older app's PUT dropped transient_retry: %d %s", w.Code, w.Body)
	}
}

// With the triage off, a transient error is still continued after its backoff, with no model call; the apps are told
// of the "Continue" on its way, and of nothing else.
func TestTransientContinuedWithTriageOff(t *testing.T) {
	s, f := transientService(t)
	ctx := context.Background()
	f.setSessions("box", apiErrorTurn("s1", "API Error: Connection lost mid-response"), stopped("s2", "A 还是 B？"),
		apiErrorTurn("s3", "API Error: 401 invalid x-api-key"))
	for _, sid := range []string{"s1", "s2", "s3"} {
		triage(t, s, sid)
	}
	for _, sid := range []string{"s2", "s3"} {
		if e, ok := s.workEntry([2]string{"box", sid}); ok {
			t.Fatalf("%s decided with the triage off: %+v", sid, e)
		}
	}
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitTransient || e.Work.By != "rule" || e.Work.Max != 0 {
		t.Fatalf("entry %+v", e)
	}
	if d := time.Until(*e.WakeAt); d < 7*time.Second || d > 16*time.Second {
		t.Fatalf("first continue in %v, want about 15 s", d)
	}
	r := s.workReport(ctx)
	if r == nil || r.On || len(r.Sessions) != 1 || r.Sessions[0].SessionID != "s1" || r.Sessions[0].Work.WakeAt == nil {
		t.Fatalf("report %+v", r)
	}
	s.workTick(ctx, time.Now())
	if len(f.sentToAgents()) != 0 {
		t.Fatal("sent before the backoff")
	}
	s.workTick(ctx, time.Now().Add(16*time.Second))
	if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: 继续" {
		t.Fatalf("sent %v", got)
	}
	if len(s.workCards()) != 0 {
		t.Fatal("a card with the triage off")
	}
}

func TestTransientContinueAllowsHerdrIdleOnlyForOpenTaskAgents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		openTask bool
	}{
		{name: "open task Agent", openTask: true},
		{name: "ordinary session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := transientService(t)
			if tc.openTask {
				putTodo(s, Todo{ID: "todo-worker", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
			}
			c := apiErrorTurn("s1", "API Error: 503 no eligible upstream")
			f.setSessions("box", c)
			triage(t, s, "s1")
			if e := entryOf(t, s, "s1"); e.WakeAt == nil || e.Work.On != proto.WaitTransient {
				t.Fatalf("transient retry was not scheduled: %+v", e)
			}

			c.Status = proto.StatusIdle // herdr observed the same failed turn; its timestamp and error are unchanged.
			f.setSessions("box", c)
			s.workTick(context.Background(), time.Now().Add(16*time.Second))

			got := f.sentToAgents()
			if tc.openTask {
				if len(got) != 1 || got[0] != "s1: 继续" {
					t.Fatalf("open task Agent lost its transient retry: sent %v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("ordinary session did not yield after being seen: sent %v", got)
			}
		})
	}
}

func TestHerdrIdleStillCancelsAnOpenTaskWake(t *testing.T) {
	s, f := workService(t)
	putTodo(s, Todo{ID: "todo-worker", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
	f.triage = `{"phase":"blocked","on":"external","line":"等 CI","wait":{"what":"CI","then":"跑测试","check_minutes":5}}`
	c := stopped("s1", "等 CI 结束后跑测试")
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: c.LastText})
	f.setSessions("box", c)
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.WakeAt == nil || e.Work.On != proto.WaitExternal {
		t.Fatalf("external wake was not scheduled: %+v", e)
	}

	c.Status = proto.StatusIdle
	f.setSessions("box", c)
	s.workTick(context.Background(), time.Now().Add(6*time.Minute))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("an open task's non-transient wake bypassed the herdr guard: sent %v", got)
	}
}

// Each "Continue" that errs again waits twice as long; after the cap the session is left as it was (its error and
// the apps' Retry), with no card. A turn that ends normally starts the count again.
func TestTransientCapAndReset(t *testing.T) {
	s, f := transientService(t)
	ctx := context.Background()
	const errText = "API Error: 529 {\"type\":\"overloaded_error\"}"
	f.setSessions("box", apiErrorTurn("s1", errText))
	triage(t, s, "s1")
	for i := 1; i <= 20; i++ {
		e := entryOf(t, s, "s1")
		if e.WakeAt == nil || e.Work.On != proto.WaitTransient || e.Work.Max != 0 {
			t.Fatalf("attempt %d stopped: %+v", i, e)
		}
		d := e.WakeAt.Sub(e.Work.At)
		if d <= 0 || d > 30*time.Minute {
			t.Fatalf("backoff out of bounds: %v", d)
		}
		s.workTick(ctx, *e.WakeAt)
		failAgain(t, s, f, i, errText)
	}
	if len(f.sentToAgents()) != 20 || len(s.workCards()) != 0 {
		t.Fatalf("sent %v, cards %+v", f.sentToAgents(), s.workCards())
	}
	e := entryOf(t, s, "s1")

	// The user tapped Retry and the turn ended normally: the next transient error counts from the start again.
	s.workStarted(ctx, "box", "s1", time.Now())
	done := stopped("s1", "测试都过了")
	done.UpdatedAt = turnEnd.Add(time.Hour)
	f.setSessions("box", done)
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.Retries != 0 {
		t.Fatalf("count after a normal turn: %d", e.Retries)
	}
	failAgain(t, s, f, 61, errText)
	e = entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Retries != 0 || e.Work.Attempts != 0 {
		t.Fatalf("after a normal turn: %+v", e)
	}
	if d := e.WakeAt.Sub(e.Work.At); d > 20*time.Second {
		t.Fatalf("first continue again in %v", d)
	}
}

// The user going on first wins: the pending "Continue" is dropped.
func TestTransientContinueYieldsToTheUser(t *testing.T) {
	s, f := transientService(t)
	ctx := context.Background()
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 no eligible upstream"))
	triage(t, s, "s1")
	if entryOf(t, s, "s1").WakeAt == nil {
		t.Fatal("not scheduled")
	}
	s.workStarted(ctx, "box", "s1", time.Now()) // the user's message, or Retry in the app
	if e := entryOf(t, s, "s1"); e.WakeAt != nil {
		t.Fatalf("still scheduled: %+v", e)
	}
	s.workTick(ctx, time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// A rate limit waits four times as long.
func TestRateLimitWaitsLonger(t *testing.T) {
	s, f := transientService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: 429 Too Many Requests"))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil {
		t.Fatalf("not scheduled: %+v", e)
	}
	if d := e.WakeAt.Sub(e.Work.At); d < 30*time.Second || d > 61*time.Second {
		t.Fatalf("first continue after a rate limit in %v, want equal jitter between 30 and 60 s", d)
	}
}
