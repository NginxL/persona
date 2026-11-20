package wire

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func graphView(g Component) *View {
	g.Type = ComponentGraph
	return &View{Sections: []Section{{Items: []Component{g}}}}
}

func fptr(f float64) *float64 { return &f }

func node(id string) GraphNode { return GraphNode{ID: id, Title: id} }

func edge(from, to string) GraphEdge { return GraphEdge{From: from, To: to} }

// Every refusable graph is refused, with its own check's words: several are also caught by a later check (a repeated
// pair by the tree's parents, an unknown from or a self-join by the cycle), so the message is what pins each one.
func TestCheckViewGraph(t *testing.T) {
	for name, c := range map[string]struct {
		g    Component
		want string
	}{
		"a repeated id":   {Component{Nodes: []GraphNode{node("a"), node("a")}}, "twice"},
		"an unknown from": {Component{Nodes: []GraphNode{node("a")}, Edges: []GraphEdge{edge("x", "a")}}, "unknown node"},
		"an unknown to":   {Component{Nodes: []GraphNode{node("a")}, Edges: []GraphEdge{edge("a", "x")}}, "unknown node"},
		"a node joined to itself": {Component{Nodes: []GraphNode{node("a")},
			Edges: []GraphEdge{edge("a", "a")}}, "joined to itself"},
		"the same pair twice": {Component{Nodes: []GraphNode{node("a"), node("b")},
			Edges: []GraphEdge{edge("a", "b"), edge("a", "b")}}, "twice"},
		"the same pair twice, layered": {Component{Layout: GraphLayoutLayered, Nodes: []GraphNode{node("a"), node("b")},
			Edges: []GraphEdge{edge("a", "b"), edge("a", "b")}}, "twice"},
		"a cycle": {Component{Nodes: []GraphNode{node("a"), node("b"), node("c")},
			Edges: []GraphEdge{edge("a", "b"), edge("b", "c"), edge("c", "a")}}, "a cycle"},
		"two parents in a tree": {Component{Nodes: []GraphNode{node("r"), node("a"), node("b"), node("c")},
			Edges: []GraphEdge{edge("r", "a"), edge("r", "b"), edge("a", "c"), edge("b", "c")}}, "parents in a tree"},
		"a tone and a color":           {Component{Nodes: []GraphNode{{ID: "a", Title: "a", Tone: "positive", Color: "green"}}}, "a tone or a color, not both"},
		"a selected node that is none": {Component{Nodes: []GraphNode{node("a")}, Selected: "b"}, "unknown node"},
		"a folded node that is none":   {Component{Nodes: []GraphNode{node("a")}, Folded: []string{"b"}}, "unknown node"},
		"a progress above 1":           {Component{Nodes: []GraphNode{{ID: "a", Title: "a", Progress: fptr(1.01)}}}, "progress"},
	} {
		err := CheckView(graphView(c.g))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}

// A cycle is found whatever the nodes are called and however it is entered, also under a layered graph where the tree
// parent rule cannot catch it.
func TestCheckViewGraphCycle(t *testing.T) {
	for name, g := range map[string]Component{
		"layered, a diamond and a cycle": {Layout: GraphLayoutLayered,
			Nodes: []GraphNode{node("a"), node("b"), node("c")},
			Edges: []GraphEdge{edge("a", "b"), edge("b", "c"), edge("c", "a")}},
		"a cycle plus a free node": {Layout: GraphLayoutLayered,
			Nodes: []GraphNode{node("a"), node("b"), node("z")},
			Edges: []GraphEdge{edge("a", "b"), edge("b", "a")}},
		"a loop far from the roots": {Layout: GraphLayoutLayered, Nodes: []GraphNode{node("r"), node("a"), node("b"), node("c")},
			Edges: []GraphEdge{edge("r", "a"), edge("a", "b"), edge("b", "c"), edge("c", "b")}},
	} {
		err := CheckView(graphView(g))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What a graph may be: 300 nodes, no edges, a layered graph with several parents, a folded leaf, one root.
func TestCheckViewGraphAccepts(t *testing.T) {
	nodes := make([]GraphNode, 300)
	for k := range nodes {
		nodes[k] = node(fmt.Sprintf("n%d", k))
	}
	for name, g := range map[string]Component{
		"300 nodes, no edges": {Nodes: nodes},
		"a folded leaf": {Nodes: []GraphNode{node("r"), node("a")},
			Edges: []GraphEdge{edge("r", "a")}, Folded: []string{"a"}},
		"a layered dag with several parents": {Layout: GraphLayoutLayered,
			Nodes: []GraphNode{node("a"), node("b"), node("c"), node("d")},
			Edges: []GraphEdge{edge("a", "d"), edge("b", "d"), edge("c", "d"), edge("a", "c")}},
		"tones, colors and progress on their own": {Nodes: []GraphNode{
			{ID: "a", Title: "a", Tone: "caution"},
			{ID: "b", Title: "b", Color: "amber", Progress: fptr(0.4)}}},
	} {
		if err := CheckView(graphView(g)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Each node is one of the page's 300 components, as a carousel's tile is: 300 nodes fit, one more component does not.
func TestCheckViewGraphCountsNodes(t *testing.T) {
	nodes := make([]GraphNode, 300)
	for k := range nodes {
		nodes[k] = node(fmt.Sprintf("n%d", k))
	}
	v := graphView(Component{Nodes: nodes})
	v.Sections[0].Items = append(v.Sections[0].Items, Component{Type: "text", Text: "one more"})
	err := CheckView(v)
	if err == nil || !strings.Contains(err.Error(), "300 items") {
		t.Fatalf("301 components: %v", err)
	}
}

// A node's action is the view's action: its push is checked as any other, and an assistant-only one is taken out
// (the node stays) while a plain one stays too.
func TestGraphNodeActions(t *testing.T) {
	push := &Action{ID: "open", Push: &Push{Page: "item"}}
	plain := &Action{ID: "plain"}
	v := graphView(Component{Nodes: []GraphNode{
		{ID: "a", Title: "a", Action: push},
		{ID: "b", Title: "b", Action: plain},
	}})
	if got := Actions(v); len(got) != 2 {
		t.Fatalf("%d actions, want the two nodes'", len(got))
	}
	if err := CheckView(v); err != nil {
		t.Fatal(err)
	}
	dropped := StripAssistantFields(v)
	if dropped == nil || !strings.Contains(strings.Join(dropped, ","), "assistant_action") {
		t.Fatalf("dropped %v", dropped)
	}
	nodes := v.Sections[0].Items[0].Nodes
	if nodes[0].Action != nil || nodes[1].Action == nil {
		t.Fatalf("nodes after the strip: %+v", nodes)
	}
}

// A pushed state too big is refused through a node's action as through any other.
func TestGraphNodeActionPushState(t *testing.T) {
	v := graphView(Component{Nodes: []GraphNode{{ID: "a", Title: "a",
		Action: &Action{ID: "open", Push: &Push{Page: "item", State: strings.Repeat("x", StateMost+1)}}}}})
	if err := CheckView(v); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("push state: %v", err)
	}
}

// A node's start of 0 is kept as written: the pointer survives the JSON round trip, and the check reads it as a
// start (not "no start").
func TestGraphStartedAtZero(t *testing.T) {
	v := graphView(Component{Nodes: []GraphNode{{ID: "a", Title: "a", StartedAt: fptr(0), EndedAt: fptr(5)}}})
	if err := CheckView(v); err != nil {
		t.Fatalf("started_at 0: %v", err)
	}
	raw, err := json.Marshal(v)
	if err != nil || !strings.Contains(string(raw), `"started_at":0`) {
		t.Fatalf("started_at 0 dropped: %s, %v", raw, err)
	}
	var back View
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Sections[0].Items[0].Nodes[0].StartedAt
	if got == nil || *got != 0 {
		t.Fatalf("started_at came back %v", got)
	}
}

// A node's times are finite and 0 or more; NaN and Inf have no JSON spelling, and a negative stamp is refused.
func TestGraphTimesFiniteAndNonNegative(t *testing.T) {
	for name, n := range map[string]GraphNode{
		"started NaN":  {ID: "a", Title: "a", StartedAt: fptr(math.NaN())},
		"started +Inf": {ID: "a", Title: "a", StartedAt: fptr(math.Inf(1))},
		"started -Inf": {ID: "a", Title: "a", StartedAt: fptr(math.Inf(-1))},
		"started < 0":  {ID: "a", Title: "a", StartedAt: fptr(-1)},
		"ended NaN":    {ID: "a", Title: "a", EndedAt: fptr(math.NaN())},
		"ended +Inf":   {ID: "a", Title: "a", EndedAt: fptr(math.Inf(1))},
		"ended < 0":    {ID: "a", Title: "a", EndedAt: fptr(-1)},
	} {
		want := "started_at"
		if strings.Contains(name, "ended") {
			want = "ended_at"
		}
		// CheckGraph directly: the schema has no spelling for NaN or Inf, and has its own message for a negative.
		err := CheckGraph(&Component{Nodes: []GraphNode{n}})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "0 or more") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The end is at or after the start: equal holds, a running node (no end) holds, a node without a start holds; an end
// before the start is refused.
func TestGraphEndedAtOrder(t *testing.T) {
	for name, n := range map[string]GraphNode{
		"equal":          {ID: "a", Title: "a", StartedAt: fptr(10), EndedAt: fptr(10)},
		"after":          {ID: "a", Title: "a", StartedAt: fptr(10), EndedAt: fptr(11)},
		"still running":  {ID: "a", Title: "a", StartedAt: fptr(10)},
		"no start":       {ID: "a", Title: "a", EndedAt: fptr(10)},
		"neither":        {ID: "a", Title: "a"},
		"start only, 0":  {ID: "a", Title: "a", StartedAt: fptr(0)},
		"start 0, end 0": {ID: "a", Title: "a", StartedAt: fptr(0), EndedAt: fptr(0)},
	} {
		if err := CheckView(graphView(Component{Nodes: []GraphNode{n}})); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", StartedAt: fptr(11), EndedAt: fptr(10)}}})
	if err == nil || !strings.Contains(err.Error(), "ended_at before started_at") {
		t.Fatalf("ended before started: %v", err)
	}
}

// A node has at most 8 facts, each with a value: 8 hold, 9 do not, and a blank value is refused.
func TestGraphFacts(t *testing.T) {
	facts := make([]Fact, 8)
	for k := range facts {
		facts[k] = Fact{Label: fmt.Sprintf("k%d", k), Value: "v"}
	}
	// CheckGraph directly for the checker's own words; CheckView would stop at the schema's first.
	if err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: facts}}}); err != nil {
		t.Fatalf("8 facts: %v", err)
	}
	nine := append(facts, Fact{Label: "k8", Value: "v"})
	if err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: nine}}}); err == nil ||
		!strings.Contains(err.Error(), "at most 8 facts") {
		t.Fatalf("9 facts: %v", err)
	}
	if err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: []Fact{{Label: "k", Value: ""}}}}}); err == nil ||
		!strings.Contains(err.Error(), "value") {
		t.Fatalf("empty value: %v", err)
	}
	if err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: []Fact{{Label: "k", Value: "v", Icon: "nope"}}}}}); err == nil ||
		!strings.Contains(err.Error(), "icon") {
		t.Fatalf("unknown icon: %v", err)
	}
	// and through CheckView, where the schema's own limits fire first
	if err := CheckView(graphView(Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: nine}}})); err == nil {
		t.Fatal("9 facts through CheckView: held")
	}
}

