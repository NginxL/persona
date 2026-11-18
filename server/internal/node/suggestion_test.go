package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/proto"
)

// A turn that ends reads the screen and keeps Claude's grey suggestion until the agent works again or input goes in.
func TestSuggestionAfterATurn(t *testing.T) {
	screen, err := filepath.Abs("../screen/testdata/claude-2.1.312-suggestion-171.ansi")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	reads := filepath.Join(dir, "reads")
	script := "#!/bin/sh\necho x >> " + shellQuote(reads) + "\ncat " + shellQuote(screen) + "\n" // a checkout path may hold spaces
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := suggestReads
	suggestReads = []time.Duration{0, 50 * time.Millisecond}
	t.Cleanup(func() { suggestReads = old })
	n := &Node{hosts: herdrHosts(&herdr.Client{Bin: bin})}

	got := func() string { return n.noteStatus("s1", "claude", "w1:p1", proto.StatusDone) }
	if s := n.noteStatus("s1", "claude", "w1:p1", proto.StatusWorking); s != "" {
		t.Fatalf("first sight: %q", s)
	}
	if s := got(); s != "" { // the reads start; nothing yet
		t.Fatalf("at once: %q", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for got() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := got(); s != "add all three to the README" {
		t.Fatalf("suggestion %q", s)
	}
	if b, _ := os.ReadFile(reads); len(b) != 2 { // found on the first read: no more
		t.Fatalf("read the screen %d times", len(b)/2)
	}
	n.dropSuggestion("s1")
	if s := got(); s != "" {
		t.Fatalf("after a send: %q", s)
	}
	if s := n.noteStatus("s1", "claude", "w1:p1", proto.StatusWorking); s != "" {
		t.Fatalf("working: %q", s)
	}
	if s := n.noteStatus("s2", "codex", "w1:p2", proto.StatusDone); s != "" {
		t.Fatalf("codex: %q", s)
	}
}
