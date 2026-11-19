package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The backfill reads every transcript from since to before with the live meter's rules and sums the replies by the
// hour: a reply in two files once, <synthetic> skipped, nothing before since or in the live window (from before on).
func TestUsageBackfillScan(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n, home := testNode(t)
	now := time.Now().UTC().Truncate(time.Hour)
	since, before := now.Add(-10*24*time.Hour), now.Add(-26*time.Hour)
	day := now.Add(-3 * 24 * time.Hour)
	project := filepath.Join(home, ".claude", "projects", "-work-app")
	files := map[string]string{
		filepath.Join(project, "s1.jsonl"): claudeReply(since.Add(-time.Hour), "old", "claude-opus-5-5", 1, 1, 1, 1) + // before since
			claudeReply(day.Add(5*time.Minute), "r1", "claude-opus-5-5", 2, 100, 1000, 50) +
			claudeReply(day.Add(5*time.Minute), "r1", "claude-opus-5-5", 2, 100, 1000, 50) + // its next content block
			claudeReply(day.Add(50*time.Minute), "r2", "claude-opus-5-5", 3, 0, 500, 20) +
			claudeReply(day.Add(70*time.Minute), "r3", "<synthetic>", 0, 0, 0, 0) +
			claudeReply(before.Add(time.Minute), "live", "claude-opus-5-5", 9, 9, 9, 9), // the live meter's
		filepath.Join(project, "s2.jsonl"): claudeReply(day.Add(5*time.Minute), "r1", "claude-opus-5-5", 2, 100, 1000, 50) + // a fork's copy
			claudeReply(day.Add(2*time.Hour), "r4", "claude-sonnet-5-5", 4, 0, 40, 7),
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lim := &readLimiter{rate: 1 << 30, start: time.Now()}
	buckets, err := n.backfillScan(context.Background(), n.backfillFiles(context.Background(), since, lim), nil, since, before, lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets = %+v", buckets)
	}
	if b := buckets[0]; b.Minute != day.Unix() || b.Model != "claude-opus-5-5" || b.Requests != 2 || b.Output != 70 || b.CacheRead != 1500 || b.Input != 5 {
		t.Fatalf("the first hour = %+v", b)
	}
	if b := buckets[1]; b.Minute != day.Add(2*time.Hour).Unix() || b.Output != 7 {
		t.Fatalf("two hours on = %+v", b)
	}

	// The connection ended: the scan stops.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.backfillScan(ctx, n.backfillFiles(context.Background(), since, lim), nil, since, before, lim); err == nil {
		t.Fatal("a scan after its connection ended")
	}
}

func TestBackfillChunks(t *testing.T) {
	hour := func(h int64, model string) proto.UsageBucket {
		return proto.UsageBucket{Minute: h * 3600, Model: model}
	}
	list := []proto.UsageBucket{hour(1, "a"), hour(1, "b"), hour(1, "c"), hour(2, "a"), hour(3, "a"), hour(3, "b")}
	chunks := backfillChunks(list, 2)
	// Two buckets a request, but hour 1's three stay together.
	if len(chunks) != 2 || len(chunks[0]) != 3 || len(chunks[1]) != 3 || chunks[1][0].Minute != 2*3600 {
		t.Fatalf("chunks = %v", chunks)
	}
	if c := backfillChunks(list, 100); len(c) != 1 || len(c[0]) != 6 {
		t.Fatalf("one request = %v", c)
	}
	if c := backfillChunks(nil, 100); len(c) != 1 || len(c[0]) != 0 {
		t.Fatalf("none = %v", c)
	}
}

