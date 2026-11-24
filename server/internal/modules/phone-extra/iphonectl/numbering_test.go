package iphonectl

import (
	"slices"
	"testing"
)

func numbers(s *Screen) map[string]int {
	out := map[string]int{}
	for _, e := range s.Elements {
		out[e.Label] = e.N
	}
	return out
}

func snap(app App, els ...Element) snapshot {
	return snapshot{App: app, Size: screenSize, Elements: els}
}

func notesScreen() snapshot { return snap(exampleApp, notesElements()...) }

func TestFirstReadNumbersInReadingOrder(t *testing.T) {
	snap := notesScreen()
	slices.Reverse(snap.Elements)
	s := number(nil, snap)
	if s.Numbering != 1 {
		t.Fatalf("numbering %d", s.Numbering)
	}
	var got []string
	for _, e := range s.Elements {
		got = append(got, e.Label)
	}
	if want := []string{"Back", "Shopping", "Milk", "Bread", "Delete"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for i, e := range s.Elements {
		if e.N != i+1 {
			t.Fatalf("%q is %d, want %d", e.Label, e.N, i+1)
		}
	}
}

func TestElementsStillOnScreenKeepTheirNumbers(t *testing.T) {
	first := number(nil, notesScreen())
	snap := notesScreen()
	// Bread is gone, Eggs is new, Milk moved down, the order the bridge lists them in changed.
	snap.Elements = []Element{snap.Elements[4], snap.Elements[1], el("cell", "Eggs", 0, 120, 390, 44),
		el("cell", "Milk", 0, 208, 390, 44), snap.Elements[0]}
	second := number(first, snap)
	if second.Numbering != first.Numbering {
		t.Fatalf("renumbered: %d → %d", first.Numbering, second.Numbering)
	}
	want := map[string]int{"Back": 1, "Shopping": 2, "Milk": 3, "Delete": 5, "Eggs": 6}
	if got := numbers(second); !mapsEqual(got, want) {
		t.Fatalf("numbers %v, want %v", got, want)
	}
	d := diff(first, second)
	if len(d.Added) != 1 || d.Added[0].Label != "Eggs" || d.Added[0].N != 6 {
		t.Fatalf("added %+v", d.Added)
	}
	if len(d.Moved) != 1 || d.Moved[0].N != 3 || d.Moved[0].From.Y != 120 {
		t.Fatalf("moved %+v", d.Moved)
	}
	if !slices.Equal(d.Gone, []int{4}) {
		t.Fatalf("gone %v", d.Gone)
	}

	// A number is never given to another element: Bread coming back is new, not 4 again.
	snap.Elements = append(snap.Elements, el("cell", "Bread", 0, 252, 390, 44))
	third := number(second, snap)
	if n := numbers(third)["Bread"]; n != 7 {
		t.Fatalf("Bread came back as %d, want 7", n)
	}
}

func TestAValueChangeKeepsTheNumber(t *testing.T) {
	snap := snap(smsApp, el("textField", "Message", 16, 700, 300, 36))
	first := number(nil, snap)
	snap.Elements[0].Value = "hello"
	snap.Elements[0].Focused = true
	second := number(first, snap)
	if second.Elements[0].N != 1 || second.Numbering != 1 {
		t.Fatalf("%+v", second)
	}
	if d := diff(first, second); !d.Empty() {
		t.Fatalf("a typed value is not a move: %+v", d)
	}
}

func TestAnIdentifierOutlivesALabel(t *testing.T) {
	a := el("button", "3 unread", 300, 52, 80, 44)
	a.Identifier = "badge"
	first := number(nil, snap(exampleApp, el("button", "Back", 8, 52, 44, 44), a))
	a.Label = "4 unread"
	second := number(first, snap(exampleApp, el("button", "Back", 8, 52, 44, 44), a))
	if got := numbers(second)["4 unread"]; got != 2 {
		t.Fatalf("relabeled element is %d, want 2", got)
	}
}

func TestTwinsAreMatchedByPosition(t *testing.T) {
	snap := snap(exampleApp, el("button", "Reply", 300, 100, 60, 30), el("button", "Reply", 300, 300, 60, 30),
		el("heading", "Thread", 0, 40, 200, 30))
	first := number(nil, snap)
	byY := func(s *Screen) map[float64]int {
		m := map[float64]int{}
		for _, e := range s.Elements {
			m[e.Frame.Y] = e.N
		}
		return m
	}
	before := byY(first)
	// Both scroll up a little: each keeps its own number.
	snap.Elements[0].Frame.Y, snap.Elements[1].Frame.Y = 90, 290
	after := byY(number(first, snap))
	if after[90] != before[100] || after[290] != before[300] {
		t.Fatalf("before %v after %v", before, after)
	}
}

// After a scroll a row's twin button must not take the number of the row that was there before.
func TestTwinsKeepTheirRowsAcrossAScroll(t *testing.T) {
	row := func(name string, y float64) []Element {
		return []Element{el("cell", name, 0, y, 300, 60), el("button", "Reply", 310, y+10, 60, 40)}
	}
	var els []Element
	for i, n := range []string{"Alex", "Sam", "Kim"} {
		els = append(els, row(n, 100+float64(i)*100)...)
	}
	first := number(nil, snap(exampleApp, els...))
	replyOf := func(s *Screen, y float64) int {
		for _, e := range s.Elements {
			if e.Label == "Reply" && e.Frame.Y == y {
				return e.N
			}
		}
		return 0
	}
	samReply := replyOf(first, 210)
	// Scrolled up by one row: Alex is gone, Sam's row is where Alex's was, Lee is new at the bottom.
	var after []Element
	for i, n := range []string{"Sam", "Kim", "Lee"} {
		after = append(after, row(n, 100+float64(i)*100)...)
	}
	second := number(first, snap(exampleApp, after...))
	if second.Numbering != first.Numbering {
		t.Fatalf("renumbered")
	}
	if got := replyOf(second, 110); got != samReply {
		t.Fatalf("Sam's Reply is %d after the scroll, want %d (Alex's was %d)", got, samReply, replyOf(first, 110))
	}
	if got := replyOf(second, 310); got <= 6 {
		t.Fatalf("Lee's Reply took an old number: %d", got)
	}
}

func TestAnonymousElementsOnlyMatchInPlace(t *testing.T) {
	img := el("image", "", 10, 10, 50, 50)
	first := number(nil, snap(exampleApp, el("heading", "Photos", 0, 0, 100, 10), img))
	img.Frame.X = 200
	second := number(first, snap(exampleApp, el("heading", "Photos", 0, 0, 100, 10), img))
	if second.Elements[1].N != 3 {
		t.Fatalf("an unlabeled image that moved far is a new element: got %d", second.Elements[1].N)
	}
}

func TestANewAppOrANewPageStartsAgainFromOne(t *testing.T) {
	first := number(nil, notesScreen())
	other := notesScreen()
	other.App = otherApp
	second := number(first, other)
	if second.Numbering != 2 || second.Elements[0].N != 1 {
		t.Fatalf("another app: numbering %d, first number %d", second.Numbering, second.Elements[0].N)
	}
	if d := diff(first, second); !d.Replaced {
		t.Fatalf("diff %+v", d)
	}

	page := snap(otherApp, el("button", "Back", 8, 52, 44, 44), el("staticText", "One", 0, 100, 100, 20),
		el("staticText", "Two", 0, 130, 100, 20), el("staticText", "Three", 0, 160, 100, 20), el("staticText", "Four", 0, 190, 100, 20))
	third := number(second, page)
	if third.Numbering != 3 {
		t.Fatalf("one element in common of five is a new page: numbering %d", third.Numbering)
	}
	if n := numbers(third)["One"]; n != 2 {
		t.Fatalf("new page numbered from 1: One is %d", n)
	}
}

func TestHitTestTakesTheInnermostElement(t *testing.T) {
	s := number(nil, snap(exampleApp, el("cell", "Row", 0, 100, 390, 60), el("button", "Star", 330, 110, 40, 40)))
	if e, ok := s.At(Point{350, 130}); !ok || e.Label != "Star" {
		t.Fatalf("got %+v %v", e, ok)
	}
	if e, ok := s.At(Point{20, 130}); !ok || e.Label != "Row" {
		t.Fatalf("got %+v %v", e, ok)
	}
	if _, ok := s.At(Point{20, 10}); ok {
		t.Fatal("nothing there")
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
