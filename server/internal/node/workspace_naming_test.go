package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingUsesTargetHomeWithoutEngineOrCreation(t *testing.T) {
	for range 2 {
		home := t.TempDir()
		n := &Node{home: home, dir: filepath.Join(home, ".persona")}
		source := workspacenaming.Source{TodoID: "todo_example", Title: "Example", PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repository.LayoutRoot, Repositories: repository.PreparationSnapshot{Repositories: []repository.PreparationRepository{{RepoID: "web", RequestedRef: repository.RequestedRef{Kind: "default"}}}}}
		raw, _ := json.Marshal(source)
		out, err := n.handle(t.Context(), "workspace.naming.resolve", raw)
		if err != nil {
			t.Fatal(err)
		}
		preview := out.(workspacenaming.Preview)
		if preview.Plan == nil || preview.Plan.RootPath != filepath.Join(home, "workspace", "task-todo_example") || preview.Plan.Repositories[0].RelativePath != "." {
			t.Fatal(preview)
		}
		files, err := os.ReadDir(home)
		if err != nil || len(files) != 0 {
			t.Fatal("preview created resources", files, err)
		}
	}
}

func TestWorkspaceNamingRejectsPrivateSymlinkAncestors(t *testing.T) {
	for _, level := range []string{"base", "task", "repository"} {
		for _, target := range []string{"state", "ssh", "missing-ssh", "missing-step-secret"} {
			t.Run(level+"/"+target, func(t *testing.T) {
				home := t.TempDir()
				state := filepath.Join(t.TempDir(), "private-state")
				if err := os.Mkdir(state, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PERSONA_HOME", state)
				n := &Node{home: home, dir: state}
				to := state
				if target != "state" {
					to = filepath.Join(home, ".ssh")
					if target == "ssh" {
						if err := os.Mkdir(to, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if target == "missing-step-secret" {
						to = filepath.Join(home, "missing") + string(filepath.Separator) + ".." + string(filepath.Separator) + ".ssh"
					}
				}
				base := filepath.Join(home, "workspace")
				at := base
				if level == "task" {
					at = filepath.Join(base, "task-todo_example")
				}
				if level == "repository" {
					at = filepath.Join(base, "task-todo_example", "web")
				}
				if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(to, at); err != nil {
					t.Skip("symlinks:", err)
				}
				s := namingGuardSource()
				before := namingGuardTree(t, home, state)
				got := namingGuardPreview(t, n, s)
				if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
					t.Fatalf("%s link into %s accepted: %+v", level, target, got)
				}
				if level == "repository" && got.Diagnostics[0].RepoID != "web" {
					t.Fatal("missing repository attribution", got)
				}
				if !reflect.DeepEqual(before, namingGuardTree(t, home, state)) {
					t.Fatal("naming created or changed resources")
				}
			})
		}
	}
}

func TestWorkspaceNamingResolvesPrivateStateItself(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_HOME", state)
	alias := filepath.Join(home, "state-alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Skip("symlinks:", err)
	}
	if err := os.Symlink(state, filepath.Join(home, "workspace")); err != nil {
		t.Fatal(err)
	}
	s := namingGuardSource()
	s.Layout, s.Repositories.Repositories = repository.LayoutRoot, s.Repositories.Repositories[:1]
	n := &Node{home: home, dir: alias}
	before := namingGuardTree(t, home, state)
	got := namingGuardPreview(t, n, s)
	if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
		t.Fatal("physical private state accepted", got)
	}
	if !reflect.DeepEqual(before, namingGuardTree(t, home, state)) {
		t.Fatal("preview changed resources")
	}
}

func TestWorkspaceNamingMissingPrivateStateBehindSymlink(t *testing.T) {
	for _, link := range []string{"ancestor", "state-root"} {
		for _, level := range []string{"base", "task", "repository"} {
			for _, target := range []string{"state", "descendant", "sibling-prefix"} {
				t.Run(link+"/"+level+"/"+target, func(t *testing.T) {
					home, physical := t.TempDir(), t.TempDir()
					physicalState := filepath.Join(physical, "missing", "private-state")
					alias := filepath.Join(home, "state-alias")
					state, to := alias, physicalState
					if link == "ancestor" {
						state, to = filepath.Join(alias, "missing", "private-state"), physical
					}
					if err := os.Symlink(to, alias); err != nil {
						t.Skip("symlinks:", err)
					}
					t.Setenv("PERSONA_HOME", state)
					n := &Node{home: home, dir: state}
					resolved, err := n.realPath(state)
					if !errors.Is(err, fs.ErrNotExist) || resolved != physicalState {
						t.Fatalf("missing state = %q, %v; want physical tail %q", resolved, err, physicalState)
					}
					to = physicalState
					if target == "descendant" {
						to = filepath.Join(to, "project")
					} else if target == "sibling-prefix" {
						to += "-projects"
					}
					at := filepath.Join(home, "workspace")
					if level != "base" {
						at = filepath.Join(at, "task-todo_example")
					}
					if level == "repository" {
						at = filepath.Join(at, "web")
					}
					if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(to, at); err != nil {
						t.Fatal(err)
					}
					source := namingGuardSource()
					if lexical := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: home, PersonaHome: state}); lexical.Plan == nil {
						t.Fatal("fixture did not reach the physical guard", lexical)
					}
					before := namingGuardTree(t, home, physical)
					got := namingGuardPreview(t, n, source)
					if target == "sibling-prefix" {
						if got.Plan == nil || got.Status != "complete" || workspacenaming.CheckPlan(*got.Plan) != nil {
							t.Fatal("safe sibling metadata rejected or changed", got)
						}
					} else {
						if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
							t.Fatal("missing physical private state accepted", got)
						}
						if level == "repository" && got.Diagnostics[0].RepoID != "web" {
							t.Fatal("missing repository attribution", got)
						}
					}
					if !reflect.DeepEqual(before, namingGuardTree(t, home, physical)) {
						t.Fatal("preview changed resources")
					}
				})
			}
		}
	}
}

