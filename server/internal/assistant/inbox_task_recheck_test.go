package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func taskRecheckHub(t *testing.T) (*inboxHub, Todo, *InboxItem) {
	t.Helper()
	s, f := newQuietService(t)
	s.inbox.handing.Lock() // isolate the maintenance action from background processing
	t.Cleanup(s.inbox.handing.Unlock)
	in := newFakeInbox()
	f.inbox, in.svc = in, s
	h := &inboxHub{Service: s, f: f, in: in}
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	todo := startedTask(t, s, f)
	it := actionItem("example:old", time.Now().Add(-72*time.Hour))
	it.Agent = &InboxAgent{State: AgentDrafted, MachineID: "box", SessionID: "processing", Of: it.Last,
		Draft: "The fix is being checked.", Note: "Keep this draft."}
	it.Handling = &InboxHandling{State: HandlingDone}
	it.Recap = "The investigation traced the crash to screen rotation."
	it.Context = "The original human message reports a login crash when rotating the screen."
	it.Suggestion = &InboxSuggestion{Kind: SuggestReply, Text: it.Agent.Draft, Version: 3}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	return h, todo, it
}

func TestInboxTaskRecheckDraft(t *testing.T) {
	h, todo, it := taskRecheckHub(t)
	turnInboxSignalsOn(t, h.Service)
	h.superviseTodo(context.Background(), todo.ID, "conv1", []string{"Verified"}, 0)
	before := *it.Agent
	model, calls := captureModel(t, `{"task_id":"`+todo.ID+`","evidence":"修登录崩溃"}`)
	h.in.setModel(model)
	path := "/api/v1/inbox/" + it.ID + "/task-link/recheck"
	code, out := inboxCall(t, h.routes(), "POST", path, "")
	if code != 200 || out["status"] != "linked" || out["method"] != "judge" || it.Agent.TodoID != todo.ID {
		t.Fatalf("recheck: %d %+v; agent %+v", code, out, it.Agent)
	}
	after := *it.Agent
	after.TodoID = ""
	if string(rawJSON(before)) != string(rawJSON(after)) || !it.open() || it.Handling.State != HandlingDone || it.Suggestion.Version != 3 {
		t.Fatalf("processing changed: %+v / %+v / %+v", it.Agent, it.Handling, it.Suggestion)
	}
	c := onlyCall(t, calls())
	if !strings.Contains(c.User, todo.ID) || !strings.Contains(c.User, todo.Notes) || !strings.Contains(c.User, it.Context) {
		t.Fatalf("the judge did not see the task and original event evidence: %s", c.User)
	}
	if strings.Contains(c.User, it.Recap) {
		t.Fatal("a generated recap must not become original task-link evidence")
	}
	waitFor(t, "the supervisor's Inbox notice", func() bool {
		return strings.Contains(strings.Join(h.f.notices(), "\n"), it.ID)
	})
	if !strings.Contains(feedKinds(h.Service, "conv1"), feedMessage) {
		t.Fatal("the existing draft has no task message card")
	}
	if code, out = inboxCall(t, h.routes(), "POST", path, ""); code != 200 || out["status"] != "existing" || len(calls()) != 1 {
		t.Fatalf("repeat: %d %+v, model calls %d", code, out, len(calls()))
	}
}

func TestInboxTaskRecheckUnmatchedAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, reply, status string
		code                int
	}{
		{"unmatched", `{"task_id":""}`, "unmatched", 200},
		{"unknown task", `{"task_id":"not-a-candidate"}`, "unmatched", 200},
		{"model failure", `not json`, "", 502},
		{"disabled", `{"task_id":""}`, "disabled", 409},
		{"closed", `{"task_id":""}`, "closed", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, it := taskRecheckHub(t)
			if tc.name == "disabled" {
				h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
			}
			if tc.name == "closed" {
				now := time.Now()
				it.DoneAt = &now
			}
			before := stateOf(h, it)
			model, calls := captureModel(t, tc.reply)
			h.in.setModel(model)
			code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/task-link/recheck", "")
			if code != tc.code || tc.status != "" && out["status"] != tc.status || stateOf(h, it) != before {
				t.Fatalf("recheck: %d %+v; changed=%v", code, out, stateOf(h, it) != before)
			}
			if (tc.name == "disabled" || tc.name == "closed") && len(calls()) != 0 {
				t.Fatal("an ineligible item called the model")
			}
		})
	}
}

