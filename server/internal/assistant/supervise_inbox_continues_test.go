package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// An Inbox item linked to a task that was closed and carried on by another (continues) follows it there (#6423,
// supervise_inbox.go followContinuations).

// closeTodo marks task id done, as an accepted review leaves it.
func closeTodo(t *testing.T, s *Service, id string) {
	t.Helper()
	now := time.Now()
	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()
	s.todos.todos[id].DoneAt = &now
}

// continuesTask records that to-do next carries on to-do prev (task_brief_todos.continues).
func continuesTask(s *Service, next, prev string) {
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.links[next] = taskBriefLink{Brief: "brief_x", Continues: prev, At: time.Now()}
}

// continuesService is a quiet service whose inbox saves to st, with the inbox signals on and the supervisor's message
// held back, so what waits can be read.
func continuesService(t *testing.T) (*Service, *inboxMemStore) {
	t.Helper()
	st := newInboxMemStore()
	s, _ := newQuietServiceOn(t, st)
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.superviseTimes.gather = time.Hour
	turnInboxSignalsOn(t, s)
	return s, st
}

// heldItem is linkedItem kept in the inbox, as a pass finds it.
func heldItem(s *Service, todo string) *InboxItem {
	it := linkedItem(s, "lark:f1:12", todo, "12", "改好了，你再试试", 1)
	s.inbox.mu.Lock()
	s.inbox.items[it.ID] = it
	s.inbox.mu.Unlock()
	return it
}

func linkOf(s *Service, it *InboxItem) string {
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	return it.Agent.TodoID
}

func storedLink(t *testing.T, st *inboxMemStore, id string) string {
	t.Helper()
	st.imu.Lock()
	raw := st.rows[id]
	st.imu.Unlock()
	var it InboxItem
	if raw == nil || json.Unmarshal(raw, &it) != nil || it.Agent == nil {
		return ""
	}
	return it.Agent.TodoID
}

// A new message on an item linked to a closed task reaches the task that carries it on: the link moves there, is saved,
// and the signal goes to that task.
func TestInboxLinkFollowsContinuation(t *testing.T) {
	s, st := continuesService(t)
	a := supervisedTodo(t, s, "核查会话关联")
	b := supervisedTodo(t, s, "发版后核对补绑")
	closeTodo(t, s, a.ID)
	continuesTask(s, b.ID, a.ID)
	it := heldItem(s, a.ID)
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	if got := linkOf(s, it); got != b.ID {
		t.Fatalf("linked to %q, want the continuing task %q", got, b.ID)
	}
	if got := storedLink(t, st, it.ID); got != b.ID {
		t.Fatalf("stored link %q, want %q", got, b.ID)
	}
	if g, ok := pendingInbox(s, b.ID); !ok || g.Inbox == nil || g.Inbox.Item != it.ID {
		t.Fatalf("no signal for the continuing task: %+v %v", g, ok)
	}
	if g, ok := pendingInbox(s, a.ID); ok {
		t.Fatalf("signal for the closed task: %+v", g)
	}
}

// An item linked before the continuation, with nothing new since it was loaded, is moved without a signal: the move
// itself is no news.
func TestInboxLinkFollowsContinuationQuietly(t *testing.T) {
	s, st := continuesService(t)
	a := supervisedTodo(t, s, "核查会话关联")
	b := supervisedTodo(t, s, "发版后核对补绑")
	closeTodo(t, s, a.ID)
	continuesTask(s, b.ID, a.ID)
	it := heldItem(s, a.ID)
	s.inbox.mu.Lock()
	_, it.Agent.signaled = receivedSince(it, 0, false) // as loading a stored item leaves it (inboxBook.stored)
	it.Agent.signaledOf = it.Last
	s.inbox.mu.Unlock()
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	if got := linkOf(s, it); got != b.ID || storedLink(t, st, it.ID) != b.ID {
		t.Fatalf("linked to %q (stored %q), want %q", got, storedLink(t, st, it.ID), b.ID)
	}
	if g, ok := pendingInbox(s, b.ID); ok {
		t.Fatalf("the move raised a signal: %+v", g.Inbox)
	}
}

