package sessionlist

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// placed is a row on machine, in project and group.
func placed(id, machine, project, group string, seconds int) Item {
	item := testItem(id, seconds)
	item.Machine, item.Project, item.Group = machine, project, group
	return item
}

// One project's directories on two computers are one list: the same path on a third computer is not in it.
func TestPlacesSelectMachineAndProjectPairs(t *testing.T) {
	var index Index
	index.Replace("mac", []Item{placed("mac-1", "mac", "/code/app", "w1", 1), placed("mac-2", "mac", "/code/app", "w2", 2), placed("mac-3", "mac", "/code/other", "w1", 3)})
	index.Replace("pc", []Item{placed("pc-1", "pc", `C:\code\app`, "w1", 4), placed("pc-2", "pc", "/code/app", "w1", 5)})
	index.Replace("lab", []Item{placed("lab-1", "lab", "/code/app", "w1", 6)})
	app := []Place{{"mac", "/code/app"}, {"pc", `C:\code\app`}}

	page := mustQuery(t, &index, Options{Places: app})
	if got := sessionIDs(page); !reflect.DeepEqual(got, []string{"pc-1", "mac-2", "mac-1"}) || page.Total != 3 {
		t.Fatalf("rows %v, total %d", got, page.Total)
	}
	counts, err := index.Counts(Options{Places: app})
	if err != nil || counts.Total != 3 || counts.Machine["mac"] != 2 || counts.Machine["pc"] != 1 || len(counts.Machine) != 2 {
		t.Fatalf("counts %+v %v", counts, err)
	}
	// It narrows with the other filters, never widens them.
	if page := mustQuery(t, &index, Options{Places: app, Machine: "pc"}); !reflect.DeepEqual(sessionIDs(page), []string{"pc-1"}) {
		t.Fatalf("with a machine: %v", sessionIDs(page))
	}
	if page := mustQuery(t, &index, Options{Places: app, Group: "w2"}); !reflect.DeepEqual(sessionIDs(page), []string{"mac-2"}) {
		t.Fatalf("with a group: %v", sessionIDs(page))
	}
	if page := mustQuery(t, &index, Options{Places: []Place{{"lab", "/code/missing"}}}); page.Total != 0 || len(page.Sessions) != 0 {
		t.Fatalf("a place nothing is at: %+v", page)
	}
	// A row without a project is at the place with the empty key.
	index.Replace("nas", []Item{placed("nas-1", "nas", "", "", 7)})
	if page := mustQuery(t, &index, Options{Places: []Place{{"nas", ""}}}); !reflect.DeepEqual(sessionIDs(page), []string{"nas-1"}) {
		t.Fatalf("the empty project: %v", sessionIDs(page))
	}
}

// Spaces select by machine and group the same way; with places, both must hold.
func TestSpacesSelectMachineAndGroupPairs(t *testing.T) {
	var index Index
	index.Replace("mac", []Item{placed("mac-1", "mac", "/code/app", "w1", 1), placed("mac-2", "mac", "/code/app", "w2", 2), placed("mac-3", "mac", "/code/other", "", 3)})
	index.Replace("pc", []Item{placed("pc-1", "pc", "/code/app", "w1", 4), placed("pc-2", "pc", "/code/app", "", 5)})
	for name, test := range map[string]struct {
		options Options
		want    []string
	}{
		"one space":               {Options{Spaces: []Place{{"mac", "w1"}}}, []string{"mac-1"}},
		"the same id on two":      {Options{Spaces: []Place{{"mac", "w1"}, {"pc", "w1"}}}, []string{"pc-1", "mac-1"}},
		"in no space":             {Options{Spaces: []Place{{"mac", ""}, {"pc", ""}}}, []string{"pc-2", "mac-3"}},
		"a space and a place":     {Options{Spaces: []Place{{"mac", "w2"}, {"mac", ""}}, Places: []Place{{"mac", "/code/app"}}}, []string{"mac-2"}},
		"a space nothing is in":   {Options{Spaces: []Place{{"pc", "w2"}}}, nil},
		"with a group that isn't": {Options{Spaces: []Place{{"mac", "w1"}}, Group: "w2"}, nil},
	} {
		page := mustQuery(t, &index, test.options)
		if got := sessionIDs(page); len(got) != len(test.want) || (len(got) > 0 && !reflect.DeepEqual(got, test.want)) || page.Total != len(test.want) {
			t.Errorf("%s: %v (total %d), want %v", name, got, page.Total, test.want)
		}
		if counts, err := index.PlaceCounts(test.options); err != nil || counts.Total != len(test.want) {
			t.Errorf("%s: place counts %+v %v", name, counts, err)
		}
	}
	first := mustQuery(t, &index, Options{Spaces: []Place{{"mac", "w1"}, {"pc", "w1"}}, Limit: 1})
	if next := mustQuery(t, &index, Options{Spaces: []Place{{"pc", "w1"}, {"mac", "w1"}}, Limit: 1, Cursor: first.NextCursor}); !reflect.DeepEqual(sessionIDs(next), []string{"mac-1"}) {
		t.Fatalf("the same spaces in another order: %+v", next)
	}
	// The same pairs as places are another list.
	_, err := index.Query(Options{Places: []Place{{"mac", "w1"}, {"pc", "w1"}}, Limit: 1, Cursor: first.NextCursor})
	requireCode(t, err, "invalid_query")
}