// Facts count characters as runes, not bytes: a 2000-character value of 3-byte runes holds, 2001 does not, and a
// 200-character label holds while 201 does not.
func TestGraphFactsRunes(t *testing.T) {
	v := func(f Fact) *Component {
		return &Component{Nodes: []GraphNode{{ID: "a", Title: "a", Facts: []Fact{f}}}}
	}
	if err := CheckGraph(v(Fact{Label: "k", Value: strings.Repeat("決", 2000)})); err != nil {
		t.Fatalf("2000-rune value: %v", err)
	}
	if err := CheckGraph(v(Fact{Label: "k", Value: strings.Repeat("決", 2001)})); err == nil ||
		!strings.Contains(err.Error(), "1 to 2000") {
		t.Fatalf("2001-rune value: %v", err)
	}
	if err := CheckGraph(v(Fact{Label: strings.Repeat("決", 200), Value: "v"})); err != nil {
		t.Fatalf("200-rune label: %v", err)
	}
	if err := CheckGraph(v(Fact{Label: strings.Repeat("決", 201), Value: "v"})); err == nil ||
		!strings.Contains(err.Error(), "label") {
		t.Fatalf("201-rune label: %v", err)
	}
}

// A detail is at most 2000 runes, counted as runes (3-byte runes: 2000 fit, 2001 do not).
func TestGraphDetailRunes(t *testing.T) {
	ok := &Component{Nodes: []GraphNode{{ID: "a", Title: "a", Detail: strings.Repeat("決", 2000)}}}
	if err := CheckGraph(ok); err != nil {
		t.Fatalf("2000-rune detail: %v", err)
	}
	over := &Component{Nodes: []GraphNode{{ID: "a", Title: "a", Detail: strings.Repeat("決", 2001)}}}
	if err := CheckGraph(over); err == nil || !strings.Contains(err.Error(), "detail") {
		t.Fatalf("2001-rune detail: %v", err)
	}
}

