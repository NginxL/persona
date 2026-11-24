package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A track's directory (#13472): its folders made private, its SPACE.md written once and then the user's, read as a
// plain file only; the instructions are the manager's then SPACE.md, written again only when they change.
func TestPrepareSpaceDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".persona")
	dir := filepath.Join(root, "tracks", "qa-plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md, err := prepareSpaceDir(dir, spacePrepare{Slug: "qa-plain", InitialSpaceMD: "# QA plain\n"})
	if err != nil || md != "# QA plain\n" {
		t.Fatalf("first prepare: %q %v", md, err)
	}
	for _, sub := range append([]string{""}, spaceFolders...) {
		fi, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%q: %v %v", sub, fi, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SPACE.md"), []byte("# Mine\n\nAnswer briefly.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if md, err := prepareSpaceDir(dir, spacePrepare{Slug: "qa-plain", InitialSpaceMD: "# QA plain\n"}); err != nil || md != "# Mine\n\nAnswer briefly.\n" {
		t.Errorf("the user's SPACE.md is kept: %q %v", md, err)
	}
	if _, err := prepareSpaceDir(dir, spacePrepare{Slug: "qa-other"}); err == nil {
		t.Error("another track's slug: accepted")
	}
	if _, err := prepareSpaceDir(filepath.Join(root, "assistant"), spacePrepare{Slug: "assistant"}); err == nil {
		t.Error("the assistant's directory: accepted")
	}
	elsewhere := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(elsewhere, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "tracks", "qa-linked")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(linked, "SPACE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSpaceDir(linked, spacePrepare{Slug: "qa-linked"}); err == nil {
		t.Error("a SPACE.md that is a link: read")
	}

	got := spaceInstructions("# Baseline\n\n", "# Mine\n")
	if got != "# Baseline\n\n# Mine\n" {
		t.Errorf("instructions = %q", got)
	}
	path := filepath.Join(dir, "CLAUDE.md")
	if err := writeIfChanged(path, []byte(got)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := writeIfChanged(path, []byte(got)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("the same instructions were written again")
	}
	if err := writeIfChanged(path, []byte(got+"more\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), "more\n") {
		t.Errorf("changed instructions not written: %q", b)
	}
	long := strings.Repeat("x", spaceMDLimit+10)
	if err := os.WriteFile(filepath.Join(dir, "SPACE.md"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	if md, err := readSpaceMD(filepath.Join(dir, "SPACE.md")); err != nil || len(md) > spaceMDLimit+200 || !strings.Contains(md, "longer than 64 KB") {
		t.Errorf("a long SPACE.md: %d %v", len(md), err)
	}
}
