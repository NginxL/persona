package db

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

func TestNodeModelSourceSurvivesManagerPersistence(t *testing.T) {
	d := open(t)
	want := modelhttp.Source{Role: "routine", Todo: "todo_123", Routine: "routine_456"}
	raw, _ := json.Marshal(want)
	state := NodeSession{Launcher: "example", Cwd: "/work/example", ModelSource: raw}
	if err := d.PutNodeSession(t.Context(), "machine", "session", state); err != nil {
		t.Fatal(err)
	}
	sessions, err := d.NodeSessions(t.Context(), "machine")
	if err != nil {
		t.Fatal(err)
	}
	var got modelhttp.Source
	if err := json.Unmarshal(sessions["session"].ModelSource, &got); err != nil || got != want {
		t.Fatalf("source=%+v err=%v", got, err)
	}
	state.ModelSource = nil
	if err := d.PutNodeSession(t.Context(), "machine", "session", state); err != nil {
		t.Fatal(err)
	}
	sessions, err = d.NodeSessions(t.Context(), "machine")
	if err != nil || len(sessions["session"].ModelSource) != 0 {
		t.Fatalf("stale source retained, err=%v", err)
	}
}
