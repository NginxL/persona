package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func mechanicalFixture(t *testing.T) (*Service, *fakeHub, Todo, proto.Session, time.Time) {
	t.Helper()
	s, f := workService(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	c := stopped("mech-agent", "finished at PR")
	c.MachineID, c.Cwd = "box", "/code/repo"
	f.setSessions("box", c)
	todo := Todo{ID: "todo-mechanical", Title: "Ship change", PRURL: "https://github.com/example/repo/pull/12",
		Agents:      []TodoAgent{{MachineID: "box", SessionID: c.SessionID, Cwd: c.Cwd, Source: "manual"}},
		Delivery:    &TodoDelivery{State: deliveryPR, Unblock: unblockOther, Gate: "upstream review", At: now.Add(-time.Hour)},
		Supervision: &Supervision{Supervisor: "conv1", DoneWhen: []string{"live and verified"}, UpdatedAt: now.Add(-2 * time.Hour)}}
	s.todos.mu.Lock()
	if s.todos.todos == nil {
		s.todos.todos = map[string]*Todo{}
	}
	copy := copyTodoLocked(&todo)
	s.todos.todos[todo.ID] = &copy
	s.todos.mu.Unlock()
	setPR := func(pr map[string]any) {
		f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
			return map[string]any{"pr": pr}, nil
		})
	}
	setPR(map[string]any{"url": todo.PRURL, "state": "OPEN", "review": "REVIEW_REQUIRED", "mergeable": "MERGEABLE",
		"head_oid": "head-1", "checks_state": "pending", "checks_complete": true, "reviews_complete": true,
		"threads_complete": true, "threads": []any{}})
	return s, f, todo, c, now
}

func TestMechanicalSignalTable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setup        func(*testing.T, *Service, *fakeHub, *Todo, *proto.Session, time.Time)
		wantDelivery string
	}{
		{name: "open upstream review stays for supervisor"},
		{name: "merged and passing advances delivery without acceptance", setup: func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, _ *proto.Session, _ time.Time) {
			f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
				return map[string]any{"pr": map[string]any{
					"url": "https://github.com/example/repo/pull/12", "state": "MERGED", "head_oid": "head-1", "mergeable": "MERGEABLE",
					"checks_state": "passing", "checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{},
				}}, nil
			})
		}, wantDelivery: deliveryMerged},
		{name: "merged delivery gate stays for supervisor", setup: func(_ *testing.T, _ *Service, f *fakeHub, todo *Todo, _ *proto.Session, _ time.Time) {
			todo.Delivery.State = deliveryMerged
			f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
				return map[string]any{"pr": map[string]any{
					"url": todo.PRURL, "state": "MERGED", "head_oid": "head-1", "mergeable": "MERGEABLE", "checks_state": "passing",
					"checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{},
				}}, nil
			})
		}},
		{name: "current future wake defers", setup: func(t *testing.T, s *Service, _ *fakeHub, _ *Todo, c *proto.Session, now time.Time) {
			at := now.Add(time.Hour)
			s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt, WakeAt: &at,
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &at}})
		}},
		{name: "native scheduled wake defers with triage wake disabled", setup: func(_ *testing.T, s *Service, f *fakeHub, todo *Todo, c *proto.Session, now time.Time) {
			todo.Delivery = nil
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			f.setSessions("box", *c)
			s.work.switches = &TriageSwitches{Enabled: true, Wake: false}
		}},
		{name: "expired wake does not defer", setup: func(t *testing.T, s *Service, _ *fakeHub, todo *Todo, c *proto.Session, now time.Time) {
			at := now.Add(-time.Minute)
			s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt, WakeAt: &at,
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &at}})
			todo.Delivery = nil
		}},
		{name: "stale wake does not defer", setup: func(t *testing.T, s *Service, _ *fakeHub, todo *Todo, c *proto.Session, now time.Time) {
			at := now.Add(time.Hour)
			s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt.Add(-time.Hour), WakeAt: &at,
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &at}})
			todo.Delivery = nil
		}},
		{name: "wake disabled does not defer", setup: func(t *testing.T, s *Service, _ *fakeHub, todo *Todo, c *proto.Session, now time.Time) {
			at := now.Add(time.Hour)
			s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt, WakeAt: &at,
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &at}})
			s.work.switches = &TriageSwitches{Enabled: true, Wake: false}
			todo.Delivery = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, c, now := mechanicalFixture(t)
			if tc.setup != nil {
				tc.setup(t, s, f, &todo, &c, now)
			}
			s.todos.mu.Lock()
			copy := copyTodoLocked(&todo)
			s.todos.todos[todo.ID] = &copy
			s.todos.mu.Unlock()
			g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Machine: c.MachineID, Session: c.SessionID, Key: "turn-1"}
			handled, retry := s.mechanicalSignal(context.Background(), g, now)
			got := todoNow(s, todo.ID)
			wantRetry := tc.name == "current future wake defers" || tc.name == "native scheduled wake defers with triage wake disabled"
			if handled || !retry.IsZero() && !wantRetry {
				t.Fatalf("mechanical signal was handled or unexpectedly retried: handled=%v retry=%v todo=%+v", handled, retry, got)
			}
			if got.Supervision.State != todo.Supervision.State || len(got.Supervision.Reviews) != len(todo.Supervision.Reviews) || got.DoneAt != nil {
				t.Fatalf("mechanical path fabricated a park or changed review state: before=%+v after=%+v", todo.Supervision, got)
			}
			if tc.wantDelivery != "" && (got.Delivery == nil || got.Delivery.State != tc.wantDelivery || got.DoneAt != nil || got.Supervision.State != "") {
				t.Fatalf("delivery transition accepted/failed: %+v", got)
			}
			if (tc.name == "current future wake defers" || tc.name == "native scheduled wake defers with triage wake disabled") && (retry.IsZero() || handled || got.Supervision.State != "") {
				t.Fatalf("future wake not deferred: handled=%v retry=%v todo=%+v", handled, retry, got)
			}
			if tc.wantDelivery == "" && tc.name != "current future wake defers" && tc.name != "native scheduled wake defers with triage wake disabled" && got.Supervision.State != "" {
				t.Fatalf("unexpected parking: %+v", got.Supervision)
			}
			if len(f.asked) != 0 {
				t.Fatalf("mechanical path called model %d times", len(f.asked))
			}
		})
	}
}

