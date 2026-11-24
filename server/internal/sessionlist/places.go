package sessionlist

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Place is one directory or group on one machine: Key is an Item's Project (Options.Places, PlaceCounts.Project) or
// its Group (PlaceCounts.Group). Neither means much without its Machine: two computers can hold the same path for
// different repositories, and group ids are machine-local.
type Place struct{ Machine, Key string }

// maxPlaces bounds Options.Places: the places of one project across a user's computers, not a list of everything.
const maxPlaces = 64

// PlaceCount is how many items are at a place, and how many of them need the user: by the state the options filter
// by, the folded one with Options.Roots.
type PlaceCount struct{ Total, NeedsYou int }

// PlaceCounts is the filtered membership counted by machine and project, and by machine and group: what Counts'
// Project and Group maps give, kept apart per machine. Items without the attribute are counted under the empty key.
type PlaceCounts struct {
	Total    int
	Project  map[Place]PlaceCount
	Group    map[Place]PlaceCount
	Revision string
}

// PlaceCounts counts the items Options selects by their place. It reads the current membership, as Counts does
// without a cursor (Since is validated and otherwise ignored); a cursor's frozen view has no place counts.
func (i *Index) PlaceCounts(options Options) (PlaceCounts, error) {
	if options.Search != "" {
		return PlaceCounts{}, failure("invalid_query", "search does not support counts")
	}
	o, err := validate(options)
	if err != nil {
		return PlaceCounts{}, err
	}
	if o.Cursor != "" {
		return PlaceCounts{}, failure("invalid_query", "place counts are of the current list, not of a cursor's view")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.initialize()
	i.pruneSnapshots()
	if o.Since != "" {
		if _, err := i.sinceRevision(o.Since); err != nil {
			return PlaceCounts{}, err
		}
	}
	f := o.filters()
	out := PlaceCounts{Project: map[Place]PlaceCount{}, Group: map[Place]PlaceCount{}, Revision: i.revisionToken()}
	count := func(r *record) {
		if !f.matches(r) {
			return
		}
		out.Total++
		needs := 0
		if stateOf(r.item, f.Roots, f.CrossMachineHierarchy) == "needs_you" {
			needs = 1
		}
		for _, at := range [...]struct {
			counts map[Place]PlaceCount
			place  Place
		}{{out.Project, Place{r.item.Machine, r.item.Project}}, {out.Group, Place{r.item.Machine, r.item.Group}}} {
			c := at.counts[at.place]
			c.Total++
			c.NeedsYou += needs
			at.counts[at.place] = c
		}
	}
	if f.ID != "" {
		count(i.items[f.ID])
		return out, nil
	}
	for _, r := range i.items {
		count(r)
	}
	return out, nil
}

// placesKey spells a set of places as one string, the same for the same set in any order: filters must stay
// comparable (a cache key, a cursor's binding). Each place is "<len>:<machine><len>:<key>", so no byte of a machine
// id or a path needs escaping.
func placesKey(places []Place) string {
	if len(places) == 0 {
		return ""
	}
	sorted := slices.Clone(places)
	slices.SortFunc(sorted, func(a, b Place) int {
		return cmp.Or(cmp.Compare(a.Machine, b.Machine), cmp.Compare(a.Key, b.Key))
	})
	sorted = slices.Compact(sorted)
	var b strings.Builder
	for _, p := range sorted {
		for _, field := range [...]string{p.Machine, p.Key} {
			b.WriteString(strconv.Itoa(len(field)))
			b.WriteByte(':')
			b.WriteString(field)
		}
	}
	return b.String()
}

// inPlaces reports whether (machine, key) is one of the places a placesKey spells. It runs for every item of a
// filtered scan, so it reads the key in place.
func inPlaces(spelled, machine, key string) bool {
	for spelled != "" {
		var m, k string
		m, spelled = lengthPrefixed(spelled)
		k, spelled = lengthPrefixed(spelled)
		if m == machine && k == key {
			return true
		}
	}
	return false
}

// lengthPrefixed takes one "<len>:<field>" off the front of a placesKey.
func lengthPrefixed(s string) (field, rest string) {
	n, at := 0, 0
	for ; s[at] != ':'; at++ {
		n = n*10 + int(s[at]-'0')
	}
	at++
	return s[at : at+n], s[at+n:]
}
