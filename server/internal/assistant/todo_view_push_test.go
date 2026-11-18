package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos view stays fresh (todo_view_push.go, #10226): the apps are told to read the list again
// ({"type":"todos"}) when what a row is filed under changes with no write to its to-do, because one of its runs
// changed or time alone moved it; never for a working run's every step.

// todoViewClock puts the views' clock, the pushes' settle and the direction tree on one fake clock, now.
func todoViewClock(e *todoEnv) *fakeClock {
	c := &fakeClock{now: time.Now()}
	e.s.tview.now, e.s.tview.after = c.Now, c.After
	e.s.tpush.after, e.s.dtree.after = c.After, c.After
	return c
}

// todosSent is how many {"type":"todos"} the apps were sent so far.
func todosSent(e *todoEnv) int {
	n := 0
	for _, ev := range strings.Fields(e.f.eventTypes()) {
		if ev == "todos" {
			n++
		}
	}
	return n
}

// todoViewsNow reads GET /api/v1/todos (which an app does after every push) and answers each row's view by id.
func todoViewsNow(e *todoEnv) map[string]assistantwire.TodoSummary {
	e.t.Helper()
	var got struct{ Todos []listedTodo }
	if err := json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &got); err != nil {
		e.t.Fatal(err)
	}
	out := map[string]assistantwire.TodoSummary{}
	for _, r := range got.Todos {
		if r.View == nil {
			e.t.Fatalf("%s has no view", r.ID)
		}
		out[r.ID] = *r.View
	}
	return out
}

// asking is run c waiting on the owner since at, as the hub lists it.
func asking(c proto.Session, at time.Time) proto.Session {
	c.Status, c.State, c.PendingInteractions = proto.StatusBlocked, proto.StateNeedsYou, 1
	c.StateSince, c.UpdatedAt, c.Asks = at, at, "Trim the top too?"
	return c
}

// A linked run that comes to ask files its to-do under Needs you: the apps are told once. Its latest line alone, or
// a run no to-do links, tells them nothing; the same lists again neither.
func TestTodoViewPushFollowsLinkedRuns(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		run := proto.Session{SessionID: "s1", Title: "Hedge trimming", Status: proto.StatusWorking, State: proto.StateWorking,
			StateSince: began, UpdatedAt: began, Activity: "measuring the hedge"}
		other := proto.Session{SessionID: "s2", Title: "Unrelated", Status: proto.StatusIdle, State: proto.StateIdle,
			StateSince: began, UpdatedAt: began}
		e.sessions("server", run, other)
		id := e.create("Prune the hedge").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
		clock.advance(time.Minute) // the writes' own pushes go out
		if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionWorking {
			t.Fatalf("a working run's to-do: %+v", v)
		}
		settled := func() int {
			clock.advance(todoViewSettle + todosSettle)
			return todosSent(e)
		}
		sent := todosSent(e)

		// The run's latest line and activity move: still Working, nothing is sent.
		step := run
		step.Activity, step.UpdatedAt = "trimming the top", began.Add(30*time.Second)
		e.sessions("server", step, other)
		if n := settled(); n != sent {
			t.Fatalf("a working run's next step sent %d todos events, want none", n-sent)
		}
		if v := todoViewsNow(e)[id]; v.Line == nil || v.Line.Args["text"] != "trimming the top" {
			t.Fatalf("the list does not follow the run's line: %+v", v)
		}

		// A run no to-do links comes to ask: nothing is sent.
		e.sessions("server", step, asking(other, began.Add(35*time.Second)))
		if n := settled(); n != sent {
			t.Fatalf("a run no to-do links sent %d todos events, want none", n-sent)
		}

		// The linked run asks: one push, and the list files it under Needs you.
		asks := asking(step, began.Add(40*time.Second))
		e.sessions("server", asks, asking(other, began.Add(35*time.Second)))
		if n := settled(); n != sent+1 {
			t.Fatalf("the linked run asking sent %d todos events, want 1", n-sent)
		}
		if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionNeedsYou || len(v.Asks) != 1 {
			t.Fatalf("after the run asks: %+v", v)
		}

		// The same lists again, with or without a list read in between: nothing more.
		e.sessions("server", asks, asking(other, began.Add(35*time.Second)))
		if n := settled(); n != sent+1 {
			t.Fatalf("the same lists again sent %d more todos events", n-sent-1)
		}
		todoViewsNow(e)
		e.sessions("server", asks, asking(other, began.Add(35*time.Second)))
		if n := settled(); n != sent+1 {
			t.Fatalf("the same lists after a read sent %d more todos events", n-sent-1)
		}
	})
}

