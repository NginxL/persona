package github

import "testing"

// The manifest is GitHub's and declares no slot: the kernel serves none of what GitHub adds yet (its connector reaches
// the core through the extension manager's port), and it would not load a module that declares one.
func TestManifestDeclaresNoSlotYet(t *testing.T) {
	info := Module{}.Info()
	if info.ID != "github" || info.Origin != "builtin" || info.API != 1 || info.Feature != "module.github" {
		t.Fatalf("manifest %+v", info)
	}
	if len(info.HubRoutes)+len(info.MCPTools)+len(info.Settings)+len(info.I18nNamespaces) != 0 {
		t.Fatalf("module.json declares slots the kernel does not serve: %+v", info)
	}
}
