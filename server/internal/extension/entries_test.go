package extension

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestEntriesUpgradeRequiresExplicitPersonalReadConsent(t *testing.T) {
	legacy := &Manifest{ID: "example-reader", Version: "1.0.0", Permissions: Permissions{HubAPI: true}}
	installed := &Extension{Record: Record{Granted: legacy.Permissions}, Manifest: legacy, Full: legacy}
	upgrade := &Manifest{ID: legacy.ID, Version: "1.1.0", Permissions: Permissions{Hub: []string{ScopeAssistantEntriesRead}}}
	if legacy.Permissions.Covers(upgrade.Permissions) || !MorePermissions(installed, upgrade) {
		t.Fatal("legacy full-hub permission silently became remote personal read consent")
	}
	title, _ := ScopeTitleIn(i18n.Default, ScopeAssistantEntriesRead)
	if !slices.ContainsFunc(Added(legacy, upgrade), func(line PermissionLine) bool {
		return line.Key == "hub" && strings.Contains(line.Detail, title)
	}) {
		t.Fatal("upgrade does not show the personal read permission")
	}
	installed.Record.Granted = upgrade.Permissions
	installed.Full = upgrade
	if MorePermissions(installed, upgrade) {
		t.Fatal("keeping an explicitly approved read scope asks again")
	}
	if !legacy.Permissions.Covers(Permissions{Hub: []string{ScopeKBRead}}) {
		t.Fatal("unrelated legacy read consent changed")
	}
}

func TestEntriesPersonalReadUsesInstalledManifestAndLiveGrant(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for _, tc := range []struct {
		id      string
		declare bool
		grant   Permissions
		want    bool
	}{
		{"approved", true, Permissions{Hub: []string{ScopeAssistantEntriesRead}}, true},
		{"unapproved", true, Permissions{}, false},
		{"undeclared", false, Permissions{Hub: []string{ScopeAssistantEntriesRead}}, false},
		{"legacy", false, Permissions{HubAPI: true}, false},
	} {
		permissions := Permissions{}
		if tc.declare {
			permissions.Hub = []string{ScopeAssistantEntriesRead}
		}
		m := &Manifest{ID: tc.id, Version: "1.0.0", Permissions: permissions, Contributes: map[string]json.RawMessage{}}
		if err := r.AddBuiltin(m, t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[tc.id].Record.Granted = tc.grant
		r.mu.Unlock()
		_, personal := r.EntriesReadAccess(tc.id)
		if personal != tc.want {
			t.Errorf("%s: personal=%v", tc.id, personal)
		}
	}
	if err := r.SetEnabled(context.Background(), "approved", false, "test"); err != nil {
		t.Fatal(err)
	}
	if allowed, personal := r.EntriesReadAccess("approved"); allowed || personal {
		t.Fatal("disabled extension retained access")
	}
	if err := r.SetEnabled(context.Background(), "approved", true, "test"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.extensions["approved"].Record.Granted = Permissions{}
	r.mu.Unlock()
	if allowed, personal := r.EntriesReadAccess("approved"); allowed || personal {
		t.Fatal("revoked scope retained access")
	}
}
