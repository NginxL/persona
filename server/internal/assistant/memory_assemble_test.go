package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// memEntrySource is a ledger in memory for the assembler (memoryEntrySource): the entries oldest first, filtered by
// the personal boundary, executor and machine as the manager database does, listed newest first and paged by index.
type memEntrySource struct {
	entries []proto.AssistantEntry
	listed  int
}

func (m *memEntrySource) NewestAssistantEntries(_ context.Context, p proto.AssistantEntriesParams, personal bool) (proto.AssistantEntriesResult, error) {
	m.listed++
	var match []proto.AssistantEntry
	for _, e := range m.entries {
		if e.Scope.Personal && !personal {
			continue
		}
		if f := p.Scope; f != nil {
			if f.Executor != "" && e.Scope.Executor != "any" && e.Scope.Executor != f.Executor {
				continue
			}
			if len(f.Machine) > 0 && len(e.Scope.Machine) > 0 && !slices.ContainsFunc(f.Machine, func(m string) bool { return slices.Contains(e.Scope.Machine, m) }) {
				continue
			}
		}
		match = append(match, e)
	}
	slices.Reverse(match)
	from, _ := strconv.Atoi(p.After)
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	out := proto.AssistantEntriesResult{Entries: []proto.AssistantEntry{}}
	if from < len(match) {
		to := min(from+limit, len(match))
		out.Entries = append(out.Entries, match[from:to]...)
		out.HasMore = to < len(match)
		out.After = strconv.Itoa(to)
	}
	return out, nil
}

// Fake entries, oldest first: a finance rule said in qa-finance (tagged finance, personal), a general personal entry
// from qa-plain (its conversation tagged with none), an entry from qa-plain whose conversation was never tagged, an
// entry from the health space, and a general entry of the main track any executor may use.
var (
	entryQAFinance = proto.AssistantEntry{ID: "entry-qa-finance", Type: "rule", Text: "qa: monthly budget totals, 记账按月汇总",
		OriginSpace: "qa-finance", Category: []string{categoryFinance}, Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-finance-1"},
		Scope: proto.AssistantEntryScope{Executor: "any", Personal: true}, Status: "active"}
	entryQAPlain = proto.AssistantEntry{ID: "entry-qa-plain", Type: "preference", Text: "qa: release notes go out on Fridays",
		OriginSpace: "qa-plain", Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-plain-1"},
		Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: true}, Status: "active"}
	entryQAUntagged = proto.AssistantEntry{ID: "entry-qa-untagged", Type: "fact", Text: "qa: a placeholder fact",
		OriginSpace: "qa-plain", Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-plain-2"},
		Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: true}, Status: "active"}
	entryQAHealth = proto.AssistantEntry{ID: "entry-qa-health", Type: "fact", Text: "qa: a placeholder step goal",
		OriginSpace: SpaceHealth, Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-health-1"},
		Scope: proto.AssistantEntryScope{Executor: "any", Personal: true}, Status: "active"}
	entryQAMain = proto.AssistantEntry{ID: "entry-qa-main", Type: "rule", Text: "qa: the monthly report uses the shared template",
		Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-main"}, Scope: proto.AssistantEntryScope{Executor: "any"}, Status: "active"}
)

// memoryAssembleService is infoUseService with the fake ledger, the owner's computer qa-machine, and the conversations
// qa-main and qa-plain-1 tagged with no category (qa-plain-2 never tagged).
func memoryAssembleService(t *testing.T) (*Service, *fakeHub, *infoCategoryMemStore, *memEntrySource) {
	t.Helper()
	ctx := context.Background()
	s, f, st := infoUseService(t)
	// The hello lists qa-machine as the assistant's: the effective settings keep a MachineID only when it is one of
	// hello.Assistant.Machines, so without it the owner's computer reads as "" and no start sees personal entries.
	f.mu.Lock()
	f.hello.Assistant.MachineID = "qa-machine"
	f.hello.Assistant.Machines = []string{"qa-machine"}
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.MachineID = "qa-machine" })
	if got := s.settings(ctx).MachineID; got != "qa-machine" {
		t.Fatalf("the owner's computer = %q, want qa-machine", got)
	}
	for _, sid := range []string{"qa-main", "qa-plain-1"} {
		if err := st.AddInfoCategories(ctx, db.InfoCategoryConversation, sid, []string{infoNoneTagged}, infoTaggedByModel, ""); err != nil {
			t.Fatal(err)
		}
	}
	src := &memEntrySource{entries: []proto.AssistantEntry{entryQAFinance, entryQAPlain, entryQAUntagged, entryQAHealth, entryQAMain}}
	s.memoryEntries = src
	return s, f, st, src
}

