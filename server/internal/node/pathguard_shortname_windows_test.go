//go:build windows

package node

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Windows gives most names a short (8.3) alias too: C:\Users\EXAMPL~1 for C:\Users\Example Home, PERSON~1 for
// .persona. A path that names an entry by its alias is the same entry: the path guard judges it by its real name, and a
// write through it keeps that name (#9891).

// shortName is p as Windows names it with every alias (GetShortPathName; p must be there).
func shortName(t *testing.T, p string) string {
	t.Helper()
	in := p
	if len(p) >= windows.MAX_PATH {
		in = `\\?\` + p
	}
	from, err := windows.UTF16PtrFromString(in)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, len(in)+1)
	n, err := windows.GetShortPathName(from, &buf[0], uint32(len(buf)))
	if err != nil || int(n) >= len(buf) {
		t.Fatalf("short name of %s: %v", p, err)
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`)
}

// aliasOf is p with its last name replaced by that name's alias, its folders as written.
func aliasOf(t *testing.T, p string) string {
	t.Helper()
	return filepath.Join(filepath.Dir(p), filepath.Base(shortName(t, p)))
}

// shortNameHome is a home whose own name has an alias (returned too), with files at rel → body. It skips where the
// volume makes no aliases.
func shortNameHome(t *testing.T, files map[string]string) (home, alias string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir()) // the temp folder's own path may be written with aliases
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(base, "Example Home")
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if alias = shortName(t, home); alias == home {
		t.Skip("this volume makes no short names")
	}
	return home, alias
}

func TestShortNamesAreGuardedAsTheNamesTheyStandFor(t *testing.T) {
	deep := filepath.Join("projects", strings.Repeat("a-long-folder-name-", 8)+"x", strings.Repeat("another-long-folder-", 8)+"y")
	home, alias := shortNameHome(t, map[string]string{
		".git-credentials":                "https://u:token@example.com",
		".ssh/config":                     "Host x",
		".persona/hub.json":               `{"hub":"wss://hub.example","token":"x"}`,
		"keys/id_ed25519":                 "key",
		filepath.Join(deep, "id_ed25519"): "key",
		"Project Notes/meeting notes.md":  "notes\n",
	})
	n := &Node{home: home, logger: log.New(io.Discard, "", 0)}
	if len(filepath.Join(home, deep)) < windows.MAX_PATH {
		t.Fatalf("the deep folder is only %d characters", len(filepath.Join(home, deep)))
	}

	secrets := []string{
		filepath.Join(alias, ".git-credentials"),                         // the home by its alias
		aliasOf(t, filepath.Join(home, ".git-credentials")),              // the file by its alias
		filepath.Join(alias, ".ssh", "config"),                           // a secret folder in the home's alias
		filepath.Join(aliasOf(t, filepath.Join(home, ".ssh")), "config"), // the secret folder by its alias
		filepath.Join(aliasOf(t, filepath.Join(home, ".persona")), "hub.json"),
		aliasOf(t, filepath.Join(home, "keys", "id_ed25519")), // a key, wherever it is, by its alias
		aliasOf(t, filepath.Join(home, deep, "id_ed25519")),   // the same past MAX_PATH
	}
	for _, p := range secrets {
		if fc, err := n.readFile(p, "", 0, false); !errors.Is(err, errOffLimits) {
			t.Errorf("file.read %s: %v (read: %v)", p, err, fc != nil)
		}
		if _, f, _, err := n.openRegular(p, ""); !errors.Is(err, errOffLimits) {
			if f != nil {
				f.Close()
			}
			t.Errorf("download %s: %v", p, err)
		}
	}
	for _, detail := range []bool{false, true} {
		for _, s := range n.statPaths(secrets, "", detail) {
			if s.Kind != "missing" {
				t.Errorf("fs.stat (detail %v) %s: %s, want missing", detail, s.Path, s.Kind)
			}
		}
	}
	for _, dir := range []string{filepath.Join(alias, ".ssh"), aliasOf(t, filepath.Join(home, ".ssh"))} {
		if _, err := n.listDir(dir, ""); !errors.Is(err, errOffLimits) {
			t.Errorf("fs.list %s: %v", dir, err)
		}
	}

	// The data folder stays read-only by its alias, and its credentials stay as they are.
	persona := aliasOf(t, filepath.Join(home, ".persona"))
	if _, err := n.makeDir(persona, "", "planted"); err == nil {
		t.Errorf("file.mkdir in %s was allowed", persona)
	}
	text := `{"hub":"wss://elsewhere.example","token":"y"}`
	if _, err := n.writeText(fileMakeParams{Path: filepath.Join(persona, "hub.json"), Text: &text, Force: true}); err == nil {
		t.Errorf("file.write to hub.json through %s was allowed", persona)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".persona", "hub.json")); err != nil || !strings.Contains(string(b), `"token":"x"`) {
		t.Errorf("hub.json is now %q (%v)", b, err)
	}
	ents, _ := os.ReadDir(filepath.Join(home, ".persona"))
	if len(ents) != 1 {
		t.Errorf("the data folder holds %d entries, want only hub.json", len(ents))
	}

	// What the guard lets through is still there by its alias.
	notes := shortName(t, filepath.Join(home, "Project Notes", "meeting notes.md"))
	if fc, err := n.readFile(notes, "", 0, false); err != nil || fc.Text != "notes\n" {
		t.Errorf("file.read %s: %v", notes, err)
	}
}

