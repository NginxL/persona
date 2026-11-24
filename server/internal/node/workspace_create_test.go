package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestWorkspaceProjectCreatesOnlyRequestedDirectory(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home}
	dir := filepath.Join(home, "Project with spaces")
	create := func() {
		t.Helper()
		w, err := n.createWorkspaceProject(context.Background(), dir, "My project", "", "", true)
		if err != nil || w.Cwd != realPath(dir) || w.Title != "My project" || w.Source != "directory" || !w.Available {
			t.Fatalf("project = %+v, %v", w, err)
		}
	}
	create()
	file := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	create()
	if b, err := os.ReadFile(file); err != nil || string(b) != "keep" {
		t.Fatalf("retry replaced contents: %q %v", b, err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("created unrelated resources: %v", entries)
	}
}

func TestWorkspaceProjectAlreadyBoundHasNoEffects(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, state: newState()}
	assertUnchanged := watchWorkspaceNativeProjectEffects(t, n)
	for _, existing := range []bool{false, true} {
		dir := filepath.Join(home, "Project with spaces")
		if existing {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		bound := dir
		if runtime.GOOS == "windows" {
			bound = strings.ToUpper(dir)
		}
		w, err := n.createWorkspaceProjectWithBindings(context.Background(), dir, "Renamed project", "codex_app", "different-key", true, []string{bound})
		if err == nil || !strings.Contains(err.Error(), "already bound") || !reflect.DeepEqual(w, proto.Workspace{}) {
			t.Fatalf("existing=%v: workspace=%+v error=%v", existing, w, err)
		}
		if existing {
			if b, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(b) != "untouched" {
				t.Fatalf("existing file changed: %q %v", b, err)
			}
		} else {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("duplicate request created a directory: %v", err)
			}
		}
		assertUnchanged()
	}
}

func TestWorkspaceProjectAlreadyBoundResolvesDirectoryAlias(t *testing.T) {
	home := t.TempDir()
	dir, alias := filepath.Join(home, "real"), filepath.Join(home, "alias")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	n := &Node{home: home}
	if _, err := n.createWorkspaceProjectWithBindings(context.Background(), alias, "Renamed project", "", "", true, []string{dir}); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("alias bypassed duplicate check: %v", err)
	}
}

func TestWorkspaceProjectRejectsBeforeCreating(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home}
	for _, tc := range []struct {
		name, app, key, path string
		create               bool
	}{
		{"", "", "", filepath.Join(home, "empty-name"), true},
		{"Project", "unsupported", "", filepath.Join(home, "bad-app"), true},
		{"Project", "codex_app", "", filepath.Join(home, "no-key"), true},
		{"Project", "", "", filepath.Join(home, "missing", "parent"), true},
		{"Project", "", "", "relative-path", true},
		{"Project", "", "", filepath.Join(home, "no-create"), false},
	} {
		if _, err := n.createWorkspaceProject(context.Background(), tc.path, tc.name, tc.app, tc.key, tc.create); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("refusals created files: %v", entries)
	}
}

func TestWorkspaceProjectDoesNotReplaceFileOrEscapeScope(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.createWorkspaceProject(context.Background(), file, "Project", "", "", true); err == nil {
		t.Fatal("accepted a file")
	}
	b, _ := os.ReadFile(file)
	if string(b) != "keep" {
		t.Fatal("existing file changed")
	}
	n.sessionScope = &nodeSessionScope{dir: filepath.Join(home, "scope")}
	if _, err := n.createWorkspaceProject(context.Background(), filepath.Join(home, "outside"), "Project", "", "", true); err == nil {
		t.Fatal("escaped scope")
	}
	if _, err := os.Stat(filepath.Join(home, "outside")); !os.IsNotExist(err) {
		t.Fatal("created outside scope")
	}
}

