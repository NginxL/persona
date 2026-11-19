package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// resetExtraPathForTest forgets what node.configure said once the test is done (t.Setenv puts PATH back).
func resetExtraPathForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		extraPath.mu.Lock()
		extraPath.base, extraPath.dirs = nil, nil
		extraPath.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// The shims directories of mise and asdf count when they exist, where their variables or defaults put them.
func TestVersionManagerShims(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := versionManagerShims(home, getenv); len(got) != 0 {
		t.Fatalf("no version manager: %q", got)
	}
	mise, asdf := mkdir(t, filepath.Join(home, ".local", "share", "mise", "shims")), mkdir(t, filepath.Join(home, ".asdf", "shims"))
	if got := versionManagerShims(home, getenv); !slices.Equal(got, []string{mise, asdf}) {
		t.Fatalf("defaults: %q", got)
	}
	data := t.TempDir()
	env["MISE_DATA_DIR"], env["ASDF_DATA_DIR"] = filepath.Join(data, "mise"), filepath.Join(data, "asdf")
	if got := versionManagerShims(home, getenv); len(got) != 0 {
		t.Fatalf("the variables' directories have no shims, the defaults do not count: %q", got)
	}
	mise, asdf = mkdir(t, filepath.Join(data, "mise", "shims")), mkdir(t, filepath.Join(data, "asdf", "shims"))
	if got := versionManagerShims(home, getenv); !slices.Equal(got, []string{mise, asdf}) {
		t.Fatalf("data-directory variables: %q", got)
	}
	env = map[string]string{"XDG_DATA_HOME": data}
	if got := versionManagerShims(home, getenv); !slices.Equal(got, []string{mise, filepath.Join(home, ".asdf", "shims")}) {
		t.Fatalf("XDG_DATA_HOME: %q", got)
	}
	if got := versionManagerShims("", func(string) string { return "" }); len(got) != 0 {
		t.Fatalf("no home: %q", got)
	}
}

// Shims go after what PATH has, once: a tool found before stays the one that runs.
func TestWithDirsAfter(t *testing.T) {
	sep := string(os.PathListSeparator)
	for _, c := range []struct {
		path string
		dirs []string
		want string
	}{
		{"/usr/bin" + sep + "/bin", []string{"/h/.local/share/mise/shims"}, "/usr/bin" + sep + "/bin" + sep + "/h/.local/share/mise/shims"},
		{"/h/.asdf/shims" + sep + "/usr/bin", []string{"/h/.asdf/shims"}, "/h/.asdf/shims" + sep + "/usr/bin"},
		{"/h/.asdf/shims/" + sep + "/usr/bin", []string{"/h/.asdf/shims"}, "/h/.asdf/shims/" + sep + "/usr/bin"},
		{"/usr/bin", nil, "/usr/bin"},
		{"", []string{"/s"}, "/s"},
	} {
		if got := withDirsAfter(c.path, c.dirs); got != c.want {
			t.Errorf("withDirsAfter(%q, %q) = %q, want %q", c.path, c.dirs, got, c.want)
		}
	}
}

