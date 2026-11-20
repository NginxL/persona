package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A private profile whose projects directory is a junction to the default profile's (how a Windows user shares one
// sign-in, settings and skills between Persona's conversations and their own) holds one memory, not a copy of it.
// filepath.EvalSymlinks does not resolve a junction, so only os.SameFile sees that the two paths are one (#11277).
func TestMemoryCopyThroughAJunctionIsNotMerged(t *testing.T) {
	n := newOneMemoryNode(t)
	writeNote(t, n.shared, "a.md", "a\n", time.Time{})
	if err := os.MkdirAll(n.privateProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	link, target := filepath.Join(n.privateProfile, "projects"), filepath.Join(n.home, ".claude", "projects")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("no junctions here: %v: %s", err, out)
	}
	if got := n.memoryCopies(memoryKey(n.cwd), n.shared); len(got) != 0 {
		t.Fatalf("the memory is listed as its own copy: %v", got)
	}
	before := dirState(t, n.shared)
	n.memoryForUse(n.cwd)
	if !sameState(before, dirState(t, n.shared)) || len(mergeRunDirs(t, n)) != 0 {
		t.Error("the memory was merged with itself")
	}
}
