package sessionlist

import (
	"net/url"
	"testing"
)

func TestParseHierarchy(t *testing.T) {
	for _, key := range []string{"roots", "parent_session_id"} {
		if !Enabled(url.Values{key: {"1"}}) {
			t.Errorf("%s did not enable indexed behavior", key)
		}
	}
	o, err := Parse(url.Values{"roots": {"1"}})
	if err != nil || !o.Roots || o.Under != "" {
		t.Fatalf("roots: %+v %v", o, err)
	}
	o, err = Parse(url.Values{"parent_session_id": {"lead/one"}, "machine_id": {"m"}, "roots": {"false"}})
	if err != nil || o.Roots || o.Under != "lead/one" || o.Machine != "m" {
		t.Fatalf("parent: %+v %v", o, err)
	}
	for _, bad := range []url.Values{
		{"parent_session_id": {"lead"}}, {"parent_session_id": {""}, "machine_id": {"m"}}, {"roots": {"yes"}},
		{"roots": {"1", "1"}}, {"parent_session_id": {"a", "b"}, "machine_id": {"m"}},
		{"roots": {"1"}, "parent_session_id": {"lead"}, "machine_id": {"m"}}, // never a root: always an empty page
	} {
		if _, err := Parse(bad); err == nil || err.(*Error).Code != "invalid_query" {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, err := validate(Options{Under: "lead"}); err == nil {
		t.Error("an adapter's Under without Machine was taken")
	}
	if _, err := validate(Options{Under: "lead", Machine: "m", Roots: true}); err == nil {
		t.Error("an adapter's Under with Roots was taken")
	}
}

// Roots leaves the folded items out of rows, totals and every count and counts the rest by their folded state (its
// background-waiting count only for an item none of whose children works or waits); Under selects one root's items.
func TestQueryHierarchy(t *testing.T) {
	var index Index
	lead := testItem("lead", 1)
	lead.State, lead.BackgroundWaiting, lead.Fold = "working", true, &Fold{State: "needs_you"}
	kid := testItem("kid", 2)
	kid.State, kid.Fold = "needs_you", &Fold{Root: "lead", State: "needs_you"}
	solo := testItem("solo", 3)
	solo.BackgroundWaiting, solo.Fold = true, &Fold{State: "working", BackgroundWaiting: true}
	index.Replace("machine-a", []Item{lead, kid, solo})

	all := mustQuery(t, &index, Options{Limit: 10})
	roots := mustQuery(t, &index, Options{Limit: 10, Roots: true})
	if all.Total != 3 || roots.Total != 2 || len(roots.Sessions) != 2 {
		t.Fatalf("all %d, roots %d %v", all.Total, roots.Total, roots.Sessions)
	}
	c, _ := index.Counts(Options{})
	r, _ := index.Counts(Options{Roots: true})
	if c.State["working"] != 2 || c.State["needs_you"] != 1 || c.BackgroundWaiting != 2 {
		t.Fatalf("counts: %+v", c)
	}
	if r.Total != 2 || r.State["needs_you"] != 1 || r.State["working"] != 1 || r.BackgroundWaiting != 1 || r.Machine["machine-a"] != 2 {
		t.Fatalf("roots counts: %+v", r)
	}
	if p := mustQuery(t, &index, Options{Limit: 10, Roots: true, State: "needs_you"}); p.Total != 1 {
		t.Fatalf("roots needs_you: %+v", p)
	}
	if p := mustQuery(t, &index, Options{Limit: 10, State: "needs_you"}); p.Total != 1 || p.Sessions[0].(testPayload).ID != "kid" {
		t.Fatalf("needs_you: %+v", p)
	}
	if p := mustQuery(t, &index, Options{Limit: 10, Machine: "machine-a", Under: "lead"}); p.Total != 1 || p.Sessions[0].(testPayload).ID != "kid" {
		t.Fatalf("under: %+v", p)
	}
	if p := mustQuery(t, &index, Options{Limit: 10, Machine: "machine-b", Under: "lead"}); p.Total != 0 {
		t.Fatalf("under on another machine: %+v", p)
	}
	page := mustQuery(t, &index, Options{Limit: 1, Roots: true})
	if _, err := index.Query(Options{Limit: 1, Cursor: page.NextCursor}); err == nil {
		t.Fatal("a roots cursor read without roots")
	}
}

func TestQueryCrossMachineHierarchyAndOrphanSubagents(t *testing.T) {
	for _, bad := range []url.Values{
		{"parent_machine_id": {"mac"}},
		{"parent_machine_id": {""}, "parent_session_id": {"lead"}},
		{"parent_machine_id": {"a", "b"}, "parent_session_id": {"lead"}},
		{"parent_machine_id": {"mac"}, "parent_session_id": {"lead"}, "roots": {"1"}},
		{"cross_machine_hierarchy": {"yes"}},
		{"cross_machine_hierarchy": {"1", "0"}},
	} {
		if _, err := Parse(bad); err == nil || err.(*Error).Code != "invalid_query" {
			t.Errorf("%v: got %v, want invalid_query", bad, err)
		}
	}
	for _, key := range []string{"parent_machine_id", "cross_machine_hierarchy"} {
		if !Enabled(url.Values{key: {"1"}}) {
			t.Fatalf("%s must enable indexed behavior", key)
		}
	}

	var index Index
	lead := testItem("lead", 1)
	lead.Machine, lead.State, lead.Fold = "mac", "idle", &Fold{State: "needs_you", HasLegacyFold: true, LegacyState: "working"}
	localKid := testItem("local-kid", 2)
	localKid.Machine, localKid.State, localKid.Subagent, localKid.Fold = "mac", "working", true, &Fold{Root: "lead", State: "working"}
	orphanSub := testItem("orphan-sub", 3)
	orphanSub.Machine, orphanSub.State, orphanSub.Subagent, orphanSub.Fold = "mac", "working", true, &Fold{DirectParent: "archived-lead", State: "working"}
	orphanNeeds := testItem("orphan-needs", 4)
	orphanNeeds.Machine, orphanNeeds.State, orphanNeeds.Subagent = "mac", "needs_you", true
	orphanNonSub := testItem("orphan-plain", 5)
	orphanNonSub.Machine, orphanNonSub.State = "mac", "idle"
	index.Replace("mac", []Item{lead, localKid, orphanSub, orphanNeeds, orphanNonSub})

	remoteKid := testItem("remote-kid", 6)
	remoteKid.Machine, remoteKid.State, remoteKid.Subagent, remoteKid.Fold = "box", "needs_you", true, &Fold{Root: "lead", RootMachine: "mac", State: "needs_you"}
	remoteOrphan := testItem("remote-orphan", 7)
	remoteOrphan.Machine, remoteOrphan.State, remoteOrphan.Subagent, remoteOrphan.Fold = "box", "idle", true, &Fold{DirectParent: "archived-lead", DirectParentMachine: "mac", State: "idle"}
	index.Replace("box", []Item{remoteKid, remoteOrphan})

	// New clients passing cross_machine_hierarchy=1 hide same-machine folded, cross-machine folded, AND all orphan subagents.
	rootsNew := mustQuery(t, &index, Options{Limit: 20, Roots: true, CrossMachineHierarchy: true})
	if rootsNew.Total != 2 || len(rootsNew.Sessions) != 2 {
		t.Fatalf("roots+cross = total %d rows %+v, want 2 (lead + orphan-plain)", rootsNew.Total, rootsNew.Sessions)
	}
	for _, s := range rootsNew.Sessions {
		id := s.(testPayload).ID
		if id != "lead" && id != "orphan-plain" {
			t.Fatalf("unexpected row in roots=1&cross_machine_hierarchy=1: %s", id)
		}
	}
	newCounts, err := index.Counts(Options{Roots: true, CrossMachineHierarchy: true})
	if err != nil || newCounts.State["needs_you"] != 1 || newCounts.State["working"] != 0 {
		t.Fatalf("new counts = %+v (err %v), want needs_you=1 working=0", newCounts, err)
	}

	// Legacy clients (roots=1 WITHOUT cross_machine_hierarchy=1) hide same-machine folded & non-blocking
	// orphan subagents, keep cross-machine folded rows and needs_you orphan subagents reachable, and use
	// LegacyState on the parent so cross-machine needs_you is NOT double-counted on both parent and child.
	rootsLegacy := mustQuery(t, &index, Options{Limit: 20, Roots: true})
	if rootsLegacy.Total != 4 || len(rootsLegacy.Sessions) != 4 {
		t.Fatalf("legacy roots = total %d rows %+v, want 4 (remote-kid + orphan-plain + orphan-needs + lead)", rootsLegacy.Total, rootsLegacy.Sessions)
	}
	legacyCounts, err := index.Counts(Options{Roots: true})
	if err != nil || legacyCounts.State["needs_you"] != 2 || legacyCounts.State["working"] != 1 {
		t.Fatalf("legacy counts = %+v (err %v), want needs_you=2 (remote-kid + orphan-needs, not lead) and working=1 (lead)", legacyCounts, err)
	}

	// parent_machine_id+parent_session_id and machine_id+parent_session_id+cross_machine_hierarchy=1
	// return both same-machine and cross-machine children folded under (mac, lead).
	for name, opts := range map[string]Options{
		"parent_machine_id":             {Limit: 20, UnderMachine: "mac", Under: "lead"},
		"machine_id+cross_machine_hier": {Limit: 20, Machine: "mac", Under: "lead", CrossMachineHierarchy: true},
	} {
		got := mustQuery(t, &index, opts)
		if got.Total != 2 || len(got.Sessions) != 2 {
			t.Fatalf("%s: got total=%d sessions=%+v, want 2 (remote-kid + local-kid)", name, got.Total, got.Sessions)
		}
	}
	// Legacy machine_id+parent_session_id without cross_machine_hierarchy returns same-machine children only.
	legacyKids := mustQuery(t, &index, Options{Limit: 20, Machine: "mac", Under: "lead"})
	if legacyKids.Total != 1 || legacyKids.Sessions[0].(testPayload).ID != "local-kid" {
		t.Fatalf("legacy machine_id+parent_session_id = %+v, want only local-kid", legacyKids)
	}

	// Combining parent_machine_id with machine_id narrows children to that child machine.
	boxOnly := mustQuery(t, &index, Options{Limit: 20, UnderMachine: "mac", Under: "lead", Machine: "box"})
	if boxOnly.Total != 1 || boxOnly.Sessions[0].(testPayload).ID != "remote-kid" {
		t.Fatalf("boxOnly = %+v", boxOnly)
	}

	// Querying an archived parent's session overview still returns its same-machine and cross-machine orphan subagents.
	archivedKids := mustQuery(t, &index, Options{Limit: 20, UnderMachine: "mac", Under: "archived-lead"})
	if archivedKids.Total != 2 || len(archivedKids.Sessions) != 2 {
		t.Fatalf("archived parent children = %+v, want 2", archivedKids)
	}
}
