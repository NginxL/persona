package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The status question (agents_closeout_ask.go): one message to an agent nobody supervises whose turn did work and
// ended without a closeout.

const askReply = "改了重试的逻辑，先到这。"

// askService has the triage and the status question on, and a turn on box that edited a file and ended with reply.
func askService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := workService(t)
	s.work.switches = &TriageSwitches{Enabled: true, CloseoutAsk: true}
	f.triage = `{"phase":"","line":""}` // unsure
	workedTurn(f, askReply)
	return s, f
}

// workedTurn makes the fake's latest turn one that edited a file and ended with reply.
func workedTurn(f *fakeHub, reply string) {
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "把重试改成退避"},
		proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Edit"},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
}

// justStopped is stopped with a turn that ended ago before now (a status question is not sent for an old turn).
func justStopped(id, last string, ago time.Duration) proto.Session {
	c := stopped(id, last)
	c.UpdatedAt = time.Now().Add(-ago).UTC().Truncate(time.Second)
	c.StartedByPersona = true // only an agent Persona started is asked
	return c
}

func askTick(s *Service) { s.workTick(context.Background(), time.Now().Add(time.Second)) }

func askEvents(s *Service) map[string]int {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	out := map[string]int{}
	for _, e := range s.work.testEvents {
		if strings.HasPrefix(e.Kind, "closeout_ask") {
			out[e.Kind]++
		}
	}
	return out
}

// Asked once and only once for the turn; the turn that answers is triaged as usual and never asked again, even when
// it has no closeout either.
func TestCloseoutAskOnce(t *testing.T) {
	s, f := askService(t)
	c := justStopped("s1", askReply, time.Minute)
	f.setSessions("box", c)
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.WakeAt == nil || e.Work.CloseoutAsk != closeoutAskPending || e.Work.WakeAt != nil {
		t.Fatalf("not scheduled: %+v", e)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("the triage itself sent %v", got)
	}
	askTick(s)
	got := f.sentToAgents()
	if len(got) != 1 || !strings.HasPrefix(got[0], "s1: ") || !strings.Contains(got[0], `<closeout status="done|not_done|blocked"`) ||
		!strings.Contains(got[0], "不要开始新的工作") {
		t.Fatalf("sent %v", got)
	}
	if e := entryOf(t, s, "s1"); e.WakeAt != nil || e.Work.CloseoutAsk != closeoutAskSent {
		t.Fatalf("after the question: %+v", e)
	}
	askTick(s)
	triage(t, s, "s1") // the same turn again
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("asked again for the same turn: %v", got)
	}
	if _, shown := reportOf(t, s, "s1"); shown {
		t.Fatal("an unsure turn is shown while it is asked")
	}

	// It answers with a closeout: done by rule, the line from the closeout, and counted.
	s.workStarted(context.Background(), "box", "s1", time.Now())
	workedTurn(f, doneCloseout)
	c2 := justStopped("s1", doneCloseout, 0)
	f.setSessions("box", c2)
	triage(t, s, "s1")
	w, ok := reportOf(t, s, "s1")
	if !ok || w.Phase != proto.WorkDone || w.Closeout == nil || w.CloseoutAsk != "" {
		t.Fatalf("the answer: %+v", w)
	}
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("the answering turn was asked: %v", got)
	}
	if ev := askEvents(s); ev[closeoutAskSentEvent] != 1 || ev[closeoutAskAnsweredEvent] != 1 || ev[closeoutAskIgnoredEvent] != 0 {
		t.Fatalf("events %v", ev)
	}
}

// An answer without a closeout is not asked again; the turn after that one is a turn like any other.
func TestCloseoutAskIgnoredIsNotRepeated(t *testing.T) {
	s, f := askService(t)
	f.setSessions("box", justStopped("s1", askReply, 2*time.Minute))
	triage(t, s, "s1")
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
	s.workStarted(context.Background(), "box", "s1", time.Now())
	f.setSessions("box", justStopped("s1", "做完了。", time.Minute)) // it did work again and still wrote none
	triage(t, s, "s1")
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("asked twice in a row: %v", got)
	}
	if ev := askEvents(s); ev[closeoutAskIgnoredEvent] != 1 || ev[closeoutAskAnsweredEvent] != 0 {
		t.Fatalf("events %v", ev)
	}
	s.workStarted(context.Background(), "box", "s1", time.Now())
	f.setSessions("box", justStopped("s1", "又改了一处。", 0)) // the user went on with it: a new turn
	triage(t, s, "s1")
	askTick(s)
	if got := f.sentToAgents(); len(got) != 2 {
		t.Fatalf("a later turn of its own is not asked: %v", got)
	}
}

