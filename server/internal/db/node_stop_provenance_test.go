package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestNodeSessionStopProvenanceJSON(t *testing.T) {
	state := NodeSession{
		Launcher:    "claude",
		Cwd:         "workspace",
		StopCause:   proto.StopTaskCompleted,
		StopCauseAt: 42,
		AppMode:     "plan",
		AppEffort:   "high",
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var got NodeSession
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.StopCause != state.StopCause {
		t.Fatalf("stop provenance round trip = %q, want %q", got.StopCause, state.StopCause)
	}
	if got.AppMode != state.AppMode || got.AppEffort != state.AppEffort || got.Launcher != state.Launcher {
		t.Fatalf("stop provenance changed unrelated session state: %+v", got)
	}

	var legacy NodeSession
	if err := json.Unmarshal([]byte(`{"launcher":"codex","cwd":"workspace","app_mode":"default"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.StopCause != proto.StopUnknown || legacy.Launcher != "codex" || legacy.AppMode != "default" {
		t.Fatalf("legacy node session decoded unexpectedly: %+v", legacy)
	}
}

func TestNodeSessionStopProvenancePersistence(t *testing.T) {
	d, ctx := open(t), context.Background()
	causes := []proto.StopCause{proto.StopUnknown, proto.StopUser, proto.StopIdle, proto.StopTaskCompleted, proto.StopRestart, proto.StopUsageLimit, proto.StopTransientError}
	for i, cause := range causes {
		want := NodeSession{
			Launcher: "claude", Cwd: "workspace", StopCause: cause, StopCauseAt: int64(i + 1),
			AppMode: "plan", AppEffort: "high",
		}
		if err := d.PutNodeSession(ctx, "synthetic-machine", "synthetic-session", want); err != nil {
			t.Fatal(err)
		}
		got, err := d.NodeSessions(ctx, "synthetic-machine")
		if err != nil {
			t.Fatal(err)
		}
		stored, ok := got["synthetic-session"]
		if !ok {
			t.Fatal("node session missing after persistence")
		}
		if stored.StopCause != cause || stored.StopCauseAt != want.StopCauseAt {
			t.Fatalf("cause %q persisted as %q", cause, stored.StopCause)
		}
		if stored.Launcher != want.Launcher || stored.AppMode != want.AppMode || stored.AppEffort != want.AppEffort {
			t.Fatalf("stop provenance changed launcher or app mode: %+v", stored)
		}
	}

	cleared := NodeSession{Launcher: "claude", Cwd: "workspace", StopCause: proto.StopUnknown, StopCauseAt: 100}
	if err := d.PutNodeSession(ctx, "synthetic-machine", "synthetic-session", cleared); err != nil {
		t.Fatal(err)
	}
	got, err := d.NodeSessions(ctx, "synthetic-machine")
	if err != nil {
		t.Fatal(err)
	}
	stored := got["synthetic-session"]
	if stored.StopCause != proto.StopUnknown {
		t.Fatalf("cleared stop provenance = %q, want empty", stored.StopCause)
	}
	if stored.Launcher != cleared.Launcher {
		t.Fatalf("clearing provenance changed launcher: %+v", stored)
	}
}

func TestNodeSessionStaleProvenanceCannotUndoUserStop(t *testing.T) {
	d, ctx := open(t), context.Background()
	for _, tc := range []struct {
		name  string
		cause proto.StopCause
		at    int64
		want  proto.StopCause
	}{
		{"old automatic observation", proto.StopIdle, 10, proto.StopUser},
		{"legacy metadata without provenance", proto.StopUnknown, 0, proto.StopUser},
		{"equal revision cannot undo user", proto.StopIdle, 20, proto.StopUser},
		{"new running observation", proto.StopUnknown, 21, proto.StopUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := NodeSession{Launcher: "claude", Cwd: "workspace", AppMode: "plan", StopCause: proto.StopUser, StopCauseAt: 20}
			if err := d.PutNodeSession(ctx, "machine", tc.name, initial); err != nil {
				t.Fatal(err)
			}
			stale := initial
			stale.StopCause, stale.StopCauseAt = tc.cause, tc.at
			stale.Model = "updated-model"
			if err := d.PutNodeSession(ctx, "machine", tc.name, stale); err != nil {
				t.Fatal(err)
			}
			sessions, err := d.NodeSessions(ctx, "machine")
			if err != nil {
				t.Fatal(err)
			}
			got := sessions[tc.name]
			if got.StopCause != tc.want || got.Model != "updated-model" || got.AppMode != "plan" {
				t.Fatalf("stale metadata undid user intent or unrelated metadata was lost: %+v", got)
			}
		})
	}
}

// ran_at, the node's last sighting of an Agent running, survives the hub (#11528): a node that starts again reads it to
// tell an Agent its restart cut off (node interrupted.go). Without it every such stop was unknown. A put that carries
// none, or an older one, keeps the last sighting; one with a stale stop cause keeps the newer cause and still moves it.
func TestNodeSessionRanAtSurvivesTheHub(t *testing.T) {
	d, ctx := open(t), context.Background()
	steps := []struct {
		name string
		put  NodeSession
		want int64
	}{
		{"first sighting", NodeSession{Launcher: "claude", Cwd: "workspace", RanAt: 1000}, 1000},
		{"later sighting", NodeSession{Launcher: "claude", Cwd: "workspace", RanAt: 1300}, 1300},
		{"a put without one keeps it", NodeSession{Launcher: "claude", Cwd: "workspace", Model: "m"}, 1300},
		{"an older one does not move it back", NodeSession{Launcher: "claude", Cwd: "workspace", RanAt: 1100}, 1300},
		{"a user stop keeps it", NodeSession{Launcher: "claude", Cwd: "workspace", StopCause: proto.StopUser, StopCauseAt: 50, RanAt: 1300}, 1300},
		{"a stale cause still moves it", NodeSession{Launcher: "claude", Cwd: "workspace", StopCause: proto.StopIdle, StopCauseAt: 10, RanAt: 1600}, 1600},
	}
	for _, step := range steps {
		if err := d.PutNodeSession(ctx, "ran-machine", "ran-session", step.put); err != nil {
			t.Fatal(err)
		}
		sessions, err := d.NodeSessions(ctx, "ran-machine")
		if err != nil {
			t.Fatal(err)
		}
		if got := sessions["ran-session"]; got.RanAt != step.want {
			t.Fatalf("%s: ran_at = %d, want %d (%+v)", step.name, got.RanAt, step.want, got)
		}
	}
	sessions, _ := d.NodeSessions(ctx, "ran-machine")
	if got := sessions["ran-session"]; got.StopCause != proto.StopUser {
		t.Fatalf("a stale cause with a newer ran_at undid the user's stop: %+v", got)
	}

	raw, err := json.Marshal(NodeSession{Launcher: "claude", RanAt: 42})
	if err != nil {
		t.Fatal(err)
	}
	var back NodeSession
	if err := json.Unmarshal(raw, &back); err != nil || back.RanAt != 42 {
		t.Fatalf("ran_at does not cross state.put / state.load: %s -> %+v (%v)", raw, back, err)
	}
}
