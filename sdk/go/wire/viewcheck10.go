package wire

import (
	"errors"
	"fmt"
)

// UI version 10's checks (docs/extensions/extensions.md "UI version 10"): a graph's nodes and edges. What the schema
// cannot say: ids unique, every edge between known nodes and once, no node joined to itself, no cycle, a second parent
// only in a layered graph, selected and folded naming nodes, and a node's tone or color, not both. The page's count
// (each node one component) is the page check's.

// checkV10 holds a graph item to UI version 10's rules.
func (p *pageCheck) checkV10(e *Component) error {
	if e.Type != ComponentGraph {
		return nil
	}
	return CheckGraph(e)
}

// CheckGraph holds a graph to UI version 10's checks the schema cannot say, so a conformance test can check a view it
// did not decode through CheckView.
func CheckGraph(e *Component) error {
	ids := make(map[string]bool, len(e.Nodes))
	for k, n := range e.Nodes {
		if ids[n.ID] {
			return fmt.Errorf("node %d: id %q twice", k, n.ID)
		}
		ids[n.ID] = true
		if n.Tone != "" && n.Color != "" {
			return fmt.Errorf("node %q: a tone or a color, not both", n.ID)
		}
		if !finitePtr(n.Progress) || n.Progress != nil && (*n.Progress < 0 || *n.Progress > 1) {
			return fmt.Errorf("node %q: progress is 0 to 1", n.ID)
		}
	}
	parents := make(map[string]int, len(e.Nodes))
	pairs := make(map[[2]string]bool, len(e.Edges))
	for k, ed := range e.Edges {
		if !ids[ed.From] {
			return fmt.Errorf("edge %d: unknown node %q", k, ed.From)
		}
		if !ids[ed.To] {
			return fmt.Errorf("edge %d: unknown node %q", k, ed.To)
		}
		if ed.From == ed.To {
			return fmt.Errorf("edge %d: %q is joined to itself", k, ed.From)
		}
		pair := [2]string{ed.From, ed.To}
		if pairs[pair] {
			return fmt.Errorf("edge %d: %q to %q twice", k, ed.From, ed.To)
		}
		pairs[pair] = true
		parents[ed.To]++
	}
	if e.Layout != GraphLayoutLayered {
		for id, n := range parents {
			if n > 1 {
				return fmt.Errorf("node %q has %d parents in a tree (use the layered layout)", id, n)
			}
		}
	}
	if e.Selected != "" && !ids[e.Selected] {
		return fmt.Errorf("selected: unknown node %q", e.Selected)
	}
	for k, id := range e.Folded {
		if !ids[id] {
			return fmt.Errorf("folded %d: unknown node %q", k, id)
		}
	}
	if graphHasCycle(e.Nodes, e.Edges) {
		return errors.New("a cycle")
	}
	return nil
}

// graphHasCycle says whether following the edges from any node comes back to it (Kahn's order: nodes left over are
// on or behind a cycle).
func graphHasCycle(nodes []GraphNode, edges []GraphEdge) bool {
	pending := make(map[string]int, len(nodes))
	next := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		pending[n.ID] = 0
	}
	for _, e := range edges {
		pending[e.To]++
		next[e.From] = append(next[e.From], e.To)
	}
	queue := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if pending[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	seen := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		seen++
		for _, c := range next[n] {
			if pending[c]--; pending[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	return seen != len(nodes)
}
