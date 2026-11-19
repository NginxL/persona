package node

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCopyIntoAFolder(t *testing.T) {
	n, home := testNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "a")
	makeFile(t, filepath.Join(home, "proj", "src", "b.go"), "package b")
	makeFile(t, filepath.Join(home, "proj", "src", "id_rsa"), "secret")
	if err := os.Chmod(filepath.Join(home, "proj", "src", "b.go"), 0o751); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("b.go", filepath.Join(home, "proj", "src", "link")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/a.txt", "~/proj/src"}, "dir": "~/dest"})
	if err != nil {
		t.Fatal(err)
	}
	copied := out["copied"].([]any)
	if len(copied) != 2 || copied[1].(map[string]any)["to"] != "~/dest/src" {
		t.Fatalf("copied %v", copied)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "dest", "src", "b.go")); string(b) != "package b" {
		t.Fatalf("b.go holds %q", b)
	}
	if st, _ := os.Stat(filepath.Join(home, "dest", "src", "b.go")); runtime.GOOS != "windows" && st.Mode().Perm() != 0o751 {
		t.Fatalf("mode %o, want 751", st.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(home, "dest", "src", "id_rsa")); !os.IsNotExist(err) {
		t.Fatal("a key was copied")
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(home, "dest", "src", "link")); err != nil || target != "b.go" {
			t.Fatalf("the link is %q, %v", target, err)
		}
	}
	// The originals stay.
	if _, err := os.Stat(filepath.Join(home, "proj", "src", "b.go")); err != nil {
		t.Fatal("the source went away")
	}

	// A taken name: refused before anything is copied, unless keep or replace.
	makeFile(t, filepath.Join(home, "proj", "c.txt"), "c")
	_, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/c.txt", "~/proj/a.txt"}, "dir": "~/dest"})
	wantCode(t, err, proto.CodeExists)
	if _, err := os.Stat(filepath.Join(home, "dest", "c.txt")); !os.IsNotExist(err) {
		t.Fatal("part of a refused copy was made")
	}
	out, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/a.txt"}, "dir": "~/dest", "conflict": "keep"})
	if err != nil || out["copied"].([]any)[0].(map[string]any)["to"] != "~/dest/a 2.txt" {
		t.Fatalf("keep = %v, %v", out, err)
	}
	skipOnRecycleBin(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "new a")
	if _, err := makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/a.txt"}, "dir": "~/dest", "conflict": "replace"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "dest", "a.txt")); string(b) != "new a" {
		t.Fatalf("replace left %q", b)
	}
	// A folder is never replaced.
	_, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/src"}, "dir": "~/dest", "conflict": "replace"})
	wantCode(t, err, proto.CodeExists)
}

func TestDuplicateAndLimits(t *testing.T) {
	n, home := testNode(t)
	makeFile(t, filepath.Join(home, "proj", "notes.md"), "n")
	makeFile(t, filepath.Join(home, "proj", "sub", "x"), "x")
	out, err := makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/notes.md", "~/proj/sub"}, "dir": "~/proj", "conflict": "keep"})
	if err != nil {
		t.Fatal(err)
	}
	got := out["copied"].([]any)
	if got[0].(map[string]any)["to"] != "~/proj/notes 2.md" || got[1].(map[string]any)["to"] != "~/proj/sub 2" {
		t.Fatalf("duplicates named %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "proj", "sub 2", "x")); string(b) != "x" {
		t.Fatal("the folder's copy is not whole")
	}
	_, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj"}, "dir": "~/proj/sub", "conflict": "keep"})
	if err == nil {
		t.Fatal("a folder was copied into itself")
	}
	if err := os.MkdirAll(filepath.Join(home, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/notes.md"}, "dir": "~/Library"})
	wantCode(t, err, proto.CodeReadOnly)
	_, err = makeCall(t, n, "file.copy", map[string]any{"paths": []string{"~/proj/nope"}, "dir": "~/proj"})
	wantCode(t, err, proto.CodeNotFound)
}
