package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/proto"
	"reflect"
	"testing"
	"time"
)

func ledgerFixture(id string) proto.AssistantEntry {
	return proto.AssistantEntry{ID: id, Type: "rule", Text: "Use the configured review workflow.", Quote: "Use the configured review workflow.",
		Source: proto.AssistantEntrySource{Machine: "computer-a", Session: "session-example", Message: "message-example"}, Scope: proto.AssistantEntryScope{Executor: "system"},
		By: "user", Trust: "user_verbatim", Status: "active"}
}
func TestAssistantEntriesPaginationAndFilters(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	add := func(e proto.AssistantEntry) proto.AssistantEntry {
		t.Helper()
		out, err := d.AppendAssistantEntry(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := ledgerFixture("entry-a")
	a.OriginSpace = "health"
	a.Category = []string{"health", "finance"}
	a.RelayedVia = []string{"session-relay"}
	a.Trust = "user_relayed"
	a.Tag = []string{"import", "example"}
	a.Scope.Machine = []string{"computer-a"}
	a.Scope.Project = []string{"project-example"}
	future := time.Now().Add(time.Hour)
	a.ExpiresAt = &future
	a = add(a)
	if replay, err := d.AppendAssistantEntry(ctx, a); err != nil || !replay.At.Equal(a.At) {
		t.Fatalf("expiry replay: %+v %v", replay, err)
	}
	b := ledgerFixture("entry-b")
	b.By = "agent"
	b.Trust = "agent"
	b.Scope.Executor = "assistant"
	b.Tag = []string{"seed"}
	add(b)
	c := ledgerFixture("entry-c")
	c.Scope = proto.AssistantEntryScope{Executor: "any", Personal: true}
	add(c)
	past := time.Now().Add(-time.Hour)
	expired := ledgerFixture("entry-expired")
	expired.ExpiresAt = &past
	add(expired)
	page := func(p proto.AssistantEntriesParams, personal bool) proto.AssistantEntriesResult {
		t.Helper()
		out, err := d.ListAssistantEntries(ctx, p, personal)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := page(proto.AssistantEntriesParams{Limit: 1}, true)
	if len(first.Entries) != 1 || first.Entries[0].ID != a.ID || !first.HasMore || first.After == "" {
		t.Fatalf("first: %+v", first)
	}
	second := page(proto.AssistantEntriesParams{Limit: 1, After: first.After}, true)
	third := page(proto.AssistantEntriesParams{Limit: 1, After: second.After}, true)
	if second.Entries[0].ID != "entry-b" || third.Entries[0].ID != "entry-c" || third.HasMore {
		t.Fatalf("pages: %+v %+v", second, third)
	}
	empty := page(proto.AssistantEntriesParams{After: third.After}, true)
	if len(empty.Entries) != 0 || empty.After != third.After {
		t.Fatalf("empty: %+v", empty)
	}
	// Appends use publication time even when the producer supplies an old time.
	later := ledgerFixture("entry-later")
	later.At = past
	add(later)
	if got := page(proto.AssistantEntriesParams{After: third.After}, true); len(got.Entries) != 1 || got.Entries[0].ID != "entry-later" {
		t.Fatalf("incremental: %+v", got)
	}
	// Newest first (the memory assembler, #13532): each page the next older one, active entries only.
	newest := func(p proto.AssistantEntriesParams) proto.AssistantEntriesResult {
		t.Helper()
		out, err := d.NewestAssistantEntries(ctx, p, true)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	n1 := newest(proto.AssistantEntriesParams{Limit: 2})
	n2 := newest(proto.AssistantEntriesParams{Limit: 2, After: n1.After})
	var newestIDs []string
	for _, e := range append(n1.Entries, n2.Entries...) {
		newestIDs = append(newestIDs, e.ID)
	}
	if !reflect.DeepEqual(newestIDs, []string{"entry-later", "entry-c", "entry-b", "entry-a"}) || !n1.HasMore || n2.HasMore {
		t.Fatalf("newest first: %v (%+v, %+v)", newestIDs, n1, n2)
	}
	yes, no := true, false
	for _, tc := range []struct {
		name     string
		p        proto.AssistantEntriesParams
		personal bool
		want     []string
	}{
		{"by", proto.AssistantEntriesParams{By: "agent"}, true, []string{"entry-b"}},
		{"trust", proto.AssistantEntriesParams{Trust: "user_relayed"}, true, []string{"entry-a"}},
		{"status", proto.AssistantEntriesParams{Status: "expired"}, true, []string{"entry-expired"}},
		{"all", proto.AssistantEntriesParams{Status: "all", Tag: "import"}, true, []string{"entry-a"}},
		{"tag", proto.AssistantEntriesParams{Tag: "seed"}, true, []string{"entry-b"}},
		{"executor", proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "system"}}, true, []string{"entry-a", "entry-c", "entry-later"}},
		{"machine", proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Machine: []string{"computer-b"}}}, false, []string{"entry-b", "entry-later"}},
		{"project", proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Project: []string{"other-project"}}}, false, []string{"entry-b", "entry-later"}},
		{"personal", proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Personal: &yes}}, true, []string{"entry-c"}},
		{"private boundary", proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Personal: &yes}}, false, []string{}},
		{"combined", proto.AssistantEntriesParams{By: "user", Trust: "user_relayed", Status: "active", Tag: "import", Scope: &proto.AssistantEntryScopeFilter{Executor: "system", Machine: []string{"computer-a"}, Project: []string{"project-example"}, Personal: &no}}, true, []string{"entry-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := page(tc.p, tc.personal)
			ids := []string{}
			for _, e := range out.Entries {
				ids = append(ids, e.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
		})
	}
	// Required fields are present even when empty/null, not silently omitted.
	original, _ := d.AssistantEntry(ctx, "entry-a")
	if original.OriginSpace != "health" || !reflect.DeepEqual(original.Category, []string{"health", "finance"}) {
		t.Fatalf("metadata round trip: %+v", original)
	}
	raw, _ := json.Marshal(original)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, field := range []string{"relayed_via", "supersedes", "expires_at", "tag", "origin_space", "category"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
	if !reflect.DeepEqual(original.RelayedVia, []string{"session-relay"}) || original.ExpiresAt == nil || !original.ExpiresAt.Equal(future) {
		t.Fatalf("round trip: %+v", original)
	}
	for _, p := range []proto.AssistantEntriesParams{{After: "invalid"}, {By: "automation"}, {Trust: "trusted"}, {Status: "deleted"}, {Limit: 201}, {Scope: &proto.AssistantEntryScopeFilter{Executor: "other"}}} {
		if _, err := d.ListAssistantEntries(ctx, p, true); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestAssistantEntriesImmutableDecisionsAndLifecycle(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	yes := true
	e := ledgerFixture("entry-decision")
	e.Type = "decision"
	e.Decision = &proto.AssistantEntryDecision{Options: []string{"Stage", "Release"}, Selected: []string{"Stage"}, Rejected: []string{"Release"}, Reason: "Keep the change reversible.", Reversible: &yes}
	original, err := d.AppendAssistantEntry(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.AssistantEntry(ctx, e.ID)
	if err != nil || !reflect.DeepEqual(got.Decision, e.Decision) {
		t.Fatalf("decision: %+v %v", got, err)
	}
	for _, sql := range []string{`UPDATE assistant_entries SET body='{}' WHERE id='entry-decision'`, `DELETE FROM assistant_entries WHERE id='entry-decision'`} {
		if _, err := d.pool.Exec(ctx, sql); err == nil {
			t.Fatal("mutation accepted")
		}
	}
	replay, err := d.AppendAssistantEntry(ctx, e)
	if err != nil || !replay.At.Equal(original.At) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	conflict := e
	conflict.Text = "Different content"
	if _, err := d.AppendAssistantEntry(ctx, conflict); err == nil {
		t.Fatal("conflicting id accepted")
	}
	replacement := ledgerFixture("entry-replacement")
	replacement.Supersedes = e.ID
	if _, err := d.AppendAssistantEntry(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListAssistantEntries(ctx, proto.AssistantEntriesParams{Status: "superseded"}, true)
	if err != nil || len(list.Entries) != 1 || list.Entries[0].ID != e.ID {
		t.Fatalf("superseded: %+v %v", list, err)
	}
	untouched, _ := d.AssistantEntry(ctx, e.ID)
	if !reflect.DeepEqual(untouched, original) {
		t.Fatal("original changed")
	}
	wrong := replacement
	wrong.ID = "entry-correction"
	wrong.Supersedes = replacement.ID
	wrong.Status = "wrong"
	if _, err := d.AppendAssistantEntry(ctx, wrong); err != nil {
		t.Fatal(err)
	}
	list, err = d.ListAssistantEntries(ctx, proto.AssistantEntriesParams{Status: "wrong"}, true)
	if err != nil || len(list.Entries) != 2 {
		t.Fatalf("wrong history: %+v %v", list, err)
	}
	duplicate := wrong
	duplicate.ID = "entry-conflicting-correction"
	if _, err := d.AppendAssistantEntry(ctx, duplicate); !errors.Is(err, ErrEntrySuperseded) {
		t.Fatalf("second successor: %v", err)
	}
	private := ledgerFixture("entry-personal")
	private.Scope = proto.AssistantEntryScope{Executor: "any", Personal: true}
	if _, err := d.AppendAssistantEntry(ctx, private); err != nil {
		t.Fatal(err)
	}
	leak := ledgerFixture("entry-declassified")
	leak.Supersedes = private.ID
	if _, err := d.AppendAssistantEntry(ctx, leak); err == nil {
		t.Fatal("personal declassification accepted")
	}
	hot := ledgerFixture("entry-hot")
	hot.Type = "hot"
	if _, err := d.AppendAssistantEntry(ctx, hot); err == nil {
		t.Fatal("hot state entered the ledger")
	}
}