// A cursor is bound to its places as a set: the same places in another order continue it, other places do not.
func TestPlacesBindCursorsAndDeltas(t *testing.T) {
	var index Index
	index.Replace("mac", []Item{placed("mac-1", "mac", "/code/app", "w1", 1), placed("mac-2", "mac", "/code/app", "w1", 2)})
	index.Replace("pc", []Item{placed("pc-1", "pc", "/code/app", "w1", 3)})
	app := []Place{{"mac", "/code/app"}, {"pc", "/code/app"}}
	first := mustQuery(t, &index, Options{Places: app, Limit: 2})
	if !first.HasMore || !reflect.DeepEqual(sessionIDs(first), []string{"pc-1", "mac-2"}) {
		t.Fatalf("first page: %+v", first)
	}
	reordered := []Place{{"pc", "/code/app"}, {"mac", "/code/app"}, {"pc", "/code/app"}}
	if next := mustQuery(t, &index, Options{Places: reordered, Limit: 2, Cursor: first.NextCursor}); !reflect.DeepEqual(sessionIDs(next), []string{"mac-1"}) {
		t.Fatalf("the same places in another order: %+v", next)
	}
	_, err := index.Query(Options{Places: app[:1], Limit: 2, Cursor: first.NextCursor})
	requireCode(t, err, "invalid_query")
	_, err = index.Query(Options{Limit: 2, Cursor: first.NextCursor})
	requireCode(t, err, "invalid_query")

	// A row that moves to another directory leaves the filtered list as a removal.
	index.Replace("pc", []Item{placed("pc-1", "pc", "/code/elsewhere", "w1", 3)})
	delta := mustQuery(t, &index, Options{Places: app, Since: first.Revision})
	if len(delta.Sessions) != 0 || !reflect.DeepEqual(delta.Remove, []string{"pc-1"}) || delta.Total != 2 {
		t.Fatalf("delta: %+v", delta)
	}
}

// Machine ids and paths holding the key's own punctuation still name exactly themselves.
func TestPlacesKeyIsUnambiguous(t *testing.T) {
	odd := []Place{{"1:a", "2:b"}, {"1", ":a2:b"}, {"a", ""}, {"", "x"}, {"a:1", "/p:3:q"}, {"机器", `C:\代码\app`}}
	spelled := placesKey(odd)
	for _, p := range odd {
		if !inPlaces(spelled, p.Machine, p.Key) {
			t.Errorf("%q does not hold %+v", spelled, p)
		}
	}
	for _, p := range []Place{{"1:a2", "b"}, {"1", "a"}, {"a", "x"}, {"", ""}, {"a:1", "/p"}, {"机器", `C:\代码`}} {
		if inPlaces(spelled, p.Machine, p.Key) {
			t.Errorf("%q wrongly holds %+v", spelled, p)
		}
	}
	if placesKey(nil) != "" || inPlaces("", "a", "b") {
		t.Fatal("no places must be the empty key, which holds nothing")
	}
	if placesKey([]Place{{"b", "2"}, {"a", "1"}}) != placesKey([]Place{{"a", "1"}, {"b", "2"}, {"a", "1"}}) {
		t.Fatal("the same set spelled two ways")
	}
}

