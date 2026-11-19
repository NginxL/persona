package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The hub only carries manager annotations into the shared list index and DTOs.
func TestRuntimeDirectoryVisibilityAdapter(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "computer"})
	h.machines, h.byID["computer"] = []*Machine{m}, m
	m.sessions = []proto.Session{
		{MachineID: "computer", SessionID: "hidden", Title: "Background workspace", Cwd: "/work/automation", Hierarchy: &proto.SessionHierarchy{HiddenByDirectory: true}, State: proto.StateNeedsYou},
		{MachineID: "computer", SessionID: "visible", Title: "Visible workspace", Cwd: "/work/project", State: proto.StateIdle},
	}
	if page := runtimePage(t, h, "limit=50&summary=1"); page.Total != 1 || len(page.Sessions) != 1 || page.Sessions[0].SessionID != "visible" {
		t.Fatalf("default page: %+v", page)
	}
	page := runtimePage(t, h, "limit=50&summary=1&include_hidden=1")
	if page.Total != 2 || len(page.Sessions) != 2 {
		t.Fatalf("inspection: %+v", page)
	}
	marked := false
	for _, c := range page.Sessions {
		if c.SessionID == "hidden" {
			marked = c.HiddenByDirectory()
		}
	}
	if !marked {
		t.Fatal("summary dropped hidden annotation")
	}
	if exact := runtimePage(t, h, "limit=1&machine_id=computer&session_id=hidden"); exact.Total != 1 || !exact.Sessions[0].HiddenByDirectory() {
		t.Fatalf("explicit user lookup: %+v", exact)
	}
}
