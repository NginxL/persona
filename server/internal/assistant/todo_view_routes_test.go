package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos view on the wire (todo_view_routes.go, #10226): every row of the list carries its view and the list its
// counts, one to-do reads as its row, and the places come with the same counts.

// The manager tells the hub it serves the view, the places and a to-do's timeline (todo_timeline.go).
func TestTodoViewBusHelloCapabilities(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "t"})
	p, _ := b.client.HelloParams().(map[string]any)
	caps, _ := p["capabilities"].([]string)
	for _, c := range []string{assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoPlaces, assistantwire.CapabilityTodoTimeline, "todo_images"} {
		if !slices.Contains(caps, c) {
			t.Errorf("the manager does not name %s: %v", c, caps)
		}
	}
}

// todoViewRows makes a plain to-do, one a run works on, a closed one and a failed start, and answers their ids.
func todoViewRows(t *testing.T, e *todoEnv) (plain, worked, closed, failed string) {
	t.Helper()
	began := time.Now().UTC().Add(-time.Minute)
	e.sessions("server", proto.Session{SessionID: "s1", Title: "Hedge trimming", Status: proto.StatusWorking,
		State: proto.StateWorking, StateSince: began, UpdatedAt: began, Activity: "measuring the hedge"})
	plain, worked, closed, failed = e.create("Water the roses").ID, e.create("Prune the hedge").ID, e.create("Order compost").ID,
		e.create("Book the ferry").ID
	e.must("PUT", "/api/v1/todos/"+worked+"/agents/server/s1", "")
	e.must("PATCH", "/api/v1/todos/"+closed, `{"done":true}`)
	if err := e.s.failTodo(context.Background(), failed, "The computer is offline"); err != nil {
		t.Fatal(err)
	}
	return plain, worked, closed, failed
}

// The list: every row with its view, the views and counts the projection works out for the same rows; the same state
// read again is the same bytes (its views hold no time relative to now), so the hub's tag holds; and each to-do on its
// own reads exactly as its row, an unknown one 404.
func TestTodosListCarriesTheView(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		plain, worked, closed, failed := todoViewRows(t, e)
		body := e.must("GET", "/api/v1/todos", "").Body.Bytes()
		var got struct {
			Todos  []listedTodo
			Counts *assistantwire.TodoCounts
		}
		if err := json.Unmarshal(body, &got); err != nil || len(got.Todos) != 4 || got.Counts == nil {
			t.Fatalf("list: %v %s", err, body)
		}
		views := map[string]assistantwire.TodoSummary{}
		rows := slices.Clone(got.Todos)
		for i, r := range got.Todos {
			if r.View == nil {
				t.Fatalf("%s has no view: %s", r.ID, body)
			}
			views[r.ID] = *r.View
			rows[i].View = nil
		}
		p := e.s.todoProjectionOf(ctx, rows, time.Now())
		listed, _ := json.Marshal(views)
		projected, _ := json.Marshal(p.byID())
		if !bytes.Equal(listed, projected) {
			t.Fatalf("the list's views:\n%s\nthe projection's:\n%s", listed, projected)
		}
		if !reflect.DeepEqual(*got.Counts, p.counts()) || got.Counts.Open != 3 || got.Counts.InProgress != 1 || got.Counts.NeedsYou != 1 {
			t.Fatalf("the list's counts %+v, the projection's %+v", *got.Counts, p.counts())
		}
		for id, reason := range map[string]string{plain: assistantwire.TodoReasonRest, worked: assistantwire.TodoReasonAgentRunning,
			closed: assistantwire.TodoReasonClosedUnseen, failed: assistantwire.TodoReasonFailedStart} {
			if views[id].Reason != reason {
				t.Errorf("%s: %s, want %s", id, views[id].Reason, reason)
			}
		}

		// The same state: the same bytes, and the same views a minute on.
		if again := e.must("GET", "/api/v1/todos", "").Body.Bytes(); !bytes.Equal(again, body) {
			t.Fatalf("unchanged state read twice:\n%s\n%s", body, again)
		}
		a, _ := json.Marshal(e.s.todoProjectionOf(ctx, rows, time.Now()).views)
		b, _ := json.Marshal(e.s.todoProjectionOf(ctx, rows, time.Now().Add(time.Minute)).views)
		if !bytes.Equal(a, b) {
			t.Fatalf("the views moved with the clock alone:\n%s\n%s", a, b)
		}

		// One to-do: {"todo": its row}, byte for byte as the list has it.
		var raw struct{ Todos []json.RawMessage }
		_ = json.Unmarshal(body, &raw)
		for i, r := range got.Todos {
			var one struct{ Todo json.RawMessage }
			if err := json.Unmarshal(e.must("GET", "/api/v1/todos/"+r.ID, "").Body.Bytes(), &one); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(one.Todo, raw.Todos[i]) {
				t.Errorf("%s on its own:\n%s\nin the list:\n%s", r.ID, one.Todo, raw.Todos[i])
			}
		}
		w := e.do("GET", "/api/v1/todos/todo_0000000000000000", "")
		var refused struct{ Code string }
		if _ = json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusNotFound || refused.Code != "todo_not_found" {
			t.Fatalf("an unknown to-do: %d %s", w.Code, w.Body)
		}
	})
}

// The places: All To-dos with N, Today, Upcoming, and the project of an open to-do titled by the knowledge base, with
// the list's counts; and "places" is not taken for a to-do's id.
func TestTodoPlacesRoute(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newKBService(t, f)
	if _, err := kbEng(t, s).Apply(kb.KindProject, "Garden", kb.Update{Name: "Garden", Kind: "long"}); err != nil {
		t.Fatal(err)
	}
	e := &todoEnv{t: t, s: s, f: f}
	todoViewRows(t, e)
	var created struct{ Todo Todo }
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos", `{"title":"Mulch the beds","project":"Garden"}`).Body.Bytes(), &created)

	var places assistantwire.TodoPlaces
	if err := json.Unmarshal(e.must("GET", "/api/v1/todos/places", "").Body.Bytes(), &places); err != nil {
		t.Fatal(err)
	}
	var list struct{ Counts assistantwire.TodoCounts }
	_ = json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &list)
	ids := []string{}
	for _, p := range places.Places {
		ids = append(ids, p.ID)
	}
	want := []string{"all", "today", "upcoming", "project:" + created.Todo.Project}
	if !slices.Equal(ids, want) || places.Places[0].NeedsYou != 1 || places.Places[3].Title != "Garden" || places.Places[3].Manager != "" ||
		!reflect.DeepEqual(places.Counts, list.Counts) || places.Counts.Open != 4 {
		t.Fatalf("places %+v, the list's counts %+v", places, list.Counts)
	}
}
