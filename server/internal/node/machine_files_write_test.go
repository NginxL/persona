package node

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func assertRPCCode(t *testing.T, name string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected rpc.Error %q, got nil", name, want)
	}
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != want {
		t.Fatalf("%s: got %v, want rpc.Error with code %q", name, err, want)
	}
}

func countTempArtifacts(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var leftover []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".persona-mf") {
			leftover = append(leftover, e.Name())
		}
	}
	return leftover
}

func TestMachineFilesWriteCASAtomicModeAndGuards(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(home, ".ssh"), filepath.Join(home, ".persona")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("line1\nline2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode passes through the umask (077 on some CI machines leaves 0600): set the mode the save must keep.
	if err := os.Chmod(notes, 0o640); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, ".env")
	if err := os.WriteFile(envFile, []byte("TOKEN=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binFile := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(binFile, []byte{0x89, 'P', 'N', 'G', 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	bigFile := filepath.Join(root, "oversized.txt")
	if err := os.WriteFile(bigFile, bytes.Repeat([]byte("a"), proto.MachineFileEditMaxBytes+64), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "escape.txt")
	if err := os.WriteFile(outsideFile, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hasSymlinks := os.Symlink(outsideFile, filepath.Join(root, "escape-link.txt")) == nil

	n := &Node{home: home}

	// Preview supplies editable flag + SHA256 + mtime for CAS.
	prev, err := n.machineFilePreview(proto.MachineFilePreviewRequest{Path: notes, Root: root})
	if err != nil {
		t.Fatalf("preview notes: %v", err)
	}
	if !prev.Editable || prev.SHA256 == "" || prev.ModTime.IsZero() {
		t.Fatalf("expected editable preview with sha256+mtime, got %+v", prev)
	}

	// Valid CAS save writes atomically, preserves mode, leaves no temp file, and advances CAS metadata.
	saved, err := n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             notes,
		Root:             root,
		Text:             "line1\nline2-edited\n",
		ExpectedMtime:    prev.ModTime,
		ExpectedSHA256:   prev.SHA256,
		ExpectedRevision: prev.Revision,
	})
	if err != nil {
		t.Fatalf("CAS write: %v", err)
	}
	if saved.SHA256 == prev.SHA256 || saved.Revision == prev.Revision {
		t.Fatalf("expected updated sha256 and revision after save: prev=%+v saved=%+v", prev, saved)
	}
	got, err := os.ReadFile(notes)
	if err != nil || string(got) != "line1\nline2-edited\n" {
		t.Fatalf("saved content mismatch: %q err=%v", string(got), err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(notes)
		if err != nil || st.Mode().Perm() != 0o640 {
			t.Fatalf("expected preserved mode 0640, got %o (err=%v)", st.Mode().Perm(), err)
		}
	}
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("leftover temp files after atomic save: %v", left)
	}

	// Stale CAS (old mtime/sha256) rejected with CodeFileChanged and preserves current disk contents.
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           notes,
		Root:           root,
		Text:           "stale overwrite\n",
		ExpectedMtime:  prev.ModTime,
		ExpectedSHA256: prev.SHA256,
	})
	assertRPCCode(t, "stale CAS write", err, proto.CodeFileChanged)
	got, _ = os.ReadFile(notes)
	if string(got) != "line1\nline2-edited\n" {
		t.Fatalf("stale write clobbered file: %q", string(got))
	}

	// External mtime-only touch also rejected by strict mtime + SHA-256 CAS.
	touchedTime := saved.ModTime.Add(5 * time.Second)
	if err := os.Chtimes(notes, touchedTime, touchedTime); err != nil {
		t.Fatal(err)
	}
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           notes,
		Root:           root,
		Text:           "after external touch\n",
		ExpectedMtime:  saved.ModTime,
		ExpectedSHA256: saved.SHA256,
	})
	assertRPCCode(t, "mtime mismatch CAS write", err, proto.CodeFileChanged)

	// Sensitive file requires explicit ConfirmSensitive=true.
	envPrev, err := n.machineFilePreview(proto.MachineFilePreviewRequest{Path: envFile, Root: root})
	if err != nil || !envPrev.Sensitive || envPrev.SensitiveReason != "env" {
		t.Fatalf("expected sensitive .env preview, got %+v err=%v", envPrev, err)
	}
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           envFile,
		Root:           root,
		Text:           "TOKEN=new\n",
		ExpectedMtime:  envPrev.ModTime,
		ExpectedSHA256: envPrev.SHA256,
	})
	assertRPCCode(t, "sensitive write without confirm", err, proto.CodeMachineFileSensitiveConfirm)
	if _, err := n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             envFile,
		Root:             root,
		Text:             "TOKEN=new\n",
		ExpectedMtime:    envPrev.ModTime,
		ExpectedSHA256:   envPrev.SHA256,
		ConfirmSensitive: true,
	}); err != nil {
		t.Fatalf("sensitive write with confirm: %v", err)
	}

	// Binary file & NUL byte payload refused.
	binSt, _ := os.Stat(binFile)
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           binFile,
		Root:           root,
		Text:           "overwrite binary",
		ExpectedMtime:  binSt.ModTime().UTC(),
		ExpectedSHA256: sha256HexBytes([]byte{0x89, 'P', 'N', 'G', 0x00, 0x01}),
	})
	assertRPCCode(t, "edit binary file", err, proto.CodeMachineFileBinary)
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           notes,
		Root:           root,
		Text:           "bad\x00binary",
		ExpectedMtime:  touchedTime,
		ExpectedSHA256: saved.SHA256,
	})
	assertRPCCode(t, "write NUL payload", err, proto.CodeMachineFileBinary)

	// Oversized file and oversized text payload refused.
	bigSt, _ := os.Stat(bigFile)
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           bigFile,
		Root:           root,
		Text:           "shrink",
		ExpectedMtime:  bigSt.ModTime().UTC(),
		ExpectedSHA256: "deadbeef",
	})
	assertRPCCode(t, "edit oversized file", err, proto.CodeTooLarge)
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:           notes,
		Root:           root,
		Text:           strings.Repeat("x", proto.MachineFileEditMaxBytes+1),
		ExpectedMtime:  touchedTime,
		ExpectedSHA256: saved.SHA256,
	})
	assertRPCCode(t, "write oversized text", err, proto.CodeTooLarge)

	// Boundary guards: symlink escape and off-limits hub.json refused.
	if hasSymlinks {
		_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
			Path:           filepath.Join(root, "escape-link.txt"),
			Root:           root,
			Text:           "nope",
			ExpectedMtime:  time.Now().UTC(),
			ExpectedSHA256: "abc",
		})
		assertRPCCode(t, "write symlink escape", err, proto.CodeMachineFileBoundary)
	}
	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             "~/.persona/hub.json",
		Text:             "{}",
		ExpectedMtime:    time.Now().UTC(),
		ExpectedSHA256:   "abc",
		ConfirmSensitive: true,
	})
	assertRPCCode(t, "write off-limits hub.json", err, proto.CodeMachineFileBoundary)
}

