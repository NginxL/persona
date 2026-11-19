package node

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// BenchmarkOldCodexTranscriptHit measures repeated successful opaque-id lookups over the same old rollouts. The cached
// matches still need their per-call path checks, but must not walk every file again.
func BenchmarkOldCodexTranscriptHit(b *testing.B) {
	n, _ := testNode(b)
	n.codices = newCodexRegistry()
	codex := canonicalCodexHome(n.codexHome(), n.home)
	now := time.Now()
	for i := range 5000 {
		day := now.AddDate(0, 0, -(20 + i%300))
		raw := fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
		if i == 0 {
			raw = remoteRaw
		}
		writeRollout(b, filepath.Dir(codex), raw, "/w", day, day)
	}
	n.configureCodex(proto.CodexConfig{Homes: []string{codex}, LegacyHome: filepath.Join(n.home, "legacy-codex")})
	id := codexSourceID(codex, remoteRaw, n.codexInventory().LegacyHome)
	if got := n.oldCodexTranscript(id); got == "" {
		b.Fatal("the benchmark transcript is missing")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if got := n.oldCodexTranscript(id); got == "" {
			b.Fatal("the benchmark transcript is missing")
		}
	}
}

// BenchmarkOldCodexTranscriptMissing measures repeated unresolved opaque-id lookups over the same 5,000 old rollouts as
// BenchmarkRolloutsOlder. Before negative-result caching, every call walked every file again.
func BenchmarkOldCodexTranscriptMissing(b *testing.B) {
	n, _ := testNode(b)
	n.codices = newCodexRegistry()
	codex := n.codexHome()
	now := time.Now()
	for i := range 5000 {
		day := now.AddDate(0, 0, -(20 + i%300))
		writeRollout(b, filepath.Dir(codex), fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i), "/w", day, day)
	}
	n.configureCodex(proto.CodexConfig{Homes: []string{codex}, LegacyHome: filepath.Join(n.home, "legacy-codex")})
	missing := codexSourceID(codex, "aaaaaaaa-0000-4000-8000-000000000000", n.codexInventory().LegacyHome)
	if got := n.oldCodexTranscript(missing); got != "" {
		b.Fatalf("unexpected transcript %q", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if got := n.oldCodexTranscript(missing); got != "" {
			b.Fatalf("unexpected transcript %q", got)
		}
	}
}