// A message on an item whose closed task nobody carries on is told nobody, and it is not counted as told: once a task
// carries it on, that task hears of it.
func TestInboxSignalWaitsForAnOpenTask(t *testing.T) {
	s, _ := continuesService(t)
	a := supervisedTodo(t, s, "核查会话关联")
	closeTodo(t, s, a.ID)
	it := heldItem(s, a.ID)
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	if g, ok := pendingInbox(s, a.ID); ok {
		t.Fatalf("signal for the closed task: %+v", g)
	}
	s.inbox.mu.Lock()
	cursor := it.Agent.signaledOf
	s.inbox.mu.Unlock()
	if cursor != "" || linkOf(s, it) != a.ID {
		t.Fatalf("cursor %q, link %q: the message was counted as told", cursor, linkOf(s, it))
	}
	b := supervisedTodo(t, s, "发版后核对补绑")
	continuesTask(s, b.ID, a.ID)
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	if g, ok := pendingInbox(s, b.ID); !ok || g.Inbox.Item != it.ID {
		t.Fatalf("the waiting message did not reach the continuing task: %+v %v", g, ok)
	}
}

// Which task a closed one stands for now: the one open task down its line of continuations; none when two carry it on;
// a cycle ends.
func TestCurrentTaskOf(t *testing.T) {
	s, _ := continuesService(t)
	todo := func(title string) string { return supervisedTodo(t, s, title).ID }
	open := todo("开着的")
	if got, ok := s.currentTaskOf(open); !ok || got != open {
		t.Fatalf("open task: %q %v", got, ok)
	}
	if got, ok := s.currentTaskOf("todo_missing"); ok || got != "" {
		t.Fatalf("missing task: %q %v", got, ok)
	}

	a, b, c := todo("A"), todo("B"), todo("C")
	closeTodo(t, s, a)
	closeTodo(t, s, b)
	continuesTask(s, b, a)
	continuesTask(s, c, b)
	if got, ok := s.currentTaskOf(a); !ok || got != c {
		t.Fatalf("A→B→C: %q %v, want %q", got, ok, c)
	}

	x, y, z := todo("X"), todo("Y"), todo("Z")
	closeTodo(t, s, x)
	continuesTask(s, y, x)
	continuesTask(s, z, x)
	if got, ok := s.currentTaskOf(x); ok || got != "" {
		t.Fatalf("two continuing tasks: %q %v", got, ok)
	}
	it := heldItem(s, x)
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	if got := linkOf(s, it); got != x {
		t.Fatalf("ambiguous continuation moved the link to %q", got)
	}

	p, q := todo("P"), todo("Q")
	closeTodo(t, s, p)
	closeTodo(t, s, q)
	continuesTask(s, q, p)
	continuesTask(s, p, q)
	done := make(chan struct{})
	go func() { s.currentTaskOf(p); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cycle of continuations does not end")
	}
	if got, ok := s.currentTaskOf(p); ok || got != "" {
		t.Fatalf("cycle: %q %v", got, ok)
	}
}

// A link to a closed task with no task carrying it on counts as none: the next escalation asks the judge again, even
// when the item already has its processing conversation.
func TestInboxJudgeRelinksAClosedTask(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	in := newFakeInbox()
	f.inbox, in.svc = in, s
	h := &inboxHub{Service: s, f: f, in: in}
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	a := supervisedTodo(t, s, "核查会话关联")
	b := supervisedTodo(t, s, "发版后核对补绑")
	closeTodo(t, s, a.ID)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	model, calls := captureModel(t, `{"task_id":"`+b.ID+`","evidence":"发版后核对补绑"}`)
	in.setModel(model)
	it := actionItem("lark:f1:12", time.Now())
	it.Agent = &InboxAgent{State: AgentDrafted, MachineID: "server", SessionID: "handling-1", Of: "11", TodoID: a.ID}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	h.handPass(ctx)
	if got := linkOf(s, it); got != b.ID {
		t.Fatalf("linked to %q after %d judge calls, want %q", got, len(calls()), b.ID)
	}
}

// A handled item linked to a closed task follows the task that carries it on too, so its detail names the work going on;
// it raises no signal, being handled.
func TestHandledInboxItemFollowsContinuation(t *testing.T) {
	s, st := continuesService(t)
	a := supervisedTodo(t, s, "核查会话关联")
	b := supervisedTodo(t, s, "发版后核对补绑")
	closeTodo(t, s, a.ID)
	continuesTask(s, b.ID, a.ID)
	it := heldItem(s, a.ID)
	now := time.Now()
	s.inbox.mu.Lock()
	it.DoneAt = &now
	s.inbox.mu.Unlock()
	s.linkInboxTaskURLs(context.Background())
	if got := linkOf(s, it); got != b.ID || storedLink(t, st, it.ID) != b.ID {
		t.Fatalf("handled item linked to %q (stored %q), want %q", got, storedLink(t, st, it.ID), b.ID)
	}
	if g, ok := pendingInbox(s, b.ID); ok {
		t.Fatalf("a handled item raised a signal: %+v", g.Inbox)
	}
}
