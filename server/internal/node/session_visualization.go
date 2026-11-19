package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// visualizationMaxBytes is the hard cap for one fragment, mirroring the Codex visualize plugin's own size contract;
// a file this large is not a visualization it would have written.
const visualizationMaxBytes = 1 << 20

// visualizationHTML reads one visualization item's HTML fragment. The marker's path is only trusted inside this Codex
// home's visualizations tree, under the directory of the session that owns the item, so a marker cannot reach
// arbitrary node files.
func (n *Node) visualizationHTML(sessionID, itemID string) (string, error) {
	f, err := n.file(sessionID)
	if err != nil {
		return "", readErr(err)
	}
	n.refresh(sessionID, f)
	it, err := f.Full(itemID)
	if err != nil {
		return "", fmt.Errorf("session %s: %w", sessionID, err)
	}
	if it.Visualization == nil {
		return "", fmt.Errorf("no visualization %s in session %s", itemID, sessionID)
	}
	home := n.codexHome()
	// Both sides are symlink-resolved before the prefix check: the home itself may be reached through a link
	// ("/home/me" -> "/data/home/me"), and the marker records the physical path Codex saw, so comparing it against
	// the unresolved root rejects every fragment on such machines.
	base, err := filepath.EvalSymlinks(filepath.Join(home, "visualizations"))
	if err != nil {
		return "", err
	}
	clean, err := filepath.EvalSymlinks(filepath.Clean(it.Visualization.Path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, clean)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("visualization path outside %s: %s", base, it.Visualization.Path)
	}
	// visualizations/<YYYY>/<MM>/<DD>/<codex session id>/<file>
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 5 || parts[4] == "" || !validID(parts[3]) {
		return "", fmt.Errorf("unexpected visualization layout: %s", rel)
	}
	if parts[3] != sessionID && parts[3] != mainThread(home, sessionID) {
		return "", fmt.Errorf("visualization session %s does not own %s", sessionID, parts[3])
	}
	st, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if st.IsDir() || st.Size() > visualizationMaxBytes {
		return "", fmt.Errorf("visualization %s is not a readable fragment under %d bytes", clean, visualizationMaxBytes)
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
