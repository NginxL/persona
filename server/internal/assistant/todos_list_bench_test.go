package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The to-do list as it grows: what GET /api/v1/todos and the supervision pass cost with n finished supervised tasks
// (#5995), and how many {"type":"todos"} events one task's life sends the apps.

// manyTodos is a manager holding n finished supervised tasks from the main conversation, each on a brief of its own,
// except that every fourth one took over the one before it (two to-dos on one brief, a lineage of two steps). All its
// times are fixed: the same n gives the same list, byte for byte.
func manyTodos(n int) *Service { return manyTodosOn(n, Options{Hub: newFakeHub()}) }

// manyTodosOn is manyTodos on a manager made with o (a fleet, a store), its log quiet.
func manyTodosOn(n int, o Options) *Service {
	o.Logf = func(string, ...any) {}
	s := New(o)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	b := &s.todos
	b.mu.Lock()
	b.todos = make(map[string]*Todo, n)
	b.activeBySession, b.spacesByKey = map[[2]string]string{}, map[string]string{}
	b.loaded = true
	tb := &s.tbriefs
	tb.mu.Lock()
	tb.init()
	brief := ""
	for i := range n {
		id := fmt.Sprintf("todo_bench%06d", i)
		at := base.Add(time.Duration(i) * time.Minute)
		done := at.Add(40 * time.Minute)
		b.todos[id] = &Todo{ID: id, Title: fmt.Sprintf("fix the login crash %d", i), Notes: "the login page crashes in landscape",
			CreatedAt: at, UpdatedAt: done, DoneAt: &done, Origin: "conv1", DoneBy: "review", DoneReason: "merged and covered by a UI test",
			Spaces: []TodoSpace{}, Stage: "merge", PRURL: fmt.Sprintf("https://github.com/o/r/pull/%d", i), StageKind: "code",
			Agents: []TodoAgent{{MachineID: "box", SessionID: fmt.Sprintf("s%06d", i), LinkedAt: at, Title: "fix the login crash",
				Cwd: "/code/app", Agent: "claude", Source: "plan"}},
			Check: &TodoCheck{Verdict: "done", Reason: "PR merged, test added", At: done, WorkSince: at},
			Supervision: &Supervision{Supervisor: "conv1", DoneWhen: []string{"PR merged to main", "a test covers landscape"},
				ExpectMinutes: 30, State: reviewAccepted, UpdatedAt: done, Reviews: []TaskReview{{At: done, Verdict: reviewAccepted,
					Note: "merged and covered by a UI test", Checks: []ReviewCheck{{Criterion: "PR merged to main", Met: true,
						Evidence: "merged at 3f2c1ab"}, {Criterion: "a test covers landscape", Met: true, Evidence: "LoginLandscapeUITests"}}}}}}
		link := taskBriefLink{At: at}
		if i%4 == 3 {
			link.Brief, link.Continues, link.Reason = brief, fmt.Sprintf("todo_bench%06d", i-1), "the first agent stopped"
		} else {
			brief = fmt.Sprintf("brief_bench%06d", i)
			link.Brief = brief
			req := &BriefRequest{Text: "the login page crashes in landscape on the iPad, please fix it and add a test", At: at}
			tb.briefs[brief] = &taskBrief{rec: briefRecord{ID: brief, CreatedAt: at, StartedBy: briefByOwner, Conversation: "conv1",
				Request: req, Goal: "the login page no longer crashes in landscape",
				DoneWhen: []string{"PR merged to main", "a test covers landscape"}, ExpectMinutes: 30}}
		}
		tb.links[id] = link
	}
	tb.mu.Unlock()
	b.mu.Unlock()
	return s
}

func todosListBody(tb testing.TB, s *Service) []byte { return todosListBodyAs(tb, s, "") }

