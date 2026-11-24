package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// activeTodosOf is the todos an agent is linked to now (the unique index allows at most one).
func activeTodosOf(t *testing.T, d *DB, machine, session string) []string {
	t.Helper()
	agents, err := d.ListAllTodoAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range agents {
		if a.MachineID == machine && a.SessionID == session && a.ReleasedAt == nil {
			out = append(out, a.TodoID)
		}
	}
	return out
}

// An automatic link of an unlinked agent still in flight when the user links it to another todo (#2704): the user's
// link waits for it instead of hitting todo_agents_active, and stays; an automatic link arriving after it does not move
// it.
func TestTodoLinkRacesAutoLink(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"t_auto", "t_hand"} {
		if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		session := fmt.Sprintf("s%d", i)
		var wg sync.WaitGroup
		var autoErr, handErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _, autoErr = d.AutoLinkUnlinkedTodoAgent(ctx, "t_auto", "m", session, "", "", "", "", "chat", "oc_x", now)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, handErr = d.LinkTodoAgent(ctx, "t_hand", "m", session, "", "", "", "", now)
		}()
		close(start)
		wg.Wait()
		if autoErr != nil || handErr != nil {
			t.Fatalf("round %d: auto %v, hand %v", i, autoErr, handErr)
		}
		if got := activeTodosOf(t, d, "m", session); len(got) != 1 || got[0] != "t_hand" {
			t.Fatalf("round %d: active %v, want the user's t_hand", i, got)
		}
	}
	// A late automatic link of an agent that works for another todo: nothing changes.
	inserted, released, err := d.AutoLinkUnlinkedTodoAgent(ctx, "t_auto", "m", "s0", "", "", "", "", "chat", "oc_x", now.Add(time.Second))
	if err != nil || inserted || len(released) != 0 {
		t.Fatalf("late auto-link: %v %v %v", inserted, released, err)
	}
	if got := activeTodosOf(t, d, "m", "s0"); len(got) != 1 || got[0] != "t_hand" {
		t.Fatalf("late auto-link moved the agent: %v", got)
	}
	// AutoLinkTodoAgent itself still moves an agent to a todo it was never on (#2742's direct database behaviour).
	if _, err := d.LinkTodoAgent(ctx, "t_hand", "m", "s_direct", "", "", "", "", now); err != nil {
		t.Fatal(err)
	}
	if inserted, released, err := d.AutoLinkTodoAgent(ctx, "t_auto", "m", "s_direct", "", "", "", "", "chat", "oc_x", now.Add(time.Second)); err != nil ||
		!inserted || len(released) != 1 || released[0] != "t_hand" {
		t.Fatalf("direct auto-link: %v %v %v", inserted, released, err)
	}
	if got := activeTodosOf(t, d, "m", "s_direct"); len(got) != 1 || got[0] != "t_auto" {
		t.Fatalf("direct auto-link: active %v", got)
	}
}

// The user linking one agent to two todos at once (two devices): both succeed in some order, one link stays active.
func TestTodoLinksOfOneAgentSerialize(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"t_a", "t_b"} {
		if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		session := fmt.Sprintf("s%d", i)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for j, todo := range []string{"t_a", "t_b"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[j] = d.LinkTodoAgent(ctx, todo, "m", session, "", "", "", "", now)
			}()
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: %v", i, errs)
		}
		if got := activeTodosOf(t, d, "m", session); len(got) != 1 {
			t.Fatalf("round %d: active %v", i, got)
		}
	}
}

// Different agents linking to the same todo at once are not serialized against each other into errors.
func TestTodoLinksOfDifferentAgents(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "t_many", Title: "many", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, errs[i] = d.LinkTodoAgent(ctx, "t_many", "m", fmt.Sprintf("s%d", i), "", "", "", "", now)
			} else {
				_, _, errs[i] = d.AutoLinkUnlinkedTodoAgent(ctx, "t_many", "m", fmt.Sprintf("s%d", i), "", "", "", "", "space", "m/w", now)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("agent %d: %v", i, err)
		}
		if got := activeTodosOf(t, d, "m", fmt.Sprintf("s%d", i)); len(got) != 1 {
			t.Fatalf("agent %d: active %v", i, got)
		}
	}
}

