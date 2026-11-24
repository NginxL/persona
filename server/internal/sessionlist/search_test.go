package sessionlist

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func searchItem(id, machine, title string, at time.Time) Item {
	return Item{ID: id, Machine: machine, State: "idle", UpdatedAt: at, Full: SearchDocument{Title: title, State: "idle"}, Summary: map[string]string{"id": id}}
}
func searchContext() *SearchContext {
	return &SearchContext{Document: func(i Item) (SearchDocument, error) { return i.Full.(SearchDocument), nil }, MachineNames: map[string]string{"computer": "Studio"}, Pinned: map[string]bool{}, Errors: map[string]bool{}, Now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
}
func TestRuntimeSearchFrozenRankedCursor(t *testing.T) {
	ctx := searchContext()
	at := ctx.Now
	items := []Item{searchItem("old-title", "computer", "Needle", at.Add(-time.Hour)), searchItem("recent-inside", "computer", "Other", at), searchItem("new-title", "computer", "Needle later", at), searchItem("assistant", "computer", "Needle main", at)}
	inside := items[1].Full.(SearchDocument)
	inside.AgentText = strings.Repeat("中😀", 300) + "NEEDLE" + strings.Repeat("😀界", 300)
	items[1].Full = inside
	main := items[3].Full.(SearchDocument)
	main.Assistant = true
	items[3].Full = main
	var index Index
	index.Replace("computer", items)
	opts := Options{Search: "needle", SearchContext: ctx, Summary: true, Limit: 1}
	page, err := index.Query(opts)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.SearchHits) != 1 || page.SearchHits["new-title"].Score != 105 {
		t.Fatalf("wrong global rank: %+v", page)
	}
	revision := page.Revision
	cursor := page.NextCursor
	// Changes to rows, pins, names, errors and wall clock cannot rewrite a frozen page.
	index.Replace("computer", []Item{searchItem("replacement", "computer", "Needle replacement", at.Add(time.Hour))})
	ctx.Pinned["old-title"] = false
	ctx.MachineNames["computer"] = "Renamed"
	ctx.Now = ctx.Now.Add(72 * time.Hour)
	opts.Cursor, opts.SearchContext = cursor, nil
	second, err := index.Query(opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != revision || second.Total != 3 || second.SearchHits["old-title"].Score != 105 {
		t.Fatalf("snapshot changed: %+v", second)
	}
	opts.Cursor = second.NextCursor
	last, err := index.Query(opts)
	if err != nil {
		t.Fatal(err)
	}
	hit := last.SearchHits["recent-inside"]
	if last.HasMore || len(last.SearchHits) != 1 || hit.Score != 45 || hit.Evidence == nil || hit.Evidence.Match != "NEEDLE" || len([]rune(hit.Evidence.Before)) > 64 || len([]rune(hit.Evidence.After)) > 160 || !utf8.ValidString(hit.Evidence.Before+hit.Evidence.Match+hit.Evidence.After) {
		t.Fatalf("bad evidence/end: %+v", last)
	}
	// Returned evidence belongs to the caller, not the frozen result retained in the index.
	hit.Evidence.Match = "mutated"
	again, err := index.Query(opts)
	if err != nil || again.SearchHits["recent-inside"].Evidence.Match != "NEEDLE" {
		t.Fatalf("aliased evidence: %+v %v", again, err)
	}
	opts.Search = "different"
	if _, err := index.Query(opts); err == nil {
		t.Fatal("cursor reused across query")
	}
}
func TestRuntimeSearchAuthorizationBeforeProjection(t *testing.T) {
	ctx := searchContext()
	var decoded []string
	ctx.Document = func(item Item) (SearchDocument, error) {
		decoded = append(decoded, item.ID)
		return item.Full.(SearchDocument), nil
	}
	a := searchItem("allowed", "one", "Needle", ctx.Now)
	secret := searchItem("private", "one", "Needle private", ctx.Now)
	secret.Private = true
	foreign := searchItem("foreign", "two", "Needle foreign", ctx.Now)
	child := searchItem("child", "one", "Needle child", ctx.Now)
	child.Fold = &Fold{Root: "allowed"}
	var index Index
	index.ReplaceOwned("one", "owner-a", []Item{a, secret, child})
	index.ReplaceOwned("two", "owner-b", []Item{foreign})
	opts := Options{Search: "needle", SearchContext: ctx, NoPrivate: true, Manager: "owner-a", AllowedMachines: []string{"one"}, Roots: true, Limit: 1}
	page, err := index.Query(opts)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || !reflect.DeepEqual(decoded, []string{"allowed"}) || len(page.SearchHits) != 1 {
		t.Fatalf("unauthorized metadata inspected or counted: page=%+v decoded=%v", page, decoded)
	}
	opts.AllowedMachines = []string{}
	page, err = index.Query(opts)
	if err != nil || page.Total != 0 || len(page.SearchHits) != 0 {
		t.Fatalf("empty authorization set widened: %+v %v", page, err)
	}
}
func TestRuntimeSearchPinsClockAndScopeBinding(t *testing.T) {
	ctx := searchContext()
	var index Index
	index.ReplaceOwned("computer", "owner", []Item{searchItem("a", "computer", "A", ctx.Now), searchItem("b", "computer", "B", ctx.Now)})
	ctx.Pinned["a"], ctx.Pinned["b"] = true, true
	opts := Options{Search: "is:pinned since:24h machine:studio", SearchContext: ctx, AllowedMachines: []string{"computer"}, Manager: "owner", Limit: 1}
	first, err := index.Query(opts)
	if err != nil || first.Total != 2 || !first.HasMore {
		t.Fatalf("first: %+v %v", first, err)
	}
	ctx.Pinned = map[string]bool{}
	ctx.Now = ctx.Now.Add(48 * time.Hour)
	ctx.MachineNames["computer"] = "Other"
	opts.Cursor = first.NextCursor
	second, err := index.Query(opts)
	if err != nil || second.Total != 2 || second.Revision != first.Revision {
		t.Fatalf("context refiltered frozen result: %+v %v", second, err)
	}
	opts.AllowedMachines = []string{}
	if _, err := index.Query(opts); err == nil {
		t.Fatal("cursor crossed changed authorization")
	}
	opts.AllowedMachines = []string{"computer"}
	opts.Manager = "other"
	if _, err := index.Query(opts); err == nil {
		t.Fatal("cursor crossed manager")
	}
	opts.Manager = "owner"
	opts.NoPrivate = true
	if _, err := index.Query(opts); err == nil {
		t.Fatal("cursor crossed private visibility")
	}
	opts.NoPrivate = false
	opts.Cursor = ""
	page, err := index.Query(opts)
	if err != nil || page.Total != 0 {
		t.Fatalf("fresh query ignored latest context: %+v %v", page, err)
	}
}
func TestRuntimeSearchStrictParsingAndLegacyQ(t *testing.T) {
	opts, err := Parse(url.Values{"search": {"needle"}})
	if err != nil || opts.Limit != 40 || !Enabled(url.Values{"search": {"needle"}}) {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	for _, values := range []url.Values{{"search": {""}}, {"search": {" "}}, {"search": {"a", "b"}}, {"search": {"x"}, "q": {""}}, {"search": {"x"}, "since": {""}}, {"search": {"x"}, "counts": {"0"}}, {"search": {"x"}, "with_counts": {"0"}}, {"search": {"x"}, "catalog": {"0"}}, {"search": {"x"}, "session_id": {"a"}}, {"search": {"x"}, "limit": {"101"}}, {"search": {strings.Repeat("界", 342)}}} {
		if _, err := Parse(values); err == nil {
			t.Fatalf("accepted %+v", values)
		}
	}
	if _, err := Parse(url.Values{"search": {strings.Repeat("x", 1024)}, "limit": {"100"}}); err != nil {
		t.Fatal(err)
	}
	if !SearchNeedsPins("is:PIN") || SearchNeedsPins("pinned notes") || SearchNeedsPins("unknown:is:pinned") {
		t.Fatal("pins context detector diverges from grammar")
	}
	ctx := searchContext()
	item := searchItem("a", "computer", "first", ctx.Now)
	d := item.Full.(SearchDocument)
	d.AgentText = "second"
	item.Full = d
	item.SearchText = "first\nsecond"
	var index Index
	index.Replace("computer", []Item{item})
	legacy, _ := index.Query(Options{Q: "first second"})
	search, _ := index.Query(Options{Search: "first second", SearchContext: ctx})
	if legacy.Total != 0 || search.Total != 1 {
		t.Fatalf("q changed or terms not AND: %d %d", legacy.Total, search.Total)
	}
	empty, _ := index.Query(Options{Search: "nothing", SearchContext: ctx})
	if empty.SearchHits == nil {
		t.Fatal("empty query result omitted authoritative hits")
	}
	if _, err := index.Counts(Options{Search: "first", SearchContext: ctx}); err == nil {
		t.Fatal("search counts accepted")
	}
}
func TestRuntimeSearchRecoversAfterProjectionError(t *testing.T) {
	ctx := searchContext()
	var index Index
	index.Replace("computer", []Item{searchItem("a", "computer", "Needle", ctx.Now)})
	ctx.Document = func(Item) (SearchDocument, error) { return SearchDocument{}, fmt.Errorf("bad row") }
	if _, err := index.Query(Options{Search: "needle", SearchContext: ctx}); err == nil {
		t.Fatal("projection error swallowed")
	}
	ctx.Document = searchContext().Document
	page, err := index.Query(Options{Search: "needle", SearchContext: ctx})
	if err != nil || page.Total != 1 {
		t.Fatalf("failed projection poisoned cache: %+v %v", page, err)
	}
	raw, _ := json.Marshal(page.SearchHits)
	if !strings.Contains(string(raw), `"score":105`) {
		t.Fatal(string(raw))
	}
}

func TestRuntimeSearchCursorExpiryAndProjectionInvalidation(t *testing.T) {
	ctx := searchContext()
	now := ctx.Now
	index := Index{now: func() time.Time { return now }}
	index.Replace("computer", []Item{searchItem("a", "computer", "Needle A", now), searchItem("b", "computer", "Needle B", now)})
	options := Options{Search: "needle", SearchContext: ctx, Limit: 1}
	first, err := index.Query(options)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(snapshotTTL + time.Second)
	options.Cursor = first.NextCursor
	_, err = index.Query(options)
	requireCode(t, err, "cursor_expired")
	options.Cursor = ""
	// A changed record gets a fresh projection, even when its clock is unchanged.
	index.Replace("computer", []Item{searchItem("a", "computer", "Changed A", ctx.Now), searchItem("b", "computer", "Needle B", ctx.Now)})
	page, err := index.Query(options)
	if err != nil || page.Total != 1 || page.SearchHits["b"].Score != 105 {
		t.Fatalf("stale cached search metadata: %+v %v", page, err)
	}
}

// Evidence is built only for the hits a page returns (rankedHit): a paged search returns the same rows, order, hits,
// evidence and totals as one page holding them all, on the first page and on its continuations.
func TestRuntimeSearchPagesKeepHitsAndEvidence(t *testing.T) {
	ctx := searchContext()
	at := ctx.Now
	text := func(id, title, agentText, userText string, age time.Duration) Item {
		item := searchItem(id, "computer", title, at.Add(-age))
		d := item.Full.(SearchDocument)
		d.AgentText, d.UserText = agentText, userText
		item.Full = d
		return item
	}
	items := []Item{
		text("prefix", "Needle first", "", "", 0),
		text("inside", "The needle inside", "", "", time.Minute),
		text("answer-a", "Other", "the agent found a NEEDLE in the logs", "", 2*time.Minute),
		text("answer-b", "Other", "", "find the needle, please", 3*time.Minute),
		text("answer-c", "Other", strings.Repeat("界", 100)+"needle"+strings.Repeat("x", 200), "", 4*time.Minute),
		text("tied-1", "Other", "needle", "", 5*time.Minute),
		text("tied-2", "Other", "needle", "", 5*time.Minute),
		text("miss", "Other", "nothing here", "", 0),
		// A side session folded under "row": the row's hit is the side's, with the side's text as its evidence.
		text("row", "Row", "", "", 6*time.Minute),
		text("side", "Side", "the side's NEEDLE", "", 7*time.Minute),
	}
	offline := text("offline", "Other", "needle offline", "", 0)
	offline.State = "offline"
	items = append(items, offline)
	items[9].Fold = &Fold{Root: "row-session", SearchAs: "row", SearchSession: "side-session"}
	var index Index
	index.Replace("computer", items)
	base := Options{Search: "needle", SearchContext: ctx, Roots: true, Summary: true}

	whole := base
	whole.Limit = 100
	all, err := index.Query(whole)
	if err != nil || all.HasMore {
		t.Fatalf("whole: %+v %v", all, err)
	}
	ids := func(p Page) []string {
		out := make([]string, len(p.Sessions))
		for n, s := range p.Sessions {
			out[n] = s.(map[string]string)["id"]
		}
		return out
	}
	want := []string{"prefix", "inside", "answer-a", "answer-b", "answer-c", "tied-1", "tied-2", "row", "offline"}
	if got := ids(all); !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	evidence := map[string]SearchEvidence{
		"answer-a": {Role: "agent", Before: "the agent found a ", Match: "NEEDLE", After: " in the logs"},
		"answer-b": {Role: "user", Before: "find the ", Match: "needle", After: ", please"},
		"answer-c": {Role: "agent", Before: strings.Repeat("界", 64), Match: "needle", After: strings.Repeat("x", 160)},
		"tied-1":   {Role: "agent", Match: "needle"},
		"tied-2":   {Role: "agent", Match: "needle"},
		"row":      {Role: "agent", Before: "the side's ", Match: "NEEDLE"},
		"offline":  {Role: "agent", Match: "needle", After: " offline"},
	}
	for _, id := range want {
		hit := all.SearchHits[id]
		if e, ok := evidence[id]; ok != (hit.Evidence != nil) || ok && *hit.Evidence != e {
			t.Fatalf("%s evidence %+v, want %+v", id, hit.Evidence, e)
		}
	}
	if hit := all.SearchHits["row"]; hit.HitSessionID != "side-session" || hit.Score != 45 {
		t.Fatalf("folded side hit: %+v", hit)
	}
	if all.SearchHits["prefix"].Score != 105 || all.SearchHits["inside"].Score != 85 || all.SearchHits["offline"].Score != 40 {
		t.Fatalf("scores: %+v", all.SearchHits)
	}

	paged := base
	paged.Limit = 4
	var got []string
	hits := map[string]SearchHit{}
	for pages := 1; ; pages++ {
		page, err := index.Query(paged)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != all.Total || page.Revision != all.Revision || len(page.SearchHits) != len(page.Sessions) {
			t.Fatalf("page %d: %+v", pages, page)
		}
		got = append(got, ids(page)...)
		for id, hit := range page.SearchHits {
			hits[id] = hit
		}
		if !page.HasMore {
			break
		}
		paged.Cursor, paged.SearchContext = page.NextCursor, nil
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(hits, all.SearchHits) {
		t.Fatalf("pages differ from one page:\n%v\n%+v\nwant\n%v\n%+v", got, hits, want, all.SearchHits)
	}
}

// A search decodes its documents without the index's lock: a session push (Replace) and a list read made while it
// decodes do not wait for it, and the search then sees the push. Cold is the first search; changed is a search after
// one row changed, which decodes only that row.
func TestRuntimeSearchDecodesOutsideTheLock(t *testing.T) {
	for _, cold := range []bool{true, false} {
		t.Run(map[bool]string{true: "cold", false: "changed"}[cold], func(t *testing.T) {
			ctx := searchContext()
			var index Index
			items := []Item{searchItem("a", "computer", "Needle A", ctx.Now), searchItem("b", "computer", "Needle B", ctx.Now)}
			index.Replace("computer", items)
			if !cold {
				if _, err := index.Query(Options{Search: "needle", SearchContext: ctx}); err != nil {
					t.Fatal(err)
				}
				items[1] = searchItem("b", "computer", "Needle B changed", ctx.Now)
				index.Replace("computer", items)
			}
			decoding, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var decoded atomic.Int32
			document := ctx.Document
			ctx.Document = func(item Item) (SearchDocument, error) {
				decoded.Add(1)
				once.Do(func() {
					close(decoding)
					<-release
				})
				return document(item)
			}
			type answer struct {
				page Page
				err  error
			}
			searched := make(chan answer, 1)
			go func() {
				page, err := index.Query(Options{Search: "needle", SearchContext: ctx})
				searched <- answer{page, err}
			}()
			<-decoding
			waited := make(chan Page, 1)
			go func() {
				index.Replace("other", []Item{searchItem("c", "computer", "Needle C", ctx.Now)})
				list, _ := index.Query(Options{})
				waited <- list
			}()
			var list Page
			select {
			case list = <-waited:
			case <-time.After(time.Minute): // a failure bound, not a wait: the push lands at once unless it waits for the search
				close(release)
				t.Fatal("a session push waited for a search's decoding")
			}
			close(release)
			search := <-searched
			if search.err != nil || list.Total != 3 || search.page.Total != 3 {
				t.Fatalf("list %+v, search %+v", list, search)
			}
			// Each document is decoded once: outside the lock, and the pushed row on the search's second pass.
			if want := map[bool]int32{true: 3, false: 2}[cold]; decoded.Load() != want {
				t.Fatalf("decoded %d documents, want %d", decoded.Load(), want)
			}
		})
	}
}

func TestRuntimeSearchAgentsOnlyExcludesFoldedPrivateAndAssistantSideSessions(t *testing.T) {
	ctx := searchContext()
	var decoded []string
	ctx.Document = func(item Item) (SearchDocument, error) {
		decoded = append(decoded, item.ID)
		return item.Full.(SearchDocument), nil
	}
	parent := searchItem("row", "computer", "Parent row", ctx.Now)

	privateSide := searchItem("private-side", "computer", "Private side", ctx.Now)
	pd := privateSide.Full.(SearchDocument)
	pd.AgentText = "secret needle in private side"
	privateSide.Full = pd
	privateSide.Private = true
	privateSide.Fold = &Fold{Root: "row-session", SearchAs: "row", SearchSession: "private-side-session"}

	assistantSide := searchItem("assistant-side", "computer", "Assistant side", ctx.Now)
	ad := assistantSide.Full.(SearchDocument)
	ad.AgentText = "assistant needle in side"
	assistantSide.Full = ad
	assistantSide.Assistant = true
	assistantSide.Fold = &Fold{Root: "row-session", SearchAs: "row", SearchSession: "assistant-side-session"}

	var index Index
	index.Replace("computer", []Item{parent, privateSide, assistantSide})
	page, err := index.Query(Options{Search: "needle", SearchContext: ctx, AgentsOnly: true, Roots: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Sessions) != 0 || len(page.SearchHits) != 0 {
		t.Fatalf("AgentsOnly leaked folded side session hit: %+v", page)
	}
	for _, id := range decoded {
		if id == "private-side" || id == "assistant-side" {
			t.Fatalf("AgentsOnly decoded excluded folded side session %q (decoded=%v)", id, decoded)
		}
	}
}