// todosListBodyAs is GET /api/v1/todos as role reads it ("" a device).
func todosListBodyAs(tb testing.TB, s *Service, role string) []byte {
	r := httptest.NewRequest("GET", "/api/v1/todos", nil)
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		tb.Fatalf("GET /api/v1/todos = %d %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// benchTodosManager is manyTodos's n tasks on a manager made with o (its hub a fake one), each handed out and
// supervised by a conversation of its own, whose spaces the manager noted. Listed: the assistant's computer lists them
// all (and they were noted from its list, as SessionsChanged notes them); else it lists only the main conversation,
// and the others' spaces are told by what the manager noted of them (conversation_space.go), as for chats quiet for
// more than a week.
func benchTodosManager(b testing.TB, n int, o Options, listed bool) *Service {
	f := newFakeHub().withMachine()
	if h, ok := o.Hub.(*todoFlushHub); ok {
		h.fakeHub = f
	} else {
		o.Hub = f
	}
	s := manyTodosOn(n, o)
	chats := []proto.Session{{SessionID: "conv1", Title: "Persona", Assistant: true, Status: proto.StatusIdle}}
	var noted []db.ConversationSpace
	s.todos.mu.Lock()
	for _, t := range s.todos.todos {
		chat := "chat_" + strings.TrimPrefix(t.ID, "todo_")
		t.Origin, t.Supervision.Supervisor = chat, chat
		chats = append(chats, proto.Session{SessionID: chat, Title: "A chat", Assistant: true, Status: proto.StatusIdle})
		noted = append(noted, db.ConversationSpace{MachineID: "server", SessionID: chat})
	}
	s.todos.mu.Unlock()
	if listed {
		f.setSessions("server", chats...)
		s.conversationSpacesListed("server", chats)
		waitFor(b, "the listed conversations' spaces to be noted", s.listedSpacesWritten)
	} else {
		f.setSessions("server", chats[0])
		s.noteConversationSpaces(context.Background(), noted)
	}
	return s
}

// BenchmarkTodosList: GET /api/v1/todos with n finished supervised tasks, each handed out and supervised by a
// conversation of its own (benchTodosManager). ext/…: as an extension's token reads it, which to-dos it is not shown
// decided (todo_scope.go): every rule's read made, the conversations told by one read of the assistant's computer, as
// they are listed outside the health space, so every to-do is listed. db/…: with Postgres as the store and four feed
// items a to-do, which ext,db/… reads the conversations of (FeedConversations); skipped without
// PERSONA_TEST_DATABASE_URL. quiet: the computer lists none of the chats, which the spaces the manager noted tell
// (from its book; cold: from the store, as the first read after a restart does).
func BenchmarkTodosList(b *testing.B) {
	for _, role := range []string{"", roleExtension} {
		for _, n := range []int{250, 1000, 4000} {
			name := fmt.Sprintf("todos=%d", n)
			if role != "" {
				name = "ext/" + name
			}
			b.Run(name, func(b *testing.B) {
				s := benchTodosManager(b, n, Options{}, true)
				b.ReportAllocs()
				for b.Loop() {
					todosListBodyAs(b, s, role)
				}
			})
		}
	}
	b.Run("ext,quiet/todos=1000", func(b *testing.B) {
		s := benchTodosManager(b, 1000, Options{}, false)
		b.ReportAllocs()
		for b.Loop() {
			todosListBodyAs(b, s, roleExtension)
		}
	})
	for _, c := range []struct {
		role        string
		quiet, cold bool
	}{{"", false, false}, {roleExtension, false, false}, {roleExtension, true, false}, {roleExtension, true, true}} {
		name := "db/todos=1000"
		switch {
		case c.cold:
			name = "ext,quiet,cold," + name
		case c.quiet:
			name = "ext,quiet," + name
		case c.role != "":
			name = "ext," + name
		}
		b.Run(name, func(b *testing.B) {
			database := todoTestDB(b)
			s := benchTodosManager(b, 1000, Options{Store: func() Store { return database }}, !c.quiet)
			for i := range 1000 {
				from := fmt.Sprintf("todo_bench%06d", i)
				id := todoCopyForBench(s, from) // the feed is never pruned: each run writes and reads its own
				s.todos.mu.Lock()
				delete(s.todos.todos, from)
				s.todos.mu.Unlock()
				todoFeedForBench(b, s, id, 4, false)
			}
			b.ReportAllocs()
			for b.Loop() {
				if c.cold {
					s.convSpaces.mu.Lock()
					s.convSpaces.known = nil
					s.convSpaces.mu.Unlock()
				}
				todosListBodyAs(b, s, c.role)
			}
		})
	}
}

// todoFlushHub is a hub that takes the to-do events and drops them: a hub with no extension installed.
type todoFlushHub struct{ *fakeHub }

func (todoFlushHub) TodoEvent(string, any) error { return nil }

// BenchmarkTodoChangedFlush: one flush of what the extensions are told of the to-dos (todoChangedFlush), with 1000
// finished supervised tasks (benchTodosManager), changed=k of them retitled since the flush before: what each change of
// the to-dos costs the manager even with no extension installed, whether each changed one is shown judged
// (todo_scope.go). quiet: the computer lists none of their chats, which the spaces the manager noted tell. db: with
// Postgres as the store and four feed items a to-do; skipped without PERSONA_TEST_DATABASE_URL.
func BenchmarkTodoChangedFlush(b *testing.B) {
	for _, c := range []struct {
		changed       int
		quiet, onDisk bool
	}{{1, false, false}, {100, false, false}, {1, true, false}, {100, true, false}, {1, true, true}, {100, true, true}} {
		name := fmt.Sprintf("changed=%d", c.changed)
		if c.quiet {
			name = "quiet/" + name
		}
		if c.onDisk {
			name = "db," + name
		}
		b.Run(name, func(b *testing.B) {
			o := Options{Hub: &todoFlushHub{}}
			if c.onDisk {
				database := todoTestDB(b)
				o.Store = func() Store { return database }
			}
			s := benchTodosManager(b, 1000, o, !c.quiet)
			if c.onDisk {
				for i := range 1000 {
					from := fmt.Sprintf("todo_bench%06d", i)
					id := todoCopyForBench(s, from)
					s.todos.mu.Lock()
					delete(s.todos.todos, from)
					s.todos.mu.Unlock()
					todoFeedForBench(b, s, id, 4, false)
				}
			}
			s.todos.mu.Lock()
			ids := make([]string, 0, len(s.todos.todos))
			for id := range s.todos.todos {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			s.todoMarksBaseline(s.todos.todos)
			s.todos.mu.Unlock()
			round := 0
			b.ReportAllocs()
			for b.Loop() {
				round++
				s.todos.mu.Lock()
				for _, id := range ids[:c.changed] {
					s.todos.todos[id].Title = fmt.Sprintf("fix the login crash, round %d", round)
				}
				s.todos.mu.Unlock()
				s.todoChangedFlush()
			}
		})
	}
}

// BenchmarkTodosView: the To-dos view's part of GET /api/v1/todos (todo_view_routes.go: every row's view and the
// list's counts over the listed rows), with n finished supervised tasks as BenchmarkTodosList has them; open=n with
// every one of them open again and its run's session listed, so each reads its run, the pass at its dearest. The
// sessions come from a device call there; fleet/… reads them as a manager that carries the fleet does (production):
// the computer lists the open runs and as many other sessions, and its archive keeps records rows it no longer lists.
func BenchmarkTodosView(b *testing.B) {
	run := func(b *testing.B, s *Service) {
		ctx := context.Background()
		list, err := s.listedTodos(ctx, true)
		if err != nil {
			b.Fatal(err)
		}
		now := time.Now()
		b.ReportAllocs()
		for b.Loop() {
			s.viewed(ctx, list, now).counts()
		}
	}
	for _, n := range []int{250, 1000, 4000} {
		for _, open := range []bool{false, true} {
			name := fmt.Sprintf("todos=%d", n)
			if open {
				name = fmt.Sprintf("open=%d", n)
			}
			b.Run(name, func(b *testing.B) {
				s := manyTodos(n)
				if open {
					s.hub.(*fakeHub).setSessions("box", reopenForBench(s)...)
				}
				run(b, s)
			})
		}
	}
	for _, records := range []int{0, 4000} {
		b.Run(fmt.Sprintf("fleet/open=1000/archive=%d", records), func(b *testing.B) {
			run(b, fleetTodosForBench(b, 1000, records))
		})
	}
	b.Run("fleet/open=1000/cards=200", func(b *testing.B) {
		s := fleetTodosForBench(b, 1000, 0)
		holdCardsForBench(s, 100)
		run(b, s)
	})
}

// holdCardsForBench puts up a card on every tenth of fleetTodosForBench's first n*10 runs, and one on each of n
// sessions no to-do links: a read hands each run its own card and copies none of the others.
func holdCardsForBench(s *Service, n int) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for i := range n {
		for _, sid := range []string{fmt.Sprintf("s%06d", i*10), fmt.Sprintf("other%05d", i)} {
			ix := proto.Interaction{ID: "ix_" + sid, MachineID: "box", SessionID: sid, Kind: proto.IxQuestion,
				CreatedAt: at, ExpiresAt: at.Add(time.Hour), Questions: []proto.Question{{Question: "Which test first?",
					Options: []proto.QuestionOption{{Label: "The landscape test"}, {Label: "The rotation test"}}}}}
			s.ixMapLocked()[ix.ID] = &pending{ix: ix}
		}
	}
}

// reopenForBench opens every to-do of manyTodos again, accepted and not seen, and answers its run's session, idle on
// its computer.
func reopenForBench(s *Service) []proto.Session {
	var sessions []proto.Session
	s.todos.mu.Lock()
	for _, t := range s.todos.todos {
		t.DoneAt, t.DoneBy, t.DoneReason = nil, "", ""
		for _, a := range t.Agents {
			sessions = append(sessions, proto.Session{MachineID: a.MachineID, SessionID: a.SessionID, Title: a.Title,
				Status: proto.StatusIdle, State: proto.StateIdle, StateSince: t.UpdatedAt, UpdatedAt: t.UpdatedAt})
		}
	}
	s.todos.mu.Unlock()
	return sessions
}

// fleetTodosForBench is manyTodos's n tasks, open again (reopenForBench), on a manager that carries the fleet: its
// computer lists their runs and n sessions no to-do links, and its archive keeps records rows the computer no longer
// lists, as months of work leave them.
func fleetTodosForBench(b *testing.B, n, records int) *Service {
	d := newFleetDevs()
	d.add("box", "Computer")
	st := newFleetMem()
	s := manyTodosOn(n, Options{Hub: newFakeHub(), Devices: d, Store: func() Store { return st }})
	archive := map[string]agentArchive{}
	for i := range records {
		at := time.Unix(1600000000+int64(i), 0)
		sess := archiveSnapshot(proto.Session{MachineID: "box", SessionID: fmt.Sprintf("a%05d", i), Title: "An archived task",
			Cwd: "/code/example", Agent: "claude", UpdatedAt: at, LastText: strings.Repeat("x", 180)})
		archive[runtimeListID("box", sess.SessionID)] = agentArchive{Session: sess, Archived: true, ChangedAt: at}
	}
	if err := st.PutSetting(context.Background(), agentArchivesKey, archive); err != nil {
		b.Fatal(err)
	}
	list := reopenForBench(s)
	for i := range n {
		list = append(list, proto.Session{SessionID: fmt.Sprintf("other%05d", i), Cwd: "/code/example", Agent: "claude",
			Status: proto.StatusIdle, Title: "Example task", LastText: strings.Repeat("Example result. ", 32)})
	}
	d.setSessions("box", list)
	return s
}

// BenchmarkSuperviseWatch: one pass of the supervision loop (every superviseTick) over n finished supervised tasks.
func BenchmarkSuperviseWatch(b *testing.B) {
	for _, n := range []int{250, 4000} {
		b.Run(fmt.Sprintf("todos=%d", n), func(b *testing.B) {
			s := manyTodos(n)
			ctx, now := context.Background(), time.Now()
			b.ReportAllocs()
			for b.Loop() {
				s.superviseWatch(ctx, now)
			}
		})
	}
}

// todoFeedForBench gives to-do id n items in its origin conversation's feed, as a long task leaves them: in each four
// the owner's instruction, two turns of its run and a review, a minute apart; old, turns written before #10465, which
// name their run's computer only, so that the timeline reads the computers' names. Into s's store when it has one.
func todoFeedForBench(tb testing.TB, s *Service, id string, n int, old bool) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	st := s.store()
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	for i := range n {
		var kind string
		var body any
		switch i % 4 {
		case 0:
			kind, body = feedRouted, map[string]string{"text": "also cover the rotation case, then merge it"}
		case 1, 2:
			r := receiptBody{Title: "fix the login crash", State: "done", Machine: "box",
				Summary: strings.Repeat("Fixed the crash in landscape and added a test. ", 12)}
			if !old {
				r.MachineID, r.SessionID = "box", "s000000"
			}
			kind, body = feedReceipt, r
		default:
			kind, body = feedReview, reviewFeedBody{Title: "fix the login crash", Verdict: reviewRework, Round: i / 4,
				Note: "the rotation case still crashes"}
		}
		raw, _ := json.Marshal(body)
		it := FeedItem{ID: fmt.Sprintf("feed_%s%06d", strings.TrimPrefix(id, "todo_"), i), Conversation: "conv1",
			At: base.Add(time.Duration(i) * time.Minute), Kind: kind, Ref: id, Body: raw}
		if st == nil {
			s.tasks.feed["conv1"] = append(s.tasks.feed["conv1"], it)
		} else if err := st.AppendFeed(context.Background(), db.FeedRow{ID: it.ID, Conversation: it.Conversation, At: it.At,
			Kind: it.Kind, Ref: it.Ref, Body: it.Body}); err != nil {
			tb.Fatal(err)
		}
	}
}

// todoCopyForBench puts manyTodos's to-do from on s again under a fresh id, which it answers: a database keeps the feed
// rows a run writes (the feed is never pruned), so each run reads only its own.
func todoCopyForBench(s *Service, from string) string {
	id := "todo_" + randomHex(8)
	s.todos.mu.Lock()
	t := *s.todos.todos[from]
	t.ID = id
	s.todos.todos[id] = &t
	s.todos.mu.Unlock()
	s.tbriefs.mu.Lock()
	s.tbriefs.links[id] = s.tbriefs.links[from]
	s.tbriefs.mu.Unlock()
	return id
}

// BenchmarkTodoItem: GET /api/v1/todos/{id} with 250 finished supervised tasks (manyTodos), the one read having feed
// items in its conversation: the item route works the view out over the whole list, and with todo_timeline (#10465)
// reads that to-do's history too, at most todoTimelineFeedCap items of it. old: turns from before #10465, which also
// read the computers' names, on a manager that carries the fleet as a deployed one does. db: the feed and the
// decisions read from Postgres (FeedByRef, ListTodoDecisions), as on a hub with a database; skipped without
// PERSONA_TEST_DATABASE_URL.
func BenchmarkTodoItem(b *testing.B) {
	for _, c := range []struct {
		name          string
		feed          int
		old, postgres bool
	}{{"feed=0", 0, false, false}, {"feed=1000", 1000, false, false}, {"feed=1000,old", 1000, true, false},
		{"feed=1000,db", 1000, false, true}} {
		b.Run(c.name, func(b *testing.B) {
			o := Options{Hub: newFakeHub()}
			if c.old {
				d := newFleetDevs()
				d.add("box", "box")
				o.Devices = d
			}
			if c.postgres {
				database := todoTestDB(b)
				o.Store = func() Store { return database }
			}
			s := manyTodosOn(250, o)
			id := "todo_bench000000"
			if c.postgres {
				id = todoCopyForBench(s, id)
			}
			todoFeedForBench(b, s, id, c.feed, c.old)
			b.ReportAllocs()
			for b.Loop() {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/todos/"+id, nil))
				if w.Code != 200 {
					b.Fatalf("GET /api/v1/todos/{id} = %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Every to-do in the list carries the brief and lineage it has on its own (GET /api/v1/todos/{id} reads them one by
// one), take-overs included.
func TestTodosListLineageAsEachHasIt(t *testing.T) {
	s := manyTodos(40)
	var list struct {
		Todos []Todo `json:"todos"`
	}
	if err := json.Unmarshal(todosListBody(t, s), &list); err != nil || len(list.Todos) != 40 {
		t.Fatalf("list: %d to-dos, %v", len(list.Todos), err)
	}
	steps := 0
	for _, got := range list.Todos {
		raw, err := s.todoView(context.Background(), got.ID)
		if err != nil {
			t.Fatal(err)
		}
		var one Todo
		if err := json.Unmarshal(raw, &one); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(struct {
			B *TaskBrief
			L []LineageStep
		}{got.Brief, got.Lineage})
		w, _ := json.Marshal(struct {
			B *TaskBrief
			L []LineageStep
		}{one.Brief, one.Lineage})
		if string(a) != string(w) {
			t.Fatalf("%s in the list:\n%s\non its own:\n%s", got.ID, a, w)
		}
		if got.Brief == nil {
			t.Fatalf("%s has no brief", got.ID)
		}
		steps += len(got.Lineage)
	}
	// 20 to-dos alone on their brief (one step each), 10 pairs on one (two steps for each of the two).
	if steps != 20+10*2*2 {
		t.Fatalf("%d lineage steps in all", steps)
	}
}

// ---- one task's {"type":"todos"} events ----

// taskStepEvents runs one supervised task through the real tools (task_plan with a brief, the agent's turns, one
// rework round, accepted) and answers how many {"type":"todos"} events the apps got for each step, each counted once
// the step's events stopped coming.
func taskStepEvents(tb testing.TB) (steps []string, events []int) {
	tb.Helper()
	s, f, _ := briefService(tb, "the login page crashes in landscape on the iPad, please fix it and add a test")
	presenceTimes(tb, 0, 0)
	f.judge = `{"verdict":"done","reason":"PR merged, test added"}`
	todosSent := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, e := range f.events {
			if e == "todos" {
				n++
			}
		}
		return n
	}
	last := 0
	step := func(name string) {
		// The step's writes go on in the background (the leader's link, the judge's answer): its events are in once
		// none came for a while.
		n, quiet := todosSent(), time.Now()
		for time.Since(quiet) < 400*time.Millisecond {
			time.Sleep(10 * time.Millisecond) // sleep-ok: an event count is final only after a quiet stretch
			if m := todosSent(); m != n {
				n, quiet = m, time.Now()
			}
		}
		steps, events = append(steps, name), append(events, n-last)
		last = n
	}
	if out, bad := tool(tb, s, "task_plan", briefTask(nil)); bad {
		tb.Fatalf("task_plan: %s", out)
	}
	waitFor(tb, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	f.setItems()
	step("task_plan: to-do made, agent started and linked")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	step("the agent starts its turn")
	agentTurn(s, f, proto.StatusDone, "Fixed the crash. PR https://github.com/o/r/pull/901 is open with a landscape UI test.", 0)
	waitFor(tb, "pending review", func() bool { v := todoNow(s, todo.ID).Supervision; return v != nil && v.State == reviewPending })
	step("its turn ends: judged, waits for review")
	if out, bad := tool(tb, s, "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "merge it",
		"checks": []ReviewCheck{{Criterion: "PR merged to main", Met: false, Evidence: "open"}}}); bad {
		tb.Fatalf("rework: %s", out)
	}
	step("task_review rework")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	step("the agent starts its turn")
	agentTurn(s, f, proto.StatusDone, "Merged https://github.com/o/r/pull/901 into main.", 0)
	waitFor(tb, "pending review again", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	step("its turn ends: judged, waits for review")
	if out, bad := tool(tb, s, "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "merged",
		"checks": []ReviewCheck{{Criterion: "PR merged to main", Met: true, Evidence: "https://github.com/o/r/pull/901 merged"},
			{Criterion: "a test covers landscape", Met: true, Evidence: "the PR's landscape UI test"}}}); bad {
		tb.Fatalf("accepted: %s", out)
	}
	step("task_review accepted: to-do closed")
	if todoNow(s, todo.ID).DoneAt == nil {
		tb.Fatal("the task did not close")
	}
	return steps, events
}

// BenchmarkTodosEventsOneTask: the {"type":"todos"} events one supervised task sends every app (each one a full read
// of GET /api/v1/todos), as todos-events/op. Run with -benchtime=1x: one task takes a few seconds.
func BenchmarkTodosEventsOneTask(b *testing.B) {
	total := 0
	for b.Loop() {
		_, events := taskStepEvents(b)
		for _, n := range events {
			total += n
		}
	}
	b.ReportMetric(float64(total)/float64(b.N), "todos-events/op")
}

// Each step of a task changes the list once as the apps see it, and sends them one {"type":"todos"} event: the writes
// of a step (the to-do, its report, its check, its supervision, its leader) are told together.
func TestTodosEventsOnePerTaskStep(t *testing.T) {
	steps, events := taskStepEvents(t)
	for i, n := range events {
		t.Logf("  %-48s %d", steps[i], n)
		if n != 1 {
			t.Errorf("%s: %d todos events, want 1", steps[i], n)
		}
	}
}

// The {"type":"todos"} of a write waits todosSettle for the rest of it; a change from the moment it goes out, even
// while it does, sends another: every change is followed by an event sent after it.
func TestTodosEventWaitsForTheRestOfTheWrite(t *testing.T) {
	f := newFakeHub()
	s := New(Options{Hub: f, Logf: testLogf(t)})
	clock := newFakeClock()
	s.tpush.after = clock.After
	sent := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, e := range f.events {
			if e == "todos" {
				n++
			}
		}
		return n
	}
	ctx := context.Background()
	for range 5 {
		s.todosChanged(ctx)
	}
	if n := sent(); n != 0 {
		t.Fatalf("%d todos events before the write settled, want 0", n)
	}
	clock.advance(todosSettle - time.Millisecond)
	if n := sent(); n != 0 {
		t.Fatalf("%d todos events before todosSettle, want 0", n)
	}
	clock.advance(time.Millisecond)
	if n := sent(); n != 1 {
		t.Fatalf("five changes in one write sent %d todos events, want 1", n)
	}
	clock.advance(time.Hour)
	if n := sent(); n != 1 {
		t.Fatalf("%d todos events with nothing changed since, want 1", n)
	}

	// A change made while the event goes out (the hub's Broadcast running) is told by one of its own after it.
	s.todosChanged(ctx)
	f.mu.Lock()
	f.onBroadcast = func() { s.todosChanged(ctx) }
	f.mu.Unlock()
	clock.advance(todosSettle)
	f.mu.Lock()
	f.onBroadcast = nil
	f.mu.Unlock()
	if n := sent(); n != 2 {
		t.Fatalf("%d todos events, want 2", n)
	}
	if clock.pending() != 1 {
		t.Fatalf("%d events waiting after a change made while one went out, want 1", clock.pending())
	}
	clock.advance(todosSettle)
	if n := sent(); n != 3 {
		t.Fatalf("%d todos events, want 3", n)
	}
}
