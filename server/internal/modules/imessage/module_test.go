package imessage

import "testing"

// The manifest is iMessage's and declares only its feature: the kernel serves none of what iMessage adds yet (its connector reaches
// the core through the extension manager's port), and it would not load a module that declares one.
func TestManifestDeclaresNoSlotYet(t *testing.T) {
	info := Module{}.Info()
	if info.ID != "imessage" || info.Origin != "builtin" || info.API != 1 || info.Feature != "module.imessage" {
		t.Fatalf("manifest %+v", info)
	}
	if len(info.HubRoutes)+len(info.MCPTools)+len(info.Settings)+len(info.I18nNamespaces) != 0 {
		t.Fatalf("module.json declares slots the kernel does not serve: %+v", info)
	}
}
