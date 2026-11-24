package transcript

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsTranscriptIdentityAcrossAppendAndMove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := inodeOf(t, path)
	if before == 0 {
		t.Fatal("Windows transcript has no file identity")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("next\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := inodeOf(t, path); got != before {
		t.Fatalf("append changed identity: %x to %x", before, got)
	}
	moved := filepath.Join(dir, "archived.jsonl")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if got := inodeOf(t, moved); got != before {
		t.Fatalf("move changed identity: %x to %x", before, got)
	}
}

func TestWindowsTranscriptReaderAllowsMove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	before, err := statOpenedTranscript(reader)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.jsonl")
	if err := os.WriteFile(replacement, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Windows permits moving a file with an open delete-sharing reader. MoveFileEx
	// cannot overwrite an open destination, so move it aside before filling its path.
	if err := os.Rename(path, filepath.Join(dir, "retired.jsonl")); err != nil {
		t.Fatalf("an open transcript reader prevented a move: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	current, err := statTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := statOpenedTranscript(reader)
	if err != nil {
		t.Fatal(err)
	}
	if inode(before) == 0 || inode(before) != inode(pinned) || inode(before) == inode(current) {
		t.Fatalf("identities before/pinned/current = %x/%x/%x", inode(before), inode(pinned), inode(current))
	}
	old, err := io.ReadAll(reader)
	if err != nil || string(old) != "old\n" {
		t.Fatalf("the open reader lost its original contents: %q, %v", old, err)
	}
}

func TestWindowsTranscriptLongAndUnicodePaths(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 280 {
		dir = filepath.Join(dir, strings.Repeat("nested", 10))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "history-\u4f1a\u8bdd.jsonl")
	if err := os.WriteFile(path, []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := inodeOf(t, path)
	t.Chdir(dir)
	for _, name := range []string{path, filepath.Base(path)} {
		reader, err := openTranscript(name)
		if err != nil {
			t.Fatal(err)
		}
		st, err := statOpenedTranscript(reader)
		if err != nil {
			reader.Close()
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || string(data) != "history\n" || inode(st) != before || inodeOf(t, name) != before {
			t.Fatalf("long path read: %q, identity %x (want %x), %v", data, inode(st), before, err)
		}
	}
}
