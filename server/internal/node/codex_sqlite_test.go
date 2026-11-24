package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sqliteTestName() string {
	if runtime.GOOS == "windows" {
		return "sqlite3.exe"
	}
	return "sqlite3"
}

func sqliteFallbackForTest(t *testing.T, dirs ...string) {
	t.Helper()
	old := codexSQLiteFallbackDirs
	codexSQLiteFallbackDirs = func() []string { return dirs }
	t.Cleanup(func() { codexSQLiteFallbackDirs = old })
}

func TestCodexSQLiteRespectsPathBeforeFallback(t *testing.T) {
	preferred, fallback := t.TempDir(), t.TempDir()
	for _, dir := range []string{preferred, fallback} {
		if err := os.WriteFile(filepath.Join(dir, sqliteTestName()), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", preferred)
	sqliteFallbackForTest(t, fallback)
	path, err := codexSQLiteProgram()
	if err != nil || path != filepath.Join(preferred, sqliteTestName()) {
		t.Fatalf("SQLite = %q, %v; want PATH's executable", path, err)
	}
}

func TestCodexSQLiteFallbackDoesNotSearchRelativeDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(sqliteTestName(), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	sqliteFallbackForTest(t, "", ".", "not-an-absolute-folder")
	if path, err := codexSQLiteProgram(); path != "" || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("SQLite = %q, %v; a working-directory executable must not be used", path, err)
	}
}

// Exercise the real state and history queries with no SQLite on PATH. The fallback
// references the installed executable; all databases and messages are synthetic.
func TestCodexSQLiteFallbackReadsStateAndHistory(t *testing.T) {
	sqlite := sqlite3WithJSON(t)
	home := t.TempDir()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Fallback chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "turn-1", status: "completed", ordinal: 0,
		items: [][2]string{userItem("u1", "Read the history"), answerItem("a1", "History is available.")}}})
	before := map[string][]byte{}
	for _, name := range []string{"state_5.sqlite", "thread_history_1.sqlite"} {
		b, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = b
	}
	bin := filepath.Join(t.TempDir(), "Installed Tools")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(bin, sqliteTestName())
	if runtime.GOOS == "windows" {
		executable, err := os.ReadFile(sqlite)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(installed, executable, 0o700); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(sqlite, installed); err != nil {
		// Keep macOS system executables at their signed location while exercising fallback lookup.
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	sqliteFallbackForTest(t, bin)
	rows, err := readCodexAppThreads(context.Background(), filepath.Join(home, "state_5.sqlite"), []string{appChat})
	if err != nil || len(rows) != 1 || rows[appChat].Title != "Fallback chat" {
		t.Fatalf("state read: %v, %v", rows, err)
	}
	output := filepath.Join(t.TempDir(), "history.jsonl")
	if _, err := writeCodexAppHistory(context.Background(), filepath.Join(home, "thread_history_1.sqlite"), rows[appChat], output, codexHistoryMark{Turn: -1}, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(b), "Read the history") || !strings.Contains(string(b), "History is available.") {
		t.Fatalf("history read: %q, %v", b, err)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("reading changed source database %s: %v", name, err)
		}
	}
}
