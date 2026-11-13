package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func subscriptionForItems(t testing.TB, d *DB, id string, now time.Time) {
	t.Helper()
	row := SubscriptionRow{ID: id, Connector: "example", Source: "notices", Revision: 1, Data: []byte(`{}`), CreatedAt: now, UpdatedAt: now}
	if err := d.CreateSubscription(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func keepData(data []byte, _ int64) ([]byte, error) { return data, nil }

// A run's new items are stored in the order the run gave them, so seq order is
// the source's order, each with its data as written (json keeps its text).
func TestSubscriptionRunItemsKeepTheirOrder(t *testing.T) {
	d, ctx := openSubscriptions(t), context.Background()
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	subscriptionForItems(t, d, "sub_items", now)
	uids := []string{"z9", "a1", "m5", "a1", "b2"} // not sorted, one repeated
	var items []SubscriptionItemRow
	for i, uid := range uids {
		items = append(items, SubscriptionItemRow{UID: uid, Data: []byte(fmt.Sprintf(`{"title": "item %d",  "n":%d}`, i, i))})
	}
	if err := d.CommitSubscriptionRun(ctx, "sub_items", keepData, nil, items, nil, now); err != nil {
		t.Fatal(err)
	}
	got, err := d.SubscriptionItems(ctx, "sub_items", SubscriptionItemsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(items) {
		t.Fatalf("stored %d items, want %d", len(got), len(items))
	}
	for i, it := range got {
		if it.UID != uids[i] || string(it.Data) != string(items[i].Data) || !it.CreatedAt.Equal(now) {
			t.Fatalf("item %d = %s %s %v, want %s %s", i, it.UID, it.Data, it.CreatedAt, uids[i], items[i].Data)
		}
		if i > 0 && it.Seq <= got[i-1].Seq {
			t.Fatalf("seq %d after %d", it.Seq, got[i-1].Seq)
		}
	}
	// A second run appends after the first.
	more := []SubscriptionItemRow{{UID: "c3", Data: []byte(`{"title":"later"}`)}}
	if err := d.CommitSubscriptionRun(ctx, "sub_items", keepData, nil, more, nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = d.SubscriptionItems(ctx, "sub_items", SubscriptionItemsQuery{})
	if len(got) != 6 || got[5].UID != "c3" || !got[5].CreatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after a second run: %+v", got)
	}
}

// Invalid item data fails the whole run: nothing of it is kept.
func TestSubscriptionRunBadItemKeepsNothing(t *testing.T) {
	d, ctx := openSubscriptions(t), context.Background()
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	subscriptionForItems(t, d, "sub_bad", now)
	items := []SubscriptionItemRow{{UID: "ok", Data: []byte(`{}`)}, {UID: "bad", Data: []byte(`{not json`)}}
	if err := d.CommitSubscriptionRun(ctx, "sub_bad", keepData, []SubscriptionSeenRow{{UID: "ok", ContentHash: "h"}}, items, nil, now); err == nil {
		t.Fatal("a run with invalid item data was committed")
	}
	if got, _ := d.SubscriptionItems(ctx, "sub_bad", SubscriptionItemsQuery{}); len(got) != 0 {
		t.Fatalf("kept %d items of a failed run", len(got))
	}
}
