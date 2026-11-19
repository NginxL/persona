package node

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const carbonModel = "gemini-4-carbon"

// normNode is a node with no shell list to wait for and an empty modes map; launcherCache is pinned for the test.
func normNode(t *testing.T) *Node {
	t.Helper()
	n, _ := testNode(t)
	n.modes = map[string]sessionModes{}
	launcherCache.Lock()
	launcherCache.list, launcherCache.at, launcherCache.fetching = []proto.Launcher{}, time.Now(), false
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.list, launcherCache.at, launcherCache.fetching = nil, time.Time{}, false
		launcherCache.Unlock()
	})
	return n
}

func TestNormalizeSessionRowModel(t *testing.T) {
	t.Run("a gateway prefix folds to the managed launcher's declared id", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: carbonModel}}
		s := &proto.Session{SessionID: "s", Launcher: "gw", Model: "passthrough/route-gemini-4-carbon/" + carbonModel}
		n.normalizeSessionRowModel(s)
		if s.Model != carbonModel {
			t.Fatalf("model = %q", s.Model)
		}
	})

	t.Run("a launcher's tier and gateway models are declared too", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: "m1", OpusModel: "opus-5", Models: []proto.ProviderModel{{ID: carbonModel}}}}
		s := &proto.Session{SessionID: "s", Launcher: "gw", Model: "route/" + carbonModel}
		n.normalizeSessionRowModel(s)
		if s.Model != carbonModel {
			t.Fatalf("model = %q", s.Model)
		}
	})

	t.Run("a row with no launcher finds the id among the managed launchers", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: carbonModel}}
		s := &proto.Session{SessionID: "s", Model: "passthrough/route-gemini-4-carbon/" + carbonModel}
		n.normalizeSessionRowModel(s)
		if s.Model != carbonModel {
			t.Fatalf("model = %q", s.Model)
		}
	})

	t.Run("the launchers.models cache is read without being fetched", func(t *testing.T) {
		n := normNode(t)
		key := n.home + "\x00gw\x00claude\x00fp"
		launcherModelsCache.Lock()
		launcherModelsCache.at[key], launcherModelsCache.got[key] = time.Now(), proto.LauncherModels{Models: []proto.AgentModel{{ID: carbonModel}}}
		launcherModelsCache.Unlock()
		t.Cleanup(func() {
			launcherModelsCache.Lock()
			delete(launcherModelsCache.at, key)
			delete(launcherModelsCache.got, key)
			launcherModelsCache.Unlock()
		})
		s := &proto.Session{SessionID: "s", Launcher: "gw", Model: "route/" + carbonModel}
		n.normalizeSessionRowModel(s)
		if s.Model != carbonModel {
			t.Fatalf("model = %q", s.Model)
		}
	})

	t.Run("an unmatched model stays whole", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: "auto/large"}}
		s := &proto.Session{SessionID: "s", Launcher: "gw", Model: "group/auto-large"}
		n.normalizeSessionRowModel(s)
		if s.Model != "group/auto-large" {
			t.Fatalf("model = %q", s.Model)
		}
		s = &proto.Session{SessionID: "s", Launcher: "gw", Model: "gateway/echo"}
		n.normalizeSessionRowModel(s)
		if s.Model != "gateway/echo" {
			t.Fatalf("model = %q", s.Model)
		}
	})

	t.Run("a runtime's row and a placeholder", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: carbonModel}}
		s := &proto.Session{SessionID: "s", SourceKind: proto.SourceRuntime, Launcher: "gw", Model: "route/" + carbonModel}
		n.normalizeSessionRowModel(s)
		if s.Model != "route/"+carbonModel {
			t.Fatalf("runtime model = %q", s.Model)
		}
		s = &proto.Session{SessionID: "s", Model: "<synthetic>"}
		n.normalizeSessionRowModel(s)
		if s.Model != "" {
			t.Fatalf("placeholder = %q", s.Model)
		}
	})

	t.Run("a pending app pick stands until its transcript catches up", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: carbonModel}}
		s := &proto.Session{SessionID: "s", Launcher: "gw", Model: "route/" + carbonModel}
		n.modes["s"] = sessionModes{Model: &pendingModel{Model: "route/" + carbonModel, At: time.Now()}}
		n.normalizeSessionRowModel(s)
		if s.Model != "route/"+carbonModel {
			t.Fatalf("pending model = %q", s.Model)
		}
		n.modes["s"] = sessionModes{Model: &pendingModel{Model: "route/" + carbonModel, At: time.Now().Add(-2 * pendingModelTTL)}}
		n.normalizeSessionRowModel(s)
		if s.Model != carbonModel {
			t.Fatalf("expired pick = %q", s.Model)
		}
	})
}
