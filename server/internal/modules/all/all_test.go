package all

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/kernel"
	"github.com/nwverse/persona/server/internal/modapi"
)

// The list has each module its build tags keep, and the kernel loads exactly that list. Run with -tags
// persona_core_only or persona_without_<id> as well (scripts/ci/server.sh).
func TestBuiltinFollowsTheBuildTags(t *testing.T) {
	var ids []string
	for _, m := range Builtin() {
		ids = append(ids, m.Info().ID)
	}
	if !slices.Equal(ids, wantModules) {
		t.Fatalf("built-in modules %v, want %v for this build's tags", ids, wantModules)
	}
	if got := kernel.Builtin(); len(got) != len(ids) {
		t.Fatalf("the kernel was provided %d modules, the list has %d", len(got), len(ids))
	}
	k := kernel.New(Builtin(), nil, t.Logf)
	for _, id := range wantModules {
		if !k.Loaded(id) {
			t.Errorf("%s is compiled in but does not load: %s", id, k.Skipped(id))
		}
	}
}

// Every built-in module announces its own flag, module.<id>: a publishing hub's hello carries the "modules" marker, so
// a client that gates a module turns it off when the flag is missing (docs/architecture/core-and-modules.md, "Hub
// flags"). A module without the flag would vanish from every client of a current hub.
func TestEveryBuiltinModuleAnnouncesItsFlag(t *testing.T) {
	for _, m := range Builtin() {
		info := m.Info()
		if want := "module." + info.ID; info.Feature != want {
			t.Errorf("module %s has feature %q in module.json, want %q", info.ID, info.Feature, want)
		}
	}
}

// A client-only module (modapi.ClientModules) has no server half; once one exists, its manifest announces the flag and
// its load state decides, so the id leaves the list in that PR.
func TestNoBuiltinModuleIsListedAsClientOnly(t *testing.T) {
	for _, m := range Builtin() {
		if id := m.Info().ID; slices.Contains(modapi.ClientModules(), id) {
			t.Errorf("module %s is built in: remove it from modapi.ClientModules", id)
		}
	}
}
