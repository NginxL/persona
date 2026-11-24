package archguard

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The guards of Persona Core and its feature modules (docs/architecture/core-and-modules.md "Guards"). They read the
// import lines of every .go file, whatever its build constraint or GOOS: a file only a persona_core_only build, or
// only Windows, compiles is checked as well as any other.

const (
	repoPath   = "github.com/nwverse/persona/"
	modulesDir = "internal/modules"
	modulesAll = "internal/modules/all"
	// modulesNode is the node's composition root: the modules that also run on the user's computers.
	modulesNode = "internal/modules/all/node"
	modulesDoc  = `see docs/architecture/core-and-modules.md "Guards"`
)

// goFile is one .go file's package (its directory under the server module), whether it is a test, and its imports.
type goFile struct {
	pkg, name string
	test      bool
	imports   []string
}

// scanGoFiles reads the imports of every .go file under root (the server module's root), testdata, vendor and hidden
// directories left out.
func scanGoFiles(t *testing.T, root string) []goFile {
	t.Helper()
	var out []goFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir, _ := filepath.Rel(root, filepath.Dir(path))
		g := goFile{pkg: filepath.ToSlash(dir), name: name, test: strings.HasSuffix(name, "_test.go")}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			g.imports = append(g.imports, p)
		}
		out = append(out, g)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// coreModuleImports are the core's imports of a module, as "<package> <module package>" (a test file's as
// "<package> (test) <module package>"), and the roots that import internal/modules/all. A root's code may import
// internal/modules/all; so may any core package's tests, to run as the manager does, and a module's stand-in
// (internal/modules/<id>/<id>test, standIn), to play the outside service the module's program talks to; nothing else
// outside internal/modules imports a module.
func coreModuleImports(files []goFile, roots []string) (violations []string, rootsUsed map[string]bool) {
	rootsUsed = map[string]bool{}
	for _, f := range files {
		if under(f.pkg, modulesDir) {
			continue
		}
		for _, imp := range f.imports {
			rel, ok := strings.CutPrefix(imp, modulePath)
			if !ok || !under(rel, modulesDir) {
				continue
			}
			switch {
			case (rel == modulesAll || rel == modulesNode) && f.test:
			case f.test && standIn(rel): // a fake of the outside service a module talks to (standInProblems)
			case (rel == modulesAll || rel == modulesNode) && slices.Contains(roots, f.pkg):
				rootsUsed[f.pkg] = true
			case f.test:
				violations = append(violations, f.pkg+" (test) "+rel)
			default:
				violations = append(violations, f.pkg+" "+rel)
			}
		}
	}
	slices.Sort(violations)
	return slices.Compact(violations), rootsUsed
}

// No package of the core imports a module: only the modules themselves and the composition roots (testdata/
// module_roots.txt) import internal/modules/…, and a root imports only internal/modules/all. The core reaches a
// module through the kernel (internal/assistant/kernel), so a module can be left out (build tags) or switched off.
func TestCoreImportsNoModule(t *testing.T) {
	roots := readList(t, "module_roots.txt")
	baseline := readList(t, "module_imports_baseline.txt")
	violations, used := coreModuleImports(scanGoFiles(t, filepath.Join("..", "..")), roots)
	for _, v := range violations {
		if !slices.Contains(baseline, v) {
			t.Errorf("%s: the core never imports a module; ask the kernel (internal/assistant/kernel) for what the module "+
				"added through modapi, or, for a binary's composition root, import %s (the node: %s) and list the package in "+
				"testdata/module_roots.txt; %s", v, modulesAll, modulesNode, modulesDoc)
		}
	}
	for _, b := range baseline {
		if !slices.Contains(violations, b) {
			t.Errorf("testdata/module_imports_baseline.txt lists %q, which no longer happens: delete the line", b)
		}
	}
	for _, r := range roots {
		if !used[r] {
			t.Errorf("testdata/module_roots.txt lists %s, which imports neither %s nor %s: delete the line", r, modulesAll, modulesNode)
		}
	}
}

// moduleManifest is what the guard reads of a module's module.json (schema/module.schema.json).
type moduleManifest struct {
	ID    string   `json:"id"`
	Needs []string `json:"needs"`
}

