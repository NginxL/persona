package runtimepkg

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The same vector as integrations/runtime-sdk/test/loader.test.js: the loader and personad must hash alike.
func TestHashVector(t *testing.T) {
	got := Hash([]File{{Path: "b/c.js", Data: []byte("y")}, {Path: "a.js", Data: []byte("x")}})
	if want := "d6f3031b914b7e5243281ea484c24fc9ef36488a09bf7fe78df53a1101f06b8f"; got != want {
		t.Fatalf("hash %s, want %s", got, want)
	}
}

func TestValidPath(t *testing.T) {
	for p, ok := range map[string]bool{"dsh/lib/a.js": true, "a.js": true, "": false, "/etc/x": false, "../x.js": false,
		"a/../b.js": false, "a//b.js": false, "a\\b.js": false, "./a.js": false} {
		if ValidPath(p) != ok {
			t.Errorf("ValidPath(%q) = %v", p, !ok)
		}
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(OverrideEnv, dir)
	if p, err := Load("persona-dsh-runtime"); p != nil || err != nil {
		t.Fatalf("no package there: %v %v", p, err)
	}
	root := filepath.Join(dir, "persona-dsh-runtime")
	for path, data := range map[string]string{"dsh/lib/a.js": "x", "runtime-sdk/index.js": "y"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Load("persona-dsh-runtime")
	if err != nil || p == nil {
		t.Fatalf("load: %v %v", p, err)
	}
	if len(p.Files) != 2 || p.Files[0].Path != "dsh/lib/a.js" || p.Hash != Hash(p.Files) {
		t.Fatalf("package %+v", p)
	}
	if _, err := Load("../x"); err == nil {
		t.Fatal("a bad name loaded")
	}
}

// The generator copies what the patterns say, and fails when one matches nothing.
func TestGenerate(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	cmd := map[string]string{"dsh/package.json": "{}", "dsh/lib/a.js": "a", "dsh/lib/b.txt": "no", "runtime-sdk/package.json": "{}",
		"runtime-sdk/index.js": "i", "runtime-sdk/lib/r.js": "r", "dsh/test/t.js": "no",
		"pi/package.json": "{}", "pi/index.js": "x", "pi/companion.mjs": "c", "pi/lib/pi.js": "p", "pi/e2e/run.mjs": "no",
		"antigravity/package.json": "{}", "antigravity/companion.mjs": "c", "antigravity/desktop.mjs": "d", "antigravity/mcp-proxy.mjs": "m", "antigravity/media-guard.mjs": "g", "antigravity/lib/runtime.js": "a", "antigravity/install.mjs": "no", "antigravity/test/map.test.js": "no",
		"paseo/package.json": "{}", "paseo/bridge.mjs": "b", "paseo/lib/bridge.js": "l", "paseo/test/bridge.test.js": "no",
		"claude-code-plugin/.claude-plugin/plugin.json": "{}", "claude-code-plugin/hooks/hooks.json": "{}",
		"claude-code-plugin/hooks/register.ts": "r", "claude-code-plugin/hooks/register.test.ts": "no",
		"claude-code-plugin/test/fixture.json": "no", "claude-code-plugin/node_modules/x/index.js": "no",
		"claude-code-plugin/.gitignore": "no", "claude-code-plugin/README.md": "doc"}
	for path, data := range cmd {
		_ = os.MkdirAll(filepath.Join(src, filepath.Dir(path)), 0o755)
		if err := os.WriteFile(filepath.Join(src, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// What an earlier run left: a tree of the layout before parts and a part no package takes now. README.md stays.
	for _, stale := range []string{"persona-dsh-runtime/dsh/lib/a.js", "dsh-0000000000000000.tar.gz", "README.md"} {
		_ = os.MkdirAll(filepath.Join(out, filepath.Dir(stale)), 0o755)
		if err := os.WriteFile(filepath.Join(out, stale), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Generate(src, out)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files["persona-dsh-runtime"] {
		got = append(got, f.Path)
	}
	want := []string{"dsh/package.json", "dsh/lib/a.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/r.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("copied %v, want %v", got, want)
	}
	// One part per directory: the four packages that take runtime-sdk share one blob. The stale files are gone.
	parts, _ := filepath.Glob(filepath.Join(out, "*"+partSuffix))
	if entries, _ := os.ReadDir(out); len(parts) != 6 || len(entries) != 8 || string(mustRead(t, filepath.Join(out, "README.md"))) != "old" {
		t.Fatalf("wrote %d parts, %d entries", len(parts), len(entries))
	}
	for _, part := range parts {
		zr, err := gzip.NewReader(bytes.NewReader(mustRead(t, part)))
		if err != nil || zr.Name != "" || zr.ModTime.Unix() > 0 {
			t.Fatalf("%s: gzip header %v", part, err)
		}
	}
	again := t.TempDir()
	if _, err := Generate(src, again); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(parts, filepath.Join(out, indexFile)) {
		name = filepath.Base(name)
		if !bytes.Equal(mustRead(t, filepath.Join(out, name)), mustRead(t, filepath.Join(again, name))) {
			t.Fatalf("%s differs between two runs", name)
		}
	}
	// Loading one package unpacks its own parts only.
	d := &dist{fsys: os.DirFS(out), root: "."}
	p, err := d.load("persona-dsh-runtime")
	if err != nil || p == nil || p.Hash != Hash(files["persona-dsh-runtime"]) || len(p.Files) != 5 {
		t.Fatalf("load: %+v %v", p, err)
	}
	for part, dp := range d.parts {
		if unpacked := dp.files != nil; unpacked != (strings.HasPrefix(part, "dsh-") || strings.HasPrefix(part, "runtime-sdk-")) {
			t.Fatalf("%s unpacked: %v", part, unpacked)
		}
	}
	got = got[:0]
	for _, f := range files["persona-pi-runtime"] {
		got = append(got, f.Path)
	}
	want = []string{"pi/package.json", "pi/index.js", "pi/companion.mjs", "pi/lib/pi.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/r.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pi copied %v, want %v", got, want)
	}
	got = got[:0]
	for _, f := range files["persona-antigravity-runtime"] {
		got = append(got, f.Path)
	}
	want = []string{"antigravity/package.json", "antigravity/companion.mjs", "antigravity/desktop.mjs", "antigravity/mcp-proxy.mjs", "antigravity/media-guard.mjs", "antigravity/lib/runtime.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/r.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("antigravity copied %v, want %v", got, want)
	}
	got = got[:0]
	for _, f := range files["persona-paseo-runtime"] {
		got = append(got, f.Path)
	}
	want = []string{"paseo/package.json", "paseo/bridge.mjs", "paseo/lib/bridge.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/r.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paseo copied %v, want %v", got, want)
	}
	// An asset's "/**" takes the directory's files, without tests, dependencies and hidden files.
	got = got[:0]
	for _, f := range files[ClaudeCodePlugin] {
		got = append(got, f.Path)
	}
	if want = []string{"claude-code-plugin/.claude-plugin/plugin.json", "claude-code-plugin/README.md",
		"claude-code-plugin/hooks/hooks.json", "claude-code-plugin/hooks/register.ts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude-code-plugin copied %v, want %v", got, want)
	}
	_ = os.Remove(filepath.Join(src, "runtime-sdk", "index.js"))
	if _, err := Generate(src, out); err == nil || !strings.Contains(err.Error(), "matches no file") {
		t.Fatalf("a missing file: %v", err)
	}
	_ = os.WriteFile(filepath.Join(src, "runtime-sdk", "index.js"), []byte("i"), 0o644)
	_ = os.RemoveAll(filepath.Join(src, "claude-code-plugin"))
	if _, err := Generate(src, out); err == nil || !strings.Contains(err.Error(), "matches no file") {
		t.Fatalf("a missing asset directory: %v", err)
	}
}

// The loader's list (integrations/runtime-sdk/lib/package.js) is this one: otherwise every hello looks like another
// version.
func TestPackagesMatchTheLoader(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "runtime-sdk", "lib", "package.js"))
	if err != nil {
		t.Skipf("no integrations checkout beside server/: %v", err)
	}
	block := regexp.MustCompile(`(?s)export const PACKAGES = (?:addPackageAliases\()?\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("PACKAGES not found in package.js")
	}
	js := map[string][]string{}
	for _, m := range regexp.MustCompile(`'([^']+)':\s*\[([^\]]*)\]`).FindAllSubmatch(block[1], -1) {
		var list []string
		for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(m[2], -1) {
			list = append(list, string(q[1]))
		}
		js[string(m[1])] = list
	}
	if !reflect.DeepEqual(js, Packages) {
		t.Fatalf("package.js PACKAGES %v, runtimepkg.Packages %v", js, Packages)
	}
}