// A node has at most 8 inspector links; each title is 1 to 80 runes and each url an https url of 1 to 2000 runes.
func TestGraphLinks(t *testing.T) {
	link := func(title, url string) *Component {
		return &Component{Nodes: []GraphNode{{ID: "a", Title: "a", Links: []GraphNodeLink{{Title: title, URL: url}}}}}
	}
	ok := func(name string, v *Component) {
		if err := CheckGraph(v); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	refuse := func(name, want string, v *Component) {
		if err := CheckGraph(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := "https://hub.example.com/s/dev-box/run-1"
	ok("one link", link("Work", good))
	ok("80-rune title", link(strings.Repeat("決", 80), good))
	refuse("81-rune title", "title", link(strings.Repeat("決", 81), good))
	refuse("empty title", "title", link("", good))
	ok("2000-rune url", link("Work", "https://hub.example.com/"+strings.Repeat("u", 2000-len("https://hub.example.com/"))))
	refuse("2001-rune url", "url", link("Work", "https://hub.example.com/"+strings.Repeat("u", 2001-len("https://hub.example.com/"))))
	refuse("http", "https url", link("Work", "http://hub.example.com/x"))
	refuse("credentials", "https url", link("Work", "https://user:pass@hub.example.com/x"))
	refuse("empty user", "https url", link("Work", "https://@hub.example.com/x"))
	refuse("port only", "https url", link("Work", "https://:443/x"))
	refuse("surrounding space", "https url", link("Work", " https://hub.example.com/x "))

	eight := make([]GraphNodeLink, 8)
	for k := range eight {
		eight[k] = GraphNodeLink{Title: fmt.Sprintf("l%d", k), URL: good}
	}
	if err := CheckGraph(&Component{Nodes: []GraphNode{{ID: "a", Title: "a", Links: eight}}}); err != nil {
		t.Fatalf("8 links: %v", err)
	}
	nine := append(eight, GraphNodeLink{Title: "l8", URL: good})
	refuse("9 links", "at most 8 links", &Component{Nodes: []GraphNode{{ID: "a", Title: "a", Links: nine}}})
	// an https link through CheckView too, so the schema and the check agree
	if err := CheckView(graphView(Component{Nodes: []GraphNode{{ID: "a", Title: "a", Links: []GraphNodeLink{{Title: "Work", URL: good}}}}})); err != nil {
		t.Errorf("one link through CheckView: %v", err)
	}
}

// The header's facts take checkFact too: 8 hold, 9 do not, and a blank value is refused.
func TestHeaderFacts(t *testing.T) {
	facts := make([]Fact, 8)
	for k := range facts {
		facts[k] = Fact{Label: "k", Value: "v"}
	}
	view := func(fs []Fact) *View {
		return &View{Sections: []Section{{Items: []Component{{Type: "header", Title: "t", Facts: fs}}}}}
	}
	if err := CheckView(view(facts)); err != nil {
		t.Fatalf("8 facts: %v", err)
	}
	// through CheckView the schema's own limit ("more than 8 items") fires first
	if err := CheckView(view(append(facts, Fact{Label: "k", Value: "v"}))); err == nil {
		t.Fatal("9 facts: held")
	}
	if err := CheckView(view([]Fact{{Label: "k", Value: ""}})); err == nil {
		t.Fatal("empty value: held")
	}
}