func TestMachineFilesWritePreRenamePathSwapRefused(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	realTarget := filepath.Join(root, "real-target.txt")
	if err := os.WriteFile(realTarget, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	altHardLink := filepath.Join(root, "alt-hardlink.txt")
	if err := os.Link(realTarget, altHardLink); err != nil {
		t.Skipf("hard links unsupported on this platform/fs: %v", err)
	}
	displayLink := filepath.Join(root, "edit-link.txt")
	if err := os.Symlink(realTarget, displayLink); err != nil {
		t.Skipf("symlinks unsupported on this platform/fs: %v", err)
	}

	n := &Node{home: home}
	prev, err := n.machineFilePreview(proto.MachineFilePreviewRequest{Path: displayLink, Root: root})
	if err != nil {
		t.Fatalf("preview displayLink: %v", err)
	}

	// Before pre-rename re-verification, replace realTarget with unrelated external content and
	// repoint displayLink to altHardLink (which still shares the original inode, size, mode, and mtime).
	// Without comparing rechecked == res.CanonicalPath, sameFileRead(st, now) would pass and rename
	// would clobber the unrelated file now sitting at res.CanonicalPath.
	unrelatedText := "unrelated-external-content\n"
	n.machineFileWriteBeforeVerify = func() {
		replacement := filepath.Join(root, "unrelated-new.txt")
		if err := os.WriteFile(replacement, []byte(unrelatedText), 0o644); err != nil {
			t.Errorf("write replacement: %v", err)
			return
		}
		if err := os.Rename(replacement, realTarget); err != nil {
			t.Errorf("replace realTarget: %v", err)
			return
		}
		if err := os.Remove(displayLink); err != nil {
			t.Errorf("remove displayLink: %v", err)
			return
		}
		if err := os.Symlink(altHardLink, displayLink); err != nil {
			t.Errorf("repoint displayLink to altHardLink: %v", err)
		}
	}

	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             displayLink,
		Root:             root,
		Text:             "attacker-edit\n",
		ExpectedMtime:    prev.ModTime,
		ExpectedSHA256:   prev.SHA256,
		ExpectedRevision: prev.Revision,
	})
	assertRPCCode(t, "pre-rename display path swapped to alternate hard link", err, proto.CodeMachineFileBoundary)

	if got, err := os.ReadFile(realTarget); err != nil || string(got) != unrelatedText {
		t.Fatalf("unrelated file at original canonical path was clobbered: got=%q err=%v", string(got), err)
	}
	if got, err := os.ReadFile(altHardLink); err != nil || string(got) != "original\n" {
		t.Fatalf("alternate hard link was modified: got=%q err=%v", string(got), err)
	}
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("temp file leaked after refused pre-rename path swap: %v", left)
	}

	// Second check: replacing a non-symlink file in place with a different inode that carries the
	// exact same size and mtime must also be refused by sameFileRead.
	plain := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plain, []byte("same-len!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainPrev, err := n.machineFilePreview(proto.MachineFilePreviewRequest{Path: plain, Root: root})
	if err != nil {
		t.Fatalf("preview plain: %v", err)
	}
	n.machineFileWriteBeforeVerify = func() {
		other := filepath.Join(root, "plain-other.txt")
		if err := os.WriteFile(other, []byte("DIFF-LEN!\n"), 0o644); err != nil {
			t.Errorf("write other: %v", err)
			return
		}
		if err := os.Chtimes(other, plainPrev.ModTime, plainPrev.ModTime); err != nil {
			t.Errorf("chtimes other: %v", err)
			return
		}
		if err := os.Rename(other, plain); err != nil {
			t.Errorf("swap plain: %v", err)
		}
	}

	_, err = n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             plain,
		Root:             root,
		Text:             "edited-plain\n",
		ExpectedMtime:    plainPrev.ModTime,
		ExpectedSHA256:   plainPrev.SHA256,
		ExpectedRevision: plainPrev.Revision,
	})
	assertRPCCode(t, "pre-rename same-size/same-mtime inode swap", err, proto.CodeFileChanged)
	if got, err := os.ReadFile(plain); err != nil || string(got) != "DIFF-LEN!\n" {
		t.Fatalf("swapped file at plain path was clobbered: got=%q err=%v", string(got), err)
	}
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("temp file leaked after inode swap refusal: %v", left)
	}
}