func TestWorkspaceNativeProjectSyncRefusesBeforeEffects(t *testing.T) {
	for _, app := range []string{"codex_app", "claude_desktop"} {
		t.Run(app, func(t *testing.T) {
			home := t.TempDir()
			n := &Node{home: home, state: newState()}
			assertUnchanged := watchWorkspaceNativeProjectEffects(t, n)
			for _, existing := range []bool{false, true} {
				for _, createDirectory := range []bool{false, true} {
					dir := filepath.Join(home, fmt.Sprintf("project-%t-%t", existing, createDirectory))
					if existing {
						if err := os.Mkdir(dir, 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("untouched"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					w, err := n.createWorkspaceProject(context.Background(), dir, "Named project", app, "stable-key", createDirectory)
					code, data := refusalOf(t, err)
					if code != "desktop_project_sync_unavailable" || data["desktop_app"] != app || data["delivered"] != "none" {
						t.Fatalf("refusal = %s %#v", code, data)
					}
					if !reflect.DeepEqual(w, proto.Workspace{}) {
						t.Fatalf("unsupported creation returned a workspace: %+v", w)
					}
					if existing {
						entries, err := os.ReadDir(dir)
						if err != nil || len(entries) != 1 || entries[0].Name() != "keep.txt" {
							t.Fatalf("existing directory changed: %v %v", entries, err)
						}
						if b, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(b) != "untouched" {
							t.Fatalf("existing file changed: %q %v", b, err)
						}
					} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatalf("unsupported creation made a directory: %v", err)
					}
					assertUnchanged()
				}
			}
		})
	}
}

func TestWorkspaceNativeProjectDuplicatePrecedesSyncRefusal(t *testing.T) {
	for _, app := range []string{"codex_app", "claude_desktop"} {
		t.Run(app, func(t *testing.T) {
			home := t.TempDir()
			n := &Node{home: home, state: newState()}
			assertUnchanged := watchWorkspaceNativeProjectEffects(t, n)
			for _, existing := range []bool{false, true} {
				dir := filepath.Join(home, fmt.Sprintf("bound-%t", existing))
				if existing {
					if err := os.Mkdir(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("untouched"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				w, err := n.createWorkspaceProjectWithBindings(context.Background(), dir, "Renamed project", app, "stable-key", true, []string{dir})
				if err == nil || !strings.Contains(err.Error(), "already bound") || !reflect.DeepEqual(w, proto.Workspace{}) {
					t.Fatalf("duplicate = %+v %v", w, err)
				}
				if existing {
					if b, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(b) != "untouched" {
						t.Fatalf("existing file changed: %q %v", b, err)
					}
				} else {
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatalf("duplicate request made a directory: %v", err)
					}
				}
				assertUnchanged()
			}
		})
	}
}

func watchWorkspaceNativeProjectEffects(t *testing.T, n *Node) func() {
	t.Helper()
	nativeHome := n.codexHome()
	if err := os.Mkdir(nativeHome, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]string{
		".codex-global-state.json": `{"project-name":"keep existing sidebar"}`,
		"state_5.sqlite":           "untouched native metadata fixture",
	}
	for name, contents := range metadata {
		if err := os.WriteFile(filepath.Join(nativeHome, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldCLI := codexAppCLI
	codexAppCLI = func() (string, error) {
		t.Fatal("refused workspace request reached native CLI discovery")
		return "", nil
	}
	t.Cleanup(func() { codexAppCLI = oldCLI })
	return func() {
		t.Helper()
		entries, err := os.ReadDir(nativeHome)
		if err != nil || len(entries) != len(metadata) {
			t.Fatalf("native metadata directory changed: %v %v", entries, err)
		}
		for name, contents := range metadata {
			if b, err := os.ReadFile(filepath.Join(nativeHome, name)); err != nil || string(b) != contents {
				t.Fatalf("native metadata %q changed: %q %v", name, b, err)
			}
		}
		if len(n.state.Sessions) != 0 {
			t.Fatalf("refused project request created session state: %+v", n.state.Sessions)
		}
	}
}
