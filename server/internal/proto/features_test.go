package proto

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The feature flags take a domain's flags from that domain's own file (docs/server/architecture.md "Registering a
// domain's entries", #12901). The Features literal in proto.go comes first and keeps its entries; a new flag is
// appended from an init() in proto/features_<domain>.go, beside the flag's constant:
//
//	func init() { Features = append(Features, FeatureX) }
//
// Go initializes every package-level variable before it runs any init(), and runs the init()s file by file in file-name
// order, so the hello and /health list the literal's flags in their order, then each domain file's, file by file.
// server/internal/archguard/central_lists_test.go keeps new names out of the literal, and scripts/test/test_doc_wire.py
// reads the appends, so every flag keeps its row in the protocol docs.

const featureSeamDoc = `docs/server/architecture.md "Registering a domain's entries"`

const featureSeamHint = "the package's own code only declares it, in proto.go, and appends a domain's flags from an " +
	"init() in that domain's file, proto/features_<domain>.go: func init() { Features = append(Features, FeatureX) }"

// featureDomainFile is the name of a domain's own file of flags.
var featureDomainFile = regexp.MustCompile(`^features_[a-z0-9]+(?:_[a-z0-9]+)*\.go$`)

// The flags the hub advertises are the literal's, in its order, then each domain file's appends in file order, and
// nothing else in the package touches the list.
func TestFeaturesAreTheLiteralThenTheDomainAppends(t *testing.T) {
	want, problems, err := featureSeam(".", filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	if !slices.Equal(Features, want) {
		t.Errorf("Features is not proto.go's literal followed by the features_<domain>.go appends in file order:\n%q\nwant\n%q",
			Features, want)
	}
	seen := map[string]bool{}
	for _, f := range Features {
		if seen[f] {
			t.Errorf("Features lists %q twice", f)
		}
		seen[f] = true
	}
}

// The seam's rules on planted sources: appends from the domain files come after the literal in file order, and a write
// anywhere else, a conditional append, a spread or a flag that is not a constant fails.
func TestFeatureSeamOnPlantedSources(t *testing.T) {
	const literal = "package proto\n\nconst FeatureB = \"b\"\n\nvar Features = []string{\n\t\"a\", // a comment\n\tFeatureB,\n}\n"
	base := map[string]string{
		"proto.go":          literal,
		"features_zeta.go":  "package proto\n\nconst FeatureZ = \"z\"\n\nfunc init() { Features = append(Features, FeatureZ, \"z2\") }\n",
		"features_alpha.go": "package proto\n\nconst FeatureAlpha = \"alpha\"\n\nfunc init() {\n\tFeatures = append(Features, FeatureAlpha)\n}\n",
		// File-name order: features_alpha.go, then features_alpha_beta.go.
		"features_alpha_beta.go": "package proto\n\nfunc init() { Features = append(Features, \"alpha_beta\" + \"_2\") }\n",
		// Test files are no part of what the hub advertises.
		"features_alpha_test.go": "package proto\n\nfunc init() { Features = append(Features, \"test_only\") }\n",
		// A read elsewhere is no write, and a local of the same name is another variable.
		"session.go": "package proto\n\nfunc local() []string {\n\tFeatures := []string{}\n\treturn append(Features, \"x\")\n}\n",
	}
	plant := func(files map[string]string) ([]string, []string) {
		t.Helper()
		dir := t.TempDir()
		for name, src := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, problems, err := featureSeam(dir, dir)
		if err != nil {
			t.Fatal(err)
		}
		return want, problems
	}
	if want, problems := plant(base); len(problems) != 0 || !slices.Equal(want, []string{"a", "b", "alpha", "alpha_beta_2", "z", "z2"}) {
		t.Fatalf("the planted seam reads as %q with problems %q", want, problems)
	}
	for _, c := range []struct {
		file, src string
		want      string // what the one problem says
	}{
		{"proto.go", literal + "\nfunc init() { Features = append(Features, \"c\") }\n", "Features is used outside its seam"},
		{"session.go", "package proto\n\nfunc init() { Features = append(Features, \"c\") }\n", "Features is used outside its seam"},
		{"features_x.go", "package proto\n\nfunc register() { Features = append(Features, \"c\") }\n", "Features is used outside its seam"},
		{"features_x.go", "package proto\n\nvar on bool\n\nfunc init() {\n\tif on {\n\t\tFeatures = append(Features, \"c\")\n\t}\n}\n",
			"Features is used outside its seam"},
		{"features_X.go", "package proto\n\nfunc init() { Features = append(Features, \"c\") }\n", "Features is used outside its seam"},
		{"features_x.go", "package proto\n\nfunc init() { Features[0] = \"c\" }\n", "Features is used outside its seam"},
		{"features_x.go", "package proto\n\nfunc init() { Features = append([]string{\"c\"}, Features...) }\n",
			"Features is used outside its seam"},
		{"session.go", "package proto\n\nimport \"slices\"\n\nfunc init() { slices.Sort(Features) }\n", "Features is used outside its seam"},
		{"session.go", "package proto\n\nfunc init() { _ = Features }\n", "Features is used outside its seam"},
		{"features_x.go", "package proto\n\nvar more = []string{\"c\"}\n\nfunc init() { Features = append(Features, more...) }\n",
			"spreads a slice"},
		{"features_x.go", "package proto\n\nvar c = \"c\"\n\nfunc init() { Features = append(Features, c) }\n", "c is not a constant string"},
		{"proto.go", strings.Replace(literal, "\tFeatureB,\n", "\tFeatureB,\n\tstring(rune(99)) + name(),\n", 1) +
			"\nfunc name() string { return \"\" }\n", "is not a constant string"},
	} {
		files := map[string]string{}
		for name, src := range base {
			files[name] = src
		}
		files[c.file] = c.src
		_, problems := plant(files)
		if len(problems) != 1 || !strings.Contains(problems[0], c.file+":") || !strings.Contains(problems[0], c.want) ||
			!strings.Contains(problems[0], featureSeamDoc) {
			t.Errorf("%s with\n%s\nreports %q, want one problem in it saying %q and citing the doc", c.file, c.src, problems, c.want)
		} else if c.want == "Features is used outside its seam" && !strings.Contains(problems[0], "proto/features_<domain>.go") {
			t.Errorf("%s: %q does not say where a flag goes", c.file, problems[0])
		}
	}
	if _, _, err := featureSeam(t.TempDir(), "."); err == nil {
		t.Error("an empty directory reads as a package")
	}
	if _, problems := plant(map[string]string{"session.go": "package proto\n\nvar Other []string\n"}); len(problems) != 1 ||
		!strings.Contains(problems[0], "no variable Features in proto.go") {
		t.Errorf("a package without Features reports %q", problems)
	}
}

// featureSeam reads the proto package in dir, whose module's directory is serverRoot: the flags it advertises, which are
// its Features literal then the domain files' appends, and how its code breaks the seam.
func featureSeam(dir, serverRoot string) (want, problems []string, err error) {
	fset := token.NewFileSet()
	files, err := featureGoFiles(fset, dir)
	if err != nil {
		return nil, nil, err
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: &featureImporter{fset: fset, root: serverRoot, pkgs: map[string]*types.Package{}},
		Error: func(error) {}}
	pkg, _ := conf.Check(featureModulePath+"internal/proto", fset, files, info)
	features, _ := pkg.Scope().Lookup("Features").(*types.Var)
	found := map[*ast.Ident]bool{} // the uses of Features that are the seam's
	declared := false
	problem := func(n ast.Node, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s: %s (%s)", fset.Position(n.Pos()), fmt.Sprintf(format, args...), featureSeamDoc))
	}
	flags := func(exprs []ast.Expr) []string {
		var out []string
		for _, e := range exprs {
			if tv := info.Types[e]; tv.Value != nil && tv.Value.Kind() == constant.String {
				out = append(out, constant.StringVal(tv.Value))
			} else {
				problem(e, "%s is not a constant string: a flag is a constant, beside it in its domain's file, so that "+
					"scripts/test/test_doc_wire.py can read it", types.ExprString(e))
			}
		}
		return out
	}
	for _, f := range files {
		name := filepath.Base(fset.File(f.Pos()).Name())
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					v, ok := s.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, n := range v.Names {
						if features == nil || info.Defs[n] != features || name != "proto.go" || i >= len(v.Values) {
							continue
						}
						if cl, ok := v.Values[i].(*ast.CompositeLit); ok {
							found[n], declared = true, true
							want = append(flags(cl.Elts), want...)
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil || d.Name.Name != "init" || !featureDomainFile.MatchString(name) || d.Body == nil {
					continue
				}
				for _, s := range d.Body.List {
					if call, to, from := featureAppend(s, info, features); call != nil {
						found[to], found[from] = true, true
						if call.Ellipsis.IsValid() {
							problem(call, "append(Features, %s...) spreads a slice: list each flag as a constant, so that "+
								"scripts/test/test_doc_wire.py can read it", types.ExprString(call.Args[len(call.Args)-1]))
							continue
						}
						want = append(want, flags(call.Args[1:])...)
					}
				}
			}
		}
	}
	if !declared {
		problems = append(problems, fmt.Sprintf("%s: no variable Features in proto.go set to a []string literal (%s)", dir, featureSeamDoc))
		return want, problems, nil
	}
	lines := map[string]bool{} // one problem per line
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || found[id] || info.Uses[id] != features && info.Defs[id] != features {
				return true
			}
			at := fset.Position(id.Pos())
			if line := fmt.Sprintf("%s:%d", at.Filename, at.Line); !lines[line] {
				lines[line] = true
				problem(id, "Features is used outside its seam: %s", featureSeamHint)
			}
			return true
		})
	}
	return want, problems, nil
}

