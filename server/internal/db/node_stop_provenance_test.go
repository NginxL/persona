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
