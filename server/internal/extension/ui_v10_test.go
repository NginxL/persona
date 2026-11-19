package extension

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension/uistream"
	"github.com/nwverse/persona/server/internal/i18n"
)

func v10Example(t *testing.T, name string) *View {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "extension", "examples", "View.v10-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseView(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

var v10Examples = []string{"work-tree", "plan-layered"}

// graphOf is the first graph of v.
func graphOf(t *testing.T, v *View) *UIElem {
	t.Helper()
	for _, e := range items(v) {
		if e.Type == wire.ComponentGraph {
			return &e
		}
	}
	t.Fatal("no graph left")
	return nil
}

// An app of UI version 10 gets every example as the extension drew it. An app of 1 to 9 gets rows instead: every
// visible node in reading order, indented by its depth, carrying its subtitle, badge, icon, color, progress and
// action. A folded node's row says how many it hides and they have no row; the key is the graph's and the node's only
// when free.
func TestFitV10Examples(t *testing.T) {
	page, _ := Slot("nav_page")
	for _, name := range v10Examples {
		v := v10Example(t, name)
		want, _ := json.Marshal(v)
		if dropped, err := Fit(v, page, 10); err != nil || len(dropped) > 0 {
			t.Fatalf("%s for 10: dropped %v, %v", name, dropped, err)
		}
		if got, _ := json.Marshal(v); string(got) != string(want) {
			t.Errorf("%s: an app of 10 gets it changed\nwant %s\n got %s", name, want, got)
		}
	}
}

// What the release checklist says for an app of 9: every visible node in reading order, its indent, its text, its
// percentage, and the folded node's count with its descendants left out.
func TestFitV10WorkTreeRows(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v10Example(t, "work-tree")
	drawn := graphOf(t, v)
	order, depth := newGraphWalk(drawn).readingOrder()
	if _, err := Fit(v, page, 9); err != nil {
		t.Fatal(err)
	}
	all := items(v)
	if len(all) != 1+len(order) {
		t.Fatalf("%d items for the header and %d visible nodes: %+v", len(all), len(order), all)
	}
	rows := all[1:]
	for k, row := range rows {
		id := order[k]
		if row.Type != wire.ComponentRow {
			t.Fatalf("row %d is a %s", k, row.Type)
		}
		indent := strings.Repeat("│ ", max(depth[id]-1, 0))
		if depth[id] > 0 {
			indent += "└ "
		}
		node := nodeOf(t, drawn, id)
		if row.Title != indent+node.Title {
			t.Errorf("row %d: title %q, want %q", k, row.Title, indent+node.Title)
		}
		if row.Subtitle != node.Subtitle || !reflect.DeepEqual(row.Badge, node.Badge) ||
			row.Icon != node.Icon || row.IconColor != node.Color {
			t.Errorf("row %d (%s): %+v, want the node's subtitle, badge, icon and color", k, id, row)
		}
		switch {
		case node.Progress != nil && row.Value != gaugePercent(*node.Progress):
			t.Errorf("row %d (%s): value %q for progress %v", k, id, row.Value, *node.Progress)
		case node.Progress == nil && row.Value != "" && id != "docs":
			t.Errorf("row %d (%s): value %q without progress", k, id, row.Value)
		}
		if row.Key != "work/"+id {
			t.Errorf("row %d (%s): key %q", k, id, row.Key)
		}
		if (row.Action == nil) != (node.Action == nil) {
			t.Errorf("row %d (%s): action %v, want the node's", k, id, row.Action)
		}
	}
	if rows[9].Title != "└ Docs" || rows[9].Value != "+2" {
		t.Errorf("the folded node: %q / %q", rows[9].Title, rows[9].Value)
	}
	for _, hidden := range []string{"English", "Japanese"} {
		if slices.ContainsFunc(rows, func(r UIElem) bool { return strings.HasSuffix(r.Title, hidden) }) {
			t.Errorf("%s has a row under the folded Docs", hidden)
		}
	}
}

// A layered graph below 10: several parents, each node once at its first visit.
func TestFitV10PlanLayeredRows(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v10Example(t, "plan-layered")
	drawn := graphOf(t, v)
	order, depth := newGraphWalk(drawn).readingOrder()
	if _, err := Fit(v, page, 9); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(items(v), func(e UIElem) bool { return e.Type == wire.ComponentGraph }) {
		t.Fatal("a graph is left")
	}
	rows := items(v)
	if len(rows) != len(order) {
		t.Fatalf("%d rows for %d visible nodes", len(rows), len(order))
	}
	seen := map[string]bool{}
	for k, row := range rows {
		id := order[k]
		if seen[id] {
			t.Errorf("%s twice", id)
		}
		seen[id] = true
		indent := strings.Repeat("│ ", max(depth[id]-1, 0))
		if depth[id] > 0 {
			indent += "└ "
		}
		if row.Title != indent+nodeOf(t, drawn, id).Title {
			t.Errorf("row %d: %q, want %q", k, row.Title, indent+id)
		}
	}
}

// A folded node keeps its percentage and gains the count after it; every node has its percentage rounded, and a
// graph without a key gives no row keys.
func TestGraphRowValue(t *testing.T) {
	page, _ := Slot("nav_page")
	v := &View{Sections: []Section{{Items: []UIElem{{Type: wire.ComponentGraph, Nodes: []wire.GraphNode{
		{ID: "r", Title: "r", Progress: num(0.755)},
		{ID: "a", Title: "a", Progress: num(0.45)},
		{ID: "b", Title: "b"},
	}, Edges: []wire.GraphEdge{{From: "r", To: "a"}, {From: "a", To: "b"}}, Folded: []string{"a"}}}}}}
	if _, err := Fit(v, page, 9); err != nil {
		t.Fatal(err)
	}
	rows := items(v)
	if len(rows) != 2 || rows[0].Value != "76%" || rows[1].Value != "45% · +1" {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Key != "" || rows[1].Key != "" {
		t.Fatalf("keys without the graph's: %+v", rows)
	}
}

// Below 6 the rows lose their color and below 3 their icon, as any row.
func TestFitV10RowsForOldApps(t *testing.T) {
	page, _ := Slot("nav_page")
	for version, keepIcon := range map[int]bool{5: true, 2: false} {
		v := &View{Sections: []Section{{Items: []UIElem{{Type: wire.ComponentGraph, Nodes: []wire.GraphNode{
			{ID: "a", Title: "a", Icon: "list", Color: "green"},
			{ID: "b", Title: "b", Icon: "check"},
		}, Edges: []wire.GraphEdge{{From: "a", To: "b"}}}}}}}
		if _, err := Fit(v, page, version); err != nil {
			t.Fatalf("for %d: %v", version, err)
		}
		rows := items(v)
		if len(rows) != 2 {
			t.Fatalf("for %d: %+v", version, rows)
		}
		for _, row := range rows {
			if row.IconColor != "" {
				t.Fatalf("for %d: row %q keeps icon_color %q", version, row.Title, row.IconColor)
			}
			if (row.Icon != "") != keepIcon {
				t.Fatalf("for %d: row %q has icon %q", version, row.Title, row.Icon)
			}
		}
	}
}

// A native stream of 10 still gets rows: a stream's components never include a graph.
func TestFitNativeV10(t *testing.T) {
	g := UIElem{Type: wire.ComponentGraph, Key: "g",
		Nodes: []wire.GraphNode{{ID: "a", Title: "one"}, {ID: "a.b", Title: "two"}},
		Edges: []wire.GraphEdge{{From: "a", To: "a.b"}}}
	raw, err := json.Marshal(&View{Sections: []Section{{Key: "s", Items: []UIElem{g}}}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := Slot("nav_page")
	fitted, err := FitNative(raw, page, 10, i18n.En)
	if err != nil {
		t.Fatal(err)
	}
	var got View
	if err := json.Unmarshal(fitted, &got); err != nil {
		t.Fatal(err)
	}
	rows := got.Sections[0].Items
	if len(rows) != 2 || rows[0].Type != wire.ComponentRow || rows[1].Title != "└ two" || rows[1].Key != "g/a.b" {
		t.Fatalf("native 10: %s", fitted)
	}
}

// A row key is the graph's and the node's only when nothing else on the page has it: an item's key and a tile's
// key share the page's one key space, and two nodes that would take one give no key instead of clashing.
func TestFitV10RowKeysOnlyWhenFree(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v10Example(t, "work-tree")
	drawn := graphOf(t, v)
	order, _ := newGraphWalk(drawn).readingOrder()
	v.Sections[0].Items = append(v.Sections[0].Items,
		UIElem{Type: "text", Text: "an item holds build", Key: "work/build"},
		UIElem{Type: "grid", Key: "grid", Tiles: []wire.Tile{{Title: "a tile holds tests", Key: "work/tests"}}})
	if _, err := Fit(v, page, 9); err != nil {
		t.Fatal(err)
	}
	rows := items(v)[1 : 1+len(order)]
	for k, id := range order {
		want := "work/" + id
		if id == "build" || id == "tests" {
			want = ""
		}
		if rows[k].Key != want {
			t.Errorf("row %d (%s): key %q, want %q", k, id, rows[k].Key, want)
		}
	}
}

// A graph in a card is one of its 12 items, not one per node: 200 nodes stay a graph at 10, and below 10 they become
// the card's rows, cut to what its 12 items leave, the first in reading order. A graph beside 12 other items is one
// too many at 10.
func TestFitV10CardGraph(t *testing.T) {
	card, _ := Slot("overview_card")
	nodes := make([]wire.GraphNode, 200)
	edges := make([]wire.GraphEdge, 199)
	for k := range nodes {
		nodes[k] = wire.GraphNode{ID: "n" + strconv.Itoa(k), Title: "n"}
	}
	for k := range edges {
		edges[k] = wire.GraphEdge{From: "n" + strconv.Itoa(k), To: "n" + strconv.Itoa(k+1)}
	}
	fresh := func() *View {
		return &View{Sections: []Section{{Items: []UIElem{{Type: wire.ComponentGraph, Key: "g", Nodes: nodes, Edges: edges}}}}}
	}

	v := fresh()
	want, _ := json.Marshal(v)
	if _, err := Fit(v, card, 10); err != nil {
		t.Fatalf("200 nodes in a card at 10: %v", err)
	}
	if got, _ := json.Marshal(v); string(got) != string(want) {
		t.Errorf("an app of 10 gets the card changed\nwant %s\n got %s", want, got)
	}

	v = fresh()
	order, depth := newGraphWalk(graphOf(t, v)).readingOrder()
	if _, err := Fit(v, card, 9); err != nil {
		t.Fatalf("200 nodes in a card at 9: %v", err)
	}
	rows := items(v)
	if len(rows) != CompactItems {
		t.Fatalf("%d rows, want the card's %d", len(rows), CompactItems)
	}
	for k, row := range rows {
		id := order[k]
		want := "n"
		if depth[id] > 0 {
			want = strings.Repeat("│ ", depth[id]-1) + "└ n"
		}
		if row.Type != wire.ComponentRow || row.Title != want {
			t.Errorf("row %d is %q (%s), want the reading order's %q", k, row.Title, row.Type, want)
		}
	}

	v = &View{Sections: []Section{{Items: []UIElem{{Type: wire.ComponentGraph, Nodes: nodes[:1]}}}}}
	for k := range CompactItems {
		v.Sections[0].Items = append(v.Sections[0].Items, UIElem{Type: "text", Text: "x" + strconv.Itoa(k)})
	}
	if _, err := Fit(v, card, 10); err == nil || !strings.Contains(err.Error(), "12 items") {
		t.Fatalf("a graph beside %d items in a card: %v", CompactItems, err)
	}
}

// A native stream's row always has a key, also when `key/id` is taken (a section's key does too) or over 128
// characters: a free digest stands in, and the snapshot decodes.
func TestFitNativeV10RowKeys(t *testing.T) {
	page, _ := Slot("nav_page")
	long := strings.Repeat("k", 100)
	section := &View{Sections: []Section{
		{Key: "g/a", Items: []UIElem{{Type: "text", Text: "x", Key: "t"}}},
		{Key: "s", Items: []UIElem{{Type: wire.ComponentGraph, Key: "g",
			Nodes: []wire.GraphNode{{ID: "a", Title: "a"}, {ID: "b", Title: "b"}},
			Edges: []wire.GraphEdge{{From: "a", To: "b"}}}}},
	}}
	tooLong := &View{Sections: []Section{{Key: "s", Items: []UIElem{{Type: wire.ComponentGraph, Key: long,
		Nodes: []wire.GraphNode{{ID: strings.Repeat("n", 40), Title: "a"}}}}}}}
	for name, v := range map[string]*View{"a section takes the row key": section, "the row key is too long": tooLong} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fitted, err := FitNative(raw, page, 10, i18n.En)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got wire.View
		if err := json.Unmarshal(fitted, &got); err != nil {
			t.Fatalf("%s: %s", name, fitted)
		}
		for _, s := range got.Sections {
			for _, e := range s.Items {
				if e.Type != wire.ComponentRow {
					continue
				}
				if e.Key == "" || utf8.RuneCountInString(e.Key) > graphRowKeyMost {
					t.Fatalf("%s: a row key of %d runes: %q", name, utf8.RuneCountInString(e.Key), e.Key)
				}
			}
		}
		snapshot, err := json.Marshal(wire.UIStreamSnapshot{Protocol: 1, Session: "s", Epoch: "e", RootKey: "root", Version: 1, View: got})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := uistream.DecodeSnapshot(snapshot); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// nodeOf is id's node of g.
func nodeOf(t *testing.T, g *UIElem, id string) *wire.GraphNode {
	t.Helper()
	for k := range g.Nodes {
		if g.Nodes[k].ID == id {
			return &g.Nodes[k]
		}
	}
	t.Fatalf("no node %q", id)
	return nil
}

// The reading order, its depths and the fold counts are the shared cases' (spec/fixtures/graph-layout).
func TestGraphReadingOrderFollowsTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "graph-layout", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name  string
			Graph struct {
				Nodes  []struct{ ID, Group string }
				Edges  []struct{ From, To string }
				Layout string
				Folded []string
			}
			Expected struct {
				Order []string
				Depth map[string]int
				Folds map[string]int
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range doc.Cases {
		g := &wire.Component{Layout: c.Graph.Layout, Folded: c.Graph.Folded}
		for _, n := range c.Graph.Nodes {
			g.Nodes = append(g.Nodes, wire.GraphNode{ID: n.ID, Title: n.ID, Group: n.Group})
		}
		for _, e := range c.Graph.Edges {
			g.Edges = append(g.Edges, wire.GraphEdge{From: e.From, To: e.To})
		}
		w := newGraphWalk(g)
		order, depth := w.readingOrder()
		folds := map[string]int{}
		for _, id := range order {
			if len(w.children[id]) > 0 {
				folds[id] = 0
				if w.folded[id] {
					folds[id] = w.hidden(id)
				}
			}
		}
		if !slices.Equal(order, c.Expected.Order) {
			t.Errorf("%s: order %v, want %v", c.Name, order, c.Expected.Order)
		}
		if !maps.Equal(depth, c.Expected.Depth) {
			t.Errorf("%s: depth %v, want %v", c.Name, depth, c.Expected.Depth)
		}
		if !maps.Equal(folds, c.Expected.Folds) {
			t.Errorf("%s: folds %v, want %v", c.Name, folds, c.Expected.Folds)
		}
	}
}

// An action's components in place holding a graph: an app of 10 gets the graph; an app of 5 to 9 gets no components,
// since its graph is one row per node and cannot stand in for one component, and draws the page again (the lines
// stay). A bad graph is refused for every app alike.
func TestFitResultGraphBelow10(t *testing.T) {
	page, _ := Slot("nav_page")
	graph := func(edges ...wire.GraphEdge) ActionResult {
		return ActionResult{
			Lines: &FileLines{Path: "a.go", Side: "new", Start: 1, Lines: []string{"a"}},
			Items: []UIElem{{Type: wire.ComponentGraph, Key: "work", Nodes: []wire.GraphNode{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}, Edges: edges}},
		}
	}
	tree := wire.GraphEdge{From: "a", To: "b"}

	res := graph(tree)
	if err := FitResult(&res, page, 10); err != nil {
		t.Fatalf("v10: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Type != wire.ComponentGraph || len(res.Items[0].Nodes) != 2 || res.Lines == nil {
		t.Fatalf("v10 does not keep the graph and the lines: %+v", res)
	}
	for _, version := range []int{9, 8, 5} {
		res := graph(tree)
		if err := FitResult(&res, page, version); err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if res.Items != nil || res.Lines == nil {
			t.Errorf("v%d: items %+v, lines %v; want no items and the lines", version, res.Items, res.Lines)
		}
	}
	for _, version := range []int{10, 9} {
		res := graph(tree, wire.GraphEdge{From: "b", To: "a"})
		if err := FitResult(&res, page, version); err == nil {
			t.Errorf("v%d: a graph with a cycle is taken", version)
		}
	}
}