// A file in the home named through aliases (the home's own: TEMP is usually C:\Users\EXAMPL~1\AppData\Local\Temp) is
// the home's: it takes writes, and they keep its name.
func TestShortNamesWriteUnderTheNamesTheyStandFor(t *testing.T) {
	home, alias := shortNameHome(t, map[string]string{
		"Project Notes/meeting notes.md": "v1\n",
		"Project Notes/moved notes.md":   "move me\n",
		"Project Notes/copied notes.md":  "copy me\n",
		"Archive/.keep":                  "",
	})
	n := &Node{home: home, logger: log.New(io.Discard, "", 0)}
	folder := filepath.Join(home, "Project Notes")
	names := func(dir string) map[string]bool {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, e := range ents {
			out[e.Name()] = true
		}
		return out
	}

	notes := shortName(t, filepath.Join(folder, "meeting notes.md")) // every name by its alias
	text := "v2\n"
	if _, err := n.writeText(fileMakeParams{Path: notes, Text: &text, Force: true}); err != nil {
		t.Errorf("file.write %s: %v", notes, err)
	}
	if b, err := os.ReadFile(filepath.Join(folder, "meeting notes.md")); err != nil || string(b) != "v2\n" {
		t.Errorf("meeting notes.md is %q (%v), want the saved text", b, err)
	}
	if names(folder)[filepath.Base(notes)] {
		t.Errorf("the save made a file named %s", filepath.Base(notes))
	}

	created := "new\n"
	if _, err := n.writeText(fileMakeParams{Path: filepath.Join(alias, "Project Notes", "new.txt"), Text: &created, Create: true}); err != nil {
		t.Errorf("new text file in the home's alias: %v", err)
	}
	if _, err := n.makeDir(filepath.Join(alias, "Project Notes"), "", "Drafts"); err != nil {
		t.Errorf("new folder in the home's alias: %v", err)
	}

	archive := filepath.Join(home, "Archive")
	if _, err := n.moveFiles([]string{aliasOf(t, filepath.Join(folder, "moved notes.md"))}, "", archive, ""); err != nil {
		t.Errorf("file.move: %v", err)
	}
	if _, err := n.copyFiles(context.Background(), []string{aliasOf(t, filepath.Join(folder, "copied notes.md"))}, "", archive, "", nil); err != nil {
		t.Errorf("file.copy: %v", err)
	}
	got := names(archive)
	for _, want := range []string{"moved notes.md", "copied notes.md"} {
		if !got[want] {
			t.Errorf("Archive holds %v, want %s by its own name", got, want)
		}
	}
}
