package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// What the extensions are told of the todos (todo_events.go, #5279): todo.changed for a created, deleted or changed
// todo (changes within a second are one), todo.decision for every decision written. All ids are placeholders.

// todoEventHub is a hub that carries extensions: it takes the todo events.
type todoEventHub struct {
	*fakeHub
	mu    sync.Mutex
	sent  []recordedKBEvent
	block chan struct{} // when set, the hub takes no event until it is closed
}

func (f *todoEventHub) TodoEvent(event string, params any) error {
	if f.block != nil {
		<-f.block
	}
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.sent = append(f.sent, recordedKBEvent{event, raw})
	f.mu.Unlock()
	return nil
}

type todoEventEnv struct {
	*todoEnv
	hub   *todoEventHub
	clock *fakeClock
}

// newTodoEventEnv is the assistant on a hub that takes todo events, st its store (nil: in memory), and a clock that
// runs the one-second wait and the thread posts' waits only when advanced.
func newTodoEventEnv(t *testing.T, st Store) *todoEventEnv {
	t.Helper()
	f := &todoEventHub{fakeHub: newFakeHub().withMachine()}
	opts := Options{Hub: f, Logf: testLogf(t)}
	if st != nil {
		opts.Store = func() Store { return st }
	}
	s := New(opts)
	clock := newFakeClock()
	clock.install(s)
	s.tevents.after = clock.After
	// Install before settings publish: a wall-clock callback left from setup can otherwise
	// re-arm the direction tree in the middle of a fake-clock scheduling assertion.
	s.directions.after, s.dtree.after = clock.After, clock.After
	s = withSettings(t, f.fakeHub, s)
	return &todoEventEnv{todoEnv: &todoEnv{t: t, s: s, f: f.fakeHub}, hub: f, clock: clock}
}

// told waits for the queue to reach the hub and returns what it has been told since the last call.
func (e *todoEventEnv) told() []recordedKBEvent {
	e.t.Helper()
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(time.Millisecond) {
		q := &e.s.tevents
		q.qmu.Lock()
		idle := !q.running && len(q.pending) == 0
		q.qmu.Unlock()
		if idle {
			e.hub.mu.Lock()
			defer e.hub.mu.Unlock()
			out := e.hub.sent
			e.hub.sent = nil
			return out
		}
	}
	e.t.Fatal("the todo events did not settle")
	return nil
}

// settle lets the second after a change pass and returns what the extensions were told by it.
func (e *todoEventEnv) settle() []recordedKBEvent {
	e.t.Helper()
	e.clock.advance(todoChangedSettle)
	return e.told()
}

func changedOf(t *testing.T, ev recordedKBEvent) assistantwire.TodoChangedEvent {
	t.Helper()
	var c assistantwire.TodoChangedEvent
	if ev.name != assistantwire.EventTodoChanged || json.Unmarshal(ev.raw, &c) != nil {
		t.Fatalf("not a todo.changed: %s %s", ev.name, ev.raw)
	}
	return c
}

func todoEventStores(t *testing.T, body func(t *testing.T, e *todoEventEnv, database *db.DB)) {
	t.Run("memory", func(t *testing.T) { body(t, newTodoEventEnv(t, nil), nil) })
	t.Run("store", func(t *testing.T) {
		database := todoTestDB(t)
		body(t, newTodoEventEnv(t, database), database)
	})
}

