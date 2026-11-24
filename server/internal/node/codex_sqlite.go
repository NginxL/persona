package node

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// A node started at login can retain an older PATH after SQLite is installed. Keep
// PATH's explicit choice first, then use the platform's installed locations.
func codexSQLiteProgram() (string, error) {
	if path, err := exec.LookPath("sqlite3"); err == nil {
		return path, nil
	}
	return findCodexSQLite(codexSQLiteFallbackDirs())
}

var codexSQLiteFallbackDirs = defaultCodexSQLiteFallbackDirs

func findCodexSQLite(dirs []string) (string, error) {
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if !filepath.IsAbs(dir) {
			continue // Neither an empty PATH entry nor the current directory is a fallback.
		}
		if path, err := exec.LookPath(filepath.Join(dir, "sqlite3")); err == nil {
			return path, nil
		}
	}
	return "", &exec.Error{Name: "sqlite3", Err: exec.ErrNotFound}
}