func TestMechanicalSignalFailsOpen(t *testing.T) {
	cases := []struct {
		name   string
		signal superviseSignal
		mutate func(*testing.T, *Service, *fakeHub, *Todo, *proto.Session, time.Time)
	}{
		{"review signal", superviseSignal{Kind: sigReview}, nil}, {"failed signal", superviseSignal{Kind: sigFailed}, nil},
		{"needs you", superviseSignal{Kind: sigNeedsYou}, nil}, {"interrupted", superviseSignal{Kind: sigInterrupted}, nil},
		{"all agents stopped needs judgment", superviseSignal{Kind: sigAllStopped}, nil},
		{"owner ended parking with a decision", superviseSignal{Kind: sigParkDecided}, nil},
		{"unjudged", superviseSignal{Kind: sigUnjudged}, nil}, {"question report", superviseSignal{Kind: sigReport, Report: upReportQuestion}, nil},
		{"rework", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, _ *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			t.Supervision.State = reviewRework
		}},
		{"pending review", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, _ *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			t.Supervision.State = reviewPending
		}},
		{"owner wait", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, _ *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			t.Supervision.Waits = &OwnerWait{Reason: "choose"}
		}},
		{"durable same-turn user wait without interaction", superviseSignal{Kind: sigIdle}, func(t *testing.T, s *Service, _ *fakeHub, _ *Todo, c *proto.Session, now time.Time) {
			s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt,
				Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Wait: "answer needed"}})
		}},
		{"expired native wake does not defer", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, todo *Todo, c *proto.Session, now time.Time) {
			todo.Delivery = nil
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(-time.Minute)}
			f.setSessions("box", *c)
		}},
		{"active agent", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, c *proto.Session, _ time.Time) {
			c.Status, c.State = proto.StatusWorking, proto.StateWorking
			f.setSessions("box", *c)
		}},
		{"pending interaction", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, c *proto.Session, _ time.Time) {
			c.PendingInteractions = 1
			f.setSessions("box", *c)
		}},
		{"queued input", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, c *proto.Session, _ time.Time) {
			c.Queued = []string{"next"}
			f.setSessions("box", *c)
		}},
		{"turn error", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, c *proto.Session, _ time.Time) {
			c.TurnError = "failed"
			f.setSessions("box", *c)
		}},
		{"PR unknown", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, _ *proto.Session, _ time.Time) {
			f.answerNode("git.pr", func(string, json.RawMessage) (any, error) { return map[string]any{"pr": nil}, nil })
		}},
		{"PR closed", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "CLOSED", "passing", true, true, true, nil)
		}},
		{"PR conflict status", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "pr_conflict", "pending", true, true, true, nil)
		}},
		{"URL mismatch", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, "https://github.com/example/repo/pull/13", "OPEN", "pending", true, true, true, nil)
		}},
		{"draft", superviseSignal{Kind: sigIdle}, func(_ *testing.T, s *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "OPEN", "pending", true, true, true, nil)
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "draft": true, "checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{}})
		}},
		{"conflict", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "mergeable": "CONFLICTING", "checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{}})
		}},
		{"dirty merge state", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "mergeable": "MERGEABLE", "merge_state": "DIRTY", "checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{}})
		}},
		{"unknown mergeability", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "mergeable": "UNKNOWN", "checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{}})
		}},
		{"failing checks", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "OPEN", "failing", true, true, true, nil)
		}},
		{"incomplete checks", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "OPEN", "pending", false, true, true, nil)
		}},
		{"new review after delivery", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, now time.Time) {
			at := now
			at = t.Delivery.At.Add(time.Minute)
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "checks_complete": true, "reviews_complete": true, "threads_complete": true, "last_review_at": at, "threads": []any{}})
		}},
		{"delivery predates stopped turn", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, c *proto.Session, _ time.Time) {
			t.Delivery.At = c.UpdatedAt.Add(-time.Minute)
			mechanicalPR(f, t.PRURL, "OPEN", "pending", true, true, true, nil)
		}},
		{"unresolved thread", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPR(f, t.PRURL, "OPEN", "pending", true, true, true, []any{map[string]any{"body": "please fix"}})
		}},
		{"missing completeness marker", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, t *Todo, _ *proto.Session, _ time.Time) {
			mechanicalPRSet(f, map[string]any{"url": t.PRURL, "state": "OPEN", "head_oid": "h", "checks_complete": true, "reviews_complete": true, "threads": []any{}})
		}},
		{"PR read error", superviseSignal{Kind: sigIdle}, func(_ *testing.T, _ *Service, f *fakeHub, _ *Todo, _ *proto.Session, _ time.Time) {
			f.answerNode("git.pr", func(string, json.RawMessage) (any, error) { return nil, errors.New("read failed") })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, c, now := mechanicalFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, s, f, &todo, &c, now)
			}
			s.todos.mu.Lock()
			copy := copyTodoLocked(&todo)
			s.todos.todos[todo.ID] = &copy
			s.todos.mu.Unlock()
			g := tc.signal
			g.Todo = todo.ID
			g.Key = "turn-1"
			g.Machine, g.Session = c.MachineID, c.SessionID
			handled, retry := s.mechanicalSignal(context.Background(), g, now)
			got := todoNow(s, todo.ID)
			if handled || !retry.IsZero() || got.Supervision.State != todo.Supervision.State || got.DoneAt != nil {
				t.Fatalf("not fail-open: handled=%v retry=%v todo=%+v", handled, retry, got)
			}
		})
	}
}

