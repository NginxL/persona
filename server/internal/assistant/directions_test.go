package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The leaders' records and leader.changed (directions.go, #3484): each change is told once, a restart or a replay of
// the same state tells nothing, a leader that leaves assistant.leaders is ended. All ids are placeholders.

// directionEnv is the assistant on a hub that takes the extensions' events, over a store that outlives it (restart).
func directionEnv(t *testing.T, st *memStore) *todoEventEnv {
	t.Helper()
	e := newTodoEventEnv(t, st)
	if err := e.s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *todoEventEnv) title(id, title string) {
	b := &e.s.titles
	b.mu.Lock()
	defer b.mu.Unlock()
	if !e.s.titlesLocked(context.Background()) {
		e.t.Fatal("titles not readable")
	}
	b.m[id] = &convTitle{MachineID: "m", Title: title, By: titleByUser, Started: time.Now()}
}

func (e *todoEventEnv) leaders(ids ...string) {
	setSettings(e.t, e.s, func(v *Settings) { v.Leaders = ids })
}

// syncLeaders compares the leaders now and returns the leader.changed events it told.
func (e *todoEventEnv) syncLeaders() []assistantwire.LeaderChangedEvent {
	e.t.Helper()
	e.s.directionsSync(context.Background())
	var out []assistantwire.LeaderChangedEvent
	for _, ev := range e.told() {
		if ev.name != assistantwire.EventLeaderChanged {
			continue
		}
		var c assistantwire.LeaderChangedEvent
		if err := json.Unmarshal(ev.raw, &c); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestLeaderChangedIsToldOncePerChange(t *testing.T) {
	st := &memStore{}
	e := directionEnv(t, st)
	e.title("conv_lead_a", "1110 | 功能 | Android parity")
	e.leaders("conv_lead_a")

	got := e.syncLeaders()
	if want := []assistantwire.LeaderChangedEvent{{ID: "conv_lead_a", Title: "1110 | 功能 | Android parity", Name: "Android parity"}}; !slices.Equal(got, want) {
		t.Fatalf("made: %+v", got)
	}
	if got = e.syncLeaders(); len(got) != 0 {
		t.Fatalf("the same state again: %+v", got)
	}

	// A restart on the same store: nothing to tell again.
	e2 := directionEnv(t, st)
	e2.title("conv_lead_a", "1110 | 功能 | Android parity")
	e2.leaders("conv_lead_a")
	if got = e2.syncLeaders(); len(got) != 0 {
		t.Fatalf("a restart told again: %+v", got)
	}

	// Retitled: told once.
	e2.title("conv_lead_a", "Android parity")
	if got = e2.syncLeaders(); len(got) != 1 || got[0].Title != "Android parity" || got[0].Ended {
		t.Fatalf("retitled: %+v", got)
	}
	if got = e2.syncLeaders(); len(got) != 0 {
		t.Fatalf("retitled, again: %+v", got)
	}

	// Ended: it left assistant.leaders. Told once, ended; the record stays, with when.
	e2.leaders()
	if got = e2.syncLeaders(); len(got) != 1 || !got[0].Ended || got[0].ID != "conv_lead_a" {
		t.Fatalf("ended: %+v", got)
	}
	if got = e2.syncLeaders(); len(got) != 0 {
		t.Fatalf("ended, again: %+v", got)
	}
	views, ok := e2.s.directionViews(context.Background())
	if !ok || views["conv_lead_a"] == nil || views["conv_lead_a"].EndedAt == nil || views["conv_lead_a"].By != directionByAssistant {
		t.Fatalf("the ended record: %+v", views)
	}

	// A leader again: no longer ended, told once.
	e2.leaders("conv_lead_a")
	if got = e2.syncLeaders(); len(got) != 1 || got[0].Ended {
		t.Fatalf("a leader again: %+v", got)
	}
	if views, _ = e2.s.directionViews(context.Background()); views["conv_lead_a"].EndedAt != nil {
		t.Fatalf("still ended: %+v", views["conv_lead_a"])
	}
}

// A direction the hub files itself starts under the project most of the leader's tasks are filed under; once filed it
// stays there (the owner moves it, H3).
func TestLeaderChangedTellsTheProjectOfItsTasks(t *testing.T) {
	e := directionEnv(t, &memStore{})
	e.title("conv_lead_a", "Infra")
	e.leaders("conv_lead_a")
	if got := e.syncLeaders(); len(got) != 1 || got[0].Project != "" {
		t.Fatalf("no tasks yet: %+v", got)
	}
	for _, p := range []string{"infra", "infra", "skills"} {
		id := e.create("task " + p).ID
		e.s.todos.mu.Lock()
		e.s.todos.todos[id].Project = p
		e.s.todos.mu.Unlock()
		e.s.superviseTodo(context.Background(), id, "conv_lead_a", nil, 0)
	}
	got := e.syncLeaders()
	if len(got) != 1 || got[0].Project != "infra" {
		t.Fatalf("filed: %+v", got)
	}
}

// A leader made and dropped before the leaders were compared is never told; the main conversation is never a
// direction.
func TestLeaderChangedSkipsWhatNobodyWasToldOf(t *testing.T) {
	e := directionEnv(t, &memStore{})
	main := e.s.settings(context.Background()).Main
	e.leaders("conv_lead_gone")
	e.leaders("conv_lead_a", main)
	got := e.syncLeaders()
	if len(got) != 1 || got[0].ID != "conv_lead_a" || got[0].Ended {
		t.Fatalf("got %+v", got)
	}
	views, _ := e.s.directionViews(context.Background())
	if _, ok := views[main]; ok && main != "" {
		t.Fatal("the main conversation got a record")
	}
	if _, ok := views["conv_lead_gone"]; ok {
		t.Fatal("a leader nobody saw got a record")
	}
}

func TestLeadersRouteListsEveryDirection(t *testing.T) {
	e := directionEnv(t, &memStore{})
	e.title("conv_lead_a", "Android parity")
	e.title("conv_lead_b", "Infra")
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()
	e.leaders("conv_lead_a")
	e.syncLeaders()
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/leaders", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Leaders []leaderView `json:"leaders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Leaders) != 2 || out.Leaders[0].ID != "conv_lead_a" || out.Leaders[0].Ended ||
		out.Leaders[1].ID != "conv_lead_b" || !out.Leaders[1].Ended || out.Leaders[1].EndedAt == nil || out.Leaders[1].Title != "Infra" {
		t.Fatalf("listed: %s", rec.Body)
	}
}

// todo.changed carries the supervisor, and a change of supervisor is told (a task moved to another leader).
func TestTodoChangedTellsTheSupervisor(t *testing.T) {
	e := newTodoEventEnv(t, nil)
	// The leaders, listed outside the health space: an extension is told of their tasks (todo_scope.go).
	e.sessions("server", proto.Session{SessionID: "conv_lead_a", Assistant: true}, proto.Session{SessionID: "conv_lead_b", Assistant: true})
	a := e.create("发版").ID
	e.settle()
	e.s.superviseTodo(context.Background(), a, "conv_lead_a", nil, 0)
	got := e.settle()
	if len(got) != 1 || changedOf(t, got[0]).Supervisor != "conv_lead_a" || changedOf(t, got[0]).What != "updated" {
		t.Fatalf("supervised: %+v", got)
	}
	if _, _, err := e.s.updateSupervision(context.Background(), a, func(v *Supervision) bool { v.Supervisor = "conv_lead_b"; return true }); err != nil {
		t.Fatal(err)
	}
	if got = e.settle(); len(got) != 1 || changedOf(t, got[0]).Supervisor != "conv_lead_b" {
		t.Fatalf("moved: %+v", got)
	}
	// The supervision changing otherwise (a review's state) is not a todo.changed.
	if _, _, err := e.s.updateSupervision(context.Background(), a, func(v *Supervision) bool { v.Round++; return true }); err != nil {
		t.Fatal(err)
	}
	if got = e.settle(); len(got) != 0 {
		t.Fatalf("a round is not a change: %+v", got)
	}
}

// The records reach the session lists through the snapshot.
func TestSnapshotCarriesTheDirection(t *testing.T) {
	e := directionEnv(t, &memStore{})
	e.title("conv_lead_a", "Android parity")
	e.leaders("conv_lead_a")
	e.syncLeaders()
	snap := e.s.snapshot(context.Background())
	i := slices.IndexFunc(snap.Sessions, func(v SessionView) bool { return v.SessionID == "conv_lead_a" })
	if i < 0 || snap.Sessions[i].Direction == nil || snap.Sessions[i].Direction.By != directionByAssistant {
		t.Fatalf("snapshot: %+v", snap.Sessions)
	}
}
