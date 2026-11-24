package node

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

func (n *Node) dirs(path string) (*proto.DirList, error) {
	if path == "" {
		path = n.home
	}
	path = filepath.Clean(proto.ExpandHome(path, n.home))
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	res := &proto.DirList{Path: path, Parent: directoryParent(path), Entries: []proto.DirEntry{}, Recent: n.recentDirs()}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		if strings.Contains(full, "Mobile Documents") { // never touch iCloud Drive: it hangs on locked Macs
			continue
		}
		_, gerr := os.Stat(filepath.Join(full, ".git"))
		res.Entries = append(res.Entries, proto.DirEntry{Name: name, Path: full, IsGit: gerr == nil})
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		return strings.ToLower(res.Entries[i].Name) < strings.ToLower(res.Entries[j].Name)
	})
	return res, nil
}

// directoryParent stops browsing at a filesystem root, including Windows drives and UNC shares.
func directoryParent(path string) string {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return parent
}

// dirsByUse is every working directory a session on this node is known to have run in, with when it was last used
// (its newest transcript, deleted ones included); a running agent's without a transcript yet has the zero time.
// Nothing is read from disk.
func (n *Node) dirsByUse() map[string]time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	last := map[string]time.Time{}
	for _, m := range n.metas {
		if m.cwd != "" && m.mtime.After(last[m.cwd]) {
			last[m.cwd] = m.mtime
		}
	}
	for dir, at := range n.goneDirs { // transcripts pruned since they were deleted (meta_prune.go)
		if at.After(last[dir]) {
			last[dir] = at
		}
	}
	for _, a := range n.agents {
		if _, ok := last[a.Cwd]; a.Cwd != "" && !ok {
			last[a.Cwd] = time.Time{} // running but no transcript yet: listed, oldest
		}
	}
	return last
}

// recentDirs are the directories New Agent offers as recently used: the 15 most recently used that still exist, the
// assistant's aside. A directory reached by more than one path (a symlinked home, a bind mount, another letter case on
// a case-insensitive disk) is one entry, under the path it was used by last and with that time (#8909).
func (n *Node) recentDirs() []proto.RecentDir {
	last := n.dirsByUse()
	paths := make([]string, 0, len(last))
	for p := range last {
		if !identity.IsAssistantDir(p) {
			paths = append(paths, p)
		}
	}
	slices.SortFunc(paths, func(a, b string) int {
		if c := last[b].Compare(last[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var out []proto.RecentDir
	var kept []os.FileInfo
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() || slices.ContainsFunc(kept, func(k os.FileInfo) bool { return os.SameFile(k, st) }) {
			continue
		}
		kept = append(kept, st)
		out = append(out, proto.RecentDir{Path: p, Name: filepath.Base(p), LastUsed: last[p].UTC()})
		if len(out) == 15 {
			break
		}
	}
	return out
}
