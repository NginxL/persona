package iphonectl

import (
	"math"
	"sort"
	"strings"
)

// Numbered is an element with the number the model refers to it by.
type Numbered struct {
	N int `json:"n"`
	Element
}

// snapshot is one read of the screen: the tree's elements and app, with what device_info says.
type snapshot struct {
	App       App
	Size      Size
	Elements  []Element
	Locked    bool
	Truncated bool
}

// Screen is a read of the screen with its elements numbered.
//
// Numbers are stable: an element still on screen at the next read keeps its number, a new one gets the next unused
// number, and a number is never given to another element. Only when the screen was replaced (another app in front,
// or too little in common with the read before) do the numbers start again from 1, and Numbering goes up by one, so
// a step that names a number from an older numbering can be refused instead of hitting whatever has that number now.
type Screen struct {
	Numbering int        `json:"numbering"`
	App       App        `json:"app"`
	Size      Size       `json:"size"`
	Locked    bool       `json:"locked,omitempty"`
	Truncated bool       `json:"truncated,omitempty"` // the bridge stopped listing elements at its limit
	Elements  []Numbered `json:"elements"`            // in reading order
	next      int        // the number the next new element gets
}

// Find is the element numbered n.
func (s *Screen) Find(n int) (Numbered, bool) {
	if s != nil {
		for _, e := range s.Elements {
			if e.N == n {
				return e, true
			}
		}
	}
	return Numbered{}, false
}

// At is the innermost element whose frame contains p, for a step that taps a point.
func (s *Screen) At(p Point) (Numbered, bool) {
	var best Numbered
	found := false
	if s == nil {
		return best, false
	}
	for _, e := range s.Elements {
		if a := e.Frame.Area(); a > 0 && e.Frame.Contains(p) && (!found || a < best.Frame.Area()) {
			best, found = e, true
		}
	}
	return best, found
}

// under is every element whose frame contains p, innermost first.
func (s *Screen) under(p Point) []Numbered {
	var out []Numbered
	if s == nil {
		return nil
	}
	for _, e := range s.Elements {
		if e.Frame.Area() > 0 && e.Frame.Contains(p) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Frame.Area() < out[j].Frame.Area() })
	return out
}

// Focused is the element that has the keyboard.
func (s *Screen) Focused() (Numbered, bool) {
	if s != nil {
		for _, e := range s.Elements {
			if e.Focused {
				return e, true
			}
		}
	}
	return Numbered{}, false
}

// replacedBelow is the share of elements two reads must have in common, of the larger read, for the numbers to stay:
// below it the screen counts as replaced. A scroll of part of a list keeps the numbers; a new page doesn't.
const replacedBelow = 0.3

// moveTolerance is how far (points, any edge) an element may shift and still not count as moved.
const moveTolerance = 2.0

// number numbers snap against prev, the read before it (nil for none).
func number(prev *Screen, snap snapshot) *Screen {
	cur := &Screen{App: snap.App, Size: snap.Size, Locked: snap.Locked, Truncated: snap.Truncated}
	els := readingOrder(snap.Elements)
	if prev == nil || !sameApp(prev.App, snap.App) {
		return fresh(cur, prev, els)
	}
	match := matchElements(prev.Elements, els)
	larger := max(len(prev.Elements), len(els))
	if larger > 0 && float64(len(match)) < replacedBelow*float64(larger) {
		return fresh(cur, prev, els)
	}
	cur.Numbering, cur.next = prev.Numbering, prev.next
	cur.Elements = make([]Numbered, len(els))
	for i, e := range els {
		if n, ok := match[i]; ok {
			cur.Elements[i] = Numbered{N: n, Element: e}
			continue
		}
		cur.Elements[i] = Numbered{N: cur.next, Element: e}
		cur.next++
	}
	return cur
}

// fresh numbers els from 1 under a new numbering.
func fresh(cur, prev *Screen, els []Element) *Screen {
	cur.Numbering = 1
	if prev != nil {
		cur.Numbering = prev.Numbering + 1
	}
	cur.Elements = make([]Numbered, len(els))
	for i, e := range els {
		cur.Elements[i] = Numbered{N: i + 1, Element: e}
	}
	cur.next = len(els) + 1
	return cur
}

func sameApp(a, b App) bool {
	if a.BundleID != "" || b.BundleID != "" {
		return a.BundleID == b.BundleID
	}
	return a.Name == b.Name
}

// identity is what must be equal for two reads' elements to be the same one: the role and the app's identifier, or
// the label when there is none. A changed value (text typed into a field) keeps the identity.
func identity(e Element) string {
	if e.Identifier != "" {
		return e.Role + "\x00id\x00" + e.Identifier
	}
	return e.Role + "\x00" + strings.TrimSpace(e.Label)
}

