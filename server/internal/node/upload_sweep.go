package node

import (
	"os"
	"path/filepath"
	"time"
)

// Files pasted or dropped into a web terminal (dest=terminal, docs/integrations/terminal.md "Pasting images / files") live in
// <node dir>/uploads/terminal/<YYYY-MM-DD>/, apart from the chat's images (uploads/<date>, saveImages) so a sweep
// of one never touches the other.

// terminalUploadKeep is how long a pasted file stays on the machine: long enough for the agent to read it and for a
// resumed session to look at it again.
const terminalUploadKeep = 7 * 24 * time.Hour

const uploadDayLayout = "2006-01-02"

// fileUploadDir is where a file a task's Agent handed to its conversation goes (dest=file): uploads/files/<date>/,
// apart from both the terminal pastes and the chat's images (uploads/<date>, saveImages). It is not swept: a
// conversation may name the file in a message the user opens weeks later.
func fileUploadDir(nodeDir string, now time.Time) string {
	return filepath.Join(nodeDir, "uploads", "files", now.Format(uploadDayLayout))
}

func terminalUploadRoot(nodeDir string) string {
	return filepath.Join(nodeDir, "uploads", "terminal")
}

func terminalUploadDir(nodeDir string, now time.Time) string {
	return filepath.Join(terminalUploadRoot(nodeDir), now.Format(uploadDayLayout))
}

// sweepTerminalUploads removes the day directories older than terminalUploadKeep; it returns how many it removed.
// Only directories whose name is a date are touched, so nothing else a user put there goes.
func sweepTerminalUploads(nodeDir string, now time.Time) int {
	root := terminalUploadRoot(nodeDir)
	ents, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	cutoff := now.Add(-terminalUploadKeep)
	removed := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		day, err := time.ParseInLocation(uploadDayLayout, e.Name(), now.Location())
		if err != nil || !day.Add(24*time.Hour).Before(cutoff) { // the whole day is older than the cutoff
			continue
		}
		if os.RemoveAll(filepath.Join(root, e.Name())) == nil {
			removed++
		}
	}
	return removed
}
