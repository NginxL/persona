package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Concerns (concerns.go, #3561): the owner's concern is a to-do of kind concern that closes only once the main
// conversation reported its outcome (a decision of source reported), never on its own, and extensions see it in
// todo.changed. conv1 is the main conversation; ids are placeholders.

func newConcernEnv(t *testing.T) *todoEventEnv {
	t.Helper()
	e := newTodoEventEnv(t, todoTestDB(t))
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	// Listed outside the health space: an extension is told of the concerns it records (todo_scope.go).
	e.sessions("server", proto.Session{SessionID: "conv1", Title: "Persona", Assistant: true})
	return e
}

func TestConcernClosesOnlyAfterItsReport(t *testing.T) {
	e := newConcernEnv(t)
	ctx := context.Background()

	// Only the main conversation records one.
	if _, err := runJSON(t, e.s, "conv_leader", "todo_create", map[string]any{"title": "发版会不会拖到下周", "kind": "concern"}); err == nil {
		t.Fatal("a concern recorded outside the main conversation")
	}
	out, err := e.tool("todo_create", map[string]any{"title": "发版会不会拖到下周", "kind": "concern"})
	if err != nil {
		t.Fatal(err)
	}
	c := onlyTodo(e.s)
	if c.Kind != db.KindConcern || c.Origin != "conv1" || c.DoneAt != nil {
		t.Fatalf("recorded: %+v (%s)", c, out)
	}
	if got := e.settle(); len(got) != 1 || changedOf(t, got[0]) != (assistantwire.TodoChangedEvent{ID: c.ID, Title: "发版会不会拖到下周",
		Kind: "concern", What: "created"}) {
		t.Fatalf("todo.changed on recording: %+v", got)
	}

	// Nothing closes it before its report: by hand (a device, an extension), the tool, the judge, a review, a failed start.
	if w := e.do("PATCH", "/api/v1/todos/"+c.ID, `{"done":true}`); w.Code != http.StatusConflict {
		t.Fatalf("closed by hand: %d %s", w.Code, w.Body)
	}
	if w := e.doExt("worklog", "PATCH", "/api/v1/todos/"+c.ID, `{"done":true}`); w.Code != http.StatusConflict {
		t.Fatalf("closed by an extension: %d %s", w.Code, w.Body)
	}
	if _, err := e.tool("todo_complete", map[string]any{"id": c.ID}); err == nil {
		t.Fatal("closed by todo_complete")
	}
	if ok, err := e.s.autoDoneTodo(ctx, c.ID, "looks finished", time.Now().Add(-time.Hour)); ok || err != nil {
		t.Fatalf("closed by the judge: %v %v", ok, err)
	}
	for _, accepted := range []bool{true, false} {
		if ok, err := e.s.reviewDoneTodo(ctx, c.ID, "accepted", accepted); ok || err != nil {
			t.Fatalf("closed by a review (accepted %v): %v %v", accepted, ok, err)
		}
	}
	if err := e.s.failTodo(ctx, c.ID, "did not start"); err != nil {
		t.Fatal(err)
	}
	if got := e.get(c.ID); got == nil || got.DoneAt != nil || got.StartError != "" {
		t.Fatalf("closed before its report: %+v", got)
	}
	if got := e.settle(); len(got) != 0 {
		t.Fatalf("told a change that did not happen: %+v", got)
	}

	// The report: only the main conversation's, never empty, never on another to-do.
	plain := e.create("买牛奶")
	e.settle()
	for _, bad := range []struct {
		self string
		args map[string]any
	}{
		{"conv_leader", map[string]any{"id": c.ID, "report": "不会"}},
		{"conv1", map[string]any{"id": c.ID, "report": "  "}},
		{"conv1", map[string]any{"id": plain.ID, "report": "买了"}},
	} {
		if _, err := runJSON(t, e.s, bad.self, "concern_report", bad.args); err == nil {
			t.Fatalf("reported: %+v", bad)
		}
	}
	if got := e.get(c.ID); got.DoneAt != nil {
		t.Fatalf("closed by a refused report: %+v", got)
	}
	out, err = e.tool("concern_report", map[string]any{"id": c.ID, "report": "不会拖：周四照常发，补丁已经合进去了"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.get(c.ID)
	if got.DoneAt == nil || got.DoneBy != db.DoneByReported || got.DoneReason != "不会拖：周四照常发，补丁已经合进去了" || got.Kind != db.KindConcern {
		t.Fatalf("after its report: %+v (%s)", got, out)
	}
	ds, _ := e.s.todoDB().ListTodoDecisions(ctx, c.ID, false)
	if len(ds) != 1 || ds[0].Source != db.DecisionReported || ds[0].Choice != "不会拖：周四照常发，补丁已经合进去了" || ds[0].SourceSession != "conv1" {
		t.Fatalf("the report as a decision: %+v", ds)
	}
	// Extensions: the report as todo.decision at once, then the close in todo.changed with its kind.
	told := e.told()
	if len(told) != 1 || decisionOf(t, told[0]).Decision.Source != "reported" || decisionOf(t, told[0]).ID != c.ID {
		t.Fatalf("todo.decision of the report: %+v", told)
	}
	if told = e.settle(); len(told) != 1 || changedOf(t, told[0]) != (assistantwire.TodoChangedEvent{ID: c.ID, Title: "发版会不会拖到下周",
		Done: true, Kind: "concern", What: "updated"}) {
		t.Fatalf("todo.changed on the report: %+v", told)
	}

	// Reopened, it needs another report: by hand it still does not close; a report by hand (a device) does.
	e.must("PATCH", "/api/v1/todos/"+c.ID, `{"done":false}`)
	if told = e.settle(); len(told) != 1 || changedOf(t, told[0]) != (assistantwire.TodoChangedEvent{ID: c.ID, Title: "发版会不会拖到下周",
		Kind: "concern", What: "updated"}) {
		t.Fatalf("todo.changed on the reopen: %+v", told)
	}
	if w := e.do("PATCH", "/api/v1/todos/"+c.ID, `{"done":true}`); w.Code != http.StatusConflict {
		t.Fatalf("closed by hand after a reopen: %d %s", w.Code, w.Body)
	}
	if w := e.doExt("worklog", "POST", "/api/v1/todos/"+c.ID+"/decisions", `{"question":"q","choice":"done","source":"reported"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an extension reported: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/todos/"+plain.ID+"/decisions", `{"choice":"买了","source":"reported"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a plain to-do reported: %d %s", w.Code, w.Body)
	}
	var res struct {
		Todo   Todo
		Closed bool
	}
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos/"+c.ID+"/decisions", `{"choice":"改到下周一了，已经告诉你","source":"reported"}`).Body.Bytes(), &res)
	if !res.Closed || res.Todo.DoneAt == nil || res.Todo.DoneBy != db.DoneByReported {
		t.Fatalf("a report by hand: %+v", res)
	}
	if ds, _ = e.s.todoDB().ListTodoDecisions(ctx, c.ID, false); len(ds) != 2 {
		t.Fatalf("two reports kept: %+v", ds)
	}
	if told = e.told(); len(told) != 1 || decisionOf(t, told[0]).Decision.Source != "reported" {
		t.Fatalf("todo.decision of the second report: %+v", told)
	}
	if told = e.settle(); len(told) != 1 || changedOf(t, told[0]) != (assistantwire.TodoChangedEvent{ID: c.ID, Title: "发版会不会拖到下周",
		Done: true, Kind: "concern", What: "updated"}) {
		t.Fatalf("todo.changed on the second report: %+v", told)
	}
}

// todo_list finds the open concerns; a plain to-do is unchanged (no kind in the apps' JSON or in todo.changed).
func TestConcernsListAndPlainTodosUnchanged(t *testing.T) {
	e := newConcernEnv(t)
	if _, err := e.tool("todo_create", map[string]any{"title": "新机器什么时候到", "kind": "concern"}); err != nil {
		t.Fatal(err)
	}
	plain := e.create("买牛奶")
	out, err := e.tool("todo_list", map[string]any{"kind": "concern"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "新机器什么时候到") || !strings.Contains(out, ", concern") || strings.Contains(out, "买牛奶") {
		t.Fatalf("todo_list kind concern: %s", out)
	}
	var raw struct{ Todos []map[string]any }
	_ = json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &raw)
	for _, td := range raw.Todos {
		if _, has := td["kind"]; has != (td["id"] != plain.ID) {
			t.Fatalf("kind in the apps' JSON: %v", td)
		}
	}
	for _, ev := range e.settle() {
		var fields map[string]any
		_ = json.Unmarshal(ev.raw, &fields)
		if _, has := fields["kind"]; has != (fields["id"] != plain.ID) {
			t.Fatalf("kind in todo.changed: %s", ev.raw)
		}
	}
	if w := e.do("POST", "/api/v1/todos", `{"title":"x","kind":"bug"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind: %d %s", w.Code, w.Body)
	}
}

// Without the hub's database a concern could never be reported, so none is recorded.
func TestConcernNeedsTheStore(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	if w := e.do("POST", "/api/v1/todos", `{"title":"x","kind":"concern"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a concern in memory: %d %s", w.Code, w.Body)
	}
	if _, err := e.tool("todo_create", map[string]any{"title": "x", "kind": "concern"}); err == nil {
		t.Fatal("a concern in memory by the tool")
	}
}