// matchElements pairs cur's elements with prev's of the same identity and returns cur's index → the number it keeps.
//
// An identity found once in each read is paired outright, wherever it moved. Those pairs say how far the screen
// scrolled (their median shift); the rest — identities shared by several elements (a "Reply" in every row) and
// elements with neither an identifier nor a label — are paired only where that shift puts them, give or take half
// their size. So after a scroll each row's "Reply" keeps its own number instead of taking the one of the row that
// was there before, and a twin that can't be told apart gets a new number rather than another's.
func matchElements(prev []Numbered, cur []Element) map[int]int {
	prevBy, curBy := map[string][]int{}, map[string][]int{}
	for i, p := range prev {
		prevBy[identity(p.Element)] = append(prevBy[identity(p.Element)], i)
	}
	for i, c := range cur {
		curBy[identity(c)] = append(curBy[identity(c)], i)
	}
	out := map[int]int{}
	used := map[int]bool{}
	var dxs, dys []float64
	for id, cs := range curBy {
		ps := prevBy[id]
		if len(cs) != 1 || len(ps) != 1 || anonymous(cur[cs[0]]) {
			continue
		}
		c, p := cur[cs[0]].Frame.Center(), prev[ps[0]].Frame.Center()
		out[cs[0]], used[ps[0]] = prev[ps[0]].N, true
		dxs, dys = append(dxs, c.X-p.X), append(dys, c.Y-p.Y)
	}
	shift := Point{median(dxs), median(dys)}

	type pair struct {
		ci, pi int
		d      float64
	}
	var pairs []pair
	for ci, c := range cur {
		if _, ok := out[ci]; ok {
			continue
		}
		tol := math.Max(2*moveTolerance, math.Min(c.Frame.W, c.Frame.H)/2)
		for _, pi := range prevBy[identity(c)] {
			if used[pi] {
				continue
			}
			was := prev[pi].Frame.Center()
			if d := distance(c.Frame.Center(), Point{was.X + shift.X, was.Y + shift.Y}); d <= tol {
				pairs = append(pairs, pair{ci, pi, d})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].d < pairs[j].d })
	for _, p := range pairs {
		if _, ok := out[p.ci]; ok || used[p.pi] {
			continue
		}
		out[p.ci], used[p.pi] = prev[p.pi].N, true
	}
	return out
}

func anonymous(e Element) bool { return e.Identifier == "" && strings.TrimSpace(e.Label) == "" }

// median of vs, 0 for none.
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func distance(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// rowHeight is the band (points) within which elements count as one row of reading order.
const rowHeight = 8.0

// readingOrder sorts elements top to bottom, then left to right within a row.
func readingOrder(els []Element) []Element {
	out := append([]Element(nil), els...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := math.Floor(out[i].Frame.Y/rowHeight), math.Floor(out[j].Frame.Y/rowHeight)
		if ri != rj {
			return ri < rj
		}
		return out[i].Frame.X < out[j].Frame.X
	})
	return out
}

// Change is an element that is new or moved since an earlier read under the same numbering.
type Change struct {
	Numbered
	Moved bool  `json:"moved,omitempty"` // false: new
	From  *Rect `json:"from,omitempty"`  // where a moved element was
}

// Diff is what changed between two reads.
type Diff struct {
	// Replaced: the later read started a new numbering, so it is listed whole and Added / Moved are empty.
	Replaced bool     `json:"replaced,omitempty"`
	Added    []Change `json:"added,omitempty"`
	Moved    []Change `json:"moved,omitempty"`
	Gone     []int    `json:"gone,omitempty"` // numbers no longer on screen
}

// Empty says nothing changed.
func (d Diff) Empty() bool {
	return !d.Replaced && len(d.Added) == 0 && len(d.Moved) == 0 && len(d.Gone) == 0
}

// diff is what changed from before to after (before may be nil: everything is new).
func diff(before, after *Screen) Diff {
	if after == nil {
		return Diff{}
	}
	if before == nil || before.Numbering != after.Numbering {
		return Diff{Replaced: true}
	}
	was := map[int]Numbered{}
	for _, e := range before.Elements {
		was[e.N] = e
	}
	var d Diff
	for _, e := range after.Elements {
		old, ok := was[e.N]
		delete(was, e.N)
		switch {
		case !ok:
			d.Added = append(d.Added, Change{Numbered: e})
		case !old.Frame.near(e.Frame, moveTolerance):
			from := old.Frame
			d.Moved = append(d.Moved, Change{Numbered: e, Moved: true, From: &from})
		}
	}
	for n := range was {
		d.Gone = append(d.Gone, n)
	}
	sort.Ints(d.Gone)
	return d
}
