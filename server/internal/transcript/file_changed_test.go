package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A watch stats its transcript on every tick (Changed) and reads it only when that says so, and a Refresh with nothing
// new answers from a stat too: both must see what a read would. New lines, a file replaced by another of the same
// size, and a file gone are changes; a file at rest is not, and Refreshing it keeps the span and history.
func TestChangedSeesWhatRefreshWould(t *testing.T) {
	dir := t.TempDir()
	one := `{"type":"user","uuid":"u1","message":{"role":"user","content":"one"}}`
	two := `{"type":"user","uuid":"u2","message":{"role":"user","content":"two"}}`
	path := writeFile(t, dir, []string{one}, "")
	f := NewFile(path)
	if !f.Changed() {
		t.Fatal("a file not read yet is a change")
	}
	_, first, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	if f.Changed() {
		t.Fatal("a file read to its end is no change")
	}
	fresh, rest, err := f.RefreshSpan()
	if err != nil || len(fresh) != 0 || rest != (Span{first.History, 1, 1}) {
		t.Fatalf("a refresh at rest = %v, %+v, %v; want nothing, {%s 1 1}", fresh, rest, err, first.History)
	}

	writeFile(t, dir, []string{one, two}, "")
	if !f.Changed() {
		t.Fatal("an appended line is a change")
	}
	if fresh, _ := f.Refresh(); len(fresh) != 1 || fresh[0].Text != "two" {
		t.Fatalf("want the appended line, got %+v", fresh)
	}

	// Another file of the same size put in its place (a rewrite through a rename): read again from the start.
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(other, []byte(two+"\n"+one+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if inodeOf(t, path) != 0 {
		if !f.Changed() {
			t.Fatal("a file replaced by one of the same size is a change")
		}
		if fresh, _ := f.Refresh(); len(fresh) != 2 || fresh[0].Text != "two" || fresh[0].Seq != 1 {
			t.Fatalf("want the replacement read from its start, got %+v", fresh)
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !f.Changed() {
		t.Fatal("a file gone is a change (Refresh says why)")
	}
	if _, err := f.Refresh(); err == nil {
		t.Fatal("want an error for the file gone")
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, err := statTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	return inode(st)
}

// Rebuilt desktop histories can preserve their first line and be the same size or larger.
// Each replacement must discard old messages and begin a new history from line one.
func TestTranscriptReplacementStartsNewHistory(t *testing.T) {
	for _, larger := range []bool{false, true} {
		name := "same size"
		if larger {
			name = "larger"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			first := `{"type":"user","uuid":"u1","message":{"role":"user","content":"one"}}`
			old := `{"type":"user","uuid":"u2","message":{"role":"user","content":"old"}}`
			next := `{"type":"user","uuid":"u3","message":{"role":"user","content":"new"}}`
			path := writeFile(t, dir, []string{first, old}, "")
			f := NewFile(path)
			_, before, err := f.RefreshSpan()
			if err != nil {
				t.Fatal(err)
			}
			replacement := first + "\n" + next + "\n"
			wantLines := 2
			if larger {
				replacement += `{"type":"user","uuid":"u4","message":{"role":"user","content":"more"}}` + "\n"
				wantLines++
			}
			other := filepath.Join(dir, "replacement.jsonl")
			if err := os.WriteFile(other, []byte(replacement), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
			if !f.Changed() {
				t.Fatal("replacement was not detected")
			}
			fresh, after, err := f.RefreshSpan()
			if err != nil || after.History == before.History || after.From != 0 || after.To != wantLines {
				t.Fatalf("replacement span = %+v, %v; before = %+v", after, err, before)
			}
			if len(fresh) != wantLines || fresh[0].Seq != 1 || fresh[1].Text != "new" {
				t.Fatalf("replacement messages = %+v", fresh)
			}
			page, _, span := f.Page(0, 10)
			if len(page) != wantLines || page[1].Text != "new" || span.History != after.History {
				t.Fatalf("replacement page = %+v, %+v", page, span)
			}
			if f.Changed() {
				t.Fatal("replacement should be unchanged after it has been read")
			}
		})
	}
}

func TestTranscriptPageDoesNotMixReplacedHistory(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	f, _ := windowedClaude(t, 0)
	base, history := f.base, f.history()
	data, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(f.Path), "replacement.jsonl")
	if err := os.WriteFile(other, bytes.ReplaceAll(data, []byte("answer"), []byte("change")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, f.Path); err != nil {
		t.Fatal(err)
	}
	// A page requested before the next refresh must not read new messages using old offsets.
	if page, _, _ := f.Page(base+1, 10); len(page) != 0 || f.base != base {
		t.Fatalf("stale page read replacement messages: %+v, base %d to %d", page, base, f.base)
	}
	if _, span, err := f.RefreshSpan(); err != nil || span.History == history {
		t.Fatalf("refresh did not replace the window: %+v, %v", span, err)
	}
	page, _, _ := f.Page(0, 10)
	if len(page) == 0 {
		t.Fatal("replacement page is empty")
	}
	for _, item := range page {
		if !bytes.HasPrefix([]byte(item.Text), []byte("change ")) {
			t.Fatalf("replacement page retained an old message: %+v", item)
		}
	}
}