func mechanicalPR(f *fakeHub, url, state, checks string, checksOK, reviewsOK, threadsOK bool, threads []any) {
	mechanicalPRSet(f, map[string]any{"url": url, "state": state, "head_oid": "head-1", "mergeable": "MERGEABLE", "checks_state": checks,
		"checks_complete": checksOK, "reviews_complete": reviewsOK, "threads_complete": threadsOK, "threads": threads})
}

func mechanicalPRSet(f *fakeHub, pr map[string]any) {
	f.answerNode("git.pr", func(string, json.RawMessage) (any, error) { return map[string]any{"pr": pr}, nil })
}

func TestScreenMechanicalSignalsKeepsMixedInboxAndIdle(t *testing.T) {
	s, f, todo, c, now := mechanicalFixture(t)
	list := []superviseSignal{{Todo: todo.ID, Kind: sigInbox, Key: "new-comment", Machine: "box", Session: c.SessionID},
		{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: "box", Session: c.SessionID}}
	got := s.screenMechanicalSignals(context.Background(), list, now, now.Add(-time.Minute))
	if len(got) != 2 || got[0].Kind != sigInbox || got[1].Kind != sigIdle || todoNow(s, todo.ID).Supervision.State != "" {
		t.Fatalf("mixed actionable inbox allowed mechanical parking: signals=%+v todo=%+v", got, todoNow(s, todo.ID))
	}
	for _, call := range f.calls {
		if call.Method == "git.pr" {
			t.Fatalf("screen inspected PR despite actionable sibling: calls=%+v", f.calls)
		}
	}
}

