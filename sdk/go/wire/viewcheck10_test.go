package wire

import (
	"fmt"
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