// Place counts are Counts' project and group maps kept apart per machine, over the same filtered membership.
func TestPlaceCountsFollowTheFilters(t *testing.T) {
	var index Index
	mac := []Item{placed("mac-1", "mac", "/code/app", "w1", 1), placed("mac-2", "mac", "/code/app", "w2", 2), placed("mac-3", "mac", "", "", 3)}
	mac[1].State = "needs_you"
	mac[2].Private = true
	pc := []Item{placed("pc-1", "pc", "/code/app", "w1", 4), placed("pc-2", "pc", `C:\notes`, "w1", 5)}
	pc[1].SearchText = "Quarterly notes"
	pc[1].Fold = &Fold{Root: "pc-1"}
	pc[0].Fold = &Fold{State: "needs_you"} // its own state is working: it needs the user through its child
	index.ReplaceOwned("mac", "assistant", mac)
	index.ReplaceOwned("pc", "m-0b0b0b", pc)

	all, err := index.PlaceCounts(Options{})
	if err != nil || all.Total != 5 || all.Revision != index.Revision() {
		t.Fatalf("all: %+v %v", all, err)
	}
	// a place's count, and how many of them need the user (mac-2 does)
	wantProject := map[Place]PlaceCount{{"mac", "/code/app"}: {2, 1}, {"mac", ""}: {1, 0}, {"pc", "/code/app"}: {1, 0}, {"pc", `C:\notes`}: {1, 0}}
	wantGroup := map[Place]PlaceCount{{"mac", "w1"}: {1, 0}, {"mac", "w2"}: {1, 1}, {"mac", ""}: {1, 0}, {"pc", "w1"}: {2, 0}}
	if !reflect.DeepEqual(all.Project, wantProject) || !reflect.DeepEqual(all.Group, wantGroup) {
		t.Fatalf("project %v, group %v", all.Project, all.Group)
	}
	for name, test := range map[string]struct {
		options Options
		total   int
		project map[Place]PlaceCount
	}{
		"state":      {Options{State: "needs_you"}, 1, map[Place]PlaceCount{{"mac", "/code/app"}: {1, 1}}},
		"search":     {Options{Q: "quarterly"}, 1, map[Place]PlaceCount{{"pc", `C:\notes`}: {1, 0}}},
		"machine":    {Options{Machine: "pc"}, 2, map[Place]PlaceCount{{"pc", "/code/app"}: {1, 0}, {"pc", `C:\notes`}: {1, 0}}},
		"manager":    {Options{Manager: "assistant"}, 3, map[Place]PlaceCount{{"mac", "/code/app"}: {2, 1}, {"mac", ""}: {1, 0}}},
		"no private": {Options{NoPrivate: true}, 4, map[Place]PlaceCount{{"mac", "/code/app"}: {2, 1}, {"pc", "/code/app"}: {1, 0}, {"pc", `C:\notes`}: {1, 0}}},
		// a parent counts as needing the user when a child folded under it does (pc-1, through pc-2's fold)
		"roots":   {Options{Roots: true}, 4, map[Place]PlaceCount{{"mac", "/code/app"}: {2, 1}, {"mac", ""}: {1, 0}, {"pc", "/code/app"}: {1, 1}}},
		"places":  {Options{Places: []Place{{"pc", "/code/app"}}}, 1, map[Place]PlaceCount{{"pc", "/code/app"}: {1, 0}}},
		"one row": {Options{ID: "mac-2"}, 1, map[Place]PlaceCount{{"mac", "/code/app"}: {1, 1}}},
		"no row":  {Options{ID: "missing"}, 0, map[Place]PlaceCount{}},
	} {
		got, err := index.PlaceCounts(test.options)
		if err != nil || got.Total != test.total || !reflect.DeepEqual(got.Project, test.project) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		// The same membership Counts reports.
		if counts, _ := index.Counts(test.options); counts.Total != got.Total {
			t.Errorf("%s: Counts has %d, PlaceCounts %d", name, counts.Total, got.Total)
		}
	}

	// It is of the current list: a baseline is checked, a cursor refused.
	if _, err := index.PlaceCounts(Options{Since: all.Revision}); err != nil {
		t.Fatalf("a valid baseline: %v", err)
	}
	_, err = index.PlaceCounts(Options{Since: "junk"})
	requireCode(t, err, "invalid_query")
	page := mustQuery(t, &index, Options{Limit: 1})
	_, err = index.PlaceCounts(Options{Cursor: page.NextCursor})
	requireCode(t, err, "invalid_query")
	_, err = index.PlaceCounts(Options{Search: "quarterly", SearchContext: searchContext()})
	requireCode(t, err, "invalid_query")
	var empty Index
	if got, err := empty.PlaceCounts(Options{}); err != nil || got.Total != 0 || got.Project == nil || got.Group == nil || got.Revision == "" {
		t.Fatalf("zero index: %+v %v", got, err)
	}
}