func TestScreenMechanicalSignalsDefersIdleWithReportSiblingAndWake(t *testing.T) {
	s, f, todo, c, now := mechanicalFixture(t)
	todo.Delivery = nil
	c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(4 * time.Minute)}
	f.setSessions("box", c)
	s.todos.mu.Lock()
	copy := copyTodoLocked(&todo)
	s.todos.todos[todo.ID] = &copy
	s.todos.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.mu.Unlock()
	idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: "box", Session: c.SessionID}
	report := superviseSignal{Todo: todo.ID, Kind: sigReport, Key: "report-1", Machine: "box", Session: c.SessionID}

	got := s.screenMechanicalSignals(context.Background(), []superviseSignal{report, idle}, now, now.Add(-time.Minute))
	if len(got) != 1 || got[0].Kind != sigReport {
		t.Fatalf("a live wakeup did not defer the report's idle sibling: %+v", got)
	}
	for _, call := range f.calls {
		if call.Method == "git.pr" {
			t.Fatalf("wake-only screening inspected the PR: calls=%+v", f.calls)
		}
	}
	if state := todoNow(s, todo.ID).Supervision.State; state != "" {
		t.Fatalf("wake-only screening changed supervision state to %q", state)
	}
}

func TestScreenMechanicalSignalsDeferredIdleLeavesDigestBatch(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(45) })
	sid := todo.Agents[0].SessionID
	now := time.Now()
	sess := proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: "waiting for the scheduled wakeup", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(10 * time.Minute)}}
	other := Todo{ID: "todo-other", Title: "other work", Agents: []TodoAgent{{MachineID: "box", SessionID: "other-session", Source: "leader"}},
		Supervision: &Supervision{Supervisor: "conv1", DoneWhen: []string{"finished"}, UpdatedAt: now.Add(-time.Hour)}}
	s.todos.mu.Lock()
	copy := copyTodoLocked(&other)
	s.todos.todos[other.ID] = &copy
	s.todos.mu.Unlock()
	f.setSessions("box", sess, proto.Session{SessionID: "other-session", MachineID: "box", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: now})
	idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-wake", Detail: "waiting for the scheduled wakeup",
		At: now.Add(-50 * time.Minute), batchTo: "conv1", batchUntil: now.Add(-time.Second)}
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.batch["conv1"] = now.Add(-time.Second)
	s.supervise.mu.Unlock()

	before := len(assistantSendsTo(f, "conv1"))
	wakeRound(s, idle, superviseSignal{Todo: other.ID, Kind: sigInterrupted, Key: "other-1", At: now.Add(-50 * time.Minute)})
	sends := assistantSendsTo(f, "conv1")
	var text string
	for _, send := range sends[before:] {
		part, _ := send["text"].(string)
		text += part + "\n"
	}
	if strings.Contains(text, todo.ID+"，signal=idle") {
		t.Fatalf("batch release reclaimed the deferred idle: %s", text)
	}
	if !strings.Contains(text, "signal=interrupted") {
		t.Fatalf("the other to-do did not release the digest control: %s", text)
	}
	s.supervise.mu.Lock()
	deferred, ok := s.supervise.pending[idle.id()]
	s.supervise.mu.Unlock()
	if !ok || deferred.batchTo != "" || !deferred.batchUntil.IsZero() {
		t.Fatalf("deferred idle retained its batch: pending=%v signal=%+v", ok, deferred)
	}
}

func TestScreenMechanicalSignalsDefersIdleDuringWakeupTurn(t *testing.T) {
	s, f, todo, c, now := mechanicalFixture(t)
	todo.Delivery = nil
	c.Status, c.State = proto.StatusWorking, proto.StateWorking
	c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(-5 * time.Second)}
	f.setSessions("box", c)
	s.todos.mu.Lock()
	copy := copyTodoLocked(&todo)
	s.todos.todos[todo.ID] = &copy
	s.todos.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.mu.Unlock()
	idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: "box", Session: c.SessionID}

	got := s.screenMechanicalSignals(context.Background(), []superviseSignal{idle}, now, now.Add(-time.Minute))
	if len(got) != 0 {
		t.Fatalf("the previous stop's idle was released during its wakeup turn: %+v", got)
	}
	s.supervise.mu.Lock()
	_, deferred := s.supervise.pending[idle.id()]
	s.supervise.mu.Unlock()
	if !deferred {
		t.Fatal("the wakeup turn's stale idle was not restored for rescreening")
	}
}