func TestWorkspaceNamingFailsClosedOnPathResolutionErrors(t *testing.T) {
	for _, kind := range []string{"loop", "not-directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			at := filepath.Join(home, "workspace")
			if kind == "loop" {
				if err := os.Symlink(at, at); err != nil {
					t.Skip("symlinks:", err)
				}
			} else if err := os.WriteFile(at, []byte("ordinary file"), 0o600); err != nil {
				t.Fatal(err)
			}
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			got := namingGuardPreview(t, n, namingGuardSource())
			if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
				t.Fatal("unresolved path accepted", got)
			}
		})
	}
}

func TestWorkspaceNamingAllowsSafeMissingTailAndSymlinks(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing tail", true: "safe link"}[linked], func(t *testing.T) {
			home := t.TempDir()
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			if linked {
				to := filepath.Join(home, "projects")
				if err := os.Mkdir(to, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(to, filepath.Join(home, "workspace")); err != nil {
					t.Skip("symlinks:", err)
				}
			}
			before := namingGuardTree(t, home)
			got := namingGuardPreview(t, n, namingGuardSource())
			if got.Status != "complete" || got.Plan == nil || got.Plan.RootPath != filepath.Join(home, "workspace", "task-todo_example") || workspacenaming.CheckPlan(*got.Plan) != nil {
				t.Fatal("safe metadata-only plan rejected or changed", got)
			}
			if !reflect.DeepEqual(before, namingGuardTree(t, home)) {
				t.Fatal("preview created resources")
			}
		})
	}
}

func namingGuardSource() workspacenaming.Source {
	return workspacenaming.Source{TodoID: "todo_example", Title: "Example", PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repository.LayoutContainer,
		Repositories: repository.PreparationSnapshot{Repositories: []repository.PreparationRepository{
			{RepoID: "api", DirectoryAlias: "api", RequestedRef: repository.RequestedRef{Kind: "default"}},
			{RepoID: "web", DirectoryAlias: "web", RequestedRef: repository.RequestedRef{Kind: "default"}},
		}}}
}

func namingGuardPreview(t *testing.T, n *Node, s workspacenaming.Source) workspacenaming.Preview {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(t.Context(), "workspace.naming.resolve", raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(workspacenaming.Preview)
}

func namingGuardTree(t *testing.T, roots ...string) map[string]os.FileMode {
	t.Helper()
	entries := map[string]os.FileMode{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err == nil {
				entries[path] = info.Mode()
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return entries
}
