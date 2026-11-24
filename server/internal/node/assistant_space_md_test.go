package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// spaceMDDir is a track directory qa-plain with a SPACE.md of human text around the state block, and a clock that
// moves a second at each backup.
func spaceMDDir(t *testing.T) (dir, initial string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), ".persona", "tracks", "qa-plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial = "# QA plain\n\n## How to answer\n\nBriefly.\n\n" + spaceStateBegin + "\n" + spaceStateEnd + "\n\nAfter the block.\n"
	if _, err := prepareSpaceDir(dir, spacePrepare{Slug: "qa-plain", InitialSpaceMD: initial}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	spaceNow = func() time.Time { clock = clock.Add(time.Second); return clock }
	t.Cleanup(func() { spaceNow = time.Now })
	return dir, initial
}

func spaceHistory(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, ".space", "history"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func spaceCall(t *testing.T, dir string, c assistantwire.SpaceMDCall) assistantwire.SpaceMD {
	t.Helper()
	c.Slug = "qa-plain"
	got, err := spaceMDCall(dir, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Op, err)
	}
	return got
}

// space_state_update's write (#13472): only the bytes between the state markers change, the file before it is kept
// in .space/history/, and an oversized state or one holding a marker is refused.
func TestSpaceMDPutStateChangesOnlyTheBlock(t *testing.T) {
	dir, initial := spaceMDDir(t)
	got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet})
	if got.Content != initial || got.Digest != digestOf(initial) || got.State != "" || got.Refused != "" {
		t.Fatalf("get: %+v", got)
	}

	got = spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: "Budget: drafted.\nNext: review.\n\n"})
	want := strings.Replace(initial, spaceStateBegin+"\n", spaceStateBegin+"\nBudget: drafted.\nNext: review.\n", 1)
	b, _ := os.ReadFile(filepath.Join(dir, "SPACE.md"))
	if string(b) != want || got.Content != want || got.State != "Budget: drafted.\nNext: review." || got.Refused != "" {
		t.Fatalf("after put_state:\n%q\nanswer %+v", b, got)
	}
	before, after := initial[:strings.Index(initial, spaceStateBegin)], initial[strings.Index(initial, spaceStateEnd):]
	if !strings.HasPrefix(string(b), before) || !strings.HasSuffix(string(b), after) {
		t.Error("bytes outside the state block changed")
	}
	hist := spaceHistory(t, dir)
	if len(hist) != 1 {
		t.Fatalf("history: %v, want one backup", hist)
	}
	if kept, _ := os.ReadFile(filepath.Join(dir, ".space", "history", hist[0])); string(kept) != initial {
		t.Errorf("backup %q holds %q", hist[0], kept)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "SPACE.md")); fi.Mode().Perm() != 0o600 {
		t.Errorf("SPACE.md mode %v", fi.Mode().Perm())
	}

	// The same state again writes nothing.
	spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: "Budget: drafted.\nNext: review."})
	if hist := spaceHistory(t, dir); len(hist) != 1 {
		t.Errorf("an unchanged state was backed up: %v", hist)
	}
	// Emptied: the block is as the first SPACE.md had it.
	spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: ""})
	if b, _ := os.ReadFile(filepath.Join(dir, "SPACE.md")); string(b) != initial {
		t.Errorf("emptied state: %q", b)
	}

	for _, bad := range []struct{ state, why string }{
		{strings.Repeat("x", assistantwire.SpaceMDStateMax+1), assistantwire.SpaceMDTooLarge},
		{"ok\n" + spaceStateEnd + "\nmine", assistantwire.SpaceMDManaged},
		{"<!-- persona:managed:other:begin -->", assistantwire.SpaceMDManaged},
	} {
		before, _ := os.ReadFile(filepath.Join(dir, "SPACE.md"))
		got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: bad.state})
		after, _ := os.ReadFile(filepath.Join(dir, "SPACE.md"))
		if got.Refused != bad.why || string(before) != string(after) {
			t.Errorf("state %.40q: refused %q, want %q; changed %v", bad.state, got.Refused, bad.why, string(before) != string(after))
		}
	}
	if got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: strings.Repeat("x", assistantwire.SpaceMDStateMax)}); got.Refused != "" {
		t.Errorf("a state of exactly 2 KB: refused %q", got.Refused)
	}

	// A SPACE.md whose state block the user took out is not given one back.
	if err := os.WriteFile(filepath.Join(dir, "SPACE.md"), []byte("# Mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: "x"}); got.Refused != assistantwire.SpaceMDNoState {
		t.Errorf("no state block: refused %q", got.Refused)
	}
}

