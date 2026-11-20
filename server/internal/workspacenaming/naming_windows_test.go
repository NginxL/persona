package workspacenaming

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func windowsNamingSource() Source {
	identity, _ := repository.Parse("https://github.com/example/web", "")
	rows := []repository.TaskRepository{{RepoID: "web", RequestedRef: repository.RequestedRef{Kind: "default"}, SourceSnapshot: repository.SourceSnapshot{Identity: identity}}}
	snapshot, _ := repository.Snapshot("example", "todo_example", 1, rows, []string{"web"})
	return Source{Manager: "example", TodoID: "todo_example", MachineID: "computer", Title: "Example task", PlannedAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: Defaults(), Layout: repository.LayoutContainer, Repositories: snapshot}
}

// These lexical tests use the Windows standard library and perform no filesystem
// or Git operations. Building them for Windows is not evidence of running them.
func TestWindowsWorkspaceNamingNativePaths(t *testing.T) {
	for _, home := range []string{"C:/Users/example", `C:\Users\example`} {
		for _, tc := range []struct{ base, want string }{
			{"C:/work", `C:\work`},
			{"C:/work/", `C:\work`},
			{"D:/work", `D:\work`},
			{"{home}/workspace", `C:\Users\example\workspace`},
			{"~/work", `C:\Users\example\work`},
			{"//files.example.com/share/work", `\\files.example.com\share\work`},
		} {
			for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
				t.Run(home+"/"+tc.base+"/"+string(layout), func(t *testing.T) {
					s := windowsNamingSource()
					s.Layout, s.Rules.BaseDirectory = layout, tc.base
					if err := Validate(s.Rules); err != nil {
						t.Fatal(err)
					}
					p := Resolve(s, NodeContext{Home: home, PathFlavor: "windows"})
					if p.Status != "complete" || p.Plan == nil || p.Plan.BasePath != tc.want || p.Plan.RootPath != tc.want+`\task-todo_example` || p.Plan.Node.PathFlavor != "windows" {
						t.Fatal(p)
					}
					wantRelative, wantAbsolute := ".", p.Plan.RootPath
					if layout == repository.LayoutContainer {
						wantRelative, wantAbsolute = "web", wantAbsolute+`\web`
					}
					if got := p.Plan.Repositories[0]; got.RelativePath != wantRelative || got.AbsolutePath != wantAbsolute || got.RequestedRef.Kind != "default" {
						t.Fatal(got)
					}
					if CheckPlan(*p.Plan) != nil || !reflect.DeepEqual(p, Resolve(s, NodeContext{Home: home, PathFlavor: "windows"})) {
						t.Fatal("Windows resolution was not deterministic")
					}
				})
			}
		}
	}
}

func TestWindowsWorkspaceNamingPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, home, state, flavor, base, task, code string
	}{
		{"drive relative home", "C:Users/example", "", "windows", "C:/work", "", "context_invalid"},
		{"rooted home without drive", "/Users/example", "", "windows", "C:/work", "", "context_invalid"},
		{"wrong target flavor", "C:/Users/example", "", "posix", "C:/work", "", "context_invalid"},
		{"drive relative base", "C:/Users/example", "", "windows", "C:work", "", "path_invalid"},
		{"rooted base without drive", "C:/Users/example", "", "windows", "/work", "", "base_scope_denied"},
		{"state base", "C:/Users/example", "", "windows", "C:/Users/example/.persona/work", "", "base_scope_denied"},
		{"state case and separators", "C:/Users/example", `C:\Users\example\.persona`, "windows", "c:/USERS/EXAMPLE/.PERSONA/work", "", "base_scope_denied"},
		{"custom state", "C:/Users/example", "D:/private-state", "windows", "d:/PRIVATE-STATE/work", "", "base_scope_denied"},
		{"UNC state", "C:/Users/example", `\\files.example.com\share\state`, "windows", "//FILES.example.com/share/STATE/work", "", "base_scope_denied"},
		{"base traversal", "C:/Users/example", "", "windows", "C:/work/../outside", "", "path_invalid"},
		{"task traversal", "C:/Users/example", "", "windows", "C:/work", "../outside", "path_invalid"},
		{"task drive", "C:/Users/example", "", "windows", "C:/work", "D:/outside", "path_invalid"},
		{"task separator injection", "C:/Users/example", "", "windows", "C:/work", `part\outside`, "template_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := windowsNamingSource()
			s.Rules.BaseDirectory = tc.base
			if tc.task != "" {
				s.Rules.TaskDirectoryTemplate = tc.task
			}
			p := Resolve(s, NodeContext{Home: tc.home, PersonaHome: tc.state, PathFlavor: tc.flavor})
			if p.Plan != nil || p.Status != "invalid" || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != tc.code {
				t.Fatal(p)
			}
		})
	}
	if within(`C:\work`, `C:\work-other\task`) || within(`C:\work`, `D:\work\task`) || within(`C:\work`, `C:\work\..\outside`) || within(`\\files.example.com\share`, `\\files.example.com\other\task`) {
		t.Fatal("containment accepted an unrelated path")
	}
	if !within(`C:\work`, `c:\WORK\task`) {
		t.Fatal("containment rejected Windows case equivalence")
	}
	// Templates use slash separators on every platform; native Node home values
	// may use backslashes, but allowing them in templates is not part of the contract.
	for _, base := range []string{`C:\work`, `\\files.example.com\share\work`} {
		if err := Validate(Rules{BaseDirectory: base}); err == nil || !strings.Contains(err.Error(), "template_invalid") {
			t.Fatal("native template separators unexpectedly accepted", base, err)
		}
	}
}

