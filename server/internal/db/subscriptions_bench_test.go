package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkCommitSubscriptionRun stores a first run of 50 items, the size a
// new subscription's first pull often is.
func BenchmarkCommitSubscriptionRun(b *testing.B) {
	d, ctx := openSubscriptions(b), context.Background()
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	subscriptionForItems(b, d, "sub_bench", now)
	items := make([]SubscriptionItemRow, 50)
	for i := range items {
		items[i] = SubscriptionItemRow{UID: fmt.Sprintf("u%d", i), Data: []byte(`{"title":"A notice","url":"https://example.com/n"}`)}
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := d.CommitSubscriptionRun(ctx, "sub_bench", keepData, nil, items, nil, now); err != nil {
			b.Fatal(err)
		}
	}
}