func TestTodoChangedEventsAreCoalescedAndOnlyForWhatMatters(t *testing.T) {
	todoEventStores(t, func(t *testing.T, e *todoEventEnv, _ *db.DB) {
		// Created: told once the second is over, not before.
		a := e.create("发版")
		if got := e.told(); len(got) != 0 {
			t.Fatalf("told inside the second: %+v", got)
		}
		got := e.settle()
		if len(got) != 1 || changedOf(t, got[0]) != (assistantwire.TodoChangedEvent{ID: a.ID, Title: "发版", What: "created"}) {
			t.Fatalf("created: %+v", got)
		}
		var fields map[string]any
		_ = json.Unmarshal(got[0].raw, &fields)
		if len(fields) != 3 { // id, title, what: no thread key for a todo with none
			t.Fatalf("fields: %s", got[0].raw)
		}

		// A burst on one todo (retitle twice, close it) and a second todo made: one event each, as they are at the end.
		e.must("PATCH", "/api/v1/todos/"+a.ID, `{"title":"发版 1.3"}`)
		e.must("PATCH", "/api/v1/todos/"+a.ID, `{"title":"发版 1.3.1"}`)
		e.must("PATCH", "/api/v1/todos/"+a.ID, `{"done":true}`)
		b := e.create("别的事")
		got = e.settle()
		if len(got) != 2 {
			t.Fatalf("a burst: %+v", got)
		}
		first, second := changedOf(t, got[0]), changedOf(t, got[1])
		if first.ID > second.ID {
			first, second = second, first
		}
		byID := map[string]assistantwire.TodoChangedEvent{first.ID: first, second.ID: second}
		if c := byID[a.ID]; c.Title != "发版 1.3.1" || c.What != "updated" || !c.Done {
			t.Fatalf("the changed todo: %+v", c)
		}
		if c := byID[b.ID]; c.Title != "别的事" || c.What != "created" {
			t.Fatalf("the new todo: %+v", c)
		}

		// Notes, and a todo back as it was, are not changes.
		e.must("PATCH", "/api/v1/todos/"+b.ID, `{"notes":"细节"}`)
		e.must("PATCH", "/api/v1/todos/"+b.ID, `{"title":"改一下"}`)
		e.must("PATCH", "/api/v1/todos/"+b.ID, `{"title":"别的事"}`)
		if got = e.settle(); len(got) != 0 {
			t.Fatalf("told of what is not a change: %+v", got)
		}

		// Reopened: a change (it is done no more).
		e.must("PATCH", "/api/v1/todos/"+a.ID, `{"done":false}`)
		if got = e.settle(); len(got) != 1 || changedOf(t, got[0]).ID != a.ID || changedOf(t, got[0]).Done {
			t.Fatalf("reopened (done is left out): %+v", got)
		}
		if strings.Contains(string(got[0].raw), `"done"`) {
			t.Fatalf("done is left out while the todo is open: %s", got[0].raw)
		}

		// Made and deleted inside one second: the extensions never hear of it.
		c := e.create("转瞬即逝")
		e.must("DELETE", "/api/v1/todos/"+c.ID, "")
		if got = e.settle(); len(got) != 0 {
			t.Fatalf("a todo that came and went: %+v", got)
		}
	})
}

func TestTodoChangedEventsTellTheThreadAndTheDeletion(t *testing.T) {
	todoEventStores(t, func(t *testing.T, e *todoEventEnv, _ *db.DB) {
		a := e.create("发版").ID
		e.settle()
		th := assistantwire.TodoEventThread{Channel: "feishu", ChatID: "oc_example_1", ThreadID: "omt_example_1", Title: "Release 1.3"}

		e.must("PUT", "/api/v1/todos/"+a+"/thread", `{"channel":"feishu","chat_id":"oc_example_1","thread_id":"omt_example_1","title":"Release 1.3"}`)
		got := e.settle()
		if len(got) != 1 {
			t.Fatalf("bound: %+v", got)
		}
		if c := changedOf(t, got[0]); c.ID != a || c.What != "updated" || c.Title != "发版" || c.Thread == nil || *c.Thread != th {
			t.Fatalf("bound: %+v", c)
		}

		// Only its title changes: still a change to tell.
		e.must("PUT", "/api/v1/todos/"+a+"/thread", `{"channel":"feishu","chat_id":"oc_example_1","thread_id":"omt_example_1","title":"Release 1.3.1"}`)
		if got = e.settle(); len(got) != 1 || changedOf(t, got[0]).Thread.Title != "Release 1.3.1" {
			t.Fatalf("retitled thread: %+v", got)
		}
		// The same binding again is not.
		e.must("PUT", "/api/v1/todos/"+a+"/thread", `{"channel":"feishu","chat_id":"oc_example_1","thread_id":"omt_example_1","title":"Release 1.3.1"}`)
		if got = e.settle(); len(got) != 0 {
			t.Fatalf("the same binding: %+v", got)
		}

		// Unbound: the event has no thread.
		e.must("DELETE", "/api/v1/todos/"+a+"/thread", "")
		if got = e.settle(); len(got) != 1 || changedOf(t, got[0]).Thread != nil {
			t.Fatalf("unbound: %+v", got)
		}

		// Deleted: its last title and thread are in the event.
		e.must("PUT", "/api/v1/todos/"+a+"/thread", `{"channel":"feishu","chat_id":"oc_example_1","thread_id":"omt_example_1","title":"Release 1.3"}`)
		e.must("PATCH", "/api/v1/todos/"+a, `{"title":"发版 1.3"}`)
		e.settle()
		e.must("DELETE", "/api/v1/todos/"+a, "")
		got = e.settle()
		if len(got) != 1 {
			t.Fatalf("deleted: %+v", got)
		}
		if c := changedOf(t, got[0]); c.ID != a || c.What != "deleted" || c.Title != "发版 1.3" || c.Thread == nil || *c.Thread != th {
			t.Fatalf("deleted: %+v", c)
		}
	})
}

