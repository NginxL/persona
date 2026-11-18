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
	oldCodexMissFor = time.Minute
	oldCodexMissMax = 1024
)

type oldCodexMiss struct {
	homes string
	stamp []int64
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
	if n.oldCodexTranscriptMissed(id, key, stamp) {
		return ""
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
				thread := rolloutID(path)
				if thread == "" {
					continue
				}
				// Configured home aliases are canonicalized, but a nested symlink
				// must not make an identity for this home read another home's file.
				real, err := filepath.EvalSymlinks(path)
				if err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						n.logf("codex transcript lookup: %v", err)
					}
					continue // A concurrently moved or unavailable transcript is not readable.
				}
				if rolloutID(real) != thread {
					continue
				}
				rel, err := filepath.Rel(home, real)
				if err != nil {
					n.logf("codex transcript lookup: %v", err)
					continue
				}
				if !(strings.HasPrefix(rel, "sessions"+string(filepath.Separator)) ||
					strings.HasPrefix(rel, "archived_sessions"+string(filepath.Separator))) {
					continue
				}
				st, err := os.Stat(real)
				if err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						n.logf("codex transcript lookup: %v", err)
					}
					continue
				}
				if st.Mode().IsRegular() && codexSourceID(home, rolloutThreadID(home, real), cfg.LegacyHome) == id {
					matches = append(matches, real)
				}
			}
			if len(matches) > 0 {
				return newestFirst(matches)[0]
			}
		}
	}
	n.noteOldCodexTranscriptMiss(id, key, stamp)
	return ""
}

// oldCodexTranscriptMissed reuses only a negative answer. Successful lookups keep their existing per-call symlink and
// home checks. The stamp notices normal Codex creation and archiving; the TTL also notices out-of-band imports into
// old day directories, which those stamps cannot see.
func (n *Node) oldCodexTranscriptMissed(id, homes string, stamp []int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	m, ok := n.oldMisses[id]
	return ok && m.homes == homes && time.Since(m.at) < oldCodexMissFor && slices.Equal(m.stamp, stamp)
}

func (n *Node) noteOldCodexTranscriptMiss(id, homes string, stamp []int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.oldMisses == nil {
		n.oldMisses = map[string]oldCodexMiss{}
	}
	if len(n.oldMisses) >= oldCodexMissMax {
		clear(n.oldMisses)
	}
	n.oldMisses[id] = oldCodexMiss{homes: homes, stamp: slices.Clone(stamp), at: time.Now()}
}
