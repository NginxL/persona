package assistant

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func memoryWireIDs(b memoryExtensionBatch) []string {
	out := make([]string, 0, len(b.Entries))
	for _, e := range b.Entries {
		out = append(out, e.ID)
	}
	return out
}

// The assembler takes entries oldest first and spends its budgets newest first, so memoryExtensionBatchOf puts the
// most important last: standing ones, then higher ranks, the extension's own order kept among entries alike.
func TestMemoryExtensionBatchOfOrder(t *testing.T) {
	page := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
		{ID: "qa-a", Text: "qa: plain first", Status: "active"},
		{ID: "qa-b", Text: "qa: ranked", Status: "active", Rank: 500},
		{ID: "qa-c", Text: "qa: standing low", Status: "active", Standing: true, Rank: 10},
		{ID: "qa-d", Text: "qa: plain second", Status: "active"},
		{ID: "qa-e", Text: "qa: standing high", Status: "active", Standing: true, Rank: 900},
	}}
	b := memoryExtensionBatchOf([]wire.MemoryEntriesResult{page})
	if want := []string{"qa-d", "qa-a", "qa-b", "qa-c", "qa-e"}; !slices.Equal(memoryWireIDs(b), want) {
		t.Errorf("order %v, want %v (oldest first: most important last)", memoryWireIDs(b), want)
	}
	if !b.Tagged {
		t.Error("a tagged page: not tagged")
	}
}

// Tagged is the promise of every page: one page without it, or no page at all, and nothing a missing field means is
// known.
func TestMemoryExtensionBatchOfTagged(t *testing.T) {
	one := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{{ID: "qa-1", Text: "qa: one", Status: "active"}}}
	two := wire.MemoryEntriesResult{Tagged: false, Entries: []wire.MemoryEntry{{ID: "qa-2", Text: "qa: two", Status: "active"}}}
	for _, c := range []struct {
		name  string
		pages []wire.MemoryEntriesResult
		want  bool
	}{
		{"none", nil, false},
		{"tagged", []wire.MemoryEntriesResult{one, one}, true},
		{"one untagged page", []wire.MemoryEntriesResult{one, two}, false},
	} {
		if got := memoryExtensionBatchOf(c.pages).Tagged; got != c.want {
			t.Errorf("%s: tagged %v, want %v", c.name, got, c.want)
		}
	}
}

// An entry is carried only when it is active and holds to the schema's rules; one that breaks a rule is dropped,
// never repaired, and an id seen before is dropped.
func TestMemoryExtensionBatchOfDrops(t *testing.T) {
	ok := wire.MemoryEntry{ID: "qa-ok", Text: "  qa: kept, trimmed  ", Status: "active", OriginSpace: "qa-finance",
		Categories: []string{"finance"}, Facet: "world"}
	bad := []wire.MemoryEntry{
		{ID: "qa-candidate", Text: "qa: not borne out", Status: "candidate"},
		{ID: "qa-dormant", Text: "qa: not seen lately", Status: "dormant"},
		{ID: "qa-retired", Text: "qa: no longer true", Status: "retired"},
		{ID: "qa-unknown-status", Text: "qa: a newer status", Status: "stable"},
		{ID: "", Text: "qa: no id", Status: "active"},
		{ID: "qa bad id", Text: "qa: a space in its id", Status: "active"},
		{ID: "qa-empty", Text: "   ", Status: "active"},
		{ID: "qa-long", Text: strings.Repeat("x", memoryEntryTextMost+1), Status: "active"},
		{ID: "qa-space", Text: "qa: an odd space", Status: "active", OriginSpace: "QA Finance"},
		{ID: "qa-cats", Text: "qa: too many categories", Status: "active", Categories: make([]string, memoryEntryCategoriesMost+1)},
		{ID: "qa-rank", Text: "qa: rank out of range", Status: "active", Rank: memoryEntryRankMost + 1},
		{ID: "qa-negative", Text: "qa: negative rank", Status: "active", Rank: -1},
		{ID: "qa-ok", Text: "qa: the same id again", Status: "active"},
	}
	b := memoryExtensionBatchOf([]wire.MemoryEntriesResult{{Tagged: true, Entries: append([]wire.MemoryEntry{ok}, bad...)}})
	if len(b.Entries) != 1 {
		t.Fatalf("entries %v, want only qa-ok", memoryWireIDs(b))
	}
	e := b.Entries[0]
	if e.ID != "qa-ok" || e.Text != "qa: kept, trimmed" || e.OriginSpace != "qa-finance" || !slices.Equal(e.Categories, []string{"finance"}) {
		t.Errorf("qa-ok = %+v", e)
	}
}

