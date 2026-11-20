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
	t.Run("a gateway's routed echo stays whole for display even when the launcher declares the bare id", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{
			"gw": {Name: "gw", Model: carbonModel, OpusModel: "opus-5", Models: []proto.ProviderModel{{ID: carbonModel}}},
		}
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

		for _, echoed := range []string{
			"passthrough/route-gemini-4-carbon/" + carbonModel,
			"route-a/" + carbonModel,
			"cursor/" + carbonModel,
			"group/auto-large",
			"gateway/echo",
		} {
			s := &proto.Session{SessionID: "s", Launcher: "gw", Model: echoed}
			n.normalizeSessionRowModel(s)
			if s.Model != echoed {
				t.Fatalf("model = %q, want verbatim %q", s.Model, echoed)
			}
			noLauncher := &proto.Session{SessionID: "s2", Model: echoed}
			n.normalizeSessionRowModel(noLauncher)
			if noLauncher.Model != echoed {
				t.Fatalf("no-launcher model = %q, want verbatim %q", noLauncher.Model, echoed)
			}
		}
	})

	t.Run("decorateIn keeps the echoed route on Model and the launch id on RequestedModel", func(t *testing.T) {
		n := normNode(t)
		n.managed.byID = map[string]proto.ManagedLauncher{"gw": {Name: "gw", Model: carbonModel}}
		n.state.put("s-routed", sessionState{Launcher: "gw", Cwd: "/code", Model: carbonModel})
		echoed := "route-a/" + carbonModel
		s := &proto.Session{SessionID: "s-routed", Cwd: "/code", Model: echoed}
		n.decorateIn(s, nil)
		if s.Model != echoed {
			t.Fatalf("Model = %q, want %q", s.Model, echoed)
		}
		if s.RequestedModel == nil || *s.RequestedModel != carbonModel {
			t.Fatalf("RequestedModel = %v, want %q", s.RequestedModel, carbonModel)
		}
	})

	t.Run("trims white space and clears bracketed placeholders across row kinds", func(t *testing.T) {
		n := normNode(t)
		n.normalizeSessionRowModel(nil)

		s := &proto.Session{SessionID: "s", SourceKind: proto.SourceRuntime, Launcher: "gw", Model: "  route/" + carbonModel + "  "}
		n.normalizeSessionRowModel(s)
		if s.Model != "route/"+carbonModel {
			t.Fatalf("runtime model = %q", s.Model)
		}

		for _, placeholder := range []string{"<synthetic>", "  <synthetic>  ", "<none>"} {
			for _, kind := range []string{"", proto.SourceRuntime} {
				row := &proto.Session{SessionID: "s", SourceKind: kind, Model: placeholder}
				n.normalizeSessionRowModel(row)
				if row.Model != "" {
					t.Fatalf("kind=%q placeholder=%q -> %q, want empty", kind, placeholder, row.Model)
				}
			}
		}
	})
}
