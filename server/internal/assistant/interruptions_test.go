package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// cutTask makes a task from conv1 and writes the interrupted receipt its agent's cut-off turn gets on machine.
func cutTask(t *testing.T, s *Service, title, machine, errText string) Todo {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, title, "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.appendInterrupted(ctx, todo, SessionRef{MachineID: machine, SessionID: "s1"},
		receiptBody{Title: title, State: "interrupted", Machine: machine, Error: errText}, false)
	return todo
}

// receiptsOf is conv1's receipts as an app reads them, by todo id (the last one of each).
func receiptsOf(t *testing.T, s *Service) map[string]receiptBody {
	t.Helper()
	items, _, err := s.feed(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]receiptBody{}
	for _, it := range items {
		if it.Kind != feedReceipt {
			continue
		}
		var rb receiptBody
		if err := json.Unmarshal(it.Body, &rb); err != nil {
			t.Fatal(err)
		}
		out[it.Ref] = rb
	}
	return out
}

// changeTodo changes todo id in memory, as cancelling, closing or linking it does, and tells the apps.
func changeTodo(s *Service, id string, change func(*Todo)) {
	s.todos.mu.Lock()
	change(s.todos.todos[id])
	s.todos.mu.Unlock()
	s.todosChanged(context.Background())
}

// interruptedAt returns the timestamp of todo's interrupted receipt, so a test can put the next effect strictly
// after it without sleeping for the wall clock to advance.
func interruptedAt(t *testing.T, s *Service, todo Todo) time.Time {
	t.Helper()
	items, _, err := s.feed(context.Background(), todo.Origin, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Ref == todo.ID && it.Kind == feedReceipt {
			return it.At
		}
	}
	t.Fatalf("no interrupted receipt for %s", todo.ID)
	return time.Time{}
}

const gatewayDown = "API Error: 502 Bad Gateway"

// A cancelled task's interrupted receipt reads cancelled, and the apps are sent the new body (same id).
func TestInterruptedReceiptCancelled(t *testing.T) {
	old := interruptRefreshDelay
	interruptRefreshDelay = 10 * time.Millisecond
	t.Cleanup(func() { interruptRefreshDelay = old })
	s, f := newTaskService(t)
	var mu sync.Mutex
	var sent []FeedItem
	f.readHooks.receipt = func(it FeedItem) {
		mu.Lock()
		sent = append(sent, it)
		mu.Unlock()
	}
	todo := cutTask(t, s, "修登录页", "mlx", gatewayDown)
	if rb := receiptsOf(t, s)[todo.ID]; rb.Resolution != resolutionPending || rb.Cause != causeModelError || rb.Batch != nil {
		t.Fatalf("a fresh interruption: %+v", rb)
	}
	now := time.Now().UTC()
	changeTodo(s, todo.ID, func(x *Todo) { x.CancelledAt, x.DoneAt, x.DoneBy = &now, &now, db.DoneByCancelled })
	if rb := receiptsOf(t, s)[todo.ID]; rb.Resolution != resolutionCancelled {
		t.Fatalf("cancelled: %+v", rb)
	}
	waitFor(t, "the cancelled receipt re-sent", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, it := range sent {
			var rb receiptBody
			_ = json.Unmarshal(it.Body, &rb)
			if it.Ref == todo.ID && rb.Resolution == resolutionCancelled {
				return true
			}
		}
		return false
	})
}

