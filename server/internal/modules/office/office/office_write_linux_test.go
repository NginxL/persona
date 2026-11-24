package office

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// limitedWriteEnv makes this test binary, run again for TestMainFailedWriteKeepsTheEarlierFile alone, its child:
// "<limit> <markdown file>".
const limitedWriteEnv = "PERSONA_OFFICE_TEST_LIMITED_WRITE"

// A write that fails part way (here at the file size limit, as on a full disk) leaves the earlier file as it was: the
// new one is written beside it and renamed over it only once it is complete. Linux only, for RLIMIT_FSIZE; Go ignores
// SIGXFSZ, so the write just fails. The limit is a whole process's, so Main runs under it in a child process (this
// test binary run again for this test): in this one it would cut off the test framework's own files too, go test's
// test log among them, and the package would fail with every test passed (#8962).
func TestMainFailedWriteKeepsTheEarlierFile(t *testing.T) {
	if spec := os.Getenv(limitedWriteEnv); spec != "" {
		limitedWrite(spec)
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# Plan\n\nOne paragraph.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := filepath.Join(dir, "notes.docx")
	v1, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	var long strings.Builder
	long.WriteString("# Plan\n\n")
	for i := range 400 {
		fmt.Fprintf(&long, "Paragraph %d of the second version, %x.\n\n", i, i*7919)
	}
	if err := os.WriteFile(md, []byte(long.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	limit := uint64(len(v1) + 100)
	if v2, err := Docx([]byte(long.String()), dir, "notes"); err != nil || uint64(len(v2)) <= limit {
		t.Fatalf("the second version is %d bytes, not over %d: %v", len(v2), limit, err)
	}

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if old.Max < limit {
		t.Skipf("the file size limit is already %d bytes", old.Max)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMainFailedWriteKeepsTheEarlierFile$")
	child.Env = append(os.Environ(), fmt.Sprintf("%s=%d %s", limitedWriteEnv, limit, md))
	var errOut bytes.Buffer
	child.Stderr = &errOut
	var exit *exec.ExitError
	if err := child.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(errOut.String(), "file too large") {
		t.Errorf("Main under the limit: %v: %s", err, errOut.String())
	}
	if after, err := os.ReadFile(doc); err != nil || !bytes.Equal(after, v1) {
		t.Fatalf("the earlier file changed (%d bytes, was %d): %v", len(after), len(v1), err)
	}
	if info, err := Check(doc); err != nil || fmt.Sprint(info["headings"]) != "[Plan]" {
		t.Errorf("check: %v %v", info, err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"notes.docx", "notes.md"}) {
		t.Errorf("left behind: %q", names)
	}
}

// limitedWrite is the child of TestMainFailedWriteKeepsTheEarlierFile: Main converting the Markdown file under the file
// size limit spec gives, its error output on stderr. It exits with Main's code, 3 when it could not set the limit.
func limitedWrite(spec string) {
	limit, md, _ := strings.Cut(spec, " ")
	n, err := strconv.ParseUint(limit, 10, 64)
	var old syscall.Rlimit
	if err == nil {
		err = syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old)
	}
	if err == nil {
		err = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: n, Max: old.Max})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s=%q: %v\n", limitedWriteEnv, spec, err)
		os.Exit(3)
	}
	os.Exit(Main([]string{"docx", md}, io.Discard, os.Stderr))
}

// -o names a file of its kind only (docs/assistant/office.md, "No approval every time"), and a link by that name to
// some other file is replaced, not written through: the new file is a new output, whatever the target's mode.
func TestMainReplacesALinkAtTheOutput(t *testing.T) {
	dir := t.TempDir()
	setUmask(t, 0o022)
	rc := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(rc, []byte("export EDITOR=vi\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "notes.docx")
	if err := os.Symlink(rc, out); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := Main([]string{"docx", md, "-o", out}, io.Discard, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if b, err := os.ReadFile(rc); err != nil || string(b) != "export EDITOR=vi\n" {
		t.Errorf("wrote through the link: %s is %d bytes: %v", rc, len(b), err)
	}
	if st, err := os.Lstat(out); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o644 {
		t.Errorf("%s is not a new file of its own: %v %v", out, modeOf(st), err)
	}
	if st, err := os.Stat(rc); err != nil || st.Mode().Perm() != 0o400 {
		t.Errorf("the link's target changed: %v %v", modeOf(st), err)
	}
}

// Replacing an earlier output keeps its permissions, as writing into it did.
func TestMainKeepsTheOutputsPermissions(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "notes.docx")
	setUmask(t, 0o022)
	for _, want := range []os.FileMode{0o644, 0o600} {
		if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
			t.Fatalf("exit %d", code)
		}
		st, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("mode %v, want %v", st.Mode().Perm(), want)
		}
		if err := os.Chmod(out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A new output gets 0644 less the umask, as os.WriteFile gave it: a document made from private notes under umask 077
// is the user's alone.
func TestMainNewOutputFollowsTheUmask(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mask int
		want os.FileMode
	}{{0o077, 0o600}, {0o027, 0o640}, {0o022, 0o644}} {
		setUmask(t, c.mask)
		out := filepath.Join(dir, fmt.Sprintf("notes-%o.docx", c.mask))
		var errOut bytes.Buffer
		if code := Main([]string{"docx", md, "-o", out}, io.Discard, &errOut); code != 0 {
			t.Fatalf("umask %o: exit %d: %s", c.mask, code, errOut.String())
		}
		if st, err := os.Stat(out); err != nil || st.Mode().Perm() != c.want {
			t.Errorf("umask %o: mode %v, want %v (%v)", c.mask, modeOf(st), c.want, err)
		}
	}
}

// An earlier output the user made read-only stays as it is: the command fails, as writing into it did, and as cp
// refuses to overwrite it.
func TestMainLeavesAReadOnlyOutputAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write any file")
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := filepath.Join(dir, "notes.docx")
	v1, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(doc, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte("# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := Main([]string{"docx", md}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "permission denied") {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
	if after, err := os.ReadFile(doc); err != nil || !bytes.Equal(after, v1) {
		t.Errorf("the read-only file changed (%d bytes, was %d): %v", len(after), len(v1), err)
	}
	if st, err := os.Stat(doc); err != nil || st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v, want 0444: %v", modeOf(st), err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"notes.docx", "notes.md"}) {
		t.Errorf("left behind: %q", names)
	}
}

// What is wrong with the input is said first, whatever the output: an Office file as input exits 2 and a missing one
// is named, even when -o is an earlier output the user cannot write. Neither touches that output.
func TestMainReportsTheInputBeforeAReadOnlyOutput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write any file")
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := filepath.Join(dir, "notes.docx")
	other := filepath.Join(dir, "other.docx")
	if code := Main([]string{"docx", md, "-o", other}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	v1, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(other, 0o444); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.md")
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"docx", doc, "-o", other}, 2, "is an Office file"},
		{[]string{"docx", missing, "-o", other}, 1, missing},
	} {
		var errOut bytes.Buffer
		if code := Main(c.args, io.Discard, &errOut); code != c.code || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("%q: exit %d, want %d with %q: %s", c.args, code, c.code, c.want, errOut.String())
		}
		if after, err := os.ReadFile(other); err != nil || !bytes.Equal(after, v1) {
			t.Errorf("%q changed the output (%d bytes, was %d): %v", c.args, len(after), len(v1), err)
		}
	}
}

// The output's folder must be writable (docs/assistant/office.md): the temporary file beside the output cannot be
// made there, so the command exits 1 and the earlier output stays as it was, even one the user may write.
func TestMainLeavesTheOutputAloneInAReadOnlyFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write into any folder")
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := filepath.Join(dir, "notes.docx")
	v1, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte("# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Before t.TempDir removes the folder, which it could not empty while read-only.
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	var errOut bytes.Buffer
	if code := Main([]string{"docx", md}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "permission denied") {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
	if after, err := os.ReadFile(doc); err != nil || !bytes.Equal(after, v1) {
		t.Errorf("the earlier output changed (%d bytes, was %d): %v", len(after), len(v1), err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"notes.docx", "notes.md"}) {
		t.Errorf("left behind: %q", names)
	}
}

// setUmask sets the process's umask for the test and puts the old one back after it. The umask is the whole
// process's, which is safe because no test here is parallel.
func setUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

// modeOf is st's mode (with its type), or nil without st.
func modeOf(st os.FileInfo) any {
	if st == nil {
		return nil
	}
	return st.Mode()
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