// A node started with a service's short PATH has mise's shims at the end of it, and the configured directories go in
// front of what New made.
func TestNewNodeAddsVersionManagerShims(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"MISE_DATA_DIR", "ASDF_DATA_DIR", "XDG_DATA_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	resetExtraPathForTest(t)
	shims := mkdir(t, filepath.Join(home, ".local", "share", "mise", "shims"))
	n, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	made := os.Getenv("PATH")
	if !strings.HasPrefix(made, filepath.Join(home, ".local/bin")+":") || !strings.HasSuffix(made, ":/usr/bin:/bin:"+shims) {
		t.Fatalf("PATH %q", made)
	}
	if _, err := request(t, n, "node.configure", map[string]any{"extra_path": []string{"/opt/tools/bin"}}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != "/opt/tools/bin:"+made {
		t.Fatalf("configured PATH %q", got)
	}
}

// A resident node (persona-node attach) makes a new Node for each hub connection in one process. The next one starts
// from the PATH the last one made, without the directories hub.json added for it, so [] still takes them out, from
// PATH and from the herdr server's tool directories (review of #10131), and the node's own directories are not put in
// front again at each connection.
func TestReconnectedNodeCanStillTakeOutExtraPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"MISE_DATA_DIR", "ASDF_DATA_DIR", "XDG_DATA_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	resetExtraPathForTest(t)
	first, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	made := os.Getenv("PATH")
	if _, err := request(t, first, "node.configure", map[string]any{"extra_path": []string{"/opt/tools/bin"}}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != "/opt/tools/bin:"+made {
		t.Fatalf("configured PATH %q", got)
	}
	second, err := New("test") // the hub connection came back
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != made {
		t.Fatalf("PATH at the next connection\n got %q\nwant %q", got, made)
	}
	if _, err := request(t, second, "node.configure", map[string]any{"extra_path": []string{}}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != made {
		t.Fatalf("PATH after []\n got %q\nwant %q", got, made)
	}
	if got := herdrToolDirs(home); slices.Contains(got, "/opt/tools/bin") {
		t.Fatalf("herdr's tool directories kept the old entry: %q", got)
	}
	// someone else set PATH meanwhile: that one is the base, with the node's directories in front as at a first start
	t.Setenv("PATH", "/usr/local/sbin:/usr/bin")
	if _, err := New("test"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); !strings.HasPrefix(got, filepath.Join(home, ".local/bin")+":") || !strings.HasSuffix(got, ":/usr/local/sbin:/usr/bin") {
		t.Fatalf("PATH after it was set elsewhere %q", got)
	}
}

// hub.json's machines[].extra_path (node.configure extra_path) goes in front of the node's PATH, replaces what was
// said before, and [] takes it out; a call that says nothing of it, an older hub's, leaves PATH alone. Only "~/" and
// absolute directories count.
func TestConfigureExtraPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	base := "/usr/bin:/bin"
	t.Setenv("PATH", base)
	resetExtraPathForTest(t)
	resetExtraPath(base)
	n, home := testNode(t)
	configure := func(params map[string]any) {
		t.Helper()
		if _, err := request(t, n, "node.configure", params); err != nil {
			t.Fatal(err)
		}
	}
	configure(map[string]any{"extra_path": []string{"~/tools/bin", "/opt/tools/bin/", "relative/bin", "$HOME/bin", "/opt/tools/bin", " "}})
	if got, want := os.Getenv("PATH"), filepath.Join(home, "tools", "bin")+":/opt/tools/bin:"+base; got != want {
		t.Fatalf("PATH\n got %q\nwant %q", got, want)
	}
	configure(map[string]any{"botmux": false})
	if got, want := os.Getenv("PATH"), filepath.Join(home, "tools", "bin")+":/opt/tools/bin:"+base; got != want {
		t.Fatalf("a call without extra_path changed PATH: %q", got)
	}
	configure(map[string]any{"extra_path": []string{"/opt/other/bin"}})
	if got := os.Getenv("PATH"); got != "/opt/other/bin:"+base {
		t.Fatalf("replaced: %q", got)
	}
	if got := herdrToolDirs(home); !slices.Equal(got, []string{"/opt/other/bin"}) {
		t.Fatalf("herdr's tool directories: %q", got)
	}
	shims := mkdir(t, filepath.Join(home, ".local", "share", "mise", "shims"))
	if got := herdrToolDirs(home); !slices.Equal(got, []string{"/opt/other/bin", shims}) {
		t.Fatalf("herdr's tool directories with mise: %q", got)
	}
	configure(map[string]any{"extra_path": []string{}})
	if got := os.Getenv("PATH"); got != base {
		t.Fatalf("taken out: %q", got)
	}
}

// The herdr server's login shell starts from the system's directories with the tool directories after them, and
// still nothing of the node's own PATH.
func TestHerdrServerEnvHasToolDirs(t *testing.T) {
	environ := []string{"HOME=/Users/me", "PATH=/Users/me/.persona/bin:/usr/bin"}
	got := herdrServerEnv(environ, "/bin/zsh", []string{"/opt/tools/bin", "/Users/me/.local/share/mise/shims"})
	if !slices.Contains(got, "PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/tools/bin:/Users/me/.local/share/mise/shims") {
		t.Fatalf("env %q", got)
	}
}

// What the issue asked for, with a real git: a repository whose LFS filter is a program only a version manager's
// shims directory has gets its worktree once the node's PATH has the shims, and says which program is missing when
// it does not.
func TestWorktreeFilterFromVersionManagerShims(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as the filter")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	home := t.TempDir()
	shims := mkdir(t, filepath.Join(home, ".local", "share", "mise", "shims"))
	if err := os.WriteFile(filepath.Join(shims, "persona-test-lfs"), []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(gitBin) + ":/usr/bin:/bin"
	t.Setenv("PATH", base)
	repo := mkdir(t, filepath.Join(home, "proj"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "a.bin"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.bin filter=lfs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, repo, "add", ".")
	runPullGit(t, repo, "commit", "-m", "first")
	runPullGit(t, repo, "config", "filter.lfs.smudge", "persona-test-lfs")
	runPullGit(t, repo, "config", "filter.lfs.required", "true")
	add := func(name string) error {
		_, err := gitTimed(context.Background(), repo, time.Minute, "worktree", "add", "-b", name, "--", filepath.Join(home, name))
		return err
	}
	err = add("without")
	if err == nil {
		t.Fatal("the filter ran without the shims on PATH")
	}
	if got := gitFailure(err); !strings.Contains(got, "persona-test-lfs") {
		t.Fatalf("the failure does not name the filter: %q (git said %q)", got, err)
	}
	t.Setenv("PATH", withDirsAfter(base, versionManagerShims(home, func(string) string { return "" })))
	if err := add("with"); err != nil {
		t.Fatalf("with the shims on PATH: %s", gitFailure(err))
	}
	if b, err := os.ReadFile(filepath.Join(home, "with", "a.bin")); err != nil || string(b) != "data\n" {
		t.Fatalf("checked out %q, %v", b, err)
	}
}
