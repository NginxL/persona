package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// UI version 10 (docs/extensions/extensions.md "UI version 10", #9727): a graph, a canvas of nodes joined by edges.
// downgrade10 draws it with version 9's for an older app, and for a native stream (whose components never include a
// graph): one row per visible node, in reading order, indented by its depth, carrying what the node said.

// graphRowKeyMost is the longest a row's key may be (view.schema.json Component.key); a generated graph row key is
// left out rather than cut past it.
const graphRowKeyMost = 128

// graphRowKeyHashMost is how much of a node id's digest a native stream's fallback row key carries.
const graphRowKeyHashMost = 16

// downgrade10 draws every graph with rows, in place: its visible nodes (the view's folded applied) in reading order,
// each indented by its depth, with the node's subtitle, badge, icon, color and action as the row's, and its progress
// as the row's percentage. A folded node's row value gains "+N" for the nodes it hides; they have no row. The graph's
// key, group, tone, selected, layout and direction are left out; a row's key is the graph's key and the node's id
// when that is free on the page. A native stream needs a key on every row, so its taken or overlong `key/id` becomes a
// free digest of the id instead.
//
// A card has at most CompactItems items and a graph is one of them (Fit's compact count); its rows have to fit in what
// the card's other items leave, so the rows are cut to the room left, in reading order, the rest left out. Each graph
// keeps at least its first row, and one row is kept back for every graph still to come, so a card with several graphs
// does not overflow.
func downgrade10(v *View, native, compact bool) {
	// every key on the page: a section's and an item's, and every item's tile keys share the page's one key space
	used := make(map[string]bool)
	for i := range v.Sections {
		if k := v.Sections[i].Key; k != "" {
			used[k] = true
		}
		for j := range v.Sections[i].Items {
			e := &v.Sections[i].Items[j]
			if k := e.Key; k != "" {
				used[k] = true
			}
			for k := range e.Tiles {
				if t := e.Tiles[k].Key; t != "" {
					used[t] = true
				}
			}
		}
	}
	// room is how many graph rows a card can still take; graphs left to come keep one row each
	room := CompactItems
	graphsLeft := 0
	if compact {
		for i := range v.Sections {
			for j := range v.Sections[i].Items {
				e := &v.Sections[i].Items[j]
				if e.Type == wire.ComponentGraph {
					graphsLeft++
				} else {
					room -= max(1, len(e.Tiles))
				}
			}
		}
		room -= max(graphsLeft-1, 0)
	}
	for i := range v.Sections {
		s := &v.Sections[i]
		items := make([]UIElem, 0, len(s.Items))
		for _, e := range s.Items {
			if e.Type != wire.ComponentGraph {
				items = append(items, e)
				continue
			}
			w := newGraphWalk(&e)
			order, depth := w.readingOrder()
			nodes := make(map[string]*wire.GraphNode, len(e.Nodes))
			for k := range e.Nodes {
				nodes[e.Nodes[k].ID] = &e.Nodes[k]
			}
			take := len(order)
			if compact {
				graphsLeft--
				take = min(take, max(room, 1))
				room -= take
			}
			for _, id := range order[:take] {
				n := nodes[id]
				row := UIElem{Type: wire.ComponentRow, Title: n.Title, Subtitle: n.Subtitle, Badge: n.Badge,
					Icon: n.Icon, IconColor: n.Color, Action: n.Action}
				if d := depth[id]; d > 0 {
					row.Title = strings.Repeat("│ ", d-1) + "└ " + n.Title
				}
				if n.Progress != nil {
					row.Value = gaugePercent(*n.Progress)
				}
				if w.folded[id] {
					if h := w.hidden(id); h > 0 {
						if row.Value != "" {
							row.Value += " · "
						}
						row.Value += "+" + strconv.Itoa(h)
					}
				}
				if e.Key != "" {
					if k := e.Key + "/" + id; !used[k] && utf8.RuneCountInString(k) <= graphRowKeyMost {
						row.Key = k
						used[k] = true
					} else if native {
						row.Key = freeGraphRowKey(e.Key, id, used)
					}
				} else if native {
					row.Key = freeGraphRowKey("", id, used)
				}
				items = append(items, row)
			}
		}
		s.Items = items
	}
}

