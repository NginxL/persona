package db

import (
	"context"
	"testing"
	"time"
)

// Calls add up per UTC day and key, the summary covers the last days by purpose, and old days go as new calls come in.
func TestModelUsageAddsUpByDayAndPrunes(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Date(2026, 11, 18, 23, 30, 0, 0, time.UTC)
	call := ModelCall{Purpose: "inbox.triage", Route: "cli", Provider: "launcher.claude", Model: "m", InputTokens: 10, OutputTokens: 2,
		CacheReadInputTokens: 300, CacheCreationInputTokens: 40}
	for _, c := range []struct {
		at     time.Time
		failed bool
	}{{now, false}, {now.Add(-time.Hour), true}, {now.AddDate(0, 0, -6), false}, {now.AddDate(0, 0, -7), false}} {
		call.Failed = c.failed
		if err := d.AddModelCall(ctx, c.at, call); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddModelCall(ctx, now, ModelCall{Purpose: "kb", Route: "api", Provider: "p", Model: "m", OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ModelUsageByPurpose(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got["inbox.triage"] != (ModelUsage{Calls: 3, Failures: 1, InputTokens: 30, OutputTokens: 6, CacheReadInputTokens: 900, CacheCreationInputTokens: 120}) ||
		got["kb"] != (ModelUsage{Calls: 1, OutputTokens: 5}) || len(got) != 2 {
		t.Fatalf("summary: %+v", got)
	}
	var rows int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE purpose = 'inbox.triage'`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("%d rows (one per day), %v", rows, err)
	}
	// A call 60 days on drops every day before the 60 it keeps.
	if err := d.AddModelCall(ctx, now.AddDate(0, 0, ModelUsageKeep), call); err != nil {
		t.Fatal(err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("%d rows after pruning, %v", rows, err)
	}
}