func TestPRConflictIsNeverMechanicalWithoutAgents(t *testing.T) {
	for _, supervisor := range []string{"main", "direction", "background"} {
		t.Run(supervisor, func(t *testing.T) {
			s, f, todo, _, now := mechanicalFixture(t)
			todo.Agents = nil
			todo.Supervision.Supervisor = supervisor
			s.todos.mu.Lock()
			copy := copyTodoLocked(&todo)
			s.todos.todos[todo.ID] = &copy
			s.todos.mu.Unlock()
			g := superviseSignal{Todo: todo.ID, Kind: sigPRConflict, Key: todo.PRURL + "@head-1"}
			if handled, retry := s.mechanicalSignal(context.Background(), g, now); handled || !retry.IsZero() {
				t.Fatalf("actionable conflict was suppressed: handled=%v, retry=%v", handled, retry)
			}
			if got := s.screenMechanicalSignals(context.Background(), []superviseSignal{g}, now, time.Time{}); len(got) != 1 || got[0].Kind != sigPRConflict {
				t.Fatalf("responsible supervisor lost the conflict: %+v", got)
			}
			if len(f.asked) != 0 || todoNow(s, todo.ID).Supervision.State != "" {
				t.Fatal("screening changed the task or called a model instead of forwarding the signal")
			}
		})
	}
}

func TestSendDigestRoutesOpenReviewForSupervision(t *testing.T) {
	s, f, todo, c, _ := mechanicalFixture(t)
	shortSupervise(t, s)
	s.noBackground = false
	f.hello.Assistant.MachineID = "server"
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.known["conv1"] = true
	setSettings(t, s, func(v *Settings) { v.Main, v.MachineID, v.MainMachineID = "conv1", "server", "server" })
	b := &s.supervise
	b.mu.Lock()
	b.init()
	g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: c.MachineID, Session: c.SessionID, At: time.Now().Add(-time.Minute)}
	b.pending[g.id()] = g
	b.last = time.Time{}
	b.mu.Unlock()
	s.sendDigest(context.Background())
	if got := todoNow(s, todo.ID); got.Supervision.State != "" || len(got.Supervision.Reviews) != 0 || got.DoneAt != nil {
		t.Fatalf("digest fabricated a park or closed the task: %+v", got)
	}
	bg := s.settings(context.Background()).BackgroundSupervisor
	if bg == "" || bg == "conv1" || len(conversationStarts(f)) != 1 {
		t.Fatalf("unsupported parking did not reach ordinary background supervision: %q", bg)
	}
	listBackground(t, s, f, bg)
	waitFor(t, "the external wait's supervision message", func() bool { return len(f.noticesTo(bg)) == 1 })
	if notices := f.noticesTo(bg); !strings.Contains(notices[0], "signal=idle") || len(f.noticesTo("conv1")) != 0 {
		t.Fatalf("open review did not reach its responsible supervisor alone: %q", notices)
	}
	if len(f.asked) != 0 {
		t.Fatalf("mechanical screening called a model instead of routing the signal: %d", len(f.asked))
	}
}

func TestMechanicalDeliveryPersistenceFailureFailsOpen(t *testing.T) {
	s, f, todo, c, now := mechanicalFixture(t)
	old := *todo.Delivery
	if !s.setTaskDelivery(context.Background(), todo.ID, old) {
		t.Fatal("could not seed persisted delivery")
	}
	st := s.store().(*memStore)
	st.failPut = errors.New("disk unavailable")
	f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
		return map[string]any{"pr": map[string]any{
			"url": todo.PRURL, "state": "MERGED", "head_oid": "head-1", "mergeable": "MERGEABLE", "checks_state": "passing",
			"checks_complete": true, "reviews_complete": true, "threads_complete": true, "threads": []any{},
		}}, nil
	})
	s.todos.mu.Lock()
	copy := copyTodoLocked(&todo)
	s.todos.todos[todo.ID] = &copy
	s.todos.mu.Unlock()
	handled, retry := s.mechanicalSignal(context.Background(), superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: "box", Session: c.SessionID}, now)
	got := todoNow(s, todo.ID)
	if handled || !retry.IsZero() || got.Delivery == nil || *got.Delivery != old || got.Supervision.State != "" {
		t.Fatalf("failed delivery persistence was consumed: handled=%v retry=%v todo=%+v", handled, retry, got)
	}
}
