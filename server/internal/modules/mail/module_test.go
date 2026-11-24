package mail

import "testing"

// The manifest is mail's and declares no slot: the kernel serves none of what mail adds yet (its routes, tools and
// settings reach the core through the extension manager's port), and it would not load a module that declares one.
func TestManifestDeclaresNoSlotYet(t *testing.T) {
	info := Module{}.Info()
	if info.ID != "mail" || info.Origin != "builtin" || info.API != 1 {
		t.Fatalf("manifest %+v", info)
	}
	if len(info.HubRoutes)+len(info.MCPTools)+len(info.Settings)+len(info.I18nNamespaces) != 0 {
		t.Fatalf("module.json declares slots the kernel does not serve: %+v", info)
	}
}