func TestParsePlaces(t *testing.T) {
	for _, key := range []string{"place", "space", "catalog"} {
		if !Enabled(url.Values{key: {""}}) {
			t.Errorf("%s did not enable indexed behavior", key)
		}
	}
	o, err := Parse(url.Values{"place": {`["mac","/code/app"]`, `["pc","C:\\code\\app"]`, `["nas",""]`}, "catalog": {"1"}})
	want := []Place{{"mac", "/code/app"}, {"pc", `C:\code\app`}, {"nas", ""}}
	if err != nil || !reflect.DeepEqual(o.Places, want) {
		t.Fatalf("parsed %+v, %v", o.Places, err)
	}
	if o, err := Parse(url.Values{"catalog": {"false"}}); err != nil || o.Places != nil || o.Spaces != nil {
		t.Fatalf("no place: %+v %v", o, err)
	}
	o, err = Parse(url.Values{"space": {`["mac","w1"]`, `["pc",""]`}})
	if err != nil || !reflect.DeepEqual(o.Spaces, []Place{{"mac", "w1"}, {"pc", ""}}) || o.Places != nil {
		t.Fatalf("parsed spaces %+v, %v", o, err)
	}
	many := make([]string, maxPlaces+1)
	for n := range many {
		many[n] = `["mac","/code/` + strconv.Itoa(n) + `"]`
	}
	if _, err := Parse(url.Values{"place": many[:maxPlaces]}); err != nil {
		t.Fatalf("%d places: %v", maxPlaces, err)
	}
	for name, values := range map[string]url.Values{
		"too many":          {"place": many},
		"no value":          {"place": nil},
		"empty":             {"place": {""}},
		"not JSON":          {"place": {"mac,/code/app"}},
		"an object":         {"place": {`{"machine":"mac"}`}},
		"one part":          {"place": {`["mac"]`}},
		"three parts":       {"place": {`["mac","/code/app","x"]`}},
		"a number":          {"place": {`["mac",1]`}},
		"null":              {"place": {"null"}},
		"no machine":        {"place": {`["","/code/app"]`}},
		"one bad of two":    {"place": {`["mac","/code/app"]`, "junk"}},
		"too many spaces":   {"space": many},
		"space not JSON":    {"space": {"mac/w1"}},
		"space no machine":  {"space": {`["","w1"]`}},
		"space no value":    {"space": nil},
		"catalog twice":     {"catalog": {"1", "1"}},
		"catalog malformed": {"catalog": {"yes"}},
		"catalog empty":     {"catalog": {""}},
	} {
		_, err := Parse(values)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		requireCode(t, err, "invalid_query")
	}
	var index Index
	_, err = index.Query(Options{Places: []Place{{"", "/code/app"}}})
	requireCode(t, err, "invalid_query")
	_, err = index.Counts(Options{Places: make([]Place, maxPlaces+1)})
	requireCode(t, err, "invalid_query")
	if !strings.Contains(err.Error(), "place") {
		t.Fatalf("the error does not name the key: %v", err)
	}
	_, err = index.Query(Options{Spaces: []Place{{"", "w1"}}})
	requireCode(t, err, "invalid_query")
	if !strings.Contains(err.Error(), "space") {
		t.Fatalf("the error does not name the key: %v", err)
	}
}