// A delayed creation-id may link its exact done card, but it remains an automatic link: a manual membership committed
// after the decision wins, and the done target is inserted without reviving an existing or dismissed row (#5732).
func TestAutoLinkDoneCreationKeepsLaterManualChoice(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"t_done", "t_hand"} {
		if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	done := true
	if _, err := d.UpdateTodo(ctx, "t_done", nil, nil, &done, now); err != nil {
		t.Fatal(err)
	}
	decided := now
	if _, err := d.LinkTodoAgent(ctx, "t_hand", "m", "s_choice", "", "", "", "", decided.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	inserted, released, err := d.AutoLinkUnlinkedTodoAgentOfDoneTask(ctx, "t_done", "m", "s_choice", "", "", "", "", "assistant_start", "t_done", decided)
	if err != nil || inserted || len(released) != 0 {
		t.Fatalf("late done creation overrode the manual choice: inserted=%v released=%v err=%v", inserted, released, err)
	}
	if got := activeTodosOf(t, d, "m", "s_choice"); len(got) != 1 || got[0] != "t_hand" {
		t.Fatalf("late done creation moved the manual membership: %v", got)
	}
	inserted, released, err = d.AutoLinkUnlinkedTodoAgentOfDoneTask(ctx, "t_done", "m", "s_exact", "", "", "", "", "assistant_start", "t_done", decided)
	if err != nil || !inserted || len(released) != 0 {
		t.Fatalf("the exact unlinked done creation was not retained: inserted=%v released=%v err=%v", inserted, released, err)
	}
	if got := activeTodosOf(t, d, "m", "s_exact"); len(got) != 1 || got[0] != "t_done" {
		t.Fatalf("exact done creation membership = %v", got)
	}
}

// An automatic link of an agent whose only active link is on a done todo moves it there: the done todo keeps it as
// taking part (#2904). A link on an open todo, or one made after the automatic decision, stays.
func TestAutoLinkReleasesADoneTodo(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"t_new", "t_done", "t_open"} {
		if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range [][2]string{{"t_done", "s_done"}, {"t_open", "s_open"}} {
		if _, err := d.LinkTodoAgent(ctx, link[0], "m", link[1], "", "", "", "", now); err != nil {
			t.Fatal(err)
		}
	}
	done := true
	if _, err := d.UpdateTodo(ctx, "t_done", nil, nil, &done, now); err != nil {
		t.Fatal(err)
	}
	decided := now.Add(time.Second)
	if inserted, released, err := d.AutoLinkUnlinkedTodoAgent(ctx, "t_new", "m", "s_done", "", "", "", "", "space", "m/w", decided); err != nil ||
		!inserted || len(released) != 1 || released[0] != "t_done" {
		t.Fatalf("done todo: %v %v %v", inserted, released, err)
	}
	if got := activeTodosOf(t, d, "m", "s_done"); len(got) != 1 || got[0] != "t_new" {
		t.Fatalf("done todo: active %v", got)
	}
	if inserted, released, err := d.AutoLinkUnlinkedTodoAgent(ctx, "t_new", "m", "s_open", "", "", "", "", "space", "m/w", decided); err != nil ||
		inserted || len(released) != 0 {
		t.Fatalf("open todo: %v %v %v", inserted, released, err)
	}
	// Linked to the done todo after the decision (a task's agent, LinkTodoAgentOfDoneTask): left there.
	if _, err := d.LinkTodoAgentOfDoneTask(ctx, "t_done", "m", "s_late", "", "", "", "", "manual", "", decided.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if inserted, _, err := d.AutoLinkUnlinkedTodoAgent(ctx, "t_new", "m", "s_late", "", "", "", "", "space", "m/w", decided); err != nil || inserted {
		t.Fatalf("late link: %v %v", inserted, err)
	}
	if got := activeTodosOf(t, d, "m", "s_late"); len(got) != 1 || got[0] != "t_done" {
		t.Fatalf("late link: active %v", got)
	}
}