// The limiter keeps the reads to its rate, and a connection that ends stops the wait.
func TestBackfillReadLimiter(t *testing.T) {
	l := &readLimiter{rate: 1 << 20, start: time.Now()}
	started := time.Now()
	if err := l.wait(context.Background(), 200<<10); err != nil { // 0.2 MB at 1 MB a second
		t.Fatal(err)
	}
	if took := time.Since(started); took < 150*time.Millisecond {
		t.Fatalf("200 KB took %s at 1 MB/s", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx, 10<<20); err == nil {
		t.Fatal("waited past the connection's end")
	}
}

// A week's requests: whole hours each saying the last hour it holds; the last says the week is read through the hour
// before its end (hours without usage too) and, for the last week, that the backfill is done; a week without usage is
// one request with no buckets.
func TestBackfillRequests(t *testing.T) {
	end := time.Unix(100*3600, 0)
	hour := func(h int64) proto.UsageBucket { return proto.UsageBucket{Minute: h * 3600, Model: "m"} }
	list := []proto.UsageBucket{hour(10), hour(10), hour(20), hour(30)}
	got := backfillRequestsSized(list, end, true, 2)
	if len(got) != 2 || got[0]["through"] != int64(10*3600) || got[0]["done"] != false || got[1]["through"] != int64(99*3600) || got[1]["done"] != true {
		t.Fatalf("requests = %v", got)
	}
	if got := backfillRequests(nil, end, false); len(got) != 1 || got[0]["through"] != int64(99*3600) || got[0]["done"] != false {
		t.Fatalf("an empty week = %v", got)
	}
}

// Week by week: each week counts its own replies, a fork's copy within its week once; a file's read stops once
// backfillPast lines in a row are past the week, so the weeks together read about one pass.
func TestUsageBackfillWeeks(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n, home := testNode(t)
	now := time.Now().UTC().Truncate(time.Hour)
	w1 := now.Add(-21 * 24 * time.Hour)
	w2 := w1.Add(backfillSlice)
	project := filepath.Join(home, ".claude", "projects", "-work-app")
	var long strings.Builder
	long.WriteString(claudeReply(w1.Add(time.Hour), "a1", "claude-opus-5-5", 0, 0, 100, 1))
	for i := range 200 { // the second week: a long tail the first week's read stops in
		long.WriteString(claudeReply(w2.Add(time.Duration(i)*time.Minute), fmt.Sprintf("b%d", i), "claude-opus-5-5", 0, 0, 10, 1))
	}
	files := map[string]string{
		filepath.Join(project, "s1.jsonl"): long.String(),
		filepath.Join(project, "s2.jsonl"): claudeReply(w1.Add(time.Hour), "a1", "claude-opus-5-5", 0, 0, 100, 1) + // a fork's copy
			claudeReply(w2.Add(2*time.Hour), "b0", "claude-opus-5-5", 0, 0, 10, 1), // and of a second-week reply
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lim := &readLimiter{rate: 1 << 30, start: time.Now()}
	listed := n.backfillFiles(context.Background(), w1.Add(-backfillSlice), lim)
	if start := backfillStart(listed, nil, w1.Add(-backfillSlice)); !start.Equal(w1.Add(time.Hour)) {
		t.Fatalf("the weeks start at %v, want the first line's hour %v", start, w1.Add(time.Hour))
	}
	first, err := n.backfillScan(context.Background(), listed, nil, w1, w2, lim)
	if err != nil || len(first) != 1 || first[0].Requests != 1 || first[0].CacheRead != 100 || first[0].Minute != w1.Add(time.Hour).Unix() {
		t.Fatalf("the first week = %+v, %v", first, err)
	}
	second, err := n.backfillScan(context.Background(), listed, nil, w2, w2.Add(backfillSlice), lim)
	var replies int
	for _, b := range second {
		replies += b.Requests
	}
	if err != nil || replies != 200 {
		t.Fatalf("the second week has %d replies (%v), want 200", replies, err)
	}

	// The first week's read of the long file stopped soon after the week's end.
	f := testUsagePath(t, filepath.Join(project, "s1.jsonl"))
	st := f.st
	m := newUsageMeter()
	m.step, m.until, m.seen = 3600, w2, map[uint64]struct{}{}
	read, err := m.scanSlice(context.Background(), f, w1, w2, lim)
	if err != nil || read >= st.Size()/2 || read == 0 {
		t.Fatalf("read %d of %d bytes for the first week (%v)", read, st.Size(), err)
	}
}

// The weeks start at the earliest first line (none before it hold anything), unless a file has no timestamp in its
// head; a week reads only the files that can hold its lines.
func TestBackfillStartAndOverlap(t *testing.T) {
	since := time.Date(2025, 11, 16, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	mk := func(name, body string, mod time.Time) backfillFile {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		return backfillFile{usagePath: usagePath{p, st, claudeUsage{}}, first: firstTime([]byte(body))}
	}
	a := mk("a.jsonl", claudeReply(time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC), "a", "m", 0, 0, 1, 1), time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	b := mk("b.jsonl", claudeReply(time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC), "b", "m", 0, 0, 1, 1), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if got := backfillStart([]backfillFile{a, b}, nil, since); !got.Equal(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v", got)
	}
	none := mk("c.jsonl", `{"type":"summary"}`+"\n", time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if got := backfillStart([]backfillFile{a, none}, nil, since); !got.Equal(since) {
		t.Fatalf("with a file of unknown start = %v, want since", got)
	}
	if got := backfillStart(nil, nil, since); !got.Equal(since) {
		t.Fatalf("no files = %v", got)
	}
	week := func(y int, m time.Month, d int) (time.Time, time.Time) {
		s := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return s, s.Add(backfillSlice)
	}
	if s, e := week(2026, 9, 1); a.overlaps(s, e) { // before its first line
		t.Fatal("a file read for a week before it began")
	}
	if s, e := week(2026, 10, 1); !a.overlaps(s, e) {
		t.Fatal("a file not read for the week it began in")
	}
	if s, e := week(2026, 10, 10); a.overlaps(s, e) { // after its last change
		t.Fatal("a file read for a week after it last changed")
	}
	if s, e := week(2026, 10, 10); !none.overlaps(s, e) {
		t.Fatal("a file of unknown start not read")
	}
}

// What to read of each agent: an older manager's one range for every agent; a newer one's per agent, an agent it did
// not answer for or one that is done left out; from the earliest start to the latest end.
func TestBackfillPlan(t *testing.T) {
	names := []string{"claude", "codex", "pi"}
	old := backfillStatus{backfillRange: backfillRange{Since: 100, Before: 900}}
	ranges, since, before, ok := backfillPlan(names, old)
	if !ok || len(ranges) != 3 || ranges["pi"] != old.backfillRange || since.Unix() != 100 || before.Unix() != 900 {
		t.Fatalf("an older manager: %v %v %v %v", ranges, since, before, ok)
	}
	st := backfillStatus{backfillRange: backfillRange{Done: true}, Agents: map[string]backfillRange{
		"claude": {Done: true}, "codex": {Done: true}, "pi": {Since: 300, Before: 900}}}
	ranges, since, _, ok = backfillPlan(append(names, "omp"), st)
	if !ok || len(ranges) != 1 || ranges["pi"].Since != 300 || since.Unix() != 300 {
		t.Fatalf("by agent: %v %v %v", ranges, since, ok)
	}
	if _, _, _, ok := backfillPlan(names, backfillStatus{backfillRange: backfillRange{Done: true}}); ok {
		t.Fatal("nothing to read")
	}
	var decoded backfillStatus
	if err := json.Unmarshal([]byte(`{"done":true,"agents":{"pi":{"done":false,"since":3600,"before":7200}}}`), &decoded); err != nil ||
		!decoded.Done || decoded.Agents["pi"].Before != 7200 {
		t.Fatalf("decoded %+v, %v", decoded, err)
	}
}

// A week keeps each agent's buckets within its range and names the agents whose range it reads.
func TestBackfillWeek(t *testing.T) {
	ranges := map[string]backfillRange{"claude": {Since: 0, Before: 10000}, "pi": {Since: 7200, Before: 10000}}
	// The scan gives a week's buckets only.
	first := []proto.UsageBucket{{Minute: 3600, Agent: "claude"}, {Minute: 3600, Agent: "pi"}}
	second := []proto.UsageBucket{{Minute: 7200, Agent: "pi"}, {Minute: 7200, Agent: "omp"}}
	keep, agents := backfillWeek(first, ranges, time.Unix(0, 0), time.Unix(7200, 0))
	if len(keep) != 1 || keep[0].Agent != "claude" || len(agents) != 1 || agents[0] != "claude" {
		t.Fatalf("first week: %+v %v", keep, agents)
	}
	keep, agents = backfillWeek(second, ranges, time.Unix(7200, 0), time.Unix(10000, 0))
	if len(keep) != 1 || keep[0].Agent != "pi" || keep[0].Minute != 7200 || len(agents) != 2 {
		t.Fatalf("second week: %+v %v", keep, agents)
	}
}
