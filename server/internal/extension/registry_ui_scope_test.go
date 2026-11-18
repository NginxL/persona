package extension

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func uiScopeManifest(t *testing.T, id string) *Manifest {
	t.Helper()
	m, err := Parse(with(t, func(m map[string]any) {
		m["id"] = id
		m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "status", "title": "Status", "placement": "extension_page"}}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUIIDsBelongToTheirExtension(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled builtin", true: "enabled builtin"}[enabled], func(t *testing.T) {
			ctx := context.Background()
			r, store, _ := newTestRegistry(t)
			builtin := uiScopeManifest(t, "volc")
			if err := r.AddBuiltin(builtin, t.TempDir(), enabled); err != nil {
				t.Fatal(err)
			}
			pkg := t.TempDir()
			writePackage(t, pkg, func(m map[string]any) {
				m["id"] = "example-status"
				m["contributes"] = builtin.Contributes
			})
			preview, err := r.Preview(ctx, pkg, "")
			if err != nil {
				t.Fatalf("an extension's status page cannot collide with volc/status: %v", err)
			}
			if _, err := r.Install(ctx, preview.ID, preview.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			// Turning one contribution off must not change the other's full or active manifest.
			if err := r.SetContribution(ctx, "example-status", "ui", "status", false, "test"); err != nil {
				t.Fatal(err)
			}
			if len(r.Get("example-status").Manifest.UIs()) != 0 || len(r.Get("volc").Manifest.UIs()) != 1 {
				t.Fatal("contribution switches crossed extension boundaries")
			}
			reloaded := NewRegistry(r.dir, store, rot13{}, 202611100000)
			if err := reloaded.AddBuiltin(builtin, t.TempDir(), enabled); err != nil {
				t.Fatal(err)
			}
			if err := reloaded.Load(ctx); err != nil {
				t.Fatal(err)
			}
			if p := reloaded.Get("example-status"); p == nil || p.Problem != "" || len(p.Manifest.UIs()) != 0 || len(p.Full.UIs()) != 1 {
				t.Fatalf("reload lost the independent contribution: %+v", p)
			}
			if p := reloaded.Get("volc"); p == nil || len(p.Manifest.UIs()) != 1 || p.Record.Enabled != enabled {
				t.Fatalf("reload changed the builtin: %+v", p)
			}
		})
	}
}

func TestUIPreviewCandidatesKeepSeparateNamespaces(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	first, second := uiScopeManifest(t, "first"), uiScopeManifest(t, "second")
	if err := r.conflictsCandidates(first, []*Manifest{first, second}); err != nil {
		t.Fatalf("a batch can contain first/status and second/status: %v", err)
	}
	// UI IDs remain unique inside one extension, across all placements.
	var items []UIItem
	_ = json.Unmarshal(first.Contributes["ui"], &items)
	items = append(items, items[0])
	first.Contributes["ui"], _ = json.Marshal(items)
	raw, _ := json.Marshal(first)
	if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate UI IDs inside one manifest must be refused: %v", err)
	}
	// UI scoping does not release a hub reservation or any other extension point.
	r.Reserve("ui", "status")
	if err := r.conflicts(second); err == nil {
		t.Fatal("the hub's reserved UI ID was released")
	}
	first, second = uiScopeManifest(t, "first"), uiScopeManifest(t, "second")
	for _, m := range []*Manifest{first, second} {
		m.Contributes["connectors"] = json.RawMessage(`[{"id":"example","name":"Example","capabilities":["status"]}]`)
	}
	if err := r.conflictsCandidates(first, []*Manifest{second}); err == nil || !strings.Contains(err.Error(), "connectors") {
		t.Fatalf("connector IDs must still be globally unique: %v", err)
	}
}

func TestUIIDsStayLocalThroughPlanUpgradeAndRollback(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	r := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	ctx := t.Context()
	preview := func(id, version, upgrade string) *Preview {
		t.Helper()
		pkg := t.TempDir()
		writePackage(t, pkg, func(m map[string]any) {
			m["id"], m["version"] = id, version
			m["contributes"] = uiScopeManifest(t, id).Contributes
		})
		pv, err := r.Preview(ctx, pkg, upgrade)
		if err != nil {
			t.Fatal(err)
		}
		return pv
	}
	first, second := preview("first", "1.0.0", ""), preview("second", "1.0.0", "")
	first.Bundle = []*Preview{second}
	if _, err := r.InstallPlan(ctx, first.ID, first.Digest, []PreviewRef{{PreviewID: second.ID, Digest: second.Digest}}, "test"); err != nil {
		t.Fatal("a single plan must install both pages:", err)
	}
	pv := preview("first", "2.0.0", "first")
	if _, err := r.Upgrade(ctx, "first", pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal("upgrading first/status must not collide with second/status:", err)
	}
	if _, err := r.Rollback(ctx, "first", "test"); err != nil {
		t.Fatal("rolling back first/status must not collide with second/status:", err)
	}
	for _, id := range []string{"first", "second"} {
		p := r.Get(id)
		if p == nil || p.Record.Version != "1.0.0" || len(p.Manifest.UIs()) != 1 {
			t.Fatalf("%s lost its page: %+v", id, p)
		}
	}
}
