package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// memExtensionSource is the memory extension's entries in memory (memoryExtensionSource).
type memExtensionSource struct {
	batch memoryExtensionBatch
	asked []bool
}

func (m *memExtensionSource) MemoryExtensionEntries(_ context.Context, personal bool) (memoryExtensionBatch, error) {
	m.asked = append(m.asked, personal)
	return m.batch, nil
}

// Fake entries of the memory extension, oldest first: a finance entry said in qa-finance, one from qa-finance the
// extension did not tag, a general one from qa-plain, a general one from the main space, and one from a space the
// registry does not know. All are standing, so a standing channel may take them (memoryExtensionBatch.standingOnly).
var (
	extQAFinance = memoryExtensionEntry{ID: "ext-qa-finance", Text: "qa: quarterly budget envelope totals",
		OriginSpace: "qa-finance", Categories: []string{categoryFinance}, Standing: true}
	extQAFinanceUntagged = memoryExtensionEntry{ID: "ext-qa-finance-untagged", Text: "qa: invoices are filed by quarter",
		OriginSpace: "qa-finance", Standing: true}
	extQAPlain   = memoryExtensionEntry{ID: "ext-qa-plain", Text: "qa: notes are written in short lines", OriginSpace: "qa-plain", Standing: true}
	extQAMain    = memoryExtensionEntry{ID: "ext-qa-main", Text: "qa: the weekly report uses bullet points", Standing: true}
	extQAUnknown = memoryExtensionEntry{ID: "ext-qa-unknown", Text: "qa: a placeholder from nowhere", OriginSpace: "qa-unknown", Standing: true}
)

func extQAEntries() []memoryExtensionEntry {
	return []memoryExtensionEntry{extQAFinance, extQAFinanceUntagged, extQAPlain, extQAMain, extQAUnknown}
}

func extID(e memoryExtensionEntry) string { return memoryExtensionIDPrefix + e.ID }

// memoryExtensionService is memoryAssembleService with an empty ledger and the memory extension's fake entries.
func memoryExtensionService(t *testing.T, tagged bool) (*Service, *memExtensionSource) {
	t.Helper()
	s, _, _, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	src := &memExtensionSource{batch: memoryExtensionBatch{Entries: extQAEntries(), Tagged: tagged}}
	s.memoryExtension = src
	return s, src
}

// How the memory extension's entries are seen (memoryExtensionUses): from an extension that tags, the space each
// states and its categories, with the space's default categories added by the manager; a space or a category the
// manager does not know is not known. From one that does not tag, no entry is in any track or known.
func TestMemoryExtensionEntryUses(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, true)
	info := s.memoryExtensionUses(ctx, true)
	for _, c := range []struct {
		e     memoryExtensionEntry
		cats  string
		known bool
	}{
		{extQAFinance, "finance", true},
		{extQAFinanceUntagged, "finance", true},
		{extQAPlain, "", true},
		{extQAMain, "", true},
		{extQAUnknown, "", false},
		{memoryExtensionEntry{ID: "ext-qa-odd", Categories: []string{"qa-not-a-category"}}, "", false},
	} {
		u := info(memoryExtensionBatch{Entries: []memoryExtensionEntry{c.e}}.assistantEntries()[0])
		if strings.Join(u.Categories, ",") != c.cats || u.Known != c.known || u.SourceTrack != c.e.OriginSpace {
			t.Errorf("%s: %+v, want categories %q known %v track %q", c.e.ID, u, c.cats, c.known, c.e.OriginSpace)
		}
	}
	old := s.memoryExtensionUses(ctx, false)
	for _, e := range (memoryExtensionBatch{Entries: extQAEntries()}).assistantEntries() {
		if u := old(e); u.Known || u.SourceTrack != entryTrackUnknown || !u.sensitive() {
			t.Errorf("an older extension's %s = %+v, want no track, not known", e.ID, u)
		}
	}
}

// A standing channel takes the memory extension's entries as it takes the ledger's: in the main conversation the
// main space's and general ones from elsewhere, never a finance one; in qa-finance its own space's. The ledger's come
// after the extension's, so a budget goes to them first.
func TestMemoryExtensionStanding(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	a, err := s.standingMemory(ctx, "qa-main")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(a.Entries), extID(extQAPlain)+","+extID(extQAMain); got != want || len(a.Sensitive) != 0 {
		t.Errorf("main standing = %s (sensitive %v), want %s", got, a.Sensitive, want)
	}
	a, _ = s.standingMemory(ctx, "qa-finance-1")
	// Every one of the extension's entries is personal: the main space's general one goes too, as the ledger's personal
	// general entries do.
	if got, want := entryIDs(a.Entries), extID(extQAFinance)+","+extID(extQAFinanceUntagged)+","+extID(extQAPlain)+","+extID(extQAMain); got != want ||
		!slices.Equal(a.Sensitive, []string{extID(extQAFinance), extID(extQAFinanceUntagged)}) {
		t.Errorf("qa-finance standing = %s (sensitive %v), want %s", got, a.Sensitive, want)
	}
	if !slices.Equal(src.asked, []bool{true, true}) {
		t.Errorf("asked the source %v", src.asked)
	}
	// With the ledger: its entries come after the extension's.
	s.memoryEntries = &memEntrySource{entries: []proto.AssistantEntry{entryQAPlain, entryQAMain}}
	a, _ = s.standingMemory(ctx, "qa-main")
	if got, want := entryIDs(a.Entries), extID(extQAPlain)+","+extID(extQAMain)+",entry-qa-plain,entry-qa-main"; got != want {
		t.Errorf("main standing with the ledger = %s, want %s", got, want)
	}
}