// The todos found when they are loaded are not "created": only what changes after is told, and not before they are loaded.
func TestTodoChangedEventsStartFromTheLoadedTodos(t *testing.T) {
	database := todoTestDB(t)
	before := newTodoEnv(t, database)
	old := before.create("早就有的").ID
	other := before.create("另一件").ID

	e := newTodoEventEnv(t, database)
	if got := e.settle(); len(got) != 0 {
		t.Fatalf("told before anything happened: %+v", got)
	}
	e.list() // loads the todos
	if got := e.settle(); len(got) != 0 {
		t.Fatalf("told of the todos as found: %+v", got)
	}
	e.must("PATCH", "/api/v1/todos/"+old, `{"title":"早就有的，改过"}`)
	got := e.settle()
	if len(got) != 1 {
		t.Fatalf("after loading: %+v", got)
	}
	if c := changedOf(t, got[0]); c.ID != old || c.What != "updated" || c.ID == other {
		t.Fatalf("after loading: %+v", c)
	}
}

func decisionOf(t *testing.T, ev recordedKBEvent) assistantwire.TodoDecisionEvent {
	t.Helper()
	var d assistantwire.TodoDecisionEvent
	if ev.name != assistantwire.EventTodoDecision || json.Unmarshal(ev.raw, &d) != nil {
		t.Fatalf("not a todo.decision: %s %s", ev.name, ev.raw)
	}
	return d
}

