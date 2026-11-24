package hub

import (
	"context"
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

// With node updates on, the release's builds replace those of the platforms it carries. A build of a platform it does
// not carry (Windows next to a release published without it, as up to 0.9.1) stayed unlabelled and passed for the new
// hub's version: Redeploy sent it and the node refused it at its self-check (#12958). It goes aside, so the manager
// fetches the build of the hub's version from its release, or says there is none.
func TestUpdateNodesSetsAsideWhatTheReleaseDoesNotCarry(t *testing.T) {
	nodes := t.TempDir()
	t.Setenv("PERSONA_NODE_BINARIES", nodes)
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(nodes, "personad-linux-amd64", "v1 linux")
	write(nodes, "personad-windows-amd64.exe", "v1 windows")
	write(nodes, "personad-windows-arm64", "older windows, named before .exe was kept")
	write(nodes, "personad-darwin-arm64", "a release's build")
	write(nodes, "personad-darwin-arm64.version", "v0-202611010000\n") // labelled: says what it is, stays
	staged := t.TempDir()
	write(staged, "personad-linux-amd64", "v2 linux")
	h := newTestHub(t)
	h.version = "v2"
	h.cfg.AutoUpdateNodes = true

	if err := h.upd.updateNodes(context.Background(), staged, "v1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(nodes, "personad-linux-amd64")); string(b) != "v2 linux" {
		t.Fatalf("the release's build: %q", b)
	}
	for _, name := range []string{"personad-windows-amd64.exe", "personad-windows-arm64"} {
		if _, err := os.Stat(filepath.Join(nodes, name)); !os.IsNotExist(err) {
			t.Errorf("%s of v1 still passes for v2: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(nodes, name+".prev")); err != nil {
			t.Errorf("%s was not set aside: %v", name, err)
		}
	}
	for _, kept := range []string{"personad-darwin-arm64", "personad-darwin-arm64.version"} {
		if _, err := os.Stat(filepath.Join(nodes, kept)); err != nil {
			t.Errorf("%s: %v", kept, err)
		}
	}
}