// Many runs on two computers come to ask at once: the apps are told once, after the settle.
func TestTodoViewPushOnceForManyRuns(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		runs := map[string][]proto.Session{}
		for _, machine := range []string{"server", "laptop"} {
			for i := range 3 {
				runs[machine] = append(runs[machine], proto.Session{SessionID: fmt.Sprintf("%s-%d", machine, i),
					Title: fmt.Sprintf("Run %d", i), Status: proto.StatusWorking, State: proto.StateWorking,
					StateSince: began, UpdatedAt: began})
			}
			e.sessions(machine, runs[machine]...)
		}
		ids := map[string]bool{}
		for machine, list := range runs {
			for _, c := range list {
				id := e.create("Chore for " + c.SessionID).ID
				ids[id] = true
				e.must("PUT", "/api/v1/todos/"+id+"/agents/"+machine+"/"+c.SessionID, "")
			}
		}
		clock.advance(time.Minute)
		todoViewsNow(e)
		sent := todosSent(e)

		for round := range 3 { // each computer's list comes again within the settle
			for machine, list := range runs {
				changed := make([]proto.Session, len(list))
				for i, c := range list {
					changed[i] = asking(c, began.Add(time.Duration(10+round)*time.Second))
				}
				e.sessions(machine, changed...)
			}
		}
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("six runs asking at once sent %d todos events, want 1", n-sent)
		}
		for id, v := range todoViewsNow(e) {
			if ids[id] && v.Section != assistantwire.TodoListSectionNeedsYou {
				t.Errorf("%s: %+v", id, v)
			}
		}
	})
}

// A closed to-do nobody has seen leaves Just finished 24 hours after it closed: at that moment the clock tells the
// apps, once, though nothing was written.
func TestTodoViewPushWhenJustFinishedEnds(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		id := e.create("Order compost").ID
		e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
		clock.advance(time.Minute)
		v := todoViewsNow(e)[id]
		if v.Section != assistantwire.TodoListSectionJustFinished || v.NextChangeAt.IsZero() {
			t.Fatalf("a closed to-do nobody saw: %+v", v)
		}
		sent := todosSent(e)

		clock.advance(v.NextChangeAt.Sub(clock.Now()) - time.Second)
		if n := todosSent(e); n != sent {
			t.Fatalf("%d todos events before its 24 hours ended, want none", n-sent)
		}
		clock.advance(time.Second + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("its 24 hours ending sent %d todos events, want 1", n-sent)
		}
		if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionDone || !v.NextChangeAt.IsZero() {
			t.Fatalf("after its 24 hours: %+v", v)
		}
		clock.advance(72 * time.Hour)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("%d more todos events with nothing left to change", n-sent-1)
		}
	})
}