// featureAppend is s when it is `Features = append(Features, …)`: the call, and the two uses of Features.
func featureAppend(s ast.Stmt, info *types.Info, features *types.Var) (call *ast.CallExpr, to, from *ast.Ident) {
	assign, ok := s.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return nil, nil, nil
	}
	to, _ = assign.Lhs[0].(*ast.Ident)
	call, _ = assign.Rhs[0].(*ast.CallExpr)
	if to == nil || call == nil || len(call.Args) < 2 || info.Uses[to] != features {
		return nil, nil, nil
	}
	fun, _ := call.Fun.(*ast.Ident)
	from, _ = call.Args[0].(*ast.Ident)
	if fun == nil || from == nil || info.Uses[from] != features {
		return nil, nil, nil
	}
	if _, builtin := info.Uses[fun].(*types.Builtin); !builtin || fun.Name != "append" {
		return nil, nil, nil
	}
	return call, to, from
}

const featureModulePath = "github.com/nwverse/persona/server/"

// featureImporter type-checks the server module's packages from their source, enough for their constants: a flag may be
// a constant of another package (nodeproto.PersonaContracts). Every other import fails, which leaves constants alone
// and needs no toolchain at run time (go test -trimpath leaves none to find).
type featureImporter struct {
	fset *token.FileSet
	root string // the server module's directory
	pkgs map[string]*types.Package
}

func (m *featureImporter) Import(path string) (*types.Package, error) {
	rel, ok := strings.CutPrefix(path, featureModulePath)
	if !ok {
		return nil, fmt.Errorf("%s: only the server module's own packages are read", path)
	}
	if p, ok := m.pkgs[path]; ok {
		return p, nil
	}
	files, err := featureGoFiles(m.fset, filepath.Join(m.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	conf := types.Config{Importer: m, Error: func(error) {}}
	p, _ := conf.Check(path, m.fset, files, nil)
	m.pkgs[path] = p
	return p, nil
}

// featureGoFiles parses the non-test Go files in dir, in file-name order.
func featureGoFiles(fset *token.FileSet, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, errors.New(dir + ": no Go files")
	}
	return files, nil
}
