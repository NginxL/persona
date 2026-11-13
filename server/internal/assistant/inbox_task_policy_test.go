package assistant

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

func taskNotification(sender string) *InboxConnector {
	return &InboxConnector{Thread: "larkcli:chat:example", Tags: []connector.Tag{{Key: "sender", Value: sender}}}
}

func TestInboxTaskBulkNotificationsSkipBothRouters(t *testing.T) {
	for _, title := range []string{"Build result", "Daily menu"} {
		t.Run(title, func(t *testing.T) {
			h, todo, it := taskRecheckHub(t)
			const pr = "https://github.com/example/project/pull/123"
			h.todos.mu.Lock()
			h.todos.todos[todo.ID].PRURL = pr
			h.todos.mu.Unlock()
			it.Kind, it.Title, it.Context, it.Connector = KindBot, title, pr, taskNotification("cli_example")
			model, calls := captureModel(t, `{"task_id":"`+todo.ID+`"}`)
			h.in.setModel(model)
			h.linkInboxTaskURLs(context.Background())
			if h.linkTask(context.Background(), it, []prompts.InboxTask{{ID: todo.ID}}) || it.Agent.TodoID != "" || len(calls()) != 0 {
				t.Fatalf("a broadcast was associated or judged: %+v, calls=%d", it.Agent, len(calls()))
			}
			out, code, err := h.recheckInboxTask(context.Background(), it.ID)
			if err != nil || code != http.StatusOK || out.Status != "excluded" || out.Method != "policy" || len(calls()) != 0 {
				t.Fatalf("recheck: %d %+v %v; calls=%d", code, out, err, len(calls()))
			}
		})
	}
}

func TestInboxTaskLegacyHumanCanStillLink(t *testing.T) {
	h, todo, it := taskRecheckHub(t)
	it.Kind, it.Connector = KindBot, taskNotification("ou_example")
	it.Context = "I forwarded a build notification. Please investigate the failure."
	model, calls := captureModel(t, `{"task_id":"`+todo.ID+`","evidence":"修登录崩溃"}`)
	h.in.setModel(model)
	out, code, err := h.recheckInboxTask(context.Background(), it.ID)
	if err != nil || code != 200 || out.Status != "linked" || it.Agent.TodoID != todo.ID || len(calls()) != 1 {
		t.Fatalf("human conversation excluded: %d %+v %v", code, out, err)
	}
	// An explicit user identity in another connector's opaque ID is not a Lark identity.
	it.Connector.Thread = "example:chat:other"
	if inboxTaskLinkEligible(it) {
		t.Fatal("a different connector's sender ID overrode its bot kind")
	}
}

func TestInboxTaskHandledBroadcastDoesNotFollowContinuation(t *testing.T) {
	s, _ := continuesService(t)
	previous := supervisedTodo(t, s, "Old work")
	next := supervisedTodo(t, s, "Continuing work")
	closeTodo(t, s, previous.ID)
	continuesTask(s, next.ID, previous.ID)
	it := heldItem(s, previous.ID)
	now := time.Now()
	s.inbox.mu.Lock()
	it.Kind, it.Connector, it.DoneAt = KindBot, taskNotification("cli_example"), &now
	s.inbox.mu.Unlock()

	s.linkInboxTaskURLs(context.Background())
	if got := linkOf(s, it); got != previous.ID {
		t.Fatalf("a handled broadcast moved onto active work: %q, want legacy link %q", got, previous.ID)
	}
	if signal, ok := pendingInbox(s, next.ID); ok {
		t.Fatalf("a handled broadcast raised a signal for the continuing task: %+v", signal)
	}
}

func TestInboxTaskRecheckClearsBulkLinkOnly(t *testing.T) {
	h, todo, it := taskRecheckHub(t)
	it.Kind, it.Connector, it.Agent.TodoID = KindBot, taskNotification("cli_example"), todo.ID
	before := *it.Agent
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	model, calls := captureModel(t, `{"task_id":"`+todo.ID+`"}`)
	h.in.setModel(model)
	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/task-link/recheck", "")
	after := *it.Agent
	before.TodoID = ""
	if code != 200 || out["status"] != "cleared" || out["method"] != "policy" || string(rawJSON(before)) != string(rawJSON(after)) {
		t.Fatalf("correction: %d %+v; agent %+v", code, out, it.Agent)
	}
	if !it.open() || it.Handling.State != HandlingDone || it.Suggestion.Version != 3 || len(calls()) != 0 || len(h.f.notices()) != 0 {
		t.Fatal("correction changed handling, called the model or notified a conversation")
	}
	if got, ok := h.todoNow(todo.ID); !ok || got.DoneAt != nil {
		t.Fatal("correction closed the task")
	}
	// Read through the API after persistence and ensure a deterministic pass cannot restore the association.
	h.linkInboxTaskURLs(context.Background())
	_, read := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/"+it.ID, "")
	if a, ok := read["agent"].(map[string]any); !ok || a["todo_id"] != nil {
		t.Fatalf("cleared link reappeared: %+v", read)
	}
}

func TestInboxTaskBulkCorrectionRollsBackFailedSave(t *testing.T) {
	h, st := newBulkFailHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.in.svc, h.f.inbox = h.Service, h.in
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := actionItem("example:notification", time.Now())
	it.Kind, it.Connector, it.Agent = KindBot, taskNotification("cli_example"), &InboxAgent{State: AgentSkipped, TodoID: "original"}
	h.inbox.items[it.ID] = it
	before := stateOf(h, it)
	st.failAll = true
	_, code, err := h.recheckInboxTask(context.Background(), it.ID)
	if code != 503 || err == nil || stateOf(h, it) != before || changedTo(h, it.ID) {
		t.Fatalf("failed correction was visible: %d %v %+v", code, err, it.Agent)
	}
	st.failAll = false
	out, code, err := h.recheckInboxTask(context.Background(), it.ID)
	if code != 200 || err != nil || out.Status != "cleared" || !st.hasRow(it.ID) {
		t.Fatalf("retry: %d %+v %v", code, out, err)
	}
}