// The clock checks the views at the earliest next_change_at, never sooner than a minute after its last check and
// never sleeping longer than an hour.
func TestTodoViewWakeBounds(t *testing.T) {
	f := newFakeHub().withMachine()
	e := &todoEnv{t: t, s: newStorelessService(t, f), f: f}
	clock := todoViewClock(e)
	b := &e.s.tview
	now := clock.Now()
	arm := func(next map[string]time.Time, woke time.Time) time.Time {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.stopWakeLocked()
		b.next, b.woke = next, woke
		e.s.todoViewArmLocked()
		return b.wakeAt
	}
	soon := now.Add(10 * time.Minute)
	if at := arm(map[string]time.Time{"a": now.Add(3 * time.Hour), "b": soon}, time.Time{}); !at.Equal(soon) {
		t.Errorf("the earliest of two: %v, want %v", at, soon)
	}
	if at := arm(map[string]time.Time{"a": now.Add(24 * time.Hour)}, time.Time{}); !at.Equal(now.Add(todoViewWakeCap)) {
		t.Errorf("a far one: %v, want the cap %v", at, now.Add(todoViewWakeCap))
	}
	if at := arm(map[string]time.Time{"a": now}, now.Add(-10*time.Second)); !at.Equal(now.Add(50 * time.Second)) {
		t.Errorf("one due now, checked 10 s ago: %v, want a minute after that check", at)
	}
	if at := arm(nil, time.Time{}); !at.IsZero() {
		t.Errorf("nothing to change: a check at %v", at)
	}
	// One due before the to-dos are loaded is looked at again a minute later, not at once over and over.
	arm(map[string]time.Time{"a": now.Add(-time.Second)}, time.Time{})
	b.mu.Lock()
	seq := b.wakeSeq
	b.mu.Unlock()
	e.s.todoViewWake(seq)
	b.mu.Lock()
	at := b.wakeAt
	b.mu.Unlock()
	if !at.Equal(now.Add(todoViewWakeEvery)) {
		t.Errorf("one due before the to-dos are loaded: a check at %v, want %v", at, now.Add(todoViewWakeEvery))
	}
}

// What a row is filed under follows its section, reason, asks, marks and steps, never its line, since,
// next_change_at or rank.
func TestTodoClassLeavesOutTheLine(t *testing.T) {
	at := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	working := func(text string) *assistantwire.TodoLine {
		return &assistantwire.TodoLine{Key: "working", Args: map[string]string{"text": text}}
	}
	base := assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWorking, Section: assistantwire.TodoListSectionWorking,
		Reason: assistantwire.TodoReasonAgentRunning, Line: working("a"), Since: at, NextChangeAt: at.Add(48 * time.Hour),
		Rank: 2}
	same := base
	same.Line, same.Since, same.NextChangeAt, same.Rank = working("b"), at.Add(time.Minute), at.Add(49*time.Hour), 0
	if todoClassOf(base) != todoClassOf(same) {
		t.Error("the line, since, next_change_at or rank changed what the row is filed under")
	}
	for name, change := range map[string]func(*assistantwire.TodoSummary){
		"section":  func(v *assistantwire.TodoSummary) { v.Section = assistantwire.TodoListSectionWaiting },
		"reason":   func(v *assistantwire.TodoSummary) { v.Reason = assistantwire.TodoReasonBackground },
		"asks":     func(v *assistantwire.TodoSummary) { v.Asks = []assistantwire.OwnerAsk{{ID: "run:server/s1@1"}} },
		"inactive": func(v *assistantwire.TodoSummary) { v.Inactive = at },
		"can_wait": func(v *assistantwire.TodoSummary) { v.CanWait = true },
		"seen":     func(v *assistantwire.TodoSummary) { v.Seen = true },
		"steps":    func(v *assistantwire.TodoSummary) { v.Steps = &assistantwire.TodoStepCount{Done: 1, Total: 2} },
	} {
		v := base
		change(&v)
		if todoClassOf(v) == todoClassOf(base) {
			t.Errorf("%s: the row is filed alike", name)
		}
	}
	// A run's ask whose card lands after it came to ask answers differently (its kind, its risk), with the same id.
	asking := base
	asking.Asks = []assistantwire.OwnerAsk{{ID: "run:server/s1@1", Kind: assistantwire.OwnerAskKindQuestion}}
	carded := asking
	carded.Asks = []assistantwire.OwnerAsk{{ID: "run:server/s1@1", Kind: assistantwire.OwnerAskKindTool, Interaction: "ix_1"}}
	if todoClassOf(carded) == todoClassOf(asking) {
		t.Error("a run's ask that got its card is filed alike")
	}
}

