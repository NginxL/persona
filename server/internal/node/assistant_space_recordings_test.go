package node

import (
	"os"
	"path/filepath"
	"testing"
)

// A recording saved into a track (#13501): written into the track's recordings/ under the manager's name only, made
// private, rewritten in place, removed on request; never into another track's directory, nor through a link.
func TestSpaceRecordingIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".persona")
	dir := filepath.Join(root, "tracks", "qa-plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "2026-11-22-0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80.md"
	p := spaceRecordingParams{Dir: "unused", Slug: "qa-plain", Name: name, Content: "# Example\n\n[0:00] hello\n"}
	if _, err := spaceRecordingIn(dir, p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "recordings", name)
	b, err := os.ReadFile(path)
	if err != nil || string(b) != p.Content {
		t.Fatalf("written: %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "recordings")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("recordings/: %v %v", fi, err)
	}
	p.Content = "# Example\n\n[0:00] hello again\n"
	if _, err := spaceRecordingIn(dir, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != p.Content {
		t.Errorf("rewritten: %q", b)
	}
	other := p
	other.Slug = "qa-finance"
	if _, err := spaceRecordingIn(dir, other); err == nil {
		t.Error("another track's slug: accepted")
	}
	if _, err := spaceRecordingIn(filepath.Join(root, "assistant"), spaceRecordingParams{Slug: "qa-plain", Name: name}); err == nil {
		t.Error("the assistant's directory: accepted")
	}
	p.Remove = true
	if out, err := spaceRecordingIn(dir, p); err != nil || out["removed"] != true {
		t.Fatalf("remove: %v %v", out, err)
	}
	if out, err := spaceRecordingIn(dir, p); err != nil || out["removed"] != false {
		t.Errorf("remove again: %v %v", out, err)
	}

	linked := filepath.Join(root, "tracks", "qa-linked")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(linked, "recordings")); err != nil {
		t.Fatal(err)
	}
	if _, err := spaceRecordingIn(linked, spaceRecordingParams{Slug: "qa-linked", Name: name, Content: "x"}); err == nil {
		t.Error("recordings/ that is a link: written through")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("written outside the track: %v", entries)
	}

	for _, bad := range []string{"../x.md", "notes.md", "2026-11-22-0B6F1C9E-3A52-4D1E-9A8F-2C4B5D6E7F80.md"} {
		if spaceRecordingName.MatchString(bad) {
			t.Errorf("name %q accepted", bad)
		}
	}
}
