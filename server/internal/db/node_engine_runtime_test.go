package db

import (
	"context"
	"encoding/json"
	"testing"
)

// The record a node keeps of an Agent an engine runtime started says so (engine_runtime): an extension's resume of
// such an Agent its computer no longer lists passes on that record alone (node resumeOutsideHealth, #10941). It must
// cross the manager's JSON boundary and come back after a node restart, and a terminal's record written over it later
// must clear it.
func TestNodeEngineRuntimeIsPersisted(t *testing.T) {
	var state NodeSession
	if err := json.Unmarshal([]byte(`{"launcher":"paseo-claude","cwd":"/work/errands","engine_runtime":"paseo"}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.EngineRuntime != "paseo" {
		t.Fatalf("engine_runtime lost at the manager boundary: %+v", state)
	}
	d, ctx := open(t), context.Background()
	for _, want := range []string{"paseo", ""} {
		state.EngineRuntime = want
		if err := d.PutNodeSession(ctx, "machine", "session", state); err != nil {
			t.Fatal(err)
		}
		sessions, err := d.NodeSessions(ctx, "machine")
		if err != nil {
			t.Fatal(err)
		}
		got := sessions["session"]
		if got.EngineRuntime != want || got.Cwd != "/work/errands" {
			t.Fatalf("persisted %+v, want engine_runtime %q", got, want)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if v, _ := back["engine_runtime"].(string); v != want {
			t.Fatalf("state.load answers %s, want engine_runtime %q", raw, want)
		}
	}
}