func TestTodoDecisionEventsFollowEveryDecisionWritten(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEventEnv(t, database)
	a := e.create("发版").ID
	e.settle()

	// By hand: at once (no waiting for a second), with the decision's own fields and no thread for an unbound todo.
	var res struct{ Decision db.TodoDecisionRow }
	if err := json.Unmarshal(e.must("POST", "/api/v1/todos/"+a+"/decisions", `{"question":"从哪个分支切","choice":"release-1.3","reason":"补丁已经在里面"}`).Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	got := e.told()
	if len(got) != 1 {
		t.Fatalf("a decision by hand: %+v", got)
	}
	d := decisionOf(t, got[0])
	want := assistantwire.TodoEventDecision{ID: res.Decision.ID, Question: "从哪个分支切", Choice: "release-1.3", Reason: "补丁已经在里面", Source: "manual"}
	if d.ID != a || d.Title != "发版" || d.Thread != nil || d.Decision != want {
		t.Fatalf("decision: %+v (want %+v)", d, want)
	}
	var fields map[string]any
	_ = json.Unmarshal(got[0].raw, &fields)
	if len(fields) != 3 { // id, title, decision
		t.Fatalf("fields: %s", got[0].raw)
	}

	// Bound to a thread, and one that supersedes: the thread is in it, and every decision is one event, not merged.
	e.must("PUT", "/api/v1/todos/"+a+"/thread", `{"channel":"feishu","chat_id":"oc_example_1","thread_id":"omt_example_1","title":"Release 1.3"}`)
	e.settle()
	for i, q := range []string{"一", "二", "三"} {
		body := `{"question":"` + q + `","choice":"选` + q + `"`
		if i == 2 {
			body += `,"supersedes_id":"` + res.Decision.ID + `"`
		}
		e.must("POST", "/api/v1/todos/"+a+"/decisions", body+"}")
	}
	got = e.told()
	if len(got) != 3 {
		t.Fatalf("three decisions: %+v", got)
	}
	for i, ev := range got {
		d := decisionOf(t, ev)
		if d.Thread == nil || d.Thread.ThreadID != "omt_example_1" || d.Decision.Question != []string{"一", "二", "三"}[i] {
			t.Fatalf("decision %d: %+v", i, d)
		}
	}
	if decisionOf(t, got[2]).Decision.SupersedesID != res.Decision.ID {
		t.Fatalf("supersedes: %s", got[2].raw)
	}

	// The thread's own reply is a decision like the rest: told, with its source (an extension decides about echoing).
	st := e.s.todoDB()
	ok, err := e.s.insertDecision(context.Background(), st, db.TodoDecisionRow{ID: "dec_thread_1", TodoID: a, Question: "回复", Choice: "好",
		Source: db.DecisionThread, CreatedAt: decisionNow()})
	if err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	got = e.told()
	if len(got) != 1 || decisionOf(t, got[0]).Decision.Source != "thread" {
		t.Fatalf("a decision from the thread: %+v", got)
	}

	// A decision that was not written (the same id again) is not told; marking one superseded is not a new decision.
	if ok, _ := e.s.insertDecision(context.Background(), st, db.TodoDecisionRow{ID: "dec_thread_1", TodoID: a, Question: "回复", Choice: "好",
		Source: db.DecisionThread, CreatedAt: decisionNow()}); ok {
		t.Fatal("the same decision was written twice")
	}
	e.must("PATCH", "/api/v1/todos/"+a+"/decisions/"+res.Decision.ID, `{"supersedes_id":""}`)
	if got = e.told(); len(got) != 0 {
		t.Fatalf("told of what was not a new decision: %+v", got)
	}
	// Decisions bump the todo's updated_at, which is no change todo.changed tells.
	if got = e.settle(); len(got) != 0 {
		t.Fatalf("decisions made todo.changed: %+v", got)
	}
}

// A hub with no extensions (the in-process tests' fake, an embedder without them) gets nothing and nothing waits.
func TestTodoEventsAreOffWithoutAPublisher(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.create("发版")
	if e.s.todoEventsOn() {
		t.Fatal("the fake hub takes todo events")
	}
	e.s.tevents.mu.Lock()
	defer e.s.tevents.mu.Unlock()
	if e.s.tevents.armed {
		t.Fatal("a flush was scheduled with nobody to tell")
	}
}

// A backlog (the hub slow to take events) is at most one todo.changed a todo, a todo made and deleted before the hub
// took the first is none, and past the limit the oldest goes.
func TestTodoEventBacklogIsMergedAndBounded(t *testing.T) {
	f := &todoEventHub{fakeHub: newFakeHub().withMachine(), block: make(chan struct{})}
	s := withSettings(t, f.fakeHub, New(Options{Hub: f, Logf: testLogf(t)}))
	released := false
	release := func() {
		if !released {
			released = true
			close(f.block)
		}
	}
	defer release()
	s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{ID: "todo_example_first", Title: "x", What: "created"}})
	// The worker holds that first one at the hub (it has left the queue when this sees it empty); what comes now waits.
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(time.Millisecond) {
		s.tevents.qmu.Lock()
		taken := len(s.tevents.pending) == 0
		s.tevents.qmu.Unlock()
		if taken {
			break
		}
	}
	const todos = 400
	for i := range todos {
		id := fmt.Sprintf("todo_example_%d", i)
		for _, what := range []string{assistantwire.TodoCreated, assistantwire.TodoUpdated, assistantwire.TodoUpdated} {
			s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{ID: id, Title: "t", What: what, Done: what == "updated"}})
		}
		if i%2 == 1 {
			s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{ID: id, Title: "t", What: assistantwire.TodoDeleted}})
		}
	}
	q := &s.tevents
	q.qmu.Lock()
	n, dropped := len(q.pending), q.dropped
	q.qmu.Unlock()
	if n != todos/2 || dropped {
		t.Fatalf("%d events wait for %d todos (half deleted unseen), dropped %v", n, todos, dropped)
	}
	q.qmu.Lock()
	for _, p := range q.pending {
		if c := p.params.(assistantwire.TodoChangedEvent); c.What != assistantwire.TodoCreated || !c.Done {
			t.Errorf("not one created event with the latest state: %+v", c)
		}
	}
	q.pending = nil
	q.qmu.Unlock()

	for i := range todoEventLimit + 5 {
		s.todoQueue(todoNotice{assistantwire.EventTodoDecision, assistantwire.TodoDecisionEvent{ID: "todo_example_1", Title: "t",
			Decision: assistantwire.TodoEventDecision{ID: fmt.Sprintf("dec_example_%d", i), Source: "manual"}}})
	}
	q.qmu.Lock()
	n, dropped = len(q.pending), q.dropped
	first := q.pending[0].params.(assistantwire.TodoDecisionEvent).Decision.ID
	q.qmu.Unlock()
	if n != todoEventLimit || !dropped || first != "dec_example_5" {
		t.Fatalf("past the limit: %d waiting, dropped %v, the oldest %s", n, dropped, first)
	}
	release()
	e := &todoEventEnv{todoEnv: &todoEnv{t: t, s: s, f: f.fakeHub}, hub: f}
	e.told()
	q.qmu.Lock()
	defer q.qmu.Unlock()
	if q.dropped {
		t.Fatal("the dropped mark stays after the queue is empty")
	}
}
