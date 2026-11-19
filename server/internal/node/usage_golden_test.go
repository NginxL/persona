package node

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

var updateUsageGolden = flag.Bool("update-usage-golden", false, "write testdata/usage-meter.golden.json again")

// The usage meter's and the backfill's buckets over every transcript fixture (transcript/testdata) laid out as a
// computer keeps them, with a fork, a subagent and a workflow agent copying replies of their session, against
// testdata/usage-meter.golden.json. The code before the usage adapters (#9924: a parser per file name) wrote that
// file; the adapters count exactly what it counted (#9984 only added each bucket's agent). Write it again (-update-usage-golden) only for a change that
// means to count differently, and say so in the change.
func TestUsageMeterGolden(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n, home := testNode(t)
	fixtures, err := filepath.Glob(filepath.Join("..", "transcript", "testdata", "*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("fixtures: %v %v", fixtures, err)
	}
	project := filepath.Join(home, ".claude", "projects", "-work-app")
	rollouts := filepath.Join(home, ".codex", "sessions", time.Now().Format("2006/01/02"))
	write := func(path string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var claude, codex [][]byte
	for i, p := range fixtures {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if strings.HasPrefix(name, "codex-") {
			codex = append(codex, body)
			write(filepath.Join(rollouts, fmt.Sprintf("rollout-2026-01-01T00-00-00-0199a213-81c0-7800-8aa1-%012d.jsonl", i)), body)
			continue
		}
		claude = append(claude, body)
		write(filepath.Join(project, name+".jsonl"), body)
	}
	// A fork of the first session (its history copied, then its own), a subagent and a workflow agent of the second
	// whose files repeat some of its replies, and a fork of the first rollout.
	first, second := strings.TrimSuffix(filepath.Base(fixtures[0]), ".jsonl"), strings.TrimSuffix(filepath.Base(fixtures[1]), ".jsonl")
	write(filepath.Join(project, first+"-fork.jsonl"), append(slices.Clone(claude[0]), claude[1]...))
	write(filepath.Join(project, second, "subagents", "agent-a1.jsonl"), append(slices.Clone(claude[1]), claude[2]...))
	write(filepath.Join(project, second, "subagents", "workflows", "wf_1", "agent-a2.jsonl"), append(slices.Clone(claude[2]), claude[3]...))
	forkMeta := []byte(`{"timestamp":"2026-01-02T00:00:00Z","type":"session_meta","payload":{"id":"0199a213-81c0-7800-8aa1-ffffffffffff"}}` + "\n")
	write(filepath.Join(rollouts, "rollout-2026-01-02T00-00-00-0199a213-81c0-7800-8aa1-ffffffffffff.jsonl"), slices.Concat(forkMeta, codex[0], codex[1]))

	type golden struct {
		Files    int                 `json:"files"`
		Meter    []proto.UsageBucket `json:"meter"`
		Backfill []proto.UsageBucket `json:"backfill"`
	}
	var got golden
	sorted := func(bs []proto.UsageBucket) []proto.UsageBucket {
		slices.SortFunc(bs, func(a, b proto.UsageBucket) int {
			return cmp.Or(cmp.Compare(a.Minute, b.Minute), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Launcher, b.Launcher))
		})
		return bs
	}
	// The live meter over everything (a cutoff before every fixture), each file's replies under its session.
	m := newUsageMeter()
	m.launcherOf = func(s string) string { return "launcher-of-" + s }
	var cutoff time.Time
	for _, f := range n.usageFiles(cutoff) {
		got.Files++
		scanFile(t, m, f.path, cutoff)
	}
	for _, b := range m.buckets {
		got.Meter = append(got.Meter, *b)
	}
	got.Meter = sorted(got.Meter)
	// The backfill over the same files, by the hour.
	lim := &readLimiter{rate: 1 << 40, start: time.Now()}
	since := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	files := n.backfillFiles(context.Background(), since, lim)
	got.Backfill, err = n.backfillScan(context.Background(), files, nil, since, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), lim)
	if err != nil {
		t.Fatal(err)
	}
	got.Backfill = sorted(got.Backfill)
	if len(got.Meter) == 0 || len(got.Backfill) == 0 {
		t.Fatalf("nothing counted: %+v", got)
	}

	path := filepath.Join("testdata", "usage-meter.golden.json")
	out, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if *updateUsageGolden {
		write(path, out)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(want) {
		t.Fatalf("the buckets differ from %s (-update-usage-golden writes them):\n%s", path, out)
	}
}
