//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A capture filed into a track (#13502): written into that track's own inbox/ only, atomically and privately; a retry
// of the same file is fine, another file of the same name is not overwritten; a link anywhere on the way is refused.
func TestWriteTrackInbox(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".persona")
	dir := filepath.Join(root, "tracks", "qa-plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := []byte("---\nid: cap_qa\n---\n\nqa: a placeholder note\n")
	rel, err := writeTrackInbox(dir, "qa-plain", "2026-11-22-cap_qa.md", b)
	if err != nil || rel != filepath.Join("inbox", "2026-11-22-cap_qa.md") {
		t.Fatalf("write: %q %v", rel, err)
	}
	path := filepath.Join(dir, rel)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(b) {
		t.Fatalf("file = %q %v", got, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "inbox")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("inbox mode: %v %v", fi, err)
	}
	if _, err := writeTrackInbox(dir, "qa-plain", "2026-11-22-cap_qa.md", b); err != nil {
		t.Errorf("a retry of the same file: %v", err)
	}
	if _, err := writeTrackInbox(dir, "qa-plain", "2026-11-22-cap_qa.md", []byte("other")); err == nil {
		t.Error("another file of the same name: overwritten")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "inbox"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file left: %s", e.Name())
		}
	}

	if _, err := writeTrackInbox(dir, "qa-other", "x.md", b); err == nil {
		t.Error("another track's slug: written")
	}
	if _, err := writeTrackInbox(filepath.Join(root, "assistant"), "assistant", "x.md", b); err == nil {
		t.Error("the assistant's directory: written")
	}
	if _, err := writeTrackInbox(filepath.Join(root, "tracks", "qa-missing"), "qa-missing", "x.md", b); err == nil {
		t.Error("a track not prepared: written")
	}

	// inbox/ a link elsewhere: refused, nothing written there.
	elsewhere := t.TempDir()
	linked := filepath.Join(root, "tracks", "qa-linked")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(linked, "inbox")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTrackInbox(linked, "qa-linked", "x.md", b); err == nil {
		t.Error("an inbox that is a link: written")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("written through the link: %v", entries)
	}
	// The file's name a link elsewhere: refused, the target untouched.
	target := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "inbox", "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTrackInbox(dir, "qa-plain", "linked.md", b); err == nil {
		t.Error("a file name that is a link: written")
	}
	if got, _ := os.ReadFile(target); string(got) != "secret" {
		t.Errorf("the link's target changed: %q", got)
	}
	// The track's directory itself a link: refused.
	realDir := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "tracks", "qa-dirlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTrackInbox(filepath.Join(root, "tracks", "qa-dirlink"), "qa-dirlink", "x.md", b); err == nil {
		t.Error("a track directory that is a link: written")
	}

	for _, name := range []string{"../x.md", ".hidden.md", "x.txt", "a/b.md", ""} {
		if trackInboxName.MatchString(name) {
			t.Errorf("name %q accepted", name)
		}
	}
}
