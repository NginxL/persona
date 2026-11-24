package db

import (
	"context"
	"testing"
)

// Outside-direction creation is one transaction across its marker and the settings that make it current. The migration
// also rejects a half machine/session reference without leaving a partial create behind.
func TestOutsideDirectionsPersistAtomically(t *testing.T) {
	d, ctx := open(t), context.Background()
	if got, err := d.OutsideDirections(ctx); err != nil || len(got) != 0 {
		t.Fatalf("initial outside directions: %+v %v", got, err)
	}
	settings := map[string]any{"assistant": map[string]any{"leaders": []string{"dir_bound"}}}
	if err := d.CreateOutsideDirection(ctx, "dir_bound", OutsideDirection{MachineID: "box", SessionID: "agent-1"}, settings); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateOutsideDirection(ctx, "dir_standalone", OutsideDirection{}, map[string]any{
		"assistant":            map[string]any{"leaders": []string{"dir_bound", "dir_standalone"}},
		"assistant.directions": map[string]any{"dir_standalone": map[string]any{"name": "Standalone"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.OutsideDirections(ctx)
	if err != nil || len(got) != 2 || got["dir_bound"].MachineID != "box" || got["dir_bound"].SessionID != "agent-1" || got["dir_standalone"].MachineID != "" {
		t.Fatalf("stored outside directions: %+v %v", got, err)
	}
	var current struct {
		Leaders []string `json:"leaders"`
	}
	if found, err := d.Setting(ctx, "assistant", &current); err != nil || !found || len(current.Leaders) != 2 {
		t.Fatalf("stored settings: found=%v %+v %v", found, current, err)
	}

	if err := d.CreateOutsideDirection(ctx, "dir_bad", OutsideDirection{MachineID: "box"}, map[string]any{
		"assistant": map[string]any{"leaders": []string{"dir_bad"}},
	}); err == nil {
		t.Fatal("a half Agent reference was accepted")
	}
	if err := d.CreateOutsideDirection(ctx, "dir_bound", OutsideDirection{}, map[string]any{
		"assistant": map[string]any{"leaders": []string{"dir_replacement"}},
	}); err == nil {
		t.Fatal("a duplicate direction id was accepted")
	}
	current.Leaders = nil
	if found, err := d.Setting(ctx, "assistant", &current); err != nil || !found || len(current.Leaders) != 2 || current.Leaders[0] != "dir_bound" {
		t.Fatalf("a failed create changed settings: found=%v %+v %v", found, current, err)
	}
	got, err = d.OutsideDirections(ctx)
	if err != nil || len(got) != 2 || got["dir_bad"].MachineID != "" {
		t.Fatalf("a failed create left a binding: %+v %v", got, err)
	}
}