// workingRun is a run on its way since began, its latest line activity.
func workingRun(id string, began time.Time, activity string) proto.Session {
	return proto.Session{SessionID: id, Title: "Hedge trimming", Status: proto.StatusWorking, State: proto.StateWorking,
		StateSince: began, UpdatedAt: began, Activity: activity}
}

// A step's run that comes to ask files the to-do it is a step of under Needs you: the apps are told once.
func TestTodoViewPushFollowsAStep(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		run := workingRun("s1", began, "measuring the hedge")
		e.sessions("server", run)
		parent := e.create("Redo the garden").ID
		step := e.create("Prune the hedge").ID
		e.must("PATCH", "/api/v1/todos/"+step, `{"part_of":"`+parent+`"}`)
		e.must("PUT", "/api/v1/todos/"+step+"/agents/server/s1", "")
		clock.advance(time.Minute)
		if v := todoViewsNow(e)[parent]; v.Section != assistantwire.TodoListSectionWorking {
			t.Fatalf("the parent of a working step: %+v", v)
		}
		sent := todosSent(e)

		e.sessions("server", asking(run, began.Add(40*time.Second)))
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("the step's run asking sent %d todos events, want 1", n-sent)
		}
		if v := todoViewsNow(e)[parent]; v.Section != assistantwire.TodoListSectionNeedsYou || len(v.Asks) != 1 {
			t.Fatalf("the parent after its step's run asks: %+v", v)
		}
	})
}

// The owner looking at a working row marks it seen; the run's next step clears the mark, which tells the apps once,
// and the steps after it nothing.
func TestTodoViewSeenThenSteps(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		run := workingRun("s1", began, "measuring the hedge")
		e.sessions("server", run)
		id := e.create("Prune the hedge").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
		e.must("PUT", "/api/v1/todos/"+id+"/seen", "")
		clock.advance(time.Minute)
		if v := todoViewsNow(e)[id]; !v.Seen || v.Section != assistantwire.TodoListSectionWorking {
			t.Fatalf("a working row the owner looked at: %+v", v)
		}
		sent := todosSent(e)

		for i := range 3 {
			step := run
			step.Activity, step.UpdatedAt = fmt.Sprintf("trimming row %d", i+1), time.Now().UTC().Add(time.Duration(i+1)*time.Minute)
			e.sessions("server", step)
			clock.advance(todoViewSettle + todosSettle)
			todoViewsNow(e) // as every app does after a push
		}
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("three steps after the owner looked sent %d todos events, want 1", n-sent)
		}
		if v := todoViewsNow(e)[id]; v.Seen || v.Section != assistantwire.TodoListSectionWorking {
			t.Fatalf("after the steps: %+v", v)
		}
	})
}

// A computer no open to-do has a run on sets nothing going, however its sessions change; one with such a run does.
func TestTodoViewOnlyComputersWithRuns(t *testing.T) {
	f := newFakeHub().withMachine()
	e := &todoEnv{t: t, s: newStorelessService(t, f), f: f}
	clock := todoViewClock(e)
	armed := func() bool {
		b := &e.s.tview
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.armed
	}
	began := time.Now().UTC().Add(-time.Minute)
	run := workingRun("s1", began, "measuring the hedge")
	e.sessions("server", run)
	id := e.create("Prune the hedge").ID
	e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
	clock.advance(time.Minute)
	todoViewsNow(e)
	sent := todosSent(e)

	e.sessions("laptop", asking(workingRun("s9", began, ""), began.Add(10*time.Second)))
	if armed() {
		t.Fatal("a computer no to-do has a run on set the recheck going")
	}
	if ids := e.s.todosOnRuns(map[string]bool{"laptop": true}); len(ids) != 0 {
		t.Fatalf("to-dos on the laptop's runs: %v", ids)
	}
	e.sessions("server", asking(run, began.Add(20*time.Second)))
	if !armed() {
		t.Fatal("the computer of a linked run did not set the recheck going")
	}
	clock.advance(todoViewSettle + todosSettle)
	if n := todosSent(e); n != sent+1 {
		t.Fatalf("the linked run asking sent %d todos events, want 1", n-sent)
	}
}

