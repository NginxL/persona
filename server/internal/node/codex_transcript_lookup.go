package node

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var codexTranscriptGlob = filepath.Glob

const (
	oldCodexLookupFor = time.Minute
	oldCodexLookupMax = 1024
)

type oldCodexLookup struct {
	homes string
	home  string
	stamp []int64
	paths []string // nil: a cached miss; otherwise the matches to revalidate
	at    time.Time
}

// oldCodexTranscript resolves an opaque identity after its rollout has aged out
// of discovery and the node has restarted. This is a read lookup, not inventory:
// do not register the source, which would advertise old or archived conversations.
func (n *Node) oldCodexTranscript(id string) string {
	if len(id) != len("c0de-")+32 || !validID(id) {
		return ""
	}
	cfg := n.codexInventory()
	var homes []string
	for _, configured := range cfg.Homes {
		home := canonicalCodexHome(configured, n.home)
		if home != "" && home != cfg.LegacyHome {
			homes = append(homes, home)
		}
	}
	key := cfg.LegacyHome + "\x00" + strings.Join(homes, "\x00")
	var stamp []int64
	for _, home := range homes {
		stamp = append(stamp, missStamp(home)...)
	}
	if path, ok := n.oldCodexTranscriptCached(id, key, stamp, cfg.LegacyHome); ok {
		return path
	}
	for _, home := range homes {
		for _, pattern := range []string{
			filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"),
			filepath.Join(home, "archived_sessions", "rollout-*.jsonl"),
		} {
			paths, err := codexTranscriptGlob(pattern)
			if err != nil {
				n.logf("codex transcript lookup: %v", err)
				continue
			}
			var matches []string
			for _, path := range paths {
				if real := n.oldCodexTranscriptMatch(home, cfg.LegacyHome, id, path); real != "" {
					matches = append(matches, real)
				}
			}
			if len(matches) > 0 {
				n.noteOldCodexTranscript(id, key, home, matches, stamp)
				return newestFirst(matches)[0]
			}
		}
	}
	n.noteOldCodexTranscript(id, key, "", nil, stamp)
	return ""
}

// oldCodexTranscriptMatch applies the same symlink, home, identity and file-type checks to a globbed path and to a
// cached match. A cached path therefore cannot become a way to read somewhere the original lookup would have rejected.
func (n *Node) oldCodexTranscriptMatch(home, legacy, id, path string) string {
	thread := rolloutID(path)
	if thread == "" {
		return ""
	}
	// Configured home aliases are canonicalized, but a nested symlink
	// must not make an identity for this home read another home's file.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			n.logf("codex transcript lookup: %v", err)
		}
		return "" // A concurrently moved or unavailable transcript is not readable.
	}
	if rolloutID(real) != thread {
		return ""
	}
	rel, err := filepath.Rel(home, real)
	if err != nil {
		n.logf("codex transcript lookup: %v", err)
		return ""
	}
	if !(strings.HasPrefix(rel, "sessions"+string(filepath.Separator)) ||
		strings.HasPrefix(rel, "archived_sessions"+string(filepath.Separator))) {
		return ""
	}
	st, err := os.Stat(real)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			n.logf("codex transcript lookup: %v", err)
		}
		return ""
	}
	if st.Mode().IsRegular() && codexSourceID(home, rolloutThreadID(home, real), legacy) == id {
		return real
	}
	return ""
}

// oldCodexTranscriptCached reuses a negative answer directly. A positive answer still runs every path check on every
// call; only the historical glob is skipped. The stamp notices normal Codex creation and archiving; the TTL also notices
// out-of-band imports into old day directories, which those stamps cannot see. A vanished cached match forces a fresh
// lookup rather than a stale answer.
func (n *Node) oldCodexTranscriptCached(id, homes string, stamp []int64, legacy string) (string, bool) {
	n.mu.Lock()
	m, ok := n.oldCache[id]
	n.mu.Unlock()
	if !ok || m.homes != homes || time.Since(m.at) >= oldCodexLookupFor || !slices.Equal(m.stamp, stamp) {
		return "", false
	}
	if m.paths == nil {
		return "", true
	}
	matches := make([]string, 0, len(m.paths))
	for _, path := range m.paths {
		if real := n.oldCodexTranscriptMatch(m.home, legacy, id, path); real != "" {
			matches = append(matches, real)
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	return newestFirst(matches)[0], true
}

func (n *Node) noteOldCodexTranscript(id, homes, home string, paths []string, stamp []int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.oldCache == nil {
		n.oldCache = map[string]oldCodexLookup{}
	}
	if len(n.oldCache) >= oldCodexLookupMax {
		clear(n.oldCache)
	}
	n.oldCache[id] = oldCodexLookup{homes: homes, home: home, paths: slices.Clone(paths), stamp: slices.Clone(stamp), at: time.Now()}
}