// Rerouted: the cut-off task was cancelled and dispatched again as a new task, or another agent took it over.
func TestInterruptedReceiptRerouted(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	old := cutTask(t, s, "升级依赖", "mlx", gatewayDown)
	receiptAt := interruptedAt(t, s, old)
	createdAt := receiptAt.Add(time.Nanosecond)
	now := time.Now().UTC()
	changeTodo(s, old.ID, func(x *Todo) { x.CancelledAt, x.DoneAt, x.DoneBy = &now, &now, db.DoneByCancelled })
	replacement, err := s.createTodo(ctx, "升级依赖", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[replacement.ID].CreatedAt = createdAt
	s.todos.mu.Unlock()
	oldNow, ok := s.todoNow(old.ID)
	if !ok || !oldNow.CreatedAt.Equal(old.CreatedAt) {
		t.Fatalf("old task CreatedAt changed from %s to %s", old.CreatedAt, oldNow.CreatedAt)
	}
	replacementNow, ok := s.todoNow(replacement.ID)
	if !ok {
		t.Fatalf("replacement task %s was not retained", replacement.ID)
	}
	if !replacementNow.CreatedAt.Equal(createdAt) || !replacementNow.CreatedAt.After(receiptAt) {
		t.Fatalf("replacement CreatedAt = %s, want %s, strictly after interrupted receipt %s",
			replacementNow.CreatedAt, createdAt, receiptAt)
	}
	if rb := receiptsOf(t, s)[old.ID]; rb.Resolution != resolutionRerouted {
		t.Fatalf("cancelled and dispatched again: %+v", rb)
	}

	moved := cutTask(t, s, "整理周报", "mlx", gatewayDown)
	linkedAt := interruptedAt(t, s, moved).Add(time.Nanosecond)
	changeTodo(s, moved.ID, func(x *Todo) {
		x.Agents = append(x.Agents, TodoAgent{MachineID: "box", SessionID: "s9", LinkedAt: linkedAt})
	})
	if rb := receiptsOf(t, s)[moved.ID]; rb.Resolution != resolutionRerouted {
		t.Fatalf("another agent took it over: %+v", rb)
	}
}

// Resumed: the task's agent works again after the interruption (or a later receipt shows it went on); accepted and
// done when the task was closed.
func TestInterruptedReceiptResumedAcceptedDone(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	going := cutTask(t, s, "补测试", "mlx", gatewayDown)
	workingAt := interruptedAt(t, s, going).Add(time.Nanosecond)
	s.tasks.mu.Lock()
	s.tasks.working[going.ID] = workingAt
	s.tasks.mu.Unlock()
	if rb := receiptsOf(t, s)[going.ID]; rb.Resolution != resolutionResumed {
		t.Fatalf("working again: %+v", rb)
	}

	// After a restart the hub no longer remembers the turn: the later receipt says it went on.
	later := cutTask(t, s, "写文档", "mlx", gatewayDown)
	if _, err := s.appendFeedAt(ctx, "conv1", feedReceipt, later.ID,
		receiptBody{Title: "写文档", State: "done", Summary: "好了"}, interruptedAt(t, s, later).Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	for _, it := range items {
		if rb, ok := interruptedBody(it); ok && it.Ref == later.ID && rb.Resolution != resolutionResumed {
			t.Fatalf("a later receipt: %+v", rb)
		}
	}

	accepted := cutTask(t, s, "改配色", "mlx", gatewayDown)
	done := cutTask(t, s, "改图标", "mlx", gatewayDown)
	now := time.Now().UTC()
	changeTodo(s, accepted.ID, func(x *Todo) { x.DoneAt, x.DoneBy = &now, db.DoneByReview })
	changeTodo(s, done.ID, func(x *Todo) { x.DoneAt, x.DoneBy = &now, db.DoneByAuto })
	got := receiptsOf(t, s)
	if got[accepted.ID].Resolution != resolutionAccepted || got[done.ID].Resolution != resolutionDone {
		t.Fatalf("accepted %+v, done %+v", got[accepted.ID], got[done.ID])
	}
}

// Interruptions with the same cause in one conversation within ten minutes are one batch: the first carries it, the
// others name it; another cause, or one after the window, starts its own.
func TestInterruptedReceiptsBatch(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	a := cutTask(t, s, "任务一", "mlx", gatewayDown)
	b := cutTask(t, s, "任务二", "mlx", "API Error: 503 upstream unavailable")
	c := cutTask(t, s, "任务三", "mlx", "API Error: Connection lost mid-response.")
	other := cutTask(t, s, "任务四", "mlx", "API Error: 429 Too Many Requests")
	now := time.Now().UTC()
	changeTodo(s, a.ID, func(x *Todo) { x.CancelledAt, x.DoneAt, x.DoneBy = &now, &now, db.DoneByCancelled })
	changeTodo(s, b.ID, func(x *Todo) { x.DoneAt, x.DoneBy = &now, db.DoneByReview })

	got := receiptsOf(t, s)
	head := got[a.ID]
	if head.Group != "" || head.Batch == nil {
		t.Fatalf("the head: %+v", head)
	}
	if bt := head.Batch; bt.Total != 3 || bt.Resolved != 2 || bt.Pending != 1 || bt.Cause != causeModelError || bt.Machine != "mlx" ||
		len(bt.Tasks) != 3 || bt.Tasks[0].TodoID != a.ID || bt.Tasks[2].TodoID != c.ID || bt.Tasks[2].Resolution != resolutionPending {
		t.Fatalf("the batch: %+v", bt)
	}
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	headID := ""
	for _, it := range items {
		if it.Ref == a.ID {
			headID = it.ID
		}
	}
	if got[b.ID].Group != headID || got[c.ID].Group != headID || got[b.ID].Batch != nil {
		t.Fatalf("members: %+v %+v", got[b.ID], got[c.ID])
	}
	if o := got[other.ID]; o.Group != "" || o.Batch != nil || o.Cause != causeRateLimited {
		t.Fatalf("another cause: %+v", o)
	}

	// A page read after the head still brings the head with its counts up to date.
	var cAt time.Time
	for _, it := range items {
		if it.Ref == c.ID {
			cAt = it.At
		}
	}
	page, _, _ := s.feed(ctx, "conv1", cAt.Add(-time.Nanosecond))
	found := false
	for _, it := range page {
		if it.ID == headID {
			var rb receiptBody
			_ = json.Unmarshal(it.Body, &rb)
			found = rb.Batch != nil && rb.Batch.Total == 3
		}
	}
	if !found {
		t.Fatal("a page after the head left it out")
	}

	// After the window a new interruption starts its own batch.
	s.tasks.mu.Lock()
	for i, it := range s.tasks.feed["conv1"] {
		it.At = it.At.Add(-interruptBatchWindow - time.Minute)
		s.tasks.feed["conv1"][i] = it
	}
	s.tasks.mu.Unlock()
	late := cutTask(t, s, "任务五", "mlx", gatewayDown)
	if rb := receiptsOf(t, s)[late.ID]; rb.Group != "" {
		t.Fatalf("after the window: %+v", rb)
	}
}

// One task cut off twice in the window counts once in a batch; alone, it is no batch, and both receipts are drawn.
func TestInterruptedReceiptsBatchCountsTasks(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	a := cutTask(t, s, "任务一", "mlx", gatewayDown)
	s.appendInterrupted(ctx, a, SessionRef{MachineID: "mlx", SessionID: "s1"},
		receiptBody{Title: "任务一", State: "interrupted", Machine: "mlx", Error: gatewayDown}, false)
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	var bodies []receiptBody
	for _, it := range items {
		if rb, ok := interruptedBody(it); ok {
			bodies = append(bodies, rb)
		}
	}
	if len(bodies) != 2 || bodies[0].Batch != nil || bodies[1].Group != "" || bodies[0].Resolution != resolutionResumed ||
		bodies[1].Resolution != resolutionPending {
		t.Fatalf("one task twice: %+v", bodies)
	}
	cutTask(t, s, "任务二", "mlx", gatewayDown)
	items, _, _ = s.feed(ctx, "conv1", time.Time{})
	for _, it := range items {
		if rb, ok := interruptedBody(it); ok && rb.Batch != nil {
			if rb.Batch.Total != 2 || len(rb.Batch.Tasks) != 2 || rb.Batch.Pending != 2 {
				t.Fatalf("two tasks, one cut off twice: %+v", rb.Batch)
			}
			return
		}
	}
	t.Fatal("no batch for two tasks")
}

func TestInterruptCause(t *testing.T) {
	for _, c := range []struct {
		text   string
		silent bool
		want   string
	}{
		{gatewayDown, false, causeModelError},
		{"API Error: 429 Too Many Requests", false, causeRateLimited},
		{"Not logged in · Please run /login", false, causeAuth},
		{"anything", true, causeNoOutput},
	} {
		if got := interruptCause(c.text, c.silent); got != c.want {
			t.Errorf("%q: %s, want %s", c.text, got, c.want)
		}
	}
}
