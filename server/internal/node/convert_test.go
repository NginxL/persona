package node

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeSoffice puts a LibreOffice first on PATH that writes "<outdir>/<input name>.pdf" and counts its runs, after
// $FAKE_SOFFICE_DELAY seconds when set.
func fakeSoffice(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o700)
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$@" >> ` + calls + `
out=""; last=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--outdir" ]; then out="$2"; shift; fi
  last="$1"; shift
done
name=$(basename "$last"); name="${name%.*}"
[ -n "$FAKE_SOFFICE_DELAY" ] && sleep "$FAKE_SOFFICE_DELAY"
printf '%%PDF-1.4 converted\n' > "$out/$name.pdf"
`
	os.WriteFile(filepath.Join(bin, "soffice"), []byte(script), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestConvertWritesAPDFOnceAndReusesIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", dir) // os.UserCacheDir on a Mac
	n := &Node{home: dir}
	calls := fakeSoffice(t, dir)
	os.WriteFile(filepath.Join(dir, "旧 文档 (1).doc"), []byte("\xd0\xcf\x11\xe0 old word"), 0o600)
	got, err := n.convertFile(context.Background(), proto.FileConvertRequest{Path: "~/旧 文档 (1).doc", To: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got.Path)
	if err != nil || !strings.HasPrefix(string(data), "%PDF") || got.Mime != "application/pdf" || got.Size != int64(len(data)) {
		t.Fatalf("%+v %q %v", got, data, err)
	}
	again, err := n.convertFile(context.Background(), proto.FileConvertRequest{Path: "旧 文档 (1).doc", Cwd: dir})
	if err != nil || again.Path != got.Path {
		t.Fatalf("second = %+v, %v", again, err)
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "\n") != 1 || !strings.Contains(string(b), "--headless") ||
		!strings.Contains(string(b), "-env:UserInstallation=file:///") {
		t.Fatalf("soffice ran %q", b)
	}
	if _, err := n.convertFile(context.Background(), proto.FileConvertRequest{Path: "~/旧 文档 (1).doc", To: "docx"}); err == nil {
		t.Fatal("converted to something other than pdf")
	}
}

// LibreOffice's private profile is a file URL on every system: a Windows drive path is file:///C:/…, never
// file://C:/…, which names a host "C:" (#9934); macOS and Linux keep file:///tmp/…. Both shapes run everywhere.
func TestLibreOfficeProfileURL(t *testing.T) {
	for _, c := range []struct {
		path    string
		windows bool
		want    string
	}{
		{"/tmp/persona-soffice-1/profile", false, "file:///tmp/persona-soffice-1/profile"},
		{"/var/folders/x y/T/persona-soffice-1/profile", false, "file:///var/folders/x%20y/T/persona-soffice-1/profile"},
		{`C:\Users\me\AppData\Local\Temp\persona-soffice-1\profile`, true, "file:///C:/Users/me/AppData/Local/Temp/persona-soffice-1/profile"},
		{`C:\Users\A B\AppData\Local\Temp\persona-soffice-1\profile`, true, "file:///C:/Users/A%20B/AppData/Local/Temp/persona-soffice-1/profile"},
		{`C:\Users\EXAMPL~1\AppData\Local\Temp\100%#\profile`, true, "file:///C:/Users/EXAMPL~1/AppData/Local/Temp/100%25%23/profile"},
		{`D:\temp\persona-soffice-1\profile`, true, "file:///D:/temp/persona-soffice-1/profile"},
		{`\\server\share\temp\profile`, true, "file://server/share/temp/profile"},
	} {
		got := fileURL(c.path, c.windows)
		if got != c.want {
			t.Errorf("fileURL(%q, windows=%v) = %q, want %q", c.path, c.windows, got, c.want)
		}
		u, err := url.Parse(got)
		if err != nil || u.Scheme != "file" || (c.windows && !strings.HasPrefix(c.path, `\\`) && u.Host != "") {
			t.Errorf("%q is not a local file URL: %+v %v", got, u, err)
		}
	}
}

func TestConvertWithoutLibreOffice(t *testing.T) {
	if runtime.GOOS == "darwin" && sofficePath() != "" {
		t.Skip("this Mac has LibreOffice installed")
	}
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "nothing"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", dir) // os.UserCacheDir on a Mac
	n := &Node{home: dir}
	os.WriteFile(filepath.Join(dir, "a.ppt"), []byte("x"), 0o600)
	_, err := n.convertFile(context.Background(), proto.FileConvertRequest{Path: "~/a.ppt"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNoConverter {
		t.Fatal(err)
	}
}

func TestConvertAcrossFileSystemsReplacesTheCopyWhole(t *testing.T) {
	cacheEnv(t, true)
	n, req, _ := convertFixture(t)
	first, err := n.convertFile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(first.Path)
	// an empty copy at the cache's name, as a cut-short write leaves it; the link shows whether it gets written in place
	if err := os.Truncate(first.Path, 0); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(n.home, "link")
	if err := os.Link(first.Path, link); err != nil {
		t.Skip("no hard links here:", err)
	}
	rename := renameConverted
	t.Cleanup(func() { renameConverted = rename })
	renameConverted = func(from, to string) error { // the temp folder on another file system than the cache
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("invalid cross-device link")}
	}
	got, err := n.convertFile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got.Path); got.Path != first.Path || string(b) != string(want) || got.Size != int64(len(want)) {
		t.Fatalf("got %+v %q, want %q", got, b, want)
	}
	if b, _ := os.ReadFile(link); len(b) != 0 {
		t.Fatalf("the cached copy was written in place: %q", b)
	}
	if ents, _ := os.ReadDir(filepath.Dir(got.Path)); len(ents) != 1 {
		t.Fatalf("cache holds %v", ents)
	}
}

func TestConvertHitKeepsTheCopyFromPruning(t *testing.T) {
	cacheEnv(t, true)
	n, req, _ := convertFixture(t)
	other := proto.FileConvertRequest{Path: filepath.Join(n.home, "b.doc")}
	writeFile(t, other.Path, "other word")
	first, err := n.convertFile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-convertKeep - time.Hour)
	os.Chtimes(first.Path, old, old)
	if again, err := n.convertFile(context.Background(), req); err != nil || again.Path != first.Path {
		t.Fatalf("hit = %+v, %v", again, err)
	}
	// the next conversion prunes by age while the client may still be reading the copy it was just given
	if _, err := n.convertFile(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("a copy just handed out was pruned: %v", err)
	}
}

func TestConvertSameFileAtOnceRunsLibreOfficeOnce(t *testing.T) {
	cacheEnv(t, true)
	n, req, calls := convertFixture(t)
	t.Setenv("FAKE_SOFFICE_DELAY", "0.5")
	const at = 6
	got := make([]*proto.FileConverted, at)
	errs := make([]error, at)
	var wg sync.WaitGroup
	for i := range at {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = n.convertFile(context.Background(), req)
		}()
	}
	wg.Wait()
	for i := range at {
		if errs[i] != nil || got[i].Path != got[0].Path {
			t.Fatalf("request %d = %+v, %v", i, got[i], errs[i])
		}
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("LibreOffice ran %d times for one file", strings.Count(string(b), "\n"))
	}
}