func entryIDs(entries []proto.AssistantEntry) string {
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return strings.Join(ids, ",")
}

// How an entry is seen (memoryEntryUses): its own categories and its track's; one with none takes its conversation's
// when that was tagged, and is not known otherwise.
func TestMemoryEntryUses(t *testing.T) {
	ctx := context.Background()
	s, _, _, src := memoryAssembleService(t)
	info := s.memoryEntryUses(ctx, src.entries)
	for _, c := range []struct {
		e     proto.AssistantEntry
		cats  string
		known bool
	}{
		{entryQAFinance, "finance", true},
		{entryQAPlain, "", true},
		{entryQAUntagged, "", false},
		{entryQAHealth, "health", false},
		{entryQAMain, "", true},
		{proto.AssistantEntry{OriginSpace: "qa-unknown", Category: []string{categoryFinance}}, "finance", false},
		{proto.AssistantEntry{Category: []string{"qa-not-a-category"}}, "", false},
	} {
		u := info(c.e)
		if strings.Join(u.Categories, ",") != c.cats || u.Known != c.known || u.SourceTrack != c.e.OriginSpace {
			t.Errorf("%s: %+v, want categories %q known %v", c.e.ID, u, c.cats, c.known)
		}
	}
	// With no model for info.tag nothing is tagged: an entry carries its track's default categories.
	s.noInfoTag = true
	info = s.memoryEntryUses(ctx, src.entries)
	if u := info(entryQAUntagged); !u.Known || len(u.Categories) != 0 {
		t.Errorf("untagged, no tagging: %+v, want general", u)
	}
	if u := info(entryQAHealth); !u.Known || strings.Join(u.Categories, ",") != "health" {
		t.Errorf("health, no tagging: %+v, want health", u)
	}
	// The categories a conversation was tagged with before still count with no model now: an entry said in a
	// conversation tagged finance stays finance, never general.
	if err := s.tagConversation(ctx, "qa-plain-2", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	info = s.memoryEntryUses(ctx, src.entries)
	if u := info(entryQAUntagged); strings.Join(u.Categories, ",") != "finance" || !u.sensitive() {
		t.Errorf("said in a conversation tagged finance, no tagging: %+v, want finance", u)
	}
}

// An entry's track is its conversation's: one that states none takes it, one that states another is not known, and
// one whose conversation cannot be told is in no track and not known, whether or not anything is tagged.
func TestMemoryEntryTrackIsItsConversations(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := memoryAssembleService(t)
	healthUnstated := proto.AssistantEntry{ID: "entry-qa-health-unstated", Category: []string{categoryHealth},
		Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-health-1"}}
	lost := proto.AssistantEntry{ID: "entry-qa-lost", Category: []string{categoryFinance},
		Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-gone"}}
	moved := proto.AssistantEntry{ID: "entry-qa-moved", OriginSpace: "qa-plain",
		Source: proto.AssistantEntrySource{Machine: "qa-machine", Session: "qa-finance-1"}}
	entries := []proto.AssistantEntry{healthUnstated, lost, moved}
	for _, tagging := range []bool{true, false} {
		s.noInfoTag = !tagging
		info := s.memoryEntryUses(ctx, entries)
		if u := info(healthUnstated); u.SourceTrack != SpaceHealth || !slices.Contains(u.Categories, categoryHealth) {
			t.Errorf("tagging %v: an entry stating no track from the health space = %+v", tagging, u)
		}
		if u := info(lost); u.Known || u.SourceTrack != entryTrackUnknown {
			t.Errorf("tagging %v: an entry whose conversation cannot be told = %+v, want no track, not known", tagging, u)
		}
		if u := info(moved); u.Known || u.SourceTrack != "qa-finance" {
			t.Errorf("tagging %v: an entry stating qa-plain from qa-finance = %+v, want qa-finance, not known", tagging, u)
		}
		// None of them reaches a recall in the main conversation.
		f := s.assembleForConversation(ctx, "qa-main", assembleRecall)
		if got := entryIDs(assembleEntries(entries, f, info).Entries); got != "" {
			t.Errorf("tagging %v: recall in main = %s", tagging, got)
		}
	}
}

// Acceptance (a): a sensitive entry from another track appears 0 times in the main conversation's standing channel,
// which carries the main track's entries and personal general ones from elsewhere; in its own track it is carried.
func TestMemoryStandingNeverCarriesSensitiveFromElsewhere(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := memoryAssembleService(t)
	a, err := s.standingMemory(ctx, "qa-main")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(a.Entries); got != "entry-qa-plain,entry-qa-main" {
		t.Errorf("main standing = %s, want the personal general entry and the main track's", got)
	}
	if strings.Contains(entryIDs(a.Entries), entryQAFinance.ID) || len(a.Sensitive) != 0 {
		t.Errorf("main standing carries sensitive entries: %+v", a)
	}
	// Even when the main conversation was tagged finance: a standing channel never carries elsewhere's sensitive ones.
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	a, _ = s.standingMemory(ctx, "qa-main")
	if strings.Contains(entryIDs(a.Entries), entryQAFinance.ID) {
		t.Errorf("main standing, tagged finance, carries %s", entryQAFinance.ID)
	}
	// In its own track (scene 1) it is carried; the main track's entry, finance by now and from elsewhere, is not.
	a, _ = s.standingMemory(ctx, "qa-finance-1")
	if got := entryIDs(a.Entries); got != "entry-qa-finance,entry-qa-plain" || !slices.Equal(a.Sensitive, []string{entryQAFinance.ID}) {
		t.Errorf("qa-finance standing = %s (sensitive %v)", got, a.Sensitive)
	}
}

// A standing channel keeps within its budget, newest first.
func TestMemoryStandingBudget(t *testing.T) {
	var entries []proto.AssistantEntry
	for i := range 4 {
		entries = append(entries, proto.AssistantEntry{ID: "entry-" + strconv.Itoa(i), Text: strings.Repeat("q", 600)})
	}
	a := assembleEntries(entries, assembleFor{Role: assembleStanding, SpaceKnown: true}, func(proto.AssistantEntry) infoUse { return infoUse{Known: true} })
	if got := entryIDs(a.Entries); got != "entry-2,entry-3" {
		t.Errorf("standing within %d bytes = %s, want the newest two", memoryStandingSpaceMax, got)
	}
}

// Acceptance (b): when the conversation's categories carry finance, a recall returns the finance entry; otherwise not.
// An entry whose categories are not known is never recalled elsewhere.
func TestMemoryRecallFollowsTheConversationsCategories(t *testing.T) {
	ctx := context.Background()
	s, _, _, src := memoryAssembleService(t)
	if got := entryIDs(s.recallMemory(ctx, "qa-main", src.entries).Entries); got != "entry-qa-plain,entry-qa-main" {
		t.Errorf("recall without finance = %s", got)
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	a := s.recallMemory(ctx, "qa-main", src.entries)
	// The main track's entry takes its conversation's categories (finance now): it is in its own track, so it stays.
	if got := entryIDs(a.Entries); got != "entry-qa-finance,entry-qa-plain,entry-qa-main" || !slices.Equal(a.Sensitive, []string{entryQAFinance.ID, entryQAMain.ID}) {
		t.Errorf("recall with finance = %s (sensitive %v)", got, a.Sensitive)
	}
	// While a turn of the conversation waits to be tagged its categories are not known: nothing sensitive is recalled.
	s.infoTagWaiting.set("qa-main", true)
	if got := entryIDs(s.recallMemory(ctx, "qa-main", src.entries).Entries); strings.Contains(got, entryQAFinance.ID) {
		t.Errorf("recall while a turn waits = %s", got)
	}
	s.infoTagWaiting.set("qa-main", false)
	// A general conversation of another track recalls its own track's entries, untagged ones included, and not the
	// main track's entry, finance by now through its conversation.
	if got := entryIDs(s.recallMemory(ctx, "qa-plain-1", src.entries).Entries); got != "entry-qa-plain,entry-qa-untagged" {
		t.Errorf("recall in qa-plain = %s", got)
	}
}

// Acceptance (c): a dispatched task's first message carries sensitive entries only after the owner's Start on a plan
// whose words were tagged with every one of their categories (PlanTask.MemoryTopic), and the to-do's brief lists them;
// general entries go when the task's words bear on them. No hand-off asks a model.
func TestMemoryHandOffCarriesOnlyWhatTheTaskMatches(t *testing.T) {
	ctx := context.Background()
	s, f, _, _ := memoryAssembleService(t)
	if _, err := s.putTaskBrief(ctx, briefRecord{Goal: "qa: a placeholder goal"}, "todo-qa-1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.infoTag = `{"categories":["finance"]}`
	asked := len(f.infoTagAsked)
	f.mu.Unlock()
	task := PlanTask{TodoID: "todo-qa-1", Title: "qa: monthly report", Prompt: "qa: prepare the monthly budget totals report",
		OwnerStarted: true, MemoryTopic: []string{categoryFinance}}
	text, carried := s.handOffMemoryContext(ctx, task, "qa-machine")
	if !strings.Contains(text, entryQAFinance.Text) || !strings.Contains(text, entryQAMain.Text) {
		t.Errorf("hand-off confirmed for finance lacks the finance entry or the relevant general one:\n%s", text)
	}
	for _, e := range []proto.AssistantEntry{entryQAHealth, entryQAPlain, entryQAUntagged} {
		if strings.Contains(text, e.Text) {
			t.Errorf("hand-off carries %s:\n%s", e.ID, text)
		}
	}
	// Assembling notes nothing: the start notes what it carried once it went through (startTaskAgent).
	if n := memoryEvents(s, "todo-qa-1"); n != 0 {
		t.Errorf("%d memory events before the start went through", n)
	}
	s.noteHandOffMemory(ctx, "todo-qa-1", carried)
	ids, sensitive := handOffMemoryOf(s, "todo-qa-1")
	if !slices.Equal(ids, []string{entryQAFinance.ID, entryQAMain.ID}) || !slices.Equal(sensitive, []string{entryQAFinance.ID}) {
		t.Errorf("the brief lists %v (sensitive %v)", ids, sensitive)
	}
	// A start tried again with the same entries notes nothing more.
	s.noteHandOffMemory(ctx, "todo-qa-1", carried)
	if n := memoryEvents(s, "todo-qa-1"); n != 1 {
		t.Errorf("%d memory events after a retry, want 1", n)
	}

	// No start that passed no confirmation carries a sensitive entry, whatever its text says: a plan the hub started
	// without asking, a plan whose words were general, and a leader's worker.
	unconfirmed := task
	unconfirmed.OwnerStarted = false
	general := task
	general.MemoryTopic = nil
	worker := task
	worker.Parent = "qa-leader-task"
	for name, tk := range map[string]PlanTask{"unconfirmed": unconfirmed, "general plan": general, "leader worker": worker} {
		if a := s.handOffMemory(ctx, tk, "qa-machine"); entryIDs(a.Entries) != entryQAMain.ID || len(a.Sensitive) != 0 {
			t.Errorf("%s hand-off = %s (sensitive %v), want only the general entry", name, entryIDs(a.Entries), a.Sensitive)
		}
	}
	// A plan confirmed for health carries no finance entry, and no unknown health one either.
	health := task
	health.MemoryTopic = []string{categoryHealth}
	if a := s.handOffMemory(ctx, health, "qa-machine"); entryIDs(a.Entries) != entryQAMain.ID || len(a.Sensitive) != 0 {
		t.Errorf("hand-off confirmed for health = %s (sensitive %v)", entryIDs(a.Entries), a.Sensitive)
	}
	// Another computer gets no personal entry; a task whose words bear on nothing (or only on common words) gets nothing.
	if got := entryIDs(s.handOffMemory(ctx, task, "qa-other").Entries); got != entryQAMain.ID {
		t.Errorf("hand-off to another computer = %s", got)
	}
	s.memoryEntries = &memEntrySource{entries: []proto.AssistantEntry{entryQAMain}}
	if a := s.handOffMemory(ctx, PlanTask{Prompt: "qa: something unrelated to the shared things"}, "qa-machine"); len(a.Entries) != 0 {
		t.Errorf("unrelated hand-off = %+v", a)
	}
	f.mu.Lock()
	n := len(f.infoTagAsked) - asked
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("hand-offs asked a model %d times", n)
	}
}

// On a ledger larger than an assembly reads, the newest entries are the ones read, oldest first.
func TestMemoryAssemblyReadsTheNewest(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := memoryAssembleService(t)
	var entries []proto.AssistantEntry
	for i := range memoryAssemblePages*200 + 5 {
		entries = append(entries, proto.AssistantEntry{ID: "entry-" + strconv.Itoa(i), Text: "qa: placeholder",
			Scope: proto.AssistantEntryScope{Executor: "any"}, Status: "active"})
	}
	s.memoryEntries = &memEntrySource{entries: entries}
	got, err := s.memoryEntriesFor(ctx, proto.AssistantEntriesParams{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != memoryAssemblePages*200 || got[0].ID != "entry-5" || got[len(got)-1].ID != entries[len(entries)-1].ID {
		t.Fatalf("read %d entries; want the newest %d, oldest first", len(got), memoryAssemblePages*200)
	}
}

// Common words make no text bear on an entry.
func TestMemoryRelevanceSkipsCommonWords(t *testing.T) {
	e := proto.AssistantEntry{Text: "qa: the release notes go out on Fridays"}
	if entryRelevant(e, "qa: the plan for the week, notes and all") {
		t.Error("one shared word and common ones made the entry relevant")
	}
	if !entryRelevant(e, "qa: draft the release notes") {
		t.Error("two shared words did not make the entry relevant")
	}
}

// Acceptance (d): the Inbox's helper context carries 0 sensitive entries, even ones the message is about.
func TestMemoryInboxCarriesNoSensitiveEntry(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := memoryAssembleService(t)
	a, err := s.inboxMemory(ctx, "qa: can you send the monthly budget totals report, 记账按月汇总")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Sensitive) != 0 || strings.Contains(entryIDs(a.Entries), entryQAFinance.ID) {
		t.Errorf("Inbox carries sensitive entries: %+v", a)
	}
	if got := entryIDs(a.Entries); got != entryQAMain.ID {
		t.Errorf("Inbox = %s, want the general entry the message bears on", got)
	}
}

// decisions_query is a recall: the finance decision from qa-finance reaches the main conversation only once it
// carries finance (scene 2), through the tool itself.
func TestMemoryDecisionsQueryIsARecall(t *testing.T) {
	ctx := context.Background()
	s, _, st, _ := memoryAssembleService(t)
	finance, main := entryQAFinance, entryQAMain
	finance.Type, main.Type = "decision", "decision"
	ledger := &entriesMemStore{infoCategoryMemStore: st, entries: []proto.AssistantEntry{finance, main}}
	s.store = func() Store { return ledger }
	query := func() string {
		t.Helper()
		raw, err := s.toolEntries(ctx, "qa-machine", "qa-main", "decisions_query", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var out proto.AssistantEntriesResult
		if err := json.Unmarshal([]byte(raw), &out); err != nil || out.Entries == nil {
			t.Fatalf("decisions_query = %s (%v)", raw, err)
		}
		return entryIDs(out.Entries)
	}
	if got := query(); got != entryQAMain.ID {
		t.Errorf("decisions in main = %s", got)
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := query(); got != entryQAFinance.ID+","+entryQAMain.ID {
		t.Errorf("decisions in main tagged finance = %s", got)
	}
	if got := s.recallMemory(ctx, "qa-main", nil).Entries; got != nil {
		t.Errorf("no entries = %v", got)
	}
}

// entriesMemStore is the fake ledger as the manager database's entries (entriesStore) on the info categories' store,
// for the tools that read it.
type entriesMemStore struct {
	*infoCategoryMemStore
	entries []proto.AssistantEntry
}

func (m *entriesMemStore) ListAssistantEntries(_ context.Context, p proto.AssistantEntriesParams, personal bool) (proto.AssistantEntriesResult, error) {
	out := proto.AssistantEntriesResult{Entries: []proto.AssistantEntry{}}
	for _, e := range m.entries {
		if (p.Type == "" || e.Type == p.Type) && (personal || !e.Scope.Personal) {
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}

func (m *entriesMemStore) AppendAssistantEntry(_ context.Context, e proto.AssistantEntry) (proto.AssistantEntry, error) {
	m.entries = append(m.entries, e)
	return e, nil
}

func (m *entriesMemStore) AssistantEntry(_ context.Context, id string) (proto.AssistantEntry, error) {
	for _, e := range m.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return proto.AssistantEntry{}, db.ErrEntryNotFound
}

// The user's own extension (trust owner, such as their memory extension) reads every track's memory, qa-finance's
// included; a third party's does not without a finance grant (scene 6).
func TestMemoryOwnerExtensionReadsEveryTrack(t *testing.T) {
	ctx := context.Background()
	st := &trustMemStore{infoCategoryMemStore: &infoCategoryMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-finance", Kind: trackKindSpace, Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}},
	}}}}
	s := newServiceOn(t, newFakeHub(), st)
	if err := st.SetExtensionTrust(ctx, "qa-memory", assistantwire.ExtensionTrustOwner, nil, "user"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.memorySpaces(ctx), "qa-finance") {
		t.Fatalf("memory spaces %v lack qa-finance", s.memorySpaces(ctx))
	}
	if s.extensionHides(s.withExtensionReader(ctx, "qa-memory"), "qa-finance") {
		t.Error("the owner's extension does not read qa-finance's memory")
	}
	if !s.extensionHides(s.withExtensionReader(ctx, "qa-third"), "qa-finance") {
		t.Error("a third party's extension reads qa-finance's memory")
	}
}

// The memory block a hand-off carries, in each language (#13532).
func TestGoldenMemoryHandOff(t *testing.T) {
	ctx := context.Background()
	a := assembled{Entries: []proto.AssistantEntry{{ID: "entry-1", Text: "qa: the release notes go out on Fridays"}, {ID: "entry-2", Text: "qa: 记账按月汇总"}}}
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		f := newFakeHub()
		f.hello.Locale = locale
		s := newTestService(t, f)
		golden(t, "memory_handoff."+locale, s.renderMemoryEntries(ctx, a))
	}
	if got := newTestService(t, newFakeHub()).renderMemoryEntries(ctx, assembled{}); got != "" {
		t.Errorf("no entries = %q", got)
	}
}

// handOffMemoryOf is the entries the latest memory event of to-do todoID's brief lists, and the sensitive ones.
func handOffMemoryOf(s *Service, todoID string) (ids, sensitive []string) {
	brief, ok := s.briefOfTodo(todoID)
	if !ok {
		return nil, nil
	}
	for i := len(brief.Log) - 1; i >= 0; i-- {
		if e := brief.Log[i]; e.Kind == briefEventMemory && e.Todo == todoID {
			return e.Entries, e.SensitiveEntries
		}
	}
	return nil, nil
}

func memoryEvents(s *Service, todoID string) int {
	brief, _ := s.briefOfTodo(todoID)
	n := 0
	for _, e := range brief.Log {
		if e.Kind == briefEventMemory && e.Todo == todoID {
			n++
		}
	}
	return n
}
