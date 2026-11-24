package db

import (
	"context"
	"slices"
	"testing"
)

// Extension trust (#13246): an extension's trust and its granted categories, the grants replaced as a whole.
func TestExtensionTrust(t *testing.T) {
	d, ctx := open(t), context.Background()
	got := func() map[string]ExtensionTrust {
		t.Helper()
		all, err := d.ExtensionTrusts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return all
	}
	if all := got(); len(all) != 0 {
		t.Fatalf("a new store: %v", all)
	}
	for _, step := range []struct {
		ext, trust string
		cats       []string
	}{
		{"qa-memory", "owner", nil},
		{"qa-other", "third_party", []string{"finance", "health"}},
		{"qa-other", "third_party", []string{"health"}},
	} {
		if err := d.SetExtensionTrust(ctx, step.ext, step.trust, step.cats, "user"); err != nil {
			t.Fatal(err)
		}
	}
	all := got()
	if m := all["qa-memory"]; m.Trust != "owner" || len(m.Categories) != 0 {
		t.Errorf("qa-memory: %+v", m)
	}
	if o := all["qa-other"]; o.Trust != "third_party" || !slices.Equal(o.Categories, []string{"health"}) {
		t.Errorf("qa-other: %+v", o)
	}
	if err := d.SetExtensionTrust(ctx, "qa-other", "third_party", nil, "user"); err != nil {
		t.Fatal(err)
	}
	if o := got()["qa-other"]; len(o.Categories) != 0 {
		t.Errorf("grants taken away: %+v", o)
	}
	if err := d.ForgetExtensionTrust(ctx, "qa-memory"); err != nil {
		t.Fatal(err)
	}
	if _, ok := got()["qa-memory"]; ok {
		t.Error("a removed extension's trust is kept")
	}
	if err := d.SetExtensionTrust(ctx, "", "owner", nil, "user"); err == nil {
		t.Error("no extension")
	}
}
