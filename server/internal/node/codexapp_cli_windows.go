//go:build windows

package node

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// bundledCodexAppCLI finds the native CLI installed with the desktop app. Builds
// live in hash-named directories; use the newest executable when old builds remain.
func bundledCodexAppCLI() string {
	local := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(local) {
		return ""
	}
	root := filepath.Join(local, "OpenAI", "Codex", "bin")
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var best string
	var newest time.Time
	for _, entry := range entries {
		if !entry.IsDir() || !codexAppBuildDirectory(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name(), "codex.exe")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(newest) || info.ModTime().Equal(newest) && path < best {
			best, newest = path, info.ModTime()
		}
	}
	return best
}

func codexAppBuildDirectory(name string) bool {
	return len(name) == 16 && strings.Trim(name, "0123456789abcdefABCDEF") == ""
}
