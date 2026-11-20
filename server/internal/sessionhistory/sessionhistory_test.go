package sessionhistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

var base = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	q, err := Parse("2026-08-21T00:00:00Z", "", "", 0, base)
	if err != nil || !q.Until.Equal(base) || q.Limit != DefaultLimit || q.Since.Month() != time.August {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	if q, _ := Parse("2026-08-21T00:00:00Z", "2026-09-01T00:00:00.5+08:00", "", 9999, base); q.Limit != MaxLimit || q.Until.UTC().Day() != 31 {
		t.Fatalf("limit clamped, until parsed: %+v", q)
	}
	for _, c := range [][3]string{{"", "", ""}, {"yesterday", "", ""}, {"2026-08-21T00:00:00Z", "soon", ""}, {"2026-08-21T00:00:00Z", "", "%%%"}} {
		if _, err := Parse(c[0], c[1], c[2], 0, base); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: want ErrInvalid, got %v", c, err)
		}
	}
	if _, err := Parse("2026-08-21T00:00:00Z", "", "", -1, base); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative limit: %v", err)
	}
	k := Key{T: 123, K: "a\x00b"}
	if got, err := DecodeCursor(EncodeCursor(k)); err != nil || *got != k {
		t.Fatalf("cursor round trip: %v %v", got, err)
	}
}

// item makes an item of source src at minute m before base.
func item(src string, m int, id string) Item[string] {
	return Item[string]{Key: KeyOf(base.Add(-time.Duration(m)*time.Minute), src+"\x00"+id), V: src + "/" + id}
}

// pager is a source with its own (offset) cursor, newest first, answering at most each page's limit.
func pager(all []Item[string], calls *int) Fetch[string] {
	all = Sorted(all)
	return func(_ context.Context, q Query) (Page[string], error) {
		*calls++
		var in []Item[string]
		for _, it := range all {
			if it.Key.T >= q.Since.UnixMilli() && it.Key.T < q.Until.UnixMilli() {
				in = append(in, it)
			}
		}
		from, _ := strconv.Atoi(q.Cursor)
		to := min(len(in), from+q.Limit)
		page := Page[string]{Items: in[from:to], Complete: true}
		if to < len(in) {
			page.NextCursor = strconv.Itoa(to)
		}
		return page, nil
	}
}

// Several sources merged page by page, each paged with its own cursor: every item once, in order, whatever the
// page size, ties on the same millisecond included.
func TestCollectAndMergeListEverySourceInFull(t *testing.T) {
	var sources [][]Item[string]
	want := 0
	for s, n := range []int{450, 3, 0, 120} {
		var list []Item[string]
		for i := 0; i < n; i++ {
			list = append(list, item(fmt.Sprint("src", s), i/5+s, fmt.Sprintf("%04d", i))) // five share each minute
		}
		sources = append(sources, list)
		want += n
	}
	q, _ := Parse(base.Add(-90*24*time.Hour).Format(time.RFC3339), base.Add(time.Minute).Format(time.RFC3339), "", 50, base)
	for _, limit := range []int{1, 7, 50, 500} {
		q.Limit = limit
		var got []Item[string]
		var after *Key
		calls := 0
		for page := 0; ; page++ {
			if page > 1000 {
				t.Fatal("no last page")
			}
			var parts []Part[string]
			for _, list := range sources {
				part, complete, err := Collect(context.Background(), pager(list, &calls), q, after, limit)
				if err != nil || !complete {
					t.Fatal(err, complete)
				}
				parts = append(parts, part)
			}
			items, next := Merge(parts, limit)
			if len(items) == 0 && next != nil {
				t.Fatal("an empty page that says there is more")
			}
			got = append(got, items...)
			if next == nil {
				break
			}
			after = next
		}
		if len(got) != want {
			t.Fatalf("limit %d: %d items, want %d", limit, len(got), want)
		}
		seen := map[string]bool{}
		for i, it := range got {
			if seen[it.V] {
				t.Fatalf("limit %d: %s twice", limit, it.V)
			}
			seen[it.V] = true
			if i > 0 && !got[i-1].Key.Before(it.Key) {
				t.Fatalf("limit %d: out of order at %d", limit, i)
			}
		}
	}
}

// A source with more stops the merged page at its last item: what it has beyond may be newer than another source's.
func TestMergeStopsAtAPartWithMore(t *testing.T) {
	a := Part[string]{Items: []Item[string]{item("a", 1, "1"), item("a", 5, "2")}, More: true}
	b := Part[string]{Items: []Item[string]{item("b", 2, "1"), item("b", 6, "2"), item("b", 9, "3")}}
	items, next := Merge([]Part[string]{a, b}, 10)
	if len(items) != 3 || items[2].V != "a/2" || next == nil || *next != items[2].Key {
		t.Fatalf("got %v next %v", items, next)
	}
	items, next = Merge([]Part[string]{b}, 10)
	if len(items) != 3 || next != nil {
		t.Fatalf("all of a part without more, no next: %v %v", items, next)
	}
}

