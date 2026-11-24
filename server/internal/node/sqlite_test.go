package node

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// sqlite3WithJSON is the sqlite3 on PATH when it can print JSON rows: the Messages reader asks it for "-json",
// which SQLite has since 3.33 (macOS's own from macOS 12 on). Elsewhere the test that reads Messages skips instead
// of failing on an option it does not know.
func sqlite3WithJSON(t *testing.T) string {
	t.Helper()
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("no sqlite3 here (macOS has one): run these on a Mac")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sqlite, "-json", ":memory:", "SELECT 1 AS n").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"n"`) {
		t.Skipf("sqlite3 at %s has no -json (SQLite 3.33 or newer; macOS's own has it): %s", sqlite, strings.TrimSpace(string(out)))
	}
	return sqlite
}