// Nothing is asked with the switch off, with a closeout, for a turn that did no work or that the user stopped, for a
// finished turn with something to show, or for an agent that is supervised or not an agent's own session.
func TestCloseoutAskOnlyWhenDue(t *testing.T) {
	for name, change := range map[string]func(s *Service, f *fakeHub, c *proto.Session){
		"switch off":      func(s *Service, _ *fakeHub, _ *proto.Session) { s.work.switches = &TriageSwitches{Enabled: true} },
		"started by hand": func(_ *Service, _ *fakeHub, c *proto.Session) { c.StartedByPersona = false },
		"triage off":      func(s *Service, _ *fakeHub, _ *proto.Session) { s.work.switches = &TriageSwitches{CloseoutAsk: true} },
		"a closeout":      func(_ *Service, f *fakeHub, c *proto.Session) { workedTurn(f, doneCloseout); c.LastText = doneCloseout },
		"no work": func(_ *Service, f *fakeHub, _ *proto.Session) {
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: askReply})
		},
		"only reads": func(_ *Service, f *fakeHub, _ *proto.Session) {
			f.setItems(proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Read"}, proto.Item{Kind: proto.KindText, Role: "assistant", Text: askReply})
		},
		"user stopped": func(_ *Service, f *fakeHub, _ *proto.Session) {
			f.setItems(proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Edit"}, proto.Item{Kind: proto.KindText, Role: "assistant", Text: askReply},
				proto.Item{Kind: proto.KindNotice, Text: userStoppedNotice})
		},
		"done with an artifact": func(_ *Service, f *fakeHub, _ *proto.Session) {
			f.triage = `{"phase":"done","line":"提了 PR","artifacts":[{"kind":"pr","ref":"https://github.com/example/app/pull/12"}]}`
		},
		"waits": func(_ *Service, f *fakeHub, c *proto.Session) {
			workedTurn(f, "A 还是 B？")
			c.LastText = "A 还是 B？"
			f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
		},
		"supervised": func(s *Service, _ *fakeHub, _ *proto.Session) {
			putTodo(s, Todo{ID: "todo_x", Title: "任务", Supervision: &Supervision{Supervisor: "conv-main"},
				Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
		},
		"completed todo": func(s *Service, _ *fakeHub, _ *proto.Session) {
			at := time.Now()
			putTodo(s, Todo{ID: "todo_x", Title: "任务", DoneAt: &at, Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
		},
		"source-owned": func(_ *Service, _ *fakeHub, c *proto.Session) { c.SourceKind = "runtime" },
		"archived":     func(_ *Service, _ *fakeHub, c *proto.Session) { c.Archived = true },
		"read-only":    func(_ *Service, _ *fakeHub, c *proto.Session) { c.ReadOnlyReason = "viewer" },
		"assistant":    func(_ *Service, _ *fakeHub, c *proto.Session) { c.Assistant = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := askService(t)
			c := justStopped("s1", askReply, time.Minute)
			change(s, f, &c)
			f.setSessions("box", c)
			triage(t, s, "s1")
			askTick(s)
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("sent %v", got)
			}
			if e, ok := s.workEntry([2]string{"box", "s1"}); ok && (e.WakeAt != nil || e.Work.CloseoutAsk != "") {
				t.Fatalf("scheduled: %+v", e)
			}
		})
	}
}

// A turn the model calls done with nothing to show for it is asked as an unsure one is; an unsupervised todo does
// not stand in the way.
func TestCloseoutAskDoneWithoutEvidence(t *testing.T) {
	s, f := askService(t)
	f.triage = `{"phase":"done","line":"改好了"}`
	putTodo(s, Todo{ID: "todo_x", Title: "任务", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
	f.setSessions("box", justStopped("s1", askReply, time.Minute))
	triage(t, s, "s1")
	if w, ok := reportOf(t, s, "s1"); !ok || w.Phase != proto.WorkDone || w.WakeAt != nil || w.CloseoutAsk != "" {
		t.Fatalf("the apps are told %+v", w)
	}
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
}

// Every recheck a wake gets holds for the question: whatever the user did meanwhile wins, and nothing is sent later
// for that turn.
func TestCloseoutAskYieldsToTheUser(t *testing.T) {
	for name, change := range map[string]func(s *Service, c *proto.Session){
		"working":       func(_ *Service, c *proto.Session) { c.Status = proto.StatusWorking },
		"new turn":      func(_ *Service, c *proto.Session) { c.UpdatedAt = c.UpdatedAt.Add(30 * time.Second) },
		"queued input":  func(_ *Service, c *proto.Session) { c.Queued = []string{"先别动"} },
		"seen in herdr": func(_ *Service, c *proto.Session) { c.Status = proto.StatusIdle },
		"asking":        func(_ *Service, c *proto.Session) { c.PendingInteractions = 1 },
		"switch off":    func(s *Service, _ *proto.Session) { s.work.switches = &TriageSwitches{Enabled: true} },
		"supervised since": func(s *Service, _ *proto.Session) {
			putTodo(s, Todo{ID: "todo_x", Title: "任务", Supervision: &Supervision{Supervisor: "conv-main"},
				Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
		},
		"todo completed since": func(s *Service, _ *proto.Session) {
			at := time.Now()
			putTodo(s, Todo{ID: "todo_x", Title: "任务", DoneAt: &at, Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, f := askService(t)
			c := justStopped("s1", askReply, time.Minute)
			f.setSessions("box", c)
			triage(t, s, "s1")
			if e := entryOf(t, s, "s1"); e.Work.CloseoutAsk != closeoutAskPending {
				t.Fatalf("setup: %+v", e)
			}
			change(s, &c)
			f.setSessions("box", c)
			askTick(s)
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("sent %v", got)
			}
			if e := entryOf(t, s, "s1"); e.WakeAt != nil {
				t.Fatalf("still scheduled: %+v", e)
			}
			if ev := askEvents(s); len(ev) != 0 {
				t.Fatalf("events %v", ev)
			}
		})
	}
}

// The user typing starts the session working: the pending question is off, and the turn they started is its own.
func TestCloseoutAskWithdrawnWhenTheUserTypes(t *testing.T) {
	s, f := askService(t)
	f.setSessions("box", justStopped("s1", askReply, time.Minute))
	triage(t, s, "s1")
	s.workStarted(context.Background(), "box", "s1", time.Now())
	if e := entryOf(t, s, "s1"); e.WakeAt != nil {
		t.Fatalf("still scheduled: %+v", e)
	}
	askTick(s)
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// Turning the switch off drops a question on its way.
func TestCloseoutAskSwitchOffDropsPending(t *testing.T) {
	s, f := askService(t)
	f.setSessions("box", justStopped("s1", askReply, time.Minute))
	triage(t, s, "s1")
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "s1"); e.WakeAt != nil {
		t.Fatalf("still scheduled: %+v", e)
	}
}

// While its machine is away the question waits; a send that fails is tried again, and counted once.
func TestCloseoutAskWaitsForTheMachine(t *testing.T) {
	s, f := askService(t)
	f.setSessions("box", justStopped("s1", askReply, time.Minute))
	triage(t, s, "s1")
	f.devices = []Device{{ID: "box", Name: "box", Online: false, Reachable: false}}
	askTick(s)
	if e := entryOf(t, s, "s1"); e.WakeAt == nil || e.Work.CloseoutAsk != closeoutAskPending || len(f.sentToAgents()) != 0 {
		t.Fatalf("with the machine away: %+v", e)
	}
	f.devices = nil
	s.workTick(context.Background(), time.Now().Add(2*time.Minute))
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
	if ev := askEvents(s); ev[closeoutAskSentEvent] != 1 {
		t.Fatalf("events %v", ev)
	}
}

// The manager restarts: a question scheduled before comes back and goes once; one already sent is not sent again,
// and the restarted manager still knows the next turn as its answer. The stats report all of it.
func TestCloseoutAskSurvivesARestart(t *testing.T) {
	f := newFakeHub()
	f.sessions["box"] = nil
	st := &workMemStore{}
	ctx := context.Background()
	s := newServiceOn(t, f, st)
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"closeout_ask":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask":true`) {
		t.Fatalf("PUT %d %s", w.Code, w.Body)
	}
	f.triage = `{"phase":"","line":""}`
	workedTurn(f, askReply)
	f.setSessions("box", justStopped("s1", askReply, time.Minute))
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.Work.CloseoutAsk != closeoutAskPending {
		t.Fatalf("setup: %+v", e)
	}
	s.waitWorkEvents()

	s = newServiceOn(t, f, st) // restarted before the question went
	if sw := s.triageSwitches(ctx); !sw.CloseoutAsk {
		t.Fatalf("switches %+v", sw)
	}
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("after the restart: sent %v", got)
	}
	s.waitWorkEvents()

	s = newServiceOn(t, f, st) // restarted again, after it went
	askTick(s)
	triage(t, s, "s1")
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent again after a restart: %v", got)
	}
	s.workStarted(ctx, "box", "s1", time.Now())
	workedTurn(f, doneCloseout)
	f.setSessions("box", justStopped("s1", doneCloseout, 0))
	triage(t, s, "s1")
	askTick(s)
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("the answering turn was asked: %v", got)
	}
	s.waitWorkEvents()
	w := serve(s, "GET", "/api/v1/agents/work/stats?hours=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask_sent":1`) || !strings.Contains(w.Body.String(), `"closeout_ask_answered":1`) {
		t.Fatalf("stats %d %s", w.Code, w.Body)
	}
}

// An app that does not know the switch leaves it as it was, and the hub's defaults carry it.
func TestCloseoutAskSwitchSurvivesOlderApps(t *testing.T) {
	s, _ := workService(t)
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"closeout_ask":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask":true`) {
		t.Fatalf("PUT %d %s", w.Code, w.Body)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"wake":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask":true`) {
		t.Fatalf("an older app's PUT cleared it: %d %s", w.Code, w.Body)
	}
	if w := serve(s, "GET", "/api/v1/agents/triage", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask":true`) {
		t.Fatalf("GET %d %s", w.Code, w.Body)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"closeout_ask":false}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout_ask":false`) {
		t.Fatalf("PUT off %d %s", w.Code, w.Body)
	}
	if (TriageSwitches{CloseoutAsk: true}).wakes(proto.WorkState{CloseoutAsk: closeoutAskPending}) {
		t.Fatal("asked without the main switch")
	}
}

// The stats count the status questions and what followed, and the stops held for a closeout.
func TestSummarizeCloseoutAsks(t *testing.T) {
	ev := func(kind string) db.WorkEvent {
		return db.WorkEvent{At: turnEnd, MachineID: "box", SessionID: "s1", Kind: kind}
	}
	s := summarizeWork(24, []db.WorkEvent{ev("closeout_asked"), ev("closeout_asked"), ev(closeoutAskSentEvent), ev(closeoutAskSentEvent),
		ev(closeoutAskSentEvent), ev(closeoutAskAnsweredEvent), ev(closeoutAskIgnoredEvent)})
	if s.Actions["closeout_asked"] != 2 || s.Actions["closeout_ask_sent"] != 3 || s.Actions["closeout_ask_answered"] != 1 ||
		s.Actions["closeout_ask_ignored"] != 1 {
		t.Fatalf("actions %v", s.Actions)
	}
}

func TestGoldenAgentCloseoutAsk(t *testing.T) {
	golden(t, "agent_closeout_ask", renderCloseoutAsk("简体中文", closeout.Example))
	golden(t, "agent_closeout_ask.no_language", renderCloseoutAsk("", closeout.Example))
}