// A failed start among the to-dos a recheck reads holds as superseded as it does over the whole list, though the
// to-do that finished its work is not read: neither the failed start nor its link makes the recheck read every to-do,
// and it tells the apps of no change that is none.
func TestTodoViewFailedStepReadsAsTheList(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		run := workingRun("s1", began, "measuring the hedge")
		e.sessions("server", run)
		parent := e.create("Redo the garden").ID
		step := e.create("Prune the hedge").ID
		failed := e.create("Repot the fern").ID
		done := e.create("Repot the fern").ID // the same piece of work, finished another way
		for _, id := range []string{step, failed} {
			e.must("PATCH", "/api/v1/todos/"+id, `{"part_of":"`+parent+`"}`)
		}
		e.must("PUT", "/api/v1/todos/"+step+"/agents/server/s1", "")
		e.must("PUT", "/api/v1/todos/"+failed+"/agents/server/s2", "")
		if err := e.s.failTodo(context.Background(), failed, "the launcher is missing"); err != nil {
			t.Fatal(err)
		}
		e.must("PATCH", "/api/v1/todos/"+done, `{"done":true}`)
		clock.advance(time.Minute)
		if v := todoViewsNow(e)[failed]; v.Line == nil || v.Line.Key != "superseded" {
			t.Fatalf("the failed start in the list: %+v", v)
		}
		ids := e.s.todosOnRuns(map[string]bool{"server": true})
		if len(ids) != 3 || !ids[parent] || !ids[step] || !ids[failed] {
			t.Fatalf("a change on the server reads %v, want the parent and its two steps", ids)
		}
		sent := todosSent(e)

		next := run
		next.Activity, next.UpdatedAt = "trimming the top", began.Add(30*time.Second)
		e.sessions("server", next)
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent {
			t.Fatalf("a step of the working run sent %d todos events, want none", n-sent)
		}
	})
}

// Views worked out from to-dos read before a push change nothing once it went out: a slow read that started before a
// to-do closed and ends after the apps' fresh read leaves the clock at the end of its Just finished.
func TestTodoViewOldReadKeepsTheClock(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		id := e.create("Order compost").ID
		clock.advance(time.Minute)
		todoViewsNow(e)
		ctx := context.Background()
		gen := e.s.todoViewGen()
		list, err := e.s.listedTodos(ctx, false) // the slow read: it started while the to-do was open
		if err != nil {
			t.Fatal(err)
		}
		old := e.s.todoProjectionOf(ctx, list, clock.Now())

		e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
		clock.advance(todosSettle)
		v := todoViewsNow(e)[id] // the apps' read after the push
		if v.Section != assistantwire.TodoListSectionJustFinished || v.NextChangeAt.IsZero() {
			t.Fatalf("a closed to-do nobody saw: %+v", v)
		}
		sent := todosSent(e)
		if e.s.todoViewsSeen(gen, old, nil) {
			t.Fatal("views from before the push read as a change")
		}
		clock.advance(v.NextChangeAt.Sub(clock.Now()) + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("its 24 hours ending after the old read sent %d todos events, want 1", n-sent)
		}
	})
}