// Namespace paths have different volume names even when they name the same
// directory. They must not bypass the private-state check in either direction.
func TestWindowsWorkspaceNamingNamespaces(t *testing.T) {
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		for _, tc := range []struct{ name, home, state, base string }{
			{"extended drive base", "C:/Users/example", `C:\Users\example\.persona`, "//?/C:/Users/example/.persona/work"},
			{"extended drive custom state", "C:/Users/example", "C:/private-state", "//?/c:/PRIVATE-STATE/work"},
			{"extended UNC base", "C:/Users/example", `\\files.example.com\share\state`, "//?/UNC/FILES.example.com/share/STATE/work"},
			{"device drive base", "C:/Users/example", "C:/private-state", "//./C:/private-state/work"},
			{"NT drive base", "C:/Users/example", "C:/private-state", "/??/C:/private-state/work"},
			{"namespace outside state", "C:/Users/example", "C:/private-state", "//?/D:/work"},
			{"native namespace template", "C:/Users/example", "C:/private-state", `\\?\C:\private-state\work`},
			{"mixed namespace template", "C:/Users/example", "C:/private-state", `/\?\C:/private-state/work`},
			{"extended drive home", `\\?\C:\Users\example`, "C:/private-state", "{home}/workspace"},
			{"extended UNC home", "//?/unc/files.example.com/share", "C:/private-state", "~/workspace"},
			{"extended drive state", "C:/Users/example", `\\?\C:\private-state`, "C:/PRIVATE-STATE/work"},
			{"extended UNC state", "C:/Users/example", `\\?\UNC\files.example.com\share\state`, "//FILES.example.com/share/STATE/work"},
			{"device state", "C:/Users/example", "//./C:/private-state", "C:/private-state/work"},
			{"NT state", "C:/Users/example", `\??\C:\private-state`, "C:/private-state/work"},
		} {
			t.Run(string(layout)+"/"+tc.name, func(t *testing.T) {
				s := windowsNamingSource()
				s.Layout, s.Rules.BaseDirectory = layout, tc.base
				p := Resolve(s, NodeContext{Home: tc.home, PersonaHome: tc.state, PathFlavor: "windows"})
				if p.Status != "invalid" || p.Plan != nil || len(p.Diagnostics) != 1 {
					t.Fatalf("namespace path returned a plan: %+v", p)
				}
			})
		}
	}
}

func TestWindowsWorkspaceNamingReservedTaskPaths(t *testing.T) {
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		for _, path := range reservedTaskDirectories() {
			t.Run(string(layout)+"/"+path, func(t *testing.T) {
				s := windowsNamingSource()
				s.Layout, s.Rules.TaskDirectoryTemplate = layout, path
				p := Resolve(s, NodeContext{Home: `C:\Users\example`, PathFlavor: "windows"})
				if p.Status != "invalid" || p.Plan != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != "path_invalid" || p.Diagnostics[0].Field != "task_directory_template" {
					t.Fatalf("reserved task directory produced a Windows plan: %+v", p)
				}
			})
		}
		for _, title := range []string{"NUL", "CON", "COM1", "LPT9"} {
			s := windowsNamingSource()
			s.Layout, s.Title = layout, title
			s.Rules.TaskDirectoryTemplate = "{task-id}/{task-slug}/work"
			if err := Validate(s.Rules); err != nil {
				t.Fatal(err)
			}
			if p := Resolve(s, NodeContext{Home: `C:\Users\example`, PathFlavor: "windows"}); p.Status != "invalid" || p.Plan != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != "path_invalid" {
				t.Errorf("reserved title produced a Windows plan: %q: %+v", title, p)
			}
		}
		for _, path := range portableTaskDirectories() {
			s := windowsNamingSource()
			s.Layout, s.Rules.TaskDirectoryTemplate = layout, path
			p := Resolve(s, NodeContext{Home: `C:\Users\example`, PathFlavor: "windows"})
			if p.Status != "complete" || p.Plan == nil {
				t.Errorf("ordinary Windows path rejected: %q: %+v", path, p)
			}
		}
	}
}