// moduleImportProblems checks the modules under root/internal/modules: each directory but all has a module.json whose
// id is its name, and every file in it (tests and every build constraint included) imports only the standard library,
// third-party packages, internal/modapi and the other contract packages, the leaf libraries (testdata/libraries.txt),
// its own packages, and the modules its manifest needs. A module it only wants may be left out of the build
// (persona_without_<id>), so it is reached through the kernel, never imported. The known exceptions (services, from
// testdata/module_services.txt) are "<module id> <core package>" for any file of the module and "<module id> (test)
// <core package>" for its tests only; used says which were needed.
func moduleImportProblems(t *testing.T, root string, files []goFile, contracts, libraries, services []string) (problems []string, checked int, used map[string]bool) {
	used = map[string]bool{}
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, modulesDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "all" {
			continue
		}
		id := e.Name()
		raw, err := os.ReadFile(filepath.Join(root, modulesDir, id, "module.json"))
		if err != nil {
			problems = append(problems, "internal/modules/"+id+" has no module.json: every module declares itself (schema/module.schema.json)")
			continue
		}
		var m moduleManifest
		if err := json.Unmarshal(raw, &m); err != nil || m.ID != id {
			problems = append(problems, "internal/modules/"+id+"/module.json: id "+strconv.Quote(m.ID)+", want the directory's name")
			continue
		}
		own := modulesDir + "/" + id
		allowed := []string{own}
		for _, need := range m.Needs {
			allowed = append(allowed, modulesDir+"/"+need)
		}
		for _, f := range files {
			if !under(f.pkg, own) {
				continue
			}
			for _, imp := range f.imports {
				rel, inServer := strings.CutPrefix(imp, modulePath)
				switch {
				case !inServer && !strings.Contains(strings.SplitN(imp, "/", 2)[0], "."): // the standard library
				case !inServer && (slices.Contains(contracts, imp) || slices.Contains(libraries, imp)):
				case !inServer && !strings.HasPrefix(imp, repoPath): // a third-party package: not Persona's core
				case !inServer:
					problems = append(problems, "module "+id+" ("+f.pkg+"/"+f.name+") imports "+imp+": of Persona's own packages "+
						"outside the server a module uses only the listed contracts and libraries")
				case slices.Contains(contracts, rel), slices.Contains(libraries, rel),
					slices.ContainsFunc(allowed, func(a string) bool { return under(rel, a) }):
				case f.test && slices.Contains(services, id+" (test) "+rel):
					used[id+" (test) "+rel] = true
				case slices.Contains(services, id+" "+rel):
					used[id+" "+rel] = true
				default:
					problems = append(problems, "module "+id+" ("+f.pkg+"/"+f.name+") imports "+rel+": a module reaches the core only "+
						"through internal/modapi (add what it needs to modapi.Host) and imports another module only when its "+
						"module.json needs it (a module it wants is reached through the kernel)")
				}
			}
		}
		checked++
	}
	return problems, checked, used
}

// A module uses only the module API (moduleImportProblems). The known exceptions (testdata/module_services.txt) only
// shrink: a line no import needs fails.
func TestModulesUseOnlyTheSPI(t *testing.T) {
	root := filepath.Join("..", "..")
	services := readList(t, "module_services.txt")
	problems, checked, used := moduleImportProblems(t, root, scanGoFiles(t, root), readList(t, "contracts.txt"), readList(t, "libraries.txt"), services)
	for _, p := range problems {
		t.Errorf("%s; %s", p, modulesDoc)
	}
	for _, s := range services {
		if !used[s] {
			t.Errorf("testdata/module_services.txt lists %q, which no module file needs: delete the line", s)
		}
	}
	if checked == 0 {
		t.Error("no module under internal/modules: the guard checks nothing")
	}
}

// The guards see files no default build compiles: a core file only persona_core_only builds that imports a module,
// a Windows-only module file that imports the core, and a module that imports one it only wants are each caught. A
// core test may use a module's stand-in but not the module, and a stand-in that imports its module is caught.
func TestModuleGuardsSeeEveryBuild(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	imp := func(p string) string { return strconv.Quote(modulePath + p) }
	write("internal/assistant/core_only.go", "//go:build persona_core_only\n\npackage assistant\n\nimport _ "+imp("internal/modules/briefing")+"\n")
	write("internal/assistant/x_test.go", "package assistant\n\nimport _ "+imp("internal/modules/all")+"\n")
	write("internal/manager/modules.go", "package manager\n\nimport _ "+imp("internal/modules/all")+"\n")
	write("internal/nodecmd/modules.go", "package nodecmd\n\nimport _ "+imp("internal/modules/all/node")+"\n")
	write("internal/node/modules.go", "package node\n\nimport _ "+imp("internal/modules/all/node")+"\n")
	write("internal/extmgr/v_test.go", "package extmgr\n\nimport _ "+imp("internal/modules/voice/voicetest")+"\n")
	write("internal/extmgr/w_test.go", "package extmgr\n\nimport _ "+imp("internal/modules/voice")+"\n")
	write("internal/modules/voice/voicetest/fake.go", "package voicetest\n\nimport _ "+imp("internal/modules/voice")+"\n")
	write("internal/modules/briefing/module.json", `{"id":"briefing","api":1,"origin":"builtin","wants":["voice"]}`)
	write("internal/modules/briefing/b_windows.go", "package briefing\n\nimport _ "+imp("internal/assistant")+"\n")
	write("internal/modules/briefing/b.go", "package briefing\n\nimport (\n\t_ \"encoding/json\"\n\t_ "+imp("internal/modapi")+
		"\n\t_ "+imp("internal/modules/voice")+"\n)\n")
	write("internal/modules/voice/module.json", `{"id":"voice","api":1,"origin":"builtin"}`)
	write("internal/modules/voice/v.go", "//go:build !persona_without_voice\n\npackage voice\n")
	files := scanGoFiles(t, root)

	violations, used := coreModuleImports(files, []string{"internal/manager", "internal/nodecmd"})
	if want := []string{"internal/assistant internal/modules/briefing", "internal/extmgr (test) internal/modules/voice",
		"internal/node internal/modules/all/node"}; !slices.Equal(violations, want) || !used["internal/manager"] || !used["internal/nodecmd"] {
		t.Fatalf("core imports %v (roots %v), want %v", violations, used, want)
	}
	problems, checked, _ := moduleImportProblems(t, root, files, []string{"internal/modapi"}, nil, nil)
	if checked != 2 || len(problems) != 2 || !strings.Contains(problems[0]+problems[1], "imports internal/assistant") ||
		!strings.Contains(problems[0]+problems[1], "imports internal/modules/voice") {
		t.Fatalf("checked %d modules, problems %q", checked, problems)
	}
	// A known exception for a module's tests does not cover its other files; one for any file does.
	if p, _, used := moduleImportProblems(t, root, files, []string{"internal/modapi"}, nil, []string{"briefing (test) internal/assistant"}); len(p) != 2 || len(used) != 0 {
		t.Fatalf("a tests-only exception: problems %q, used %v", p, used)
	}
	if p, _, used := moduleImportProblems(t, root, files, []string{"internal/modapi"}, nil, []string{"briefing internal/assistant"}); len(p) != 1 || !used["briefing internal/assistant"] {
		t.Fatalf("an exception for any file: problems %q, used %v", p, used)
	}
	if p := standInProblems(files, []string{"internal/modapi"}, nil); len(p) != 1 || !strings.Contains(p[0], "voicetest") {
		t.Fatalf("stand-in problems %q", p)
	}
}