// A difference a list read finds (a run whose change reached no recheck) is told once todoViewSettle passed, as one
// push; a push before that tells it, and nothing more goes out.
func TestTodoViewReadFoundWaitsTheSettle(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		clock := todoViewClock(e)
		began := time.Now().UTC().Add(-time.Minute)
		run := workingRun("s1", began, "measuring the hedge")
		run.MachineID = "server"
		e.sessions("server", run)
		id := e.create("Prune the hedge").ID
		other := e.create("Sweep the path").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
		clock.advance(time.Minute)
		todoViewsNow(e)
		sent := todosSent(e)

		// The node's list changes with no word to the manager: the next read finds the run asking.
		e.f.setSessions("server", asking(run, began.Add(40*time.Second)))
		if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionNeedsYou {
			t.Fatalf("the read after the run asks: %+v", v)
		}
		clock.advance(todosSettle)
		if n := todosSent(e); n != sent {
			t.Fatalf("%d todos events before the settle", n-sent)
		}
		clock.advance(todoViewSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("the difference a read found sent %d todos events, want 1", n-sent)
		}

		// It works again, and a write goes out before the settle: the write's push is the only one.
		todoViewsNow(e)
		e.f.setSessions("server", run)
		todoViewsNow(e)
		e.must("PATCH", "/api/v1/todos/"+other, `{"title":"Sweep the whole path"}`)
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent+2 {
			t.Fatalf("a difference a write's push told sent %d more todos events, want only the write's", n-sent-1)
		}
	})
}

// On the fleet a card that comes to a linked run reaches its to-do's view through the list the apps get
// (publishList), with no new list from the node: the apps are told once.
func TestTodoViewPushFollowsACard(t *testing.T) {
	for _, store := range []string{"memory", "store"} {
		t.Run(store, func(t *testing.T) {
			ft := newFleetTest(t)
			if store == "store" {
				ft = newFleetTestOn(t, todoTestDB(t))
			}
			e := &todoEnv{t: t, s: ft.s, f: ft.f}
			clock := todoViewClock(e)
			began := time.Now().UTC().Add(-time.Minute)
			run := workingRun("s1", began, "measuring the hedge")
			run.MachineID = "mac"
			ft.sessions("mac", run)
			id := e.create("Prune the hedge").ID
			e.must("PUT", "/api/v1/todos/"+id+"/agents/mac/s1", "")
			waitFleetOrder(t, ft.s)
			clock.advance(time.Minute)
			todoViewsNow(e)
			clock.advance(time.Minute) // a started manager's first views tell the apps once
			if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionWorking {
				t.Fatalf("a working run's to-do: %+v", v)
			}
			sent := todosSent(e)

			ix := proto.Interaction{ID: "ix_trim", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
				Summary: "Trim the top too?", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
			ft.hold(ix)
			ft.s.tellApps(ix)
			clock.advance(todoViewSettle + todosSettle)
			if n := todosSent(e); n != sent+1 {
				t.Fatalf("a card on the linked run sent %d todos events, want 1", n-sent)
			}
			if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionNeedsYou {
				t.Fatalf("after the card: %+v", v)
			}
		})
	}
}

// A run that came to ask before the manager held its card (a runtime without a terminal, whose card is still on its
// way) is told once; its card landing then tells the apps once more, with no new list from the node, since the card
// says what the run asks and how its row may answer it. Read again, nothing more goes out.
func TestTodoViewPushWhenTheAskGetsItsCard(t *testing.T) {
	ft := newFleetTest(t)
	e := &todoEnv{t: t, s: ft.s, f: ft.f}
	clock := todoViewClock(e)
	began := time.Now().UTC().Add(-time.Minute)
	run := asking(workingRun("s1", began, ""), began.Add(10*time.Second))
	run.MachineID, run.SourceKind = "mac", "runtime" // no screen to read a dialog off, so no screen card meanwhile
	ft.sessions("mac", run)
	id := e.create("Prune the hedge").ID
	e.must("PUT", "/api/v1/todos/"+id+"/agents/mac/s1", "")
	waitFleetOrder(t, ft.s)
	clock.advance(time.Minute)
	todoViewsNow(e)
	clock.advance(time.Minute) // a started manager's first views tell the apps once
	if v := todoViewsNow(e)[id]; v.Section != assistantwire.TodoListSectionNeedsYou || len(v.Asks) != 1 ||
		v.Asks[0].Interaction != "" {
		t.Fatalf("a run asking with no card held: %+v", v)
	}
	sent := todosSent(e)

	ix := proto.Interaction{ID: "ix_trim", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(), Questions: []proto.Question{{
			Question: "Trim the top too?", Options: []proto.QuestionOption{{Label: "Yes"}, {Label: "Not this time"}}}}}
	ft.hold(ix)
	ft.s.tellApps(ix)
	clock.advance(todoViewSettle + todosSettle)
	if n := todosSent(e); n != sent+1 {
		t.Fatalf("the run's card landing sent %d todos events, want 1", n-sent)
	}
	if v := todoViewsNow(e)[id]; len(v.Asks) != 1 || v.Asks[0].Interaction != "ix_trim" ||
		v.Asks[0].Risk != assistantwire.OwnerAskRiskSafe {
		t.Fatalf("after the card: %+v", v)
	}
	clock.advance(todoViewSettle + todosSettle)
	if n := todosSent(e); n != sent+1 {
		t.Fatalf("reading it again sent %d more todos events", n-sent-1)
	}
}

// What the apps were told is the manager's memory alone: the first views worked out after Run starts tell them once,
// since a run may have come to ask while it was away; later reads tell nothing.
func TestTodoViewFirstViewsAfterStartTell(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		if database != nil {
			e.create("Order compost")
			todosTold(t, e.s)
			e = &todoEnv{t: t, s: newServiceOn(t, e.f, database), f: e.f} // the manager starts again
		}
		clock := todoViewClock(e)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.s.running.Store(&ctx)
		sent := todosSent(e)

		todoViewsNow(e)
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("the first read after the start sent %d todos events, want 1", n-sent)
		}
		todoViewsNow(e)
		clock.advance(todoViewSettle + todosSettle)
		if n := todosSent(e); n != sent+1 {
			t.Fatalf("a later read sent %d more todos events", n-sent-1)
		}
	})
}

