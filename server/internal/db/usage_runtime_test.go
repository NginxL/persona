package db

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRuntimeUsageAddAndFull(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Minute)
	b := func(agent string, n int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: now.Unix(), Model: "m", Agent: agent, Input: n, Output: 2 * n, CacheRead: 3 * n, CacheWrite: 4 * n, Requests: 1}
	}
	writes := []UsageWrite{{Machine: "m1", Buckets: []proto.UsageBucket{b("claude", 100), b("", 100)}}, {Machine: "m1", Buckets: []proto.UsageBucket{b("grok", 1)}, Add: true}, {Machine: "m1", Buckets: []proto.UsageBucket{b("grok", 2)}, Add: true}, {Machine: "m1", Buckets: []proto.UsageBucket{b("claude", 5)}, ReplaceFrom: now.Add(-time.Hour)}}
	if rejected, err := d.PutUsages(ctx, writes); err != nil {
		t.Fatal(err)
	} else {
		for _, err := range rejected {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := d.pool.Query(ctx, `SELECT agent,input,output,cache_read,cache_write,requests FROM machine_usage WHERE machine_id='m1' ORDER BY agent`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var agent string
		var in, out, cr, cw int64
		var req int
		if err := rows.Scan(&agent, &in, &out, &cr, &cw, &req); err != nil {
			t.Fatal(err)
		}
		count++
		if agent == "grok" {
			if in != 3 || out != 6 || cr != 9 || cw != 12 || req != 2 {
				t.Fatalf("delta %d %d %d %d %d", in, out, cr, cw, req)
			}
		} else if agent != "claude" || out != 10 {
			t.Fatalf("replacement %s %d", agent, out)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count != 2 {
		t.Fatalf("row count %d", count)
	}
	if err := d.AddUsage(ctx, "m1", []proto.UsageBucket{b("grok", 4)}); err != nil {
		t.Fatal(err)
	}
	recent, err := d.RecentRuntimeUsage(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent["m1"]) != 1 || recent["m1"][0].Agent != "grok" || recent["m1"][0].Output != 14 {
		t.Fatalf("runtime restart snapshot: %+v", recent)
	}
	if _, err := d.RollupUsage(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	series, err := d.UsageSeries(ctx, "m1", now.Add(-time.Hour), now.Add(time.Hour), 600)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range series {
		total += b.Output
	}
	if total != 24 {
		t.Fatalf("rollup total %d, rows %+v", total, series)
	}
}
