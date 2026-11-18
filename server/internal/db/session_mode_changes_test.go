package db

import (
	"context"
	"testing"
	"time"
)

// A session's mode changes come back oldest first, its own only, the latest limit of them, with who asked and how it
// took effect (#10297).
func TestSessionModeChanges(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	add := func(c SessionModeChange) {
		t.Helper()
		if err := d.AddSessionModeChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	add(SessionModeChange{MachineID: "mac", SessionID: "s1", Kind: ModeChangeSet, From: "default", To: "acceptEdits",
		Applied: ModeAppliedNow, ActorKind: "device", DeviceID: "d1", DeviceName: "Phone", At: at})
	add(SessionModeChange{MachineID: "mac", SessionID: "s1", Kind: ModeChangeSet, From: "acceptEdits", To: "bypassPermissions",
		Applied: ModeAppliedRestart, ActorKind: "device", DeviceID: "d1", DeviceName: "Phone", At: at.Add(time.Minute)})
	add(SessionModeChange{MachineID: "mac", SessionID: "s1", Kind: ModeChangeRestartDropped, To: "bypassPermissions",
		Reason: "claude 4101, the process it was picked for, has exited", At: at.Add(2 * time.Minute)})
	add(SessionModeChange{MachineID: "mac", SessionID: "s2", Kind: ModeChangeSet, To: "plan", At: at})
	add(SessionModeChange{MachineID: "other", SessionID: "s1", Kind: ModeChangeSet, To: "plan", At: at})

	got, err := d.SessionModeChanges(ctx, "mac", "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].To != "acceptEdits" || got[1].Applied != ModeAppliedRestart || got[2].Kind != ModeChangeRestartDropped {
		t.Fatalf("changes = %+v", got)
	}
	if got[1].DeviceName != "Phone" || got[1].ActorKind != "device" || got[1].From != "acceptEdits" || !got[1].At.Equal(at.Add(time.Minute)) {
		t.Errorf("who and when = %+v", got[1])
	}
	if got[2].Reason == "" {
		t.Error("the drop's reason was not kept")
	}
	latest, err := d.SessionModeChanges(ctx, "mac", "s1", 2)
	if err != nil || len(latest) != 2 || latest[0].Kind != ModeChangeSet || latest[1].Kind != ModeChangeRestartDropped {
		t.Fatalf("latest two = %+v, %v", latest, err)
	}
}