// Once Run's context ended neither the recheck nor the clock does anything: no list is read and nothing is told.
func TestTodoViewStopsWithTheService(t *testing.T) {
	f := newFakeHub().withMachine()
	e := &todoEnv{t: t, s: newStorelessService(t, f), f: f}
	clock := todoViewClock(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.s.running.Store(&ctx)
	began := time.Now().UTC().Add(-time.Minute)
	run := workingRun("s1", began, "measuring the hedge")
	e.sessions("server", run)
	closed := e.create("Order compost").ID
	linked := e.create("Prune the hedge").ID
	e.must("PUT", "/api/v1/todos/"+linked+"/agents/server/s1", "")
	e.must("PATCH", "/api/v1/todos/"+closed, `{"done":true}`)
	clock.advance(time.Minute)
	todoViewsNow(e)
	clock.advance(time.Minute) // the first views' push
	v := todoViewsNow(e)[closed]
	if v.NextChangeAt.IsZero() {
		t.Fatalf("a closed to-do nobody saw: %+v", v)
	}
	sent := todosSent(e)
	clock.advance(v.NextChangeAt.Sub(clock.Now()) - 30*time.Minute) // the clock's next check is at its moment

	e.sessions("server", asking(run, began.Add(40*time.Second))) // waits for the settle
	todoViewsNow(e)                                              // and so does what this read finds
	cancel()
	e.sessions("server", asking(run, began.Add(50*time.Second)))
	clock.advance(time.Hour)
	if n := todosSent(e); n != sent {
		t.Fatalf("%d todos events after the service stopped", n-sent)
	}
	b := &e.s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.woke.IsZero() || b.armed {
		t.Fatalf("the views were looked at after the service stopped: woke %v, armed %v", b.woke, b.armed)
	}
}