func TestMachineFilesPreviewDetectsConcurrentModification(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "live.txt")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(target, t0, t0); err != nil {
		t.Fatal(err)
	}

	n := &Node{home: home}

	// 1. Transient concurrent write during attempt 1 triggers a retry and returns consistent text/mtime/sha256.
	calls := 0
	n.machineFilePreviewAfterRead = func() {
		calls++
		if calls == 1 {
			if err := os.WriteFile(target, []byte("v2-updated\n"), 0o644); err != nil {
				t.Errorf("concurrent write: %v", err)
				return
			}
			t1 := t0.Add(2 * time.Second)
			if err := os.Chtimes(target, t1, t1); err != nil {
				t.Errorf("chtimes v2: %v", err)
			}
		}
	}
	prev, err := n.machineFilePreview(proto.MachineFilePreviewRequest{Path: target, Root: root})
	if err != nil {
		t.Fatalf("preview with transient mutation: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 preview attempts after concurrent mutation, got %d", calls)
	}
	if prev.Text != "v2-updated\n" || prev.SHA256 != sha256HexBytes([]byte("v2-updated\n")) || !prev.Editable {
		t.Fatalf("preview returned inconsistent snapshot after retry: %+v", prev)
	}
	// Subsequent CAS write using the retried preview succeeds cleanly.
	n.machineFilePreviewAfterRead = nil
	if _, err := n.machineFileWrite(proto.MachineFileWriteRequest{
		Path:             target,
		Root:             root,
		Text:             "v3-saved\n",
		ExpectedMtime:    prev.ModTime,
		ExpectedSHA256:   prev.SHA256,
		ExpectedRevision: prev.Revision,
	}); err != nil {
		t.Fatalf("CAS write after retried preview failed: %v", err)
	}

	// 2. Persistent concurrent modification on every attempt returns CodeFileChanged instead of inconsistent CAS metadata.
	seq := 0
	n.machineFilePreviewAfterRead = func() {
		seq++
		content := "mutated-" + strings.Repeat("x", seq) + "\n"
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Errorf("persistent write %d: %v", seq, err)
			return
		}
		ts := time.Now().UTC().Add(time.Duration(seq) * 2 * time.Second)
		_ = os.Chtimes(target, ts, ts)
	}
	_, err = n.machineFilePreview(proto.MachineFilePreviewRequest{Path: target, Root: root})
	assertRPCCode(t, "preview under continuous concurrent writes", err, proto.CodeFileChanged)
}