// Standing is carried on the entry, not only used for the order: the standing channel takes only standing entries,
// the rest come in by relevance.
func TestMemoryExtensionBatchOfStanding(t *testing.T) {
	page := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
		{ID: "qa-resident", Text: "qa: always in context", Status: "active", Standing: true},
		{ID: "qa-named", Text: "qa: when named", Status: "active", Subjects: []string{"qa-project"}},
	}}
	got := map[string]bool{}
	for _, e := range memoryExtensionBatchOf([]wire.MemoryEntriesResult{page}).Entries {
		got[e.ID] = e.Standing
	}
	if len(got) != 2 || !got["qa-resident"] || got["qa-named"] {
		t.Errorf("standing %v, want qa-resident true and qa-named false", got)
	}
}

// Categories go through as written (an unknown one makes the entry not known downstream); subjects become tags, on
// one line, the empty, the over-long and repeated ones dropped, at most 16.
func TestMemoryExtensionBatchOfFields(t *testing.T) {
	subjects := []string{" budget\n envelope ", "", "budget envelope", strings.Repeat("y", memoryEntrySubjectMost+1)}
	for i := range memoryEntrySubjectsMost + 2 {
		subjects = append(subjects, fmt.Sprintf("qa-subject-%d", i))
	}
	w := wire.MemoryEntry{ID: "qa-fields", Text: "qa: fields", Status: "active", Categories: []string{"qa-not-a-category"},
		Subjects: subjects}
	b := memoryExtensionBatchOf([]wire.MemoryEntriesResult{{Tagged: true, Entries: []wire.MemoryEntry{w}}})
	if len(b.Entries) != 1 {
		t.Fatalf("entries %v", memoryWireIDs(b))
	}
	e := b.Entries[0]
	if !slices.Equal(e.Categories, []string{"qa-not-a-category"}) {
		t.Errorf("categories %v, want them as written", e.Categories)
	}
	if len(e.Tags) != memoryEntrySubjectsMost || e.Tags[0] != "budget envelope" || e.Tags[1] != "qa-subject-0" {
		t.Errorf("tags %q", e.Tags)
	}
	// The mapped entry is the adapter's input: from a tagged batch an unknown category makes it not known.
	s, _ := memoryExtensionService(t, true)
	if u := s.memoryExtensionUses(t.Context(), true)(b.assistantEntries()[0]); u.Known {
		t.Errorf("an unknown category: %+v, want not known", u)
	}
	// Its subjects bear on a task naming them (entryRelevant reads tags).
	if !entryRelevant(b.assistantEntries()[0], "review the budget envelope this week") {
		t.Error("a task naming a subject: not relevant")
	}
}

// Past memoryExtensionMost entries over all pages the rest are dropped.
func TestMemoryExtensionBatchOfBounds(t *testing.T) {
	var pages []wire.MemoryEntriesResult
	for p := range memoryExtensionPages + 1 {
		page := wire.MemoryEntriesResult{Tagged: true, HasMore: true}
		for i := range memoryExtensionPageLimit {
			page.Entries = append(page.Entries, wire.MemoryEntry{ID: fmt.Sprintf("qa-%d-%d", p, i), Text: "qa: filler", Status: "active"})
		}
		pages = append(pages, page)
	}
	b := memoryExtensionBatchOf(pages)
	if len(b.Entries) != memoryExtensionMost {
		t.Errorf("%d entries, want %d", len(b.Entries), memoryExtensionMost)
	}
	if b.Entries[len(b.Entries)-1].ID != "qa-0-0" {
		t.Errorf("most important %s, want the first answered", b.Entries[len(b.Entries)-1].ID)
	}
}
