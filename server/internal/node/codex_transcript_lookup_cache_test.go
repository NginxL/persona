package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestOldCodexOpaqueTranscriptArchiveInvalidatesMiss(t *testing.T) {
	n := remoteTestNode(t)
	base := t.TempDir()
	home := canonicalCodexHome(filepath.Join(base, ".codex"), n.home)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})
	const raw = "aaaaaaaa-0000-4000-8000-000000000001"
	id := codexSourceID(home, raw, n.codexInventory().LegacyHome)
	if got := n.oldCodexTranscript(id); got != "" {
		t.Fatalf("missing lookup = %q", got)
	}

	// Creating this file in an old day does not change the recent-day stamps. Moving it into archived_sessions does.
	old := time.Now().AddDate(0, 0, -30)
	writeRollout(t, base, raw, "/wanted", old, old)
	from := rolloutPath(home, raw)
	archived := filepath.Join(home, "archived_sessions", filepath.Base(from))
	if err := os.MkdirAll(filepath.Dir(archived), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, archived); err != nil {
		t.Fatal(err)
	}
	if got := n.oldCodexTranscript(id); got != archived {
		t.Fatalf("lookup after archiving = %q, want %q", got, archived)
	}
}

func TestOldCodexOpaqueTranscriptHitCacheBoundsRepeatedScans(t *testing.T) {
	n := remoteTestNode(t)
	base := t.TempDir()
	home := canonicalCodexHome(filepath.Join(base, ".codex"), n.home)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})
	old := time.Now().AddDate(0, 0, -30)
	writeRollout(t, base, remoteRaw, "/wanted", old, old)
	path := rolloutPath(home, remoteRaw)
	id := codexSourceID(home, remoteRaw, n.codexInventory().LegacyHome)

	calls := 0
	glob := codexTranscriptGlob
	codexTranscriptGlob = func(pattern string) ([]string, error) {
		calls++
		return glob(pattern)
	}
	t.Cleanup(func() { codexTranscriptGlob = glob })

	if got := n.oldCodexTranscript(id); got != path {
		t.Fatalf("first lookup = %q, want %q", got, path)
	}
	first := calls
	if first == 0 {
		t.Fatal("the first hit did not inspect the home")
	}
	if got := n.oldCodexTranscript(id); got != path {
		t.Fatalf("cached lookup = %q, want %q", got, path)
	}
	if calls != first {
		t.Fatalf("unchanged hit globbed again: %d -> %d", first, calls)
	}

	// A cached path is still checked on every call. Its disappearance triggers a fresh lookup, not a stale answer.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := n.oldCodexTranscript(id); got != "" {
		t.Fatalf("lookup after removing the cached path = %q, want missing", got)
	}
	if calls <= first {
		t.Fatal("a missing cached path did not trigger a fresh lookup")
	}
}

func TestOldCodexOpaqueTranscriptMissCacheBoundsRepeatedScans(t *testing.T) {
	n := remoteTestNode(t)
	base := t.TempDir()
	home := canonicalCodexHome(filepath.Join(base, ".codex"), n.home)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})
	id := codexSourceID(home, remoteRaw, n.codexInventory().LegacyHome)

	calls := 0
	glob := codexTranscriptGlob
	codexTranscriptGlob = func(pattern string) ([]string, error) {
		calls++
		return glob(pattern)
	}
	t.Cleanup(func() { codexTranscriptGlob = glob })

	if got := n.oldCodexTranscript(id); got != "" {
		t.Fatalf("missing lookup = %q", got)
	}
	first := calls
	if first == 0 {
		t.Fatal("the first miss did not inspect the home")
	}
	if got := n.oldCodexTranscript(id); got != "" {
		t.Fatalf("cached missing lookup = %q", got)
	}
	if calls != first {
		t.Fatalf("unchanged miss globbed again: %d -> %d", first, calls)
	}
	n.configureCodex(proto.CodexConfig{Homes: []string{home, filepath.Join(t.TempDir(), "other-codex")}})
	_ = n.oldCodexTranscript(id)
	if calls <= first {
		t.Fatal("a different home set reused the old miss")
	}
	first = calls
	_ = n.oldCodexTranscript(id)
	if calls != first {
		t.Fatalf("the new home set did not settle its miss: %d -> %d", first, calls)
	}

	// Creating a rollout in a recent day invalidates the negative answer immediately.
	now := time.Now()
	writeRollout(t, base, remoteRaw, "/wanted", now, now)
	path := rolloutPath(home, remoteRaw)
	if got := n.oldCodexTranscript(id); got != path {
		t.Fatalf("lookup after a recent rollout = %q, want %q", got, path)
	}
	if calls <= first {
		t.Fatal("a new recent rollout did not invalidate the miss")
	}
	if _, ok := n.codexSource(id); ok {
		t.Fatal("the cache registered an inventory source")
	}

	// The one-minute bound also expires a miss whose directory stamps stayed identical.
	missing := codexSourceID(home, "aaaaaaaa-0000-4000-8000-000000000000", n.codexInventory().LegacyHome)
	_ = n.oldCodexTranscript(missing)
	before := calls
	_ = n.oldCodexTranscript(missing)
	if calls != before {
		t.Fatalf("second independent miss globbed again: %d -> %d", before, calls)
	}
	n.mu.Lock()
	miss := n.oldCache[missing]
	miss.at = time.Now().Add(-2 * oldCodexLookupFor)
	n.oldCache[missing] = miss
	n.mu.Unlock()
	_ = n.oldCodexTranscript(missing)
	if calls <= before {
		t.Fatal("an expired miss was not checked again")
	}
}
