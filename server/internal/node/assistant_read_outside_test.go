package node

import (
	"os"
	"path/filepath"
	"testing"
)

func touchFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Read, Glob and Grep outside the conversation's directory go without a card, except the health space, other spaces,
// Persona's state directory and credentials (decision rights, 2026-11-24).
func TestReadOutsideTheWorkspace(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".persona")
	cwd := filepath.Join(state, "assistant")
	for _, f := range []string{
		filepath.Join(cwd, "notes.md"),
		filepath.Join(state, "hub.json"),
		filepath.Join(state, "health", "report.pdf"),
		filepath.Join(state, "tracks", "family", "notes.md"),
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "projects", "x", "memory.md"),
		filepath.Join(home, "proj", "README.md"),
		filepath.Join(home, "proj", "src", "main.go"),
		filepath.Join(home, "proj", ".env"),
		filepath.Join(home, "clean", "a.md"),
		filepath.Join(home, "clean", "sub", "b.md"),
		filepath.Join(home, "keys", "server.pem"),
		filepath.Join(home, "ops", "launcher-settings", "x.json"),
		filepath.Join(home, "ops", "admin.cred"),
	} {
		touchFile(t, f)
	}
	states := []string{state}
	for _, c := range []struct {
		tool, path string
		want       bool
	}{
		{"Read", filepath.Join(home, "proj", "README.md"), true},
		{"Read", filepath.Join(home, "proj", "src", "main.go"), true},
		{"Read", filepath.Join(home, "proj", "missing.md"), true}, // not there yet: still a plain file
		{"Glob", filepath.Join(home, "proj"), true},               // names only
		{"Grep", filepath.Join(home, "clean"), true},
		{"Grep", filepath.Join(home, "proj", "src"), true},

		{"Grep", filepath.Join(home, "proj"), false}, // would read its .env
		{"Read", filepath.Join(home, "proj", ".env"), false},
		{"Read", filepath.Join(state, "hub.json"), false},
		{"Read", filepath.Join(state, "health", "report.pdf"), false},
		{"Read", filepath.Join(state, "tracks", "family", "notes.md"), false},
		{"Read", filepath.Join(home, ".ssh", "id_ed25519"), false},
		{"Read", filepath.Join(home, ".claude", "settings.json"), false},
		{"Read", filepath.Join(home, "keys", "server.pem"), false},
		{"Read", filepath.Join(home, "ops", "launcher-settings", "x.json"), false},
		{"Read", filepath.Join(home, "ops", "admin.cred"), false},
		{"Glob", home, false}, // the home directory holds the state directory and the credentials
		{"Grep", home, false},
		{"Glob", filepath.Join(home, ".claude"), false},
		{"Glob", state, false},

		{"Write", filepath.Join(home, "proj", "README.md"), false}, // only reads
		{"Edit", filepath.Join(home, "proj", "README.md"), false},
		{"Read", "proj/README.md", false},
	} {
		if got := readOutsideAllowed(c.tool, c.path, cwd, home, states); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.tool, c.path, got, c.want)
		}
	}
}

// A conversation of a space reads the main conversation's files by the same rules, but not another space's.
func TestReadOutsideFromASpace(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".persona")
	health := filepath.Join(state, "health")
	touchFile(t, filepath.Join(health, "a.md"))
	touchFile(t, filepath.Join(state, "tracks", "family", "notes.md"))
	touchFile(t, filepath.Join(home, "docs", "plan.md"))
	if !readOutsideAllowed("Read", filepath.Join(home, "docs", "plan.md"), health, home, []string{state}) {
		t.Error("the health space's conversation cannot read an ordinary file")
	}
	if readOutsideAllowed("Read", filepath.Join(state, "tracks", "family", "notes.md"), health, home, []string{state}) {
		t.Error("the health space's conversation read another space")
	}
}