// freeGraphRowKey is a key for a native stream's row whose `key/id` is taken or too long: the graph's key cut so the
// whole stays under graphRowKeyMost, a slash and the first graphRowKeyHashMost hex characters of the id's digest, with
// "~2", "~3", … until that is free. The digest keeps it stable across redraws and the suffix separates ids that only
// agree in their digest's start.
func freeGraphRowKey(graphKey, id string, used map[string]bool) string {
	sum := sha256.Sum256([]byte(id))
	base := []rune(graphKey)
	if most := graphRowKeyMost - graphRowKeyHashMost - 9; len(base) > most {
		base = base[:most]
	}
	head := string(base) + "/" + hex.EncodeToString(sum[:])[:graphRowKeyHashMost]
	for n := 1; ; n++ {
		k := head
		if n > 1 {
			k += "~" + strconv.Itoa(n)
		}
		if !used[k] {
			used[k] = true
			return k
		}
	}
}

// graphWalk is a graph as the reading order reads it (spec/fixtures/graph-layout/README.md, rules 1, 2, 3 and 7):
// structure, what is visible under its fold set, and the group of each node.
type graphWalk struct {
	ids      []string
	children map[string][]string
	parents  map[string][]string
	group    map[string]string
	folded   map[string]bool
	visible  map[string]bool
}

func newGraphWalk(g *wire.Component) *graphWalk {
	w := &graphWalk{
		ids:      make([]string, 0, len(g.Nodes)),
		children: make(map[string][]string, len(g.Nodes)),
		parents:  make(map[string][]string, len(g.Nodes)),
		group:    make(map[string]string, len(g.Nodes)),
		folded:   make(map[string]bool, len(g.Folded)),
		visible:  make(map[string]bool, len(g.Nodes)),
	}
	for _, n := range g.Nodes {
		w.ids = append(w.ids, n.ID)
		w.group[n.ID] = n.Group
	}
	for _, e := range g.Edges {
		w.children[e.From] = append(w.children[e.From], e.To)
		w.parents[e.To] = append(w.parents[e.To], e.From)
	}
	for _, id := range g.Folded {
		w.folded[id] = true
	}
	queue := make([]string, 0, len(w.ids))
	for _, id := range w.ids {
		if len(w.parents[id]) == 0 {
			w.visible[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if w.folded[id] {
			continue
		}
		for _, c := range w.children[id] {
			if !w.visible[c] {
				w.visible[c] = true
				queue = append(queue, c)
			}
		}
	}
	return w
}

// regroup pulls the first member of a group and then the others of its group up beside it, in their order; a node
// without a group (its key "") keeps its place.
func regroup(items []string, group map[string]string) []string {
	out := make([]string, 0, len(items))
	placed := map[string]bool{}
	for _, id := range items {
		g := group[id]
		if g == "" {
			out = append(out, id)
			continue
		}
		if placed[g] {
			continue
		}
		placed[g] = true
		for _, other := range items {
			if group[other] == g {
				out = append(out, other)
			}
		}
	}
	return out
}

// kids are the node's visible children in edge order, grouped, none when it is folded.
func (w *graphWalk) kids(id string) []string {
	if w.folded[id] {
		return nil
	}
	kids := make([]string, 0, len(w.children[id]))
	for _, c := range w.children[id] {
		if w.visible[c] {
			kids = append(kids, c)
		}
	}
	return regroup(kids, w.group)
}

// readingOrder is the visible nodes depth first from the roots (grouped), parents before children, each once at its
// first visit, with its depth there.
func (w *graphWalk) readingOrder() ([]string, map[string]int) {
	order := make([]string, 0, len(w.ids))
	depth := make(map[string]int, len(w.ids))
	var visit func(id string, d int)
	visit = func(id string, d int) {
		if _, seen := depth[id]; seen {
			return
		}
		depth[id] = d
		order = append(order, id)
		for _, c := range w.kids(id) {
			visit(c, d+1)
		}
	}
	roots := make([]string, 0, len(w.ids))
	for _, id := range w.ids {
		if len(w.parents[id]) == 0 {
			roots = append(roots, id)
		}
	}
	for _, r := range regroup(roots, w.group) {
		visit(r, 0)
	}
	return order, depth
}

// hidden is how many nodes under id are hidden by its fold, each once.
func (w *graphWalk) hidden(id string) int {
	seen := map[string]bool{}
	stack := append([]string(nil), w.children[id]...)
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[c] {
			continue
		}
		seen[c] = true
		stack = append(stack, w.children[c]...)
	}
	n := 0
	for c := range seen {
		if !w.visible[c] {
			n++
		}
	}
	return n
}
