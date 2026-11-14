package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// An open item linked to a to-do that is finished is filed; one whose to-do is cancelled or still open is not.

func TestInboxTaskDoneFilesLinkedItem(t *testing.T) {
	for _, c := range []struct {
		name            string
		done, cancelled bool
		handled         bool
		twin            bool // the AI twin's processing conversation has it: its judge relinks it instead
		wantFiled       bool
	}{
		{name: "done", done: true, wantFiled: true},
		{name: "cancelled", done: true, cancelled: true},
		{name: "open"},
		{name: "handled by its conversation", done: true, handled: true},
		{name: "the twin's conversation has it", done: true, twin: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietService(t)
			in := newFakeInbox()
			f.inbox, in.svc = in, s
			if err := s.inboxReady(ctx); err != nil {
				t.Fatal(err)
			}
			todo := supervisedTodo(t, s, "Review the change")
			now := time.Now()
			s.todos.mu.Lock()
			task := s.todos.todos[todo.ID]
			if c.done {
				task.DoneAt = &now
			}
			if c.cancelled {
				task.CancelledAt = &now
			}
			s.todos.mu.Unlock()
			it := &InboxItem{ID: "github:done-task", Source: "github", Kind: KindMention, Title: "Review comment", Last: "1", UpdatedAt: now,
				Agent: &InboxAgent{State: AgentSkipped, TodoID: todo.ID}}
			if c.handled {
				it.Handling = &InboxHandling{State: HandlingWaiting}
			}
			if c.twin {
				it.Agent.State, it.Agent.MachineID, it.Agent.SessionID = AgentDrafted, "server", "handling-1"
			}
			s.inbox.mu.Lock()
			s.inbox.items[it.ID] = it
			s.inbox.mu.Unlock()
			s.linkInboxTaskURLs(ctx)
			s.inbox.mu.Lock()
			defer s.inbox.mu.Unlock()
			if filed := !it.open(); filed != c.wantFiled {
				t.Fatalf("filed=%v, want %v (done_by %q)", filed, c.wantFiled, it.DoneBy)
			}
			if !c.wantFiled {
				return
			}
			if it.DoneBy != doneByManager {
				t.Fatalf("done_by %q, want %q", it.DoneBy, doneByManager)
			}
			want := i18n.T(s.inbox.userLang(), "assistant-inbox_chain.done_task")
			var found bool
			for _, e := range it.Chain {
				if e.Kind == ChainDone && e.Text == want && e.Detail == "Review the change" && e.Link != nil && e.Link.ID == todo.ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("no done_task chain entry: %+v", it.Chain)
			}
			// Reopened by hand, the same finished to-do doesn't file it again.
			it.DoneAt, it.DoneBy = nil, ""
			s.inbox.mu.Unlock()
			s.linkInboxTaskURLs(ctx)
			s.inbox.mu.Lock()
			if !it.open() {
				t.Fatalf("filed again after a reopen: %+v", it)
			}
		})
	}
}