// standIn says whether rel (a path under the server module) is a module's stand-in: internal/modules/<id>/<id>test, a
// fake of the outside service the module's program talks to (the Volcengine service, a realtime voice upstream).
func standIn(rel string) bool {
	rest, ok := strings.CutPrefix(rel, modulesDir+"/")
	id, sub, nested := strings.Cut(rest, "/")
	return ok && nested && id != "all" && sub == id+"test"
}

// standInProblems checks that a stand-in holds none of its module's code: it imports only the standard library,
// third-party packages, the contracts and the leaf libraries. That is what lets a core package's tests use it (the
// extension host's tests run a module's program against it) without reaching into the module.
func standInProblems(files []goFile, contracts, libraries []string) (problems []string) {
	for _, f := range files {
		if !standIn(f.pkg) || f.test {
			continue
		}
		for _, imp := range f.imports {
			rel, inServer := strings.CutPrefix(imp, modulePath)
			switch {
			case !inServer && !strings.HasPrefix(imp, repoPath):
			case !inServer && (slices.Contains(contracts, imp) || slices.Contains(libraries, imp)):
			case inServer && (slices.Contains(contracts, rel) || slices.Contains(libraries, rel)):
			default:
				problems = append(problems, "stand-in "+f.pkg+" ("+f.name+") imports "+imp+": a stand-in fakes the outside "+
					"service and holds none of the module's code, so core tests may use it")
			}
		}
	}
	return problems
}

// A module's stand-in holds none of its code (standInProblems).
func TestStandInsHoldNoModuleCode(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, p := range standInProblems(scanGoFiles(t, root), readList(t, "contracts.txt"), readList(t, "libraries.txt")) {
		t.Errorf("%s; %s", p, modulesDoc)
	}
}

// Every leaf library (testdata/libraries.txt), the packages of the core a module may use, depends only on the standard
// library, third-party packages, the contracts and the other libraries: no manager, no hub, no node, no module, no
// store. A module that uses one so reaches no business logic of the core and nothing stateful of it.
func TestLibrariesAreLeaves(t *testing.T) {
	contracts, libraries := readList(t, "contracts.txt"), readList(t, "libraries.txt")
	for _, l := range libraries {
		if slices.Contains(contracts, l) {
			t.Errorf("testdata/libraries.txt lists %s, which is a contract: delete the line", l)
		}
		for _, dep := range goList(t, "{{if not .Standard}}{{.ImportPath}}{{end}}", true, l) {
			rel, inServer := strings.CutPrefix(dep, modulePath)
			switch {
			case dep == importPath(l):
			case !inServer && !strings.HasPrefix(dep, repoPath):
			case !inServer && (slices.Contains(contracts, dep) || slices.Contains(libraries, dep)):
			case inServer && (slices.Contains(contracts, rel) || slices.Contains(libraries, rel)):
			default:
				t.Errorf("library %s depends on %s, which is neither a contract nor a listed library: a module may import "+
					"%s, so it must stay a leaf; move what it needs into a library or a contract, or take %s off the "+
					"list; %s", l, dep, l, l, modulesDoc)
			}
		}
	}
}
