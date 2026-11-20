package hub

import (
	"os"
	"path/filepath"
	"testing"
)

// A Windows node package retains .exe when a hub update stages, sets aside or retains its node downloads. Losing that
// suffix here would leave the executable outside manager lookup even though manifest and download handling were right.
func TestWindowsNodePackagesSurviveHubUpdateRetention(t *testing.T) {
	dir := t.TempDir()
	name := "personad-windows-amd64.exe"
	if !nodeBuildName.MatchString(name) {
		t.Fatal("the hub does not recognize a Windows node executable")
	}
	if nodeBuildName.MatchString("personad-windows-amd64.exe.gz") || nodeBuildName.MatchString("personad-windows-amd64.exe.prev") {
		t.Fatal("a compressed or retired artifact is not an installed node executable")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("old Windows build"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved, err := setAsideOldNodes(dir)
	if err != nil || len(moved) != 1 || moved[0] != name {
		t.Fatalf("setAsideOldNodes = %v, %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".prev")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("new Windows build"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := t.TempDir()
	if err := os.WriteFile(filepath.Join(staged, "personad-linux-amd64"), []byte("another platform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := labelUnstagedNodes(dir, staged, "previous-version"); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(dir, name+".version"))
	if err != nil || string(marker) != "previous-version\n" {
		t.Fatalf("Windows node version marker = %q, %v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "personad-linux-amd64.version")); !os.IsNotExist(err) {
		t.Fatalf("a platform the new release supplies must not retain the old version: %v", err)
	}
}