// A recall in the main conversation returns the extension's finance entries only once the conversation carries
// finance; the one from an unknown space never.
func TestMemoryExtensionRecall(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, true)
	p := proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "assistant"}}
	recall := func() assembled {
		a, err := s.assembleMemory(ctx, s.assembleForConversation(ctx, "qa-main", assembleRecall), p, true)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if got, want := entryIDs(recall().Entries), extID(extQAPlain)+","+extID(extQAMain); got != want {
		t.Errorf("recall without finance = %s, want %s", got, want)
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	a := recall()
	if got := entryIDs(a.Entries); !strings.Contains(got, extID(extQAFinance)) || !strings.Contains(got, extID(extQAFinanceUntagged)) ||
		strings.Contains(got, extID(extQAUnknown)) {
		t.Errorf("recall with finance = %s", got)
	}
}

// A hand-off carries the extension's finance entries only after the owner's Start on a plan confirmed for finance, and
// none of the extension's entries to a computer other than the owner's; the Inbox's helper none sensitive.
func TestMemoryExtensionHandOffAndInbox(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	task := PlanTask{TodoID: "todo-qa-ext", Title: "qa: quarterly report", Prompt: "qa: prepare the quarterly budget envelope totals",
		OwnerStarted: true, MemoryTopic: []string{categoryFinance}}
	// Both finance entries are on the confirmed topic (the untagged one by its space's default category); the general
	// ones share too few words with the task.
	finance := []string{extID(extQAFinance), extID(extQAFinanceUntagged)}
	if a := s.handOffMemory(ctx, task, "qa-machine"); !slices.Equal(a.IDs(), finance) || !slices.Equal(a.Sensitive, finance) {
		t.Errorf("confirmed hand-off = %s (sensitive %v)", entryIDs(a.Entries), a.Sensitive)
	}
	unconfirmed := task
	unconfirmed.OwnerStarted = false
	if a := s.handOffMemory(ctx, unconfirmed, "qa-machine"); len(a.Entries) != 0 {
		t.Errorf("unconfirmed hand-off = %s", entryIDs(a.Entries))
	}
	src.asked = nil
	if a := s.handOffMemory(ctx, task, "qa-other"); len(a.Entries) != 0 || len(src.asked) != 0 {
		t.Errorf("hand-off to another computer = %s (source asked %v)", entryIDs(a.Entries), src.asked)
	}
	a, err := s.inboxMemory(ctx, "qa: can you send the quarterly budget envelope totals, and the weekly report bullet points")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(a.Entries); got != extID(extQAMain) || len(a.Sensitive) != 0 {
		t.Errorf("Inbox = %s (sensitive %v), want only the general entry the message bears on", got, a.Sensitive)
	}
}

// An older extension, which sets neither origin_space nor categories: where its entries were said cannot be told, so
// the strictest rule holds and no assembly carries them (its own exits are not filtered here, docs/assistant/
// health.md "Memory by scene").
func TestMemoryExtensionOlderCarriesNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, false)
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	p := proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "assistant"}}
	for _, sid := range []string{"qa-main", "qa-plain-1", "qa-finance-1"} {
		for _, r := range []assembleRole{assembleStanding, assembleRecall} {
			if a, _ := s.assembleMemory(ctx, s.assembleForConversation(ctx, sid, r), p, true); len(a.Entries) != 0 {
				t.Errorf("%s role %d carries %s", sid, r, entryIDs(a.Entries))
			}
		}
	}
	task := PlanTask{Title: "qa: quarterly report", Prompt: "qa: prepare the quarterly budget envelope totals, weekly report bullet points",
		OwnerStarted: true, MemoryTopic: []string{categoryFinance}}
	if a := s.handOffMemory(ctx, task, "qa-machine"); len(a.Entries) != 0 {
		t.Errorf("hand-off carries %s", entryIDs(a.Entries))
	}
	if a, _ := s.inboxMemory(ctx, task.Prompt); len(a.Entries) != 0 {
		t.Errorf("Inbox carries %s", entryIDs(a.Entries))
	}
}