// A source that never ends its pages is read a bounded number of times and said to have more.
func TestCollectIsBounded(t *testing.T) {
	calls := 0
	fetch := func(_ context.Context, q Query) (Page[string], error) {
		calls++
		return Page[string]{NextCursor: fmt.Sprint(calls)}, nil
	}
	q, _ := Parse("2026-08-21T00:00:00Z", "", "", 10, base)
	part, _, err := Collect(context.Background(), fetch, q, nil, 10)
	if err != nil || !part.More || calls != collectPages {
		t.Fatalf("calls %d more %v err %v", calls, part.More, err)
	}
}

func TestRowJSON(t *testing.T) {
	three := 3
	r := Row{Session: proto.Session{SessionID: "s1", Title: "t", Archived: true, UpdatedAt: base}, Source: SourceRuntime, Runtime: "dsh",
		ActiveAt: base.Add(-time.Hour), Origin: OriginAutomation, UserMessages: &three}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"session_id":"s1"`, `"archived":true`, `"history_source":"runtime"`, `"history_runtime":"dsh"`, `"active_at":"2026-11-18T23:00:00Z"`, `"origin":"automation"`, `"user_messages":3`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	if strings.Contains(string(b), "started_at") {
		t.Errorf("an empty started_at is left out: %s", b)
	}
	var back Row
	if err := json.Unmarshal(b, &back); err != nil || back.Session.SessionID != "s1" || !back.Session.Archived || back.Runtime != "dsh" ||
		!back.ActiveAt.Equal(r.ActiveAt) || back.Origin != OriginAutomation || *back.UserMessages != 3 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if !back.At().Equal(r.ActiveAt) {
		t.Errorf("the time is active_at when given")
	}
	back.ActiveAt = time.Time{}
	if !back.At().Equal(base) {
		t.Errorf("else updated_at")
	}
}

func TestRuntimeRow(t *testing.T) {
	r, deleted, err := RuntimeRow([]byte(`{"session_id":"a","status":"done","updated_at":"2026-11-19T00:00:00Z","started_at":"2026-10-01T00:00:00Z","origin":"robot","user_messages":-2}`))
	if err != nil || deleted || r.Origin != "" || r.UserMessages != nil || r.StartedAt.Month() != time.October {
		t.Fatalf("unknown origin and a negative count are not relayed: %+v %v %v", r, deleted, err)
	}
	for _, raw := range []string{`{"session_id":"a","status":"deleted"}`, `{"session_id":"a","deleted":true}`} {
		if _, deleted, _ := RuntimeRow([]byte(raw)); !deleted {
			t.Errorf("%s is deleted", raw)
		}
	}
	r, _, _ = RuntimeRow([]byte(`{"session_id":"a","started_at":"2026-10-01T00:00:00Z"}`))
	if !r.At().Equal(r.StartedAt) {
		t.Errorf("started_at is the last fallback")
	}
}

// A source whose rows read so far were all left out (Reached past its last item, or no item at all) stops the merged
// page at how far it read: another source's older rows wait, and an empty page still carries the cursor on.
func TestMergeStopsWhereAPartWithMoreReached(t *testing.T) {
	reached := item("a", 4, "x").Key
	a := Part[string]{More: true, Reached: &reached}
	b := Part[string]{Items: []Item[string]{item("b", 2, "1"), item("b", 6, "2")}}
	items, next := Merge([]Part[string]{a, b}, 10)
	if len(items) != 1 || items[0].V != "b/1" || next == nil || *next != items[0].Key {
		t.Fatalf("stopped at a's reach: got %v next %v", items, next)
	}
	items, next = Merge([]Part[string]{a}, 10)
	if len(items) != 0 || next == nil || *next != reached {
		t.Fatalf("an empty page goes on from where a reached: %v %v", items, next)
	}
}

// Collect keeps how far a source read when every row past its last item was left out, over its bounded pages.
func TestCollectReachesPastRowsLeftOut(t *testing.T) {
	calls := 0
	fetch := func(_ context.Context, q Query) (Page[string], error) {
		calls++
		k := item("a", calls, "x").Key
		return Page[string]{NextCursor: fmt.Sprint(calls), Reached: &k}, nil
	}
	q, _ := Parse("2026-08-21T00:00:00Z", "", "", 10, base)
	part, _, err := Collect(context.Background(), fetch, q, nil, 10)
	if err != nil || !part.More || len(part.Items) != 0 || part.Reached == nil || *part.Reached != item("a", collectPages, "x").Key {
		t.Fatalf("more %v items %d reached %v err %v", part.More, len(part.Items), part.Reached, err)
	}
}
