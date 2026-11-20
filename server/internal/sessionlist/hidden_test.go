package sessionlist

import (
	"net/url"
	"testing"
)

func TestDirectoryVisibility(t *testing.T) {
	var index Index
	visible, hidden := testItem("visible", 1), testItem("hidden", 2)
	hidden.HiddenByDirectory, hidden.State = true, "needs_you"
	index.Replace("machine-a", []Item{visible, hidden})
	page := mustQuery(t, &index, Options{})
	if page.Total != 1 || len(page.Sessions) != 1 {
		t.Fatalf("default: %+v", page)
	}
	exact := mustQuery(t, &index, Options{ID: hidden.ID})
	if exact.Total != 1 || len(exact.Sessions) != 1 {
		t.Fatalf("explicit lookup: %+v", exact)
	}
	counts, err := index.Counts(Options{})
	if err != nil || counts.Total != 1 || counts.State["needs_you"] != 0 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	places, err := index.PlaceCounts(Options{})
	if err != nil || places.Total != 1 {
		t.Fatalf("places: %+v %v", places, err)
	}
	inspected := mustQuery(t, &index, Options{IncludeHidden: true, Limit: 1})
	if inspected.Total != 2 || !inspected.HasMore {
		t.Fatalf("inspection: %+v", inspected)
	}
	if _, err := index.Query(Options{Cursor: inspected.NextCursor}); err == nil {
		t.Fatal("inspection cursor reused for default list")
	}
	hidden.HiddenByDirectory = false
	index.Replace("machine-a", []Item{visible, hidden})
	restored := mustQuery(t, &index, Options{Since: page.Revision})
	if restored.Total != 2 || len(restored.Sessions) != 1 {
		t.Fatalf("restored: %+v", restored)
	}
	hidden.HiddenByDirectory = true
	index.Replace("machine-a", []Item{visible, hidden})
	removed := mustQuery(t, &index, Options{Since: restored.Revision})
	if removed.Total != 1 || len(removed.Remove) != 1 {
		t.Fatalf("hidden delta: %+v", removed)
	}
}

func TestParseIncludeHidden(t *testing.T) {
	v := url.Values{"include_hidden": {"1"}}
	if !Enabled(v) {
		t.Fatal("not indexed")
	}
	o, err := Parse(v)
	if err != nil || !o.IncludeHidden {
		t.Fatalf("%+v %v", o, err)
	}
	for _, values := range [][]string{{"yes"}, {"1", "0"}, {""}} {
		if _, err := Parse(url.Values{"include_hidden": values}); err == nil {
			t.Fatalf("accepted %v", values)
		}
	}
}

func TestDirectoryHiddenSearchDoesNotLeakThroughVisibleParent(t *testing.T) {
	ctx := searchContext()
	parent := searchItem("parent", "computer", "Visible", ctx.Now)
	child := searchItem("child", "computer", "secret needle", ctx.Now)
	child.HiddenByDirectory = true
	child.Fold = &Fold{Root: "parent", SearchAs: "parent", SearchSession: "child"}
	var index Index
	index.Replace("computer", []Item{parent, child})
	p, err := index.Query(Options{Search: "needle", SearchContext: ctx, Roots: true})
	if err != nil || p.Total != 0 {
		t.Fatalf("hidden child leaked through parent: %+v %v", p, err)
	}
	p, err = index.Query(Options{Search: "needle", SearchContext: ctx, Roots: true, IncludeHidden: true})
	if err != nil || p.Total != 1 {
		t.Fatalf("explicit inspection lost child: %+v %v", p, err)
	}
}

// Both independently introduced filters must survive the merge: revealing
// directory-hidden agents never reveals Persona or private conversations.
func TestDirectoryVisibilityWithAgentsOnly(t *testing.T) {
	var index Index
	visible, hidden, assistant, private := testItem("visible", 1), testItem("hidden", 2), testItem("assistant", 3), testItem("private", 4)
	hidden.HiddenByDirectory = true
	assistant.Assistant = true
	private.Private = true
	index.Replace("machine-a", []Item{visible, hidden, assistant, private})
	for _, include := range []bool{false, true} {
		opts := Options{AgentsOnly: true, IncludeHidden: include}
		page := mustQuery(t, &index, opts)
		want := 1
		if include {
			want = 2
		}
		if page.Total != want || len(page.Sessions) != want {
			t.Fatalf("include_hidden=%v: %+v", include, page)
		}
		counts, err := index.Counts(opts)
		if err != nil || counts.Total != want {
			t.Fatalf("counts: %+v %v", counts, err)
		}
		places, err := index.PlaceCounts(opts)
		if err != nil || places.Total != want {
			t.Fatalf("catalog: %+v %v", places, err)
		}
	}
}