// generateIntegrations runs Generate on the packages as committed in integrations/ into a temporary directory.
func generateIntegrations(t *testing.T) (map[string][]File, string) {
	t.Helper()
	src := filepath.Join("..", "..", "..", "integrations")
	if _, err := os.Stat(filepath.Join(src, "runtime-sdk", "package.json")); err != nil {
		t.Skipf("no integrations checkout beside server/: %v", err)
	}
	out := t.TempDir()
	files, err := Generate(src, out)
	if err != nil {
		t.Fatal(err)
	}
	return files, out
}

// The packages as committed in integrations/: each unpacks with exactly the files and hash of its sources, and stays
// under MaxSize, the bound every package is offered and fetched within.
func TestGeneratedPackages(t *testing.T) {
	files, out := generateIntegrations(t)
	d := &dist{fsys: os.DirFS(out), root: "."}
	for name, want := range files {
		p, err := d.load(name)
		if err != nil || p == nil {
			t.Fatalf("%s: %v %v", name, p, err)
		}
		want = append([]File(nil), want...)
		sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
		if !reflect.DeepEqual(p.Files, want) || p.Hash != Hash(want) {
			t.Fatalf("%s: unpacked files or hash differ from integrations/", name)
		}
		size := 0
		for _, f := range p.Files {
			size += len(f.Data)
		}
		if size > MaxSize {
			t.Fatalf("%s: %d files, %d bytes (MaxSize %d)", name, len(p.Files), size, MaxSize)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