// The user's write from the apps (#13472): made against the file it was read at, and refused when it touches a managed
// block or its markers in any way.
func TestSpaceMDPutHuman(t *testing.T) {
	dir, initial := spaceMDDir(t)
	spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: "Kept by Persona."})
	cur := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet})
	block := cur.Content[strings.Index(cur.Content, spaceStateBegin) : strings.Index(cur.Content, spaceStateEnd)+len(spaceStateEnd)]

	for name, content := range map[string]string{
		"block text changed":   strings.Replace(cur.Content, "Kept by Persona.", "Kept by me.", 1),
		"block removed":        strings.Replace(cur.Content, block, "", 1),
		"begin marker removed": strings.Replace(cur.Content, spaceStateBegin, "", 1),
		"end marker changed":   strings.Replace(cur.Content, spaceStateEnd, "<!-- persona:managed:state:end  -->", 1),
		"block doubled":        cur.Content + "\n" + block + "\n",
		"marker added":         cur.Content + "\n<!-- persona:managed:notes:begin -->\n",
		"space added in block": strings.Replace(cur.Content, "Kept by Persona.", "Kept by Persona. ", 1),
	} {
		got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutHuman, Content: content, BaseDigest: cur.Digest})
		if got.Refused != assistantwire.SpaceMDManaged || got.Content != cur.Content {
			t.Errorf("%s: refused %q", name, got.Refused)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SPACE.md")); string(b) != cur.Content {
		t.Fatal("a refused write changed SPACE.md")
	}

	edited := strings.Replace(cur.Content, "Briefly.", "Briefly, with sources.", 1) + "One more rule.\n"
	got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutHuman, Content: edited, BaseDigest: cur.Digest})
	if got.Refused != "" || got.Content != edited || got.Digest != digestOf(edited) || got.State != "Kept by Persona." {
		t.Fatalf("human edit: %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SPACE.md")); string(b) != edited {
		t.Errorf("SPACE.md = %q", b)
	}
	// Two backups now: the first SPACE.md (before the state) and the one before this edit.
	if hist := spaceHistory(t, dir); len(hist) != 2 {
		t.Errorf("history: %v", hist)
	}
	if got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutHuman, Content: initial, BaseDigest: cur.Digest}); got.Refused != assistantwire.SpaceMDStale || got.Content != edited {
		t.Errorf("a stale base: %+v", got)
	}
	if got := spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutHuman, Content: strings.Repeat("x", spaceMDLimit+1), BaseDigest: got.Digest}); got.Refused != assistantwire.SpaceMDTooLarge {
		t.Errorf("a long SPACE.md: %+v", got.Refused)
	}
}

// The history keeps the latest 20 files; the method only works in a track's own directory, on a plain SPACE.md.
func TestSpaceMDHistoryAndPlace(t *testing.T) {
	dir, _ := spaceMDDir(t)
	for i := range 25 {
		spaceCall(t, dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: strings.Repeat("v", i+1)})
	}
	hist := spaceHistory(t, dir)
	if len(hist) != spaceHistoryKeep {
		t.Fatalf("history: %d files, want %d", len(hist), spaceHistoryKeep)
	}
	// The oldest went: the newest backup holds the state of the 24th write.
	if b, _ := os.ReadFile(filepath.Join(dir, ".space", "history", hist[len(hist)-1])); !strings.Contains(string(b), "\n"+strings.Repeat("v", 24)+"\n") {
		t.Errorf("newest backup: %q", b)
	}

	if nodeMethods.handlers[assistantwire.DeviceAssistantSpaceMD] == nil {
		t.Error("assistant.space_md is not in the method table")
	}
	if _, err := spaceMDCall(dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet, Slug: "qa-other"}); err == nil {
		t.Error("another track's slug: answered")
	}
	assistant := filepath.Join(filepath.Dir(filepath.Dir(dir)), "assistant")
	if _, err := spaceMDCall(assistant, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet, Slug: "assistant"}); err == nil {
		t.Error("the assistant's directory: answered")
	}
	if _, err := spaceMDCall(dir, assistantwire.SpaceMDCall{Op: "delete", Slug: "qa-plain"}); err == nil {
		t.Error("an unknown op: answered")
	}
	missing := filepath.Join(filepath.Dir(dir), "qa-missing")
	if got, err := spaceMDCall(missing, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet, Slug: "qa-missing"}); err != nil || got.Refused != assistantwire.SpaceMDMissing {
		t.Errorf("no directory: %+v %v", got, err)
	}

	elsewhere := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(elsewhere, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(dir, "SPACE.md")
	if err := os.Remove(md); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, md); err != nil {
		t.Fatal(err)
	}
	if got, err := spaceMDCall(dir, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet, Slug: "qa-plain"}); err == nil && got.Content == "secret" {
		t.Error("a SPACE.md that is a link: read through it")
	}
}
