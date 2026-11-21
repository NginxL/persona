package devices

import (
	"slices"
	"testing"
)

func TestPhysicalAliasesFollowCurrentFleetAbsorption(t *testing.T) {
	d := newTestDevices(t)
	owner := summaryMachine(d, "computer", "ssh", "", true)
	alias := summaryMachine(d, "former-entry", kindLink, "", false)
	d.addEntry(owner)
	d.addEntry(alias)
	d.fleet.mu.Lock()
	d.fleet.init()
	d.fleet.absorbed[alias] = owner
	d.fleet.mu.Unlock()
	for _, id := range []string{"computer", "former-entry"} {
		if got := d.PhysicalAliases(id); !slices.Equal(got, []string{"computer", "former-entry"}) {
			t.Fatalf("aliases(%s): %v", id, got)
		}
	}
	d.fleet.mu.Lock()
	delete(d.fleet.absorbed, alias)
	d.fleet.mu.Unlock()
	if got := d.PhysicalAliases("computer"); !slices.Equal(got, []string{"computer"}) {
		t.Fatalf("stale absorption: %v", got)
	}
	if got := d.PhysicalAliases("unknown"); !slices.Equal(got, []string{"unknown"}) {
		t.Fatalf("unknown machine: %v", got)
	}
}
