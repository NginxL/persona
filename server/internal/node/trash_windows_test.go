//go:build windows

package node

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/proto"
)

// The Recycle Bin these tests use is the user's own. They recycle only what is in their temp folders, and
// keepNothingRecycled puts back whatever they left in it, so the bin is as it was when they end.

// keepNothingRecycled, when the test ends, takes out of this user's Recycle Bin everything that was recycled from under
// dir (told by the original path in each item's $I file) into a temp folder of the test, which is then removed.
func keepNothingRecycled(t *testing.T, dir string) {
	t.Helper()
	back := t.TempDir() // made before the cleanup below is added, so removed after it runs
	t.Cleanup(func() {
		items, err := recycledFrom(dir)
		if err != nil {
			t.Errorf("can't look in the Recycle Bin for what the test left there: %v", err)
		}
		for i, item := range items {
			if err := unrecycle(item, filepath.Join(back, strconv.Itoa(i))); err != nil {
				t.Errorf("%s is still in the Recycle Bin: %v", item, err)
			}
		}
	})
}

// recycledFrom lists the items ($R…) in this user's Recycle Bin on dir's drive whose original path is in dir.
func recycledFrom(dir string) ([]string, error) {
	bin, err := recycleBinDir(dir)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(bin)
	if err != nil {
		return nil, err
	}
	in := []string{dir, longPath(dir)}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		in = append(in, r)
	}
	for i, d := range in {
		in[i] = strings.ToLower(filepath.Clean(d)) + `\`
	}
	var items []string
	for _, e := range ents {
		name := e.Name()
		if len(name) < 3 || !strings.EqualFold(name[:2], "$I") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			continue
		}
		orig := strings.ToLower(recycledOriginal(b))
		for _, prefix := range in {
			if strings.HasPrefix(orig, prefix) {
				items = append(items, filepath.Join(bin, "$R"+name[2:]))
				break
			}
		}
	}
	return items, nil
}

func recycledFromTest(t *testing.T, dir string) []string {
	t.Helper()
	items, err := recycledFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// recycledOriginal is the original path an $I file holds: after the version, the size and the deletion time (8 bytes
// each), version 2 (Windows 10 on) has the path's length in UTF-16 units and the path, version 1 a fixed 260 units.
func recycledOriginal(b []byte) string {
	if len(b) < 24 {
		return ""
	}
	var units []uint16
	switch binary.LittleEndian.Uint64(b) {
	case 1:
		for i := 24; i+1 < len(b); i += 2 {
			units = append(units, binary.LittleEndian.Uint16(b[i:]))
		}
	case 2:
		if len(b) < 28 {
			return ""
		}
		for i, n := 28, int(binary.LittleEndian.Uint32(b[24:])); i+1 < len(b) && n > 0; i, n = i+2, n-1 {
			units = append(units, binary.LittleEndian.Uint16(b[i:]))
		}
	}
	s := string(utf16.Decode(units))
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return s
}

func trashIDsOf(t *testing.T, out map[string]any) []string {
	t.Helper()
	var ids []string
	for _, x := range out["trashed"].([]any) {
		ids = append(ids, x.(map[string]any)["trash_id"].(string))
	}
	return ids
}

// file.trash on Windows moves a file and a folder to the user's Recycle Bin and answers as on macOS and Linux. Undo
// (file.restore) from a node started afresh puts them back by their trash_ids alone and clears them from the bin; a
// restored id is then not_found, and a name taken meanwhile refuses the restore with exists (#9933).
func TestRecycleBinTrashAndRestore(t *testing.T) {
	n, home := writeNode(t)
	keepNothingRecycled(t, home)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "first")
	makeFile(t, filepath.Join(home, "proj", "dir", "in.txt"), "in")
	out, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"a.txt", "dir"}, "cwd": filepath.Join(home, "proj")})
	if err != nil {
		t.Fatal(err)
	}
	ids := trashIDsOf(t, out)
	if b, _ := json.Marshal(out["trashed"]); len(ids) != 2 || !strings.Contains(string(b), `{"path":"a.txt","trash_id":`) ||
		!strings.Contains(string(b), `{"path":"dir","trash_id":`) {
		t.Fatalf("trashed: %s", b)
	}
	notThere(t, filepath.Join(home, "proj", "a.txt"))
	notThere(t, filepath.Join(home, "proj", "dir"))
	var items, infos []string
	for _, s := range ids {
		id, err := decodeTrashID(s)
		if err != nil {
			t.Fatal(err)
		}
		item, info, err := recycledAt(id.Real, id.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(info); err != nil {
			t.Fatalf("the Recycle Bin has no record of %s: %v", id.Shown, err)
		}
		items, infos = append(items, item), append(infos, info)
	}
	if fileText(t, items[0]) != "first" || fileText(t, filepath.Join(items[1], "in.txt")) != "in" {
		t.Fatal("what is in the Recycle Bin is not what was deleted")
	}
	if got := len(recycledFromTest(t, home)); got != 2 {
		t.Fatalf("%d items in the Recycle Bin came from the test, want 2", got)
	}

	fresh := &Node{dir: t.TempDir(), home: home, logger: n.logger}
	res, err := editCall(t, fresh, "file.restore", map[string]any{"trash_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res["restored"]); string(b) != `[{"path":"a.txt"},{"path":"dir"}]` {
		t.Fatalf("restored: %s", b)
	}
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "first" || fileText(t, filepath.Join(home, "proj", "dir", "in.txt")) != "in" {
		t.Fatal("not put back")
	}
	for i := range items {
		notThere(t, items[i])
		notThere(t, infos[i])
	}
	_, err = editCall(t, fresh, "file.restore", map[string]any{"trash_ids": ids[:1]})
	wantCode(t, err, proto.CodeNotFound)

	out, err = editCall(t, n, "file.trash", map[string]any{"paths": []string{"~/proj/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	again := trashIDsOf(t, out)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "second")
	_, err = editCall(t, n, "file.restore", map[string]any{"trash_ids": again})
	wantCode(t, err, proto.CodeExists)
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "second" {
		t.Fatal("a refused restore replaced the file")
	}
	if err := os.Remove(filepath.Join(home, "proj", "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := editCall(t, n, "file.restore", map[string]any{"trash_ids": again}); err != nil {
		t.Fatalf("a refused restore left nothing to restore: %v", err)
	}
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "first" {
		t.Fatal("not put back")
	}
}

// Replace in a move sends what was at the destination to the Recycle Bin, and a replace that fails afterwards puts it
// back (#1968), on Windows as elsewhere (#9933).
func TestRecycleBinReplace(t *testing.T) {
	n, home := writeNode(t)
	keepNothingRecycled(t, home)
	makeFile(t, filepath.Join(home, "proj", "b.txt"), "newer b")
	makeFile(t, filepath.Join(home, "dest", "b.txt"), "b")
	if _, err := editCall(t, n, "file.move", map[string]any{"paths": []string{"~/proj/b.txt"}, "dir": "~/dest", "conflict": "replace"}); err != nil {
		t.Fatal(err)
	}
	if fileText(t, filepath.Join(home, "dest", "b.txt")) != "newer b" {
		t.Fatal("replace did not replace")
	}
	if items := recycledFromTest(t, home); len(items) != 1 || fileText(t, items[0]) != "b" {
		t.Fatalf("the replaced file is not in the Recycle Bin: %v", items)
	}

	makeFile(t, filepath.Join(home, "proj", "report.txt"), "new")
	makeFile(t, filepath.Join(home, "dest", "report.txt"), "original destination")
	e, err := n.entry("~/proj/report.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.real); err != nil { // another program moved it meanwhile
		t.Fatal(err)
	}
	if _, err := n.moveOne(e, filepath.Join(home, "dest"), "report.txt", proto.UploadConflictReplace); err == nil {
		t.Fatal("a move of a missing source succeeded")
	}
	if got := fileText(t, filepath.Join(home, "dest", "report.txt")); got != "original destination" {
		t.Fatalf("destination after a failed replace: %q", got)
	}
	if items := recycledFromTest(t, home); len(items) != 1 {
		t.Fatalf("a failed replace left the destination in the Recycle Bin: %v", items)
	}
}

// A file another program holds open stays where it is, and the answer says why (#9933).
func TestRecycleBinLeavesAFileInUse(t *testing.T) {
	n, home := writeNode(t)
	keepNothingRecycled(t, home)
	f := filepath.Join(home, "proj", "open.txt")
	makeFile(t, f, "open")
	p, err := windows.UTF16PtrFromString(f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0) // shared with nobody
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	release := func() {
		if !closed {
			windows.CloseHandle(h)
			closed = true
		}
	}
	defer release()
	_, err = editCall(t, n, "file.trash", map[string]any{"paths": []string{"~/proj/open.txt"}})
	if err == nil || !strings.Contains(err.Error(), "open in another program") {
		t.Fatalf("trash a file in use: %v", err)
	}
	release()
	if fileText(t, f) != "open" {
		t.Fatal("the file in use is gone")
	}
}

// What the shell would delete for good instead of recycling is refused and stays where it is: a file whose path is
// longer than the Recycle Bin takes, and a file and a folder on a network share (this computer's own administrative
// share of the temp folder's drive), which has no Recycle Bin (#9933).
func TestRecycleBinRefusesWhatItWouldDeleteForGood(t *testing.T) {
	n, home := writeNode(t)
	keepNothingRecycled(t, home)
	deep := []string{strings.Repeat("d", 120), strings.Repeat("e", 120), "long.txt"}
	makeFile(t, filepath.Join(append([]string{home, "proj"}, deep...)...), "long")
	_, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"~/proj/" + strings.Join(deep, "/")}})
	if err == nil || !strings.Contains(err.Error(), "can't go to the Recycle Bin, which takes paths of up to 259 characters") {
		t.Fatalf("trash a long path: %v", err)
	}
	if fileText(t, filepath.Join(append([]string{home, "proj"}, deep...)...)) != "long" {
		t.Fatal("a refused delete removed the file")
	}

	local := resolved(t, t.TempDir())
	vol := filepath.VolumeName(local)
	if len(vol) != 2 || vol[1] != ':' {
		t.Skipf("%s is not on a drive letter", local)
	}
	share := `\\localhost\` + vol[:1] + "$" + local[len(vol):]
	if _, err := os.Stat(share); err != nil {
		t.Skipf("this computer's administrative share is not reachable: %v", err)
	}
	makeFile(t, filepath.Join(local, "a.txt"), "a")
	makeFile(t, filepath.Join(local, "dir", "in.txt"), "in")
	n = &Node{dir: t.TempDir(), home: share, logger: log.New(io.Discard, "", 0)}
	for _, p := range []string{"~/a.txt", "~/dir"} {
		_, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{p}})
		if err == nil || !strings.Contains(err.Error(), "can't go to the Recycle Bin") || !strings.Contains(err.Error(), "not deleted") {
			t.Fatalf("trash %s on a share: %v", p, err)
		}
	}
	if fileText(t, filepath.Join(local, "a.txt")) != "a" || fileText(t, filepath.Join(local, "dir", "in.txt")) != "in" {
		t.Fatal("a refused delete removed something")
	}
}

// A made-up trash_id can't take an item out of the Recycle Bin into a place the guard keeps, or name anything outside
// the bin (#9933).
func TestRecycleBinRestoreChecksTheGuard(t *testing.T) {
	n, home := writeNode(t)
	keepNothingRecycled(t, home)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "a")
	makeFile(t, filepath.Join(home, "proj", "b.txt"), "b")
	makeFile(t, filepath.Join(home, ".persona", "keep"), "k")
	out, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"~/proj/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	ids := trashIDsOf(t, out)
	id, err := decodeTrashID(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	bin, _, _ := recycledAt(id.Real, id.Name)
	rel, err := filepath.Rel(filepath.Dir(bin), filepath.Join(home, "proj", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []trashID{
		{Name: id.Name, Real: filepath.Join(home, ".persona", "evil"), Shown: "x"},
		{Name: id.Name, Real: filepath.Join(t.TempDir(), "evil"), Shown: "x"},
		{Name: "$R" + `\..\` + rel, Real: filepath.Join(home, "proj", "stolen"), Shown: "x"},
		{Name: rel, Real: filepath.Join(home, "proj", "stolen"), Shown: "x"},
		{Name: "b.txt", Real: filepath.Join(home, "proj", "c.txt"), Shown: "x"},
	} {
		if _, err := editCall(t, n, "file.restore", map[string]any{"trash_ids": []string{bad.encode()}}); err == nil {
			t.Errorf("restored %+v", bad)
		}
	}
	if fileText(t, filepath.Join(home, "proj", "b.txt")) != "b" {
		t.Fatal("a made-up id moved a file outside the Recycle Bin")
	}
	if _, err := editCall(t, n, "file.restore", map[string]any{"trash_ids": ids}); err != nil {
		t.Fatal(err)
	}
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "a" {
		t.Fatal("not put back")
	}
}