func TestMachineFilesCreateRenameDeleteAndGuards(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(home, ".persona")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hasSymlinks := os.Symlink(outside, filepath.Join(root, "escape-dir")) == nil
	n := &Node{home: home}

	// Create dir + file inside root.
	dirResp, err := n.machineFileCreate(proto.MachineFileCreateRequest{
		Dir:  root,
		Name: "docs",
		Kind: "dir",
		Root: root,
	})
	if err != nil || dirResp.Entry.Kind != "dir" {
		t.Fatalf("create dir: %v %+v", err, dirResp)
	}
	fileResp, err := n.machineFileCreate(proto.MachineFileCreateRequest{
		Dir:  dirResp.Entry.Path,
		Name: "readme.md",
		Kind: "file",
		Root: root,
		Text: "# Hello\n",
	})
	if err != nil || fileResp.Entry.Kind != "file" || fileResp.SHA256 == "" {
		t.Fatalf("create file: %v %+v", err, fileResp)
	}

	// Duplicate create refused with CodeExists.
	_, err = n.machineFileCreate(proto.MachineFileCreateRequest{
		Dir:  dirResp.Entry.Path,
		Name: "readme.md",
		Kind: "file",
		Root: root,
	})
	assertRPCCode(t, "duplicate create", err, proto.CodeExists)

	// Invalid names and escaping symlink parent refused.
	_, err = n.machineFileCreate(proto.MachineFileCreateRequest{Dir: root, Name: "../escape.txt", Kind: "file", Root: root})
	assertRPCCode(t, "dotdot create name", err, proto.CodeMachineFileBoundary)
	if hasSymlinks {
		_, err = n.machineFileCreate(proto.MachineFileCreateRequest{Dir: filepath.Join(root, "escape-dir"), Name: "x.txt", Kind: "file", Root: root})
		assertRPCCode(t, "create inside escaping symlink dir", err, proto.CodeMachineFileBoundary)
	}
	// Sensitive file creation requires ConfirmSensitive.
	_, err = n.machineFileCreate(proto.MachineFileCreateRequest{Dir: root, Name: ".env.local", Kind: "file", Root: root, Text: "K=1\n"})
	assertRPCCode(t, "create sensitive without confirm", err, proto.CodeMachineFileSensitiveConfirm)
	envCreated, err := n.machineFileCreate(proto.MachineFileCreateRequest{Dir: root, Name: ".env.local", Kind: "file", Root: root, Text: "K=1\n", ConfirmSensitive: true})
	if err != nil || !envCreated.Entry.Sensitive {
		t.Fatalf("create sensitive with confirm: %v %+v", err, envCreated)
	}

	// Rename & move within allowed root.
	moved, err := n.machineFileRename(proto.MachineFileRenameRequest{
		Path:     fileResp.Entry.Path,
		DestPath: filepath.Join(root, "README.md"),
		Root:     root,
	})
	if err != nil || moved.Name != "README.md" {
		t.Fatalf("rename/move file: %v %+v", err, moved)
	}
	// Refuse moving outside root or moving dir into itself.
	_, err = n.machineFileRename(proto.MachineFileRenameRequest{
		Path:     moved.Path,
		DestPath: filepath.Join(outside, "stolen.md"),
		Root:     root,
	})
	assertRPCCode(t, "move outside root", err, proto.CodeMachineFileBoundary)
	_, err = n.machineFileRename(proto.MachineFileRenameRequest{
		Path:     dirResp.Entry.Path,
		DestPath: filepath.Join(dirResp.Entry.Path, "nested"),
		Root:     root,
	})
	assertRPCCode(t, "move dir into itself", err, proto.CodeMachineFileBoundary)
	// Sensitive rename requires confirmation.
	_, err = n.machineFileRename(proto.MachineFileRenameRequest{
		Path:     envCreated.Entry.Path,
		DestPath: filepath.Join(root, ".env.prod"),
		Root:     root,
	})
	assertRPCCode(t, "rename sensitive without confirm", err, proto.CodeMachineFileSensitiveConfirm)

	// Non-recursive safe-delete guardrail: non-empty dir refused.
	if _, err := n.machineFileCreate(proto.MachineFileCreateRequest{Dir: dirResp.Entry.Path, Name: "child.txt", Kind: "file", Root: root}); err != nil {
		t.Fatal(err)
	}
	_, err = n.machineFileDelete(proto.MachineFileDeleteRequest{Path: dirResp.Entry.Path, Root: root})
	assertRPCCode(t, "delete non-empty dir", err, proto.CodeMachineFileNotEmpty)

	// Delete child, then empty dir succeeds.
	if _, err := n.machineFileDelete(proto.MachineFileDeleteRequest{Path: filepath.Join(dirResp.Entry.Path, "child.txt"), Root: root}); err != nil {
		t.Fatalf("delete child: %v", err)
	}
	if _, err := n.machineFileDelete(proto.MachineFileDeleteRequest{Path: dirResp.Entry.Path, Root: root}); err != nil {
		t.Fatalf("delete empty dir: %v", err)
	}

	// Deleting a symlink removes ONLY the symlink, never the target.
	if hasSymlinks {
		keepTarget := filepath.Join(root, "keep.txt")
		if err := os.WriteFile(keepTarget, []byte("stay"), 0o644); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(root, "keep-link.txt")
		if err := os.Symlink(keepTarget, linkPath); err != nil {
			t.Fatal(err)
		}
		delLink, err := n.machineFileDelete(proto.MachineFileDeleteRequest{Path: linkPath, Root: root})
		if err != nil || delLink.Kind != "symlink" {
			t.Fatalf("delete symlink: %v %+v", err, delLink)
		}
		if _, err := os.Stat(keepTarget); err != nil {
			t.Fatalf("deleting symlink removed its target: %v", err)
		}
	}

	// Renaming onto an existing hard link to the same inode must fail with CodeExists (not POSIX no-op).
	hlSrc := filepath.Join(root, "hardlink-a.txt")
	hlDst := filepath.Join(root, "hardlink-b.txt")
	if err := os.WriteFile(hlSrc, []byte("same-inode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if os.Link(hlSrc, hlDst) == nil {
		_, err = n.machineFileRename(proto.MachineFileRenameRequest{
			Path:     hlSrc,
			DestPath: hlDst,
			Root:     root,
		})
		assertRPCCode(t, "rename onto hard link in same dir", err, proto.CodeExists)
		if _, statErr := os.Lstat(hlSrc); statErr != nil {
			t.Fatalf("source hard link unexpectedly removed: %v", statErr)
		}
		subDir := filepath.Join(root, "hl-sub")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		hlSubDst := filepath.Join(subDir, "hardlink-c.txt")
		if err := os.Link(hlSrc, hlSubDst); err != nil {
			t.Fatal(err)
		}
		_, err = n.machineFileRename(proto.MachineFileRenameRequest{
			Path:     hlSrc,
			DestPath: hlSubDst,
			Root:     root,
		})
		assertRPCCode(t, "rename onto hard link in subdir", err, proto.CodeExists)
		if _, statErr := os.Lstat(hlSrc); statErr != nil {
			t.Fatalf("source hard link unexpectedly removed after cross-dir check: %v", statErr)
		}
	}

	// Refuse deleting root boundary itself and sensitive files without confirm.
	_, err = n.machineFileDelete(proto.MachineFileDeleteRequest{Path: root, Root: root})
	assertRPCCode(t, "delete root boundary", err, proto.CodeMachineFileBoundary)
	_, err = n.machineFileDelete(proto.MachineFileDeleteRequest{Path: envCreated.Entry.Path, Root: root})
	assertRPCCode(t, "delete sensitive without confirm", err, proto.CodeMachineFileSensitiveConfirm)
	if _, err := n.machineFileDelete(proto.MachineFileDeleteRequest{Path: envCreated.Entry.Path, Root: root, ConfirmSensitive: true}); err != nil {
		t.Fatalf("delete sensitive with confirm: %v", err)
	}
}

func TestMachineFilesStreamingUploadSizeCapsAndAbortCleanup(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hasSymlinks := os.Symlink(outside, filepath.Join(root, "escape-dir")) == nil

	n := &Node{home: home, machineFileUploadMaxBytes: 4096}

	// 1. Multi-chunk streaming upload commits atomically and cleans up temp files.
	payload := bytes.Repeat([]byte("upload-stream-data-"), 150) // 2850 bytes <= 4096
	begin, err := n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: "bundle.dat",
		Root: root,
		Size: int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("upload begin: %v", err)
	}
	for off := 0; off < len(payload); off += 700 {
		end := min(len(payload), off+700)
		if _, err := n.machineFileUploadChunk(proto.MachineFileUploadChunkRequest{
			UploadID: begin.UploadID,
			Offset:   int64(off),
			Data:     payload[off:end],
		}); err != nil {
			t.Fatalf("upload chunk at %d: %v", off, err)
		}
	}
	done, err := n.machineFileUploadEnd(proto.MachineFileUploadEndRequest{UploadID: begin.UploadID})
	if err != nil {
		t.Fatalf("upload end: %v", err)
	}
	if done.Size != int64(len(payload)) || done.SHA256 != sha256HexBytes(payload) {
		t.Fatalf("upload result mismatch: %+v", done)
	}
	if got, err := os.ReadFile(filepath.Join(root, "bundle.dat")); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded file mismatch: err=%v len=%d", err, len(got))
	}
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("leftover temp files after upload commit: %v", left)
	}

	// 2. Duplicate upload refused without overwrite; succeeds with overwrite=true.
	_, err = n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: "bundle.dat",
		Root: root,
		Size: 4,
	})
	assertRPCCode(t, "duplicate upload without overwrite", err, proto.CodeExists)

	// 3. Announced size > hard cap rejected before creating temp file.
	_, err = n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: "too-big.bin",
		Root: root,
		Size: 5000,
	})
	assertRPCCode(t, "upload exceeds announced cap", err, proto.CodeTooLarge)
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("temp file leaked on rejected begin: %v", left)
	}

	// 4. Unknown-size (chunked) upload exceeding cap mid-stream aborts and cleans up temp file.
	chunked, err := n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: "overflow.bin",
		Root: root,
		Size: -1,
	})
	if err != nil {
		t.Fatalf("chunked begin: %v", err)
	}
	if _, err := n.machineFileUploadChunk(proto.MachineFileUploadChunkRequest{
		UploadID: chunked.UploadID,
		Offset:   0,
		Data:     bytes.Repeat([]byte("a"), 3000),
	}); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	_, err = n.machineFileUploadChunk(proto.MachineFileUploadChunkRequest{
		UploadID: chunked.UploadID,
		Offset:   3000,
		Data:     bytes.Repeat([]byte("b"), 2000),
	})
	assertRPCCode(t, "mid-stream cap overflow", err, proto.CodeTooLarge)
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("temp file leaked after mid-stream overflow: %v", left)
	}

	// 5. Explicit abort cleans up partial temp file immediately.
	aborted, err := n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: "aborted.bin",
		Root: root,
		Size: 2048,
	})
	if err != nil {
		t.Fatalf("abort begin: %v", err)
	}
	if _, err := n.machineFileUploadChunk(proto.MachineFileUploadChunkRequest{
		UploadID: aborted.UploadID,
		Offset:   0,
		Data:     bytes.Repeat([]byte("x"), 512),
	}); err != nil {
		t.Fatalf("abort chunk: %v", err)
	}
	if left := countTempArtifacts(t, root); len(left) != 1 {
		t.Fatalf("expected 1 active temp file before abort, got %v", left)
	}
	n.machineFileUploadAbort(proto.MachineFileUploadAbortRequest{UploadID: aborted.UploadID})
	if left := countTempArtifacts(t, root); len(left) > 0 {
		t.Fatalf("partial temp file not cleaned on abort: %v", left)
	}
	if _, err := n.machineFileUploadEnd(proto.MachineFileUploadEndRequest{UploadID: aborted.UploadID}); err == nil {
		t.Fatalf("expected error finalizing aborted upload")
	}

	// 6. Sensitive upload & escaping symlink dir refused.
	_, err = n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
		Dir:  root,
		Name: ".env",
		Root: root,
		Size: 10,
	})
	assertRPCCode(t, "sensitive upload without confirm", err, proto.CodeMachineFileSensitiveConfirm)
	if hasSymlinks {
		_, err = n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
			Dir:  filepath.Join(root, "escape-dir"),
			Name: "pwn.txt",
			Root: root,
			Size: 10,
		})
		assertRPCCode(t, "upload into escaping symlink dir", err, proto.CodeMachineFileBoundary)

		// 7. Uploading over an existing leaf symlink (even pointing inside root with overwrite=true) is refused
		// without modifying the symlink's target or leaking temp files.
		realTarget := filepath.Join(root, "real-target.txt")
		if err := os.WriteFile(realTarget, []byte("keep-me"), 0o644); err != nil {
			t.Fatal(err)
		}
		leafLink := filepath.Join(root, "target-link.txt")
		if err := os.Symlink(realTarget, leafLink); err != nil {
			t.Fatal(err)
		}
		danglingLink := filepath.Join(root, "dangling-link.txt")
		if err := os.Symlink(filepath.Join(root, "does-not-exist.txt"), danglingLink); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			desc      string
			name      string
			overwrite bool
		}{
			{"leaf symlink without overwrite", "target-link.txt", false},
			{"leaf symlink with overwrite", "target-link.txt", true},
			{"dangling symlink with overwrite", "dangling-link.txt", true},
		} {
			_, err = n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
				Dir:       root,
				Name:      tc.name,
				Root:      root,
				Size:      7,
				Overwrite: tc.overwrite,
			})
			assertRPCCode(t, tc.desc, err, proto.CodeMachineFileBoundary)
		}
		if got, err := os.ReadFile(realTarget); err != nil || string(got) != "keep-me" {
			t.Fatalf("symlink target modified by refused upload: %q err=%v", string(got), err)
		}
		if left := countTempArtifacts(t, root); len(left) > 0 {
			t.Fatalf("temp file leaked on symlink upload refusal: %v", left)
		}

		// 8. TOCTOU: if a symlink is swapped in at the leaf before upload end, commit is refused and cleaned up.
		toctouBegin, err := n.machineFileUploadBegin(proto.MachineFileUploadBeginRequest{
			Dir:       root,
			Name:      "toctou-link.txt",
			Root:      root,
			Size:      4,
			Overwrite: true,
		})
		if err != nil {
			t.Fatalf("toctou begin: %v", err)
		}
		if _, err := n.machineFileUploadChunk(proto.MachineFileUploadChunkRequest{
			UploadID: toctouBegin.UploadID,
			Offset:   0,
			Data:     []byte("evil"),
		}); err != nil {
			t.Fatalf("toctou chunk: %v", err)
		}
		if err := os.Symlink(realTarget, filepath.Join(root, "toctou-link.txt")); err != nil {
			t.Fatal(err)
		}
		_, err = n.machineFileUploadEnd(proto.MachineFileUploadEndRequest{UploadID: toctouBegin.UploadID})
		assertRPCCode(t, "toctou leaf symlink on upload end", err, proto.CodeMachineFileBoundary)
		if got, err := os.ReadFile(realTarget); err != nil || string(got) != "keep-me" {
			t.Fatalf("symlink target modified by TOCTOU upload end: %q err=%v", string(got), err)
		}
		if left := countTempArtifacts(t, root); len(left) > 0 {
			t.Fatalf("temp file leaked after TOCTOU upload end refusal: %v", left)
		}
	}
}