func TestInboxTaskRecheckConcurrentChanges(t *testing.T) {
	for _, change := range []string{"new message", "filed", "task closed", "linked elsewhere"} {
		t.Run(change, func(t *testing.T) {
			h, todo, it := taskRecheckHub(t)
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.inbox.mu.Lock()
				switch change {
				case "new message":
					it.Last = "new"
				case "filed":
					now := time.Now()
					it.DoneAt = &now
				case "linked elsewhere":
					it.Agent.TodoID = "other"
				}
				h.inbox.mu.Unlock()
				if change == "task closed" {
					h.todos.mu.Lock()
					now := time.Now()
					h.todos.todos[todo.ID].DoneAt = &now
					h.todos.mu.Unlock()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"task_id":"` + todo.ID + `","evidence":"修登录崩溃"}`}}})
			}))
			t.Cleanup(model.Close)
			h.in.setModel(modelAt(t, model.URL))
			out, code, err := h.recheckInboxTask(context.Background(), it.ID)
			if change == "linked elsewhere" {
				if err != nil || code != 200 || out.Status != "existing" || it.Agent.TodoID != "other" {
					t.Fatalf("existing link overwritten: %d %+v %v", code, out, err)
				}
			} else if err != nil || code != 409 || it.Agent.TodoID != "" {
				t.Fatalf("stale judgment applied: %d %+v %v", code, out, err)
			}
		})
	}
}

func TestInboxTaskRecheckSaveFailure(t *testing.T) {
	h, st := newBulkFailHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.in.svc, h.f.inbox = h.Service, h.in
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	todo, err := h.createTodo(context.Background(), "Check the fix", "Verify it", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	it := actionItem("example:old", time.Now())
	h.inbox.items[it.ID] = it
	h.in.setModel(fakeModel(t, `{"task_id":"`+todo.ID+`","evidence":"Check the fix"}`))
	before := stateOf(h, it)
	st.failAll = true
	_, code, err := h.recheckInboxTask(context.Background(), it.ID)
	if code != 503 || err == nil || stateOf(h, it) != before || st.hasRow(it.ID) || changedTo(h, it.ID) {
		t.Fatalf("failed write stuck: %d %v %+v", code, err, it.Agent)
	}
	st.failAll = false
	out, code, err := h.recheckInboxTask(context.Background(), it.ID)
	if code != 200 || err != nil || out.Status != "linked" || !st.hasRow(it.ID) {
		t.Fatalf("retry: %d %+v %v", code, out, err)
	}
}

func TestInboxTaskRecheckAllOpenCandidates(t *testing.T) {
	h, todo, it := taskRecheckHub(t)
	ctx := context.Background()
	var ids []string
	for i := range 15 {
		td, err := h.createTodo(ctx, fmt.Sprintf("Open task %d", i), "Check progress", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, td.ID)
	}
	h.todos.mu.Lock()
	h.todos.todos[todo.ID].UpdatedAt = time.Now().Add(-8 * 24 * time.Hour)
	now := time.Now()
	h.todos.todos[ids[0]].DoneAt = &now
	h.todos.mu.Unlock()
	model, calls := captureModel(t, `{"task_id":"`+todo.ID+`","evidence":"修登录崩溃"}`)
	h.in.setModel(model)
	out, code, err := h.recheckInboxTask(ctx, it.ID)
	if err != nil || code != 200 || out.Status != "linked" {
		t.Fatalf("old open task was lost: %d %+v %v", code, out, err)
	}
	input := onlyCall(t, calls()).User
	if strings.Contains(input, ids[0]) {
		t.Fatal("the judge was shown a closed task")
	}
	for _, id := range append(ids[1:], todo.ID) {
		if !strings.Contains(input, id) {
			t.Errorf("the judge did not see open task %s", id)
		}
	}
}
