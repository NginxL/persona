package archguard

import (
	"flag"
	"fmt"
	"go/ast"
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

// Name freezes on the central lists (docs/server/architecture.md "Registering a domain's entries", #12844). A central
// list is one that nearly every feature used to add a line to. Each list here has a home in the domain's own files now,
// so the list takes no new name. The names it may have are in testdata/central_lists/<id>.txt: the ones it had when its
// freeze landed, then the ones the PRs open that day were adding, each with its PRs. Any other name fails, and the
// message says where the entry goes instead. A listed name the list no longer has is a notice, and its line can go. The
// freeze is on names, not lines: editing an existing entry is fine. scripts/test/name_freezes.py writes the pending
// part from the open PRs' heads, the day a freeze lands.

const centralListsDoc = `docs/server/architecture.md "Registering a domain's entries"`

// A centralList is one frozen list: where it is, how its names are read, and where a new entry goes instead.
type centralList struct {
	id     string // its file is testdata/central_lists/<id>.txt
	source string // the Go file it is read from, under the server module
	what   string // what one name is, for messages: "a new <what>"
	names  func(*ast.File) []centralName
	seam   string // where a new entry goes instead
}

// A centralName is one entry of a list, as the freeze compares it, and where it is.
type centralName struct {
	text string
	node ast.Node
}

var centralLists = []centralList{
	{
		id:     "assistantwire_routes",
		source: "internal/assistantwire/bus.go",
		what:   "entry of the Routes literal",
		names:  literalEntries("Routes"),
		seam: "list a new relayed route from an init() in its domain's own file, assistantwire/<domain>.go, " +
			"func init() { Routes = append(Routes, …) }, as agents.go and board.go do",
	},
	{
		id:     "prompts_assistant",
		source: "internal/prompts/assistant.go",
		what:   "top-level declaration of prompts/assistant.go",
		names:  declaredNames,
		seam: "give a new prompt's types and Render function a file of their own, prompts/assistant_<topic>.go, " +
			"as assistant_answer_again.go does",
	},
	{
		id:     "extwire_route_scopes",
		source: "internal/extwire/scopes.go",
		what:   "key of the RouteScopes literal",
		names:  literalEntries("RouteScopes"),
		seam: "give a route opened to extensions its scope from an init() in its domain's own file, " +
			"extwire/<domain>_scopes.go, func init() { addRouteScopes(map[string]string{…}) }, as inbox_scopes.go does",
	},
}

func (l centralList) file() string { return "testdata/central_lists/" + l.id + ".txt" }

// centralListByID is the row of centralLists with this id. A test names the list it plants in by id, not by its place
// in centralLists, so a row added beside it moves nothing the test reads.
func centralListByID(t *testing.T, id string) centralList {
	t.Helper()
	i := slices.IndexFunc(centralLists, func(l centralList) bool { return l.id == id })
	if i < 0 {
		t.Fatalf("no central list %q", id)
	}
	return centralLists[i]
}

func TestCentralListsTakeNoNewName(t *testing.T) {
	for _, l := range centralLists {
		src, err := os.ReadFile(filepath.Join(serverDir, l.source))
		if err != nil {
			t.Fatal(err)
		}
		list, err := os.ReadFile(l.file())
		if err != nil {
			t.Fatal(err)
		}
		failures, notices, err := checkCentralList(l, src, string(list))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range failures {
			t.Error(f)
		}
		for _, n := range notices {
			t.Log("notice: " + n)
		}
	}
}

// checkCentralList is what a list's freeze reports for src, the text of its source file, and list, its testdata file's.
func checkCentralList(l centralList, src []byte, list string) (failures, notices []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(serverDir, l.source), src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	entries, problems := readCentralList(list)
	for _, p := range problems {
		failures = append(failures, l.file()+": "+p)
	}
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.text] = true
	}
	has := map[string]bool{}
	for _, n := range l.names(f) {
		has[n.text] = true
		if !listed[n.text] {
			failures = append(failures, fmt.Sprintf("%s: %s is a new %s, which takes no new name (%s): %s; %s",
				pos(fset, n.node), n.text, l.what, l.file(), l.seam, centralListsDoc))
		}
	}
	var waiting []string
	for _, e := range entries {
		switch {
		case has[e.text]:
		case len(e.prs) == 0:
			notices = append(notices, fmt.Sprintf("%s no longer has %s: delete line %d of %s", l.source, e.text, e.line, l.file()))
		default:
			waiting = append(waiting, e.text+" ("+strings.Join(e.prs, " ")+")")
		}
	}
	if len(waiting) > 0 {
		notices = append(notices, fmt.Sprintf("pending, not in %s yet (delete a line once its PRs have all closed): %s",
			l.source, strings.Join(waiting, ", ")))
	}
	return failures, notices, nil
}

// A centralEntry is one line of a list's file: the name and, for a pending one, the open PRs that add it (["#4512"]).
type centralEntry struct {
	line int
	text string
	prs  []string
}

// centralEntryLine is an entry: the name, and a pending one's PRs after it. scripts/test/name_freezes.py reads the
// same format (its ENTRY).
var centralEntryLine = regexp.MustCompile(`^(\S(?:.*?\S)??)(?:[ \t]+(#\d+(?:[ \t]+#\d+)*))?$`)

// readCentralList reads a list's file, the format scripts/mock/mock_hub_switches.txt has too: comment lines start with
// #, then the names the list had when its freeze landed, then the pending ones, each followed by its PRs. Names stay
// sorted and unique within each part.
func readCentralList(text string) (entries []centralEntry, problems []string) {
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := centralEntryLine.FindStringSubmatch(line)
		entries = append(entries, centralEntry{i + 1, m[1], strings.Fields(m[2])})
	}
	seen := map[string]int{}
	for _, e := range entries {
		if first, dup := seen[e.text]; dup {
			problems = append(problems, fmt.Sprintf("line %d repeats %s (line %d)", e.line, e.text, first))
		} else {
			seen[e.text] = e.line
		}
	}
	pending := slices.IndexFunc(entries, func(e centralEntry) bool { return len(e.prs) > 0 })
	if pending < 0 {
		pending = len(entries)
	}
	for _, e := range entries[pending:] {
		if len(e.prs) == 0 {
			problems = append(problems, fmt.Sprintf("line %d (%s) has no PR after the pending entries: those come last", e.line, e.text))
		}
	}
	for _, part := range [][]centralEntry{entries[:pending], entries[pending:]} {
		for i := 1; i < len(part); i++ {
			if part[i].text < part[i-1].text {
				problems = append(problems, fmt.Sprintf("line %d (%s) is out of order: it belongs before %s (keep the "+
					"names, and the pending ones, sorted)", part[i].line, part[i].text, part[i-1].text))
			}
		}
	}
	return entries, problems
}

// literalEntries reads the entries of the composite literal the package-level variable name is set to, each by its
// source text ("GET /api/v1/todos" with its quotes, "GET " + StarredImportPath); a map's by its key.
func literalEntries(name string) func(*ast.File) []centralName {
	return func(f *ast.File) []centralName {
		var out []centralName
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				v := s.(*ast.ValueSpec)
				for i, n := range v.Names {
					if n.Name != name || i >= len(v.Values) {
						continue
					}
					cl, ok := v.Values[i].(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, e := range cl.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							e = kv.Key
						}
						out = append(out, centralName{types.ExprString(e), e})
					}
				}
			}
		}
		return out
	}
}

// declaredNames reads a file's top-level declarations: functions, types, variables and constants by name, methods as
// Type.Method; init and _ are no names.
func declaredNames(f *ast.File) []centralName {
	var out []centralName
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			switch {
			case d.Recv != nil && len(d.Recv.List) == 1:
				out = append(out, centralName{receiverType(d.Recv.List[0].Type) + "." + d.Name.Name, d.Name})
			case d.Name.Name != "init":
				out = append(out, centralName{d.Name.Name, d.Name})
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					out = append(out, centralName{s.Name.Name, s.Name})
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name != "_" {
							out = append(out, centralName{n.Name, n})
						}
					}
				}
			}
		}
	}
	return out
}

// receiverType is a method receiver's type name: T for T, *T, T[K] and *T[K, V].
func receiverType(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return receiverType(e.X)
	case *ast.ParenExpr:
		return receiverType(e.X)
	case *ast.IndexExpr:
		return receiverType(e.X)
	case *ast.IndexListExpr:
		return receiverType(e.X)
	}
	return types.ExprString(e)
}

// centralListNames is for scripts/test/name_freezes.py, which needs each list's names at other commits (an open PR's
// head, its merge base) read exactly as the freeze reads them, so that the pending names it writes are the ones the
// freeze compares. Given a directory, TestCentralListNames writes lists.tsv there, one line per list (its id, source
// file, list file and what a name is; paths from the repository's root), and for each file <dir>/<id>/<name>.go the
// names list <id> reads in it to <dir>/<id>/<name>.names, one per line (no .names for a file that does not parse). It
// checks nothing; without the flag it is skipped.
var centralListNames = flag.String("central-list-names", "",
	"write the central lists' table, and their names in the files under this directory (scripts/test/name_freezes.py)")

func TestCentralListNames(t *testing.T) {
	dir := *centralListNames
	if dir == "" {
		t.Skip("only with -central-list-names (scripts/test/name_freezes.py)")
	}
	var table strings.Builder
	for _, l := range centralLists {
		fmt.Fprintf(&table, "%s\tserver/%s\tserver/internal/archguard/%s\t%s\n", l.id, l.source, l.file(), l.what)
		paths, err := filepath.Glob(filepath.Join(dir, l.id, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
			if err != nil {
				t.Logf("%s: %v", p, err)
				continue
			}
			var names strings.Builder
			for _, n := range l.names(f) {
				names.WriteString(n.text + "\n")
			}
			if err := os.WriteFile(strings.TrimSuffix(p, ".go")+".names", []byte(names.String()), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "lists.tsv"), []byte(table.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The freeze's own behaviour, on planted sources: a new name fails with where it goes instead, a removed one is a
// notice, a pending one passes before and after its PR merges, and the list's format is checked.
func TestCentralListFreeze(t *testing.T) {
	routes, prompts := centralLists[0], centralLists[1]
	const bus = `package assistantwire

const P = "/api/v1/p"

var Routes = []string{
	"GET /api/v1/a",
	"GET " + P, // a comment
	"PUT /api/v1/b", "POST /api/v1/c",
}

var Other = []string{"GET /api/v1/other"}

func init() { Routes = append(Routes, "GET /api/v1/appended") }
`
	// The route scopes' list is read like the routes' one, from a literal, but by its keys. A route given its scope from
	// a domain's init() is no key of the literal, so it passes.
	scopes := centralListByID(t, "extwire_route_scopes")
	const scope = `package extwire

var RouteScopes = map[string]string{
	"GET /api/v1/a": ScopeA,
	"PUT /api/v1/b": ScopeB, // a comment
}

func init() { addRouteScopes(map[string]string{"GET /api/v1/registered": ScopeA}) }
`
	const prompt = `package prompts

type Snapshot struct{}

type (
	A        struct{}
	B[T any] struct{}
)

const C, _ = 1, 2

var v = 0

func init() {}

func RenderA(a A) string { return "" }

func (s Snapshot) Up() int { return 0 }

func (b *B[T]) Down() int { return 0 }
`
	const routesList = "# a header\n\"GET \" + P\n\"GET /api/v1/a\"\n\"POST /api/v1/c\"\n\"PUT /api/v1/b\"\n"
	const scopesList = "\"GET /api/v1/a\"\n\"PUT /api/v1/b\"\n"
	const promptsList = "A\nB\nB.Down\nC\nRenderA\nSnapshot\nSnapshot.Up\nv\n"

	check := func(l centralList, src, list string) ([]string, []string) {
		t.Helper()
		failures, notices, err := checkCentralList(l, []byte(src), list)
		if err != nil {
			t.Fatal(err)
		}
		return failures, notices
	}
	const routeSeam, promptSeam = "assistantwire/<domain>.go", "prompts/assistant_<topic>.go"
	for _, c := range []struct {
		l          centralList
		src, list  string
		at         string // the planted entry goes after this text of src
		planted    string
		name, seam string // what the failure names
	}{
		{routes, bus, routesList, "var Routes = []string{\n", "\t\"DELETE /api/v1/planted\",\n", `"DELETE /api/v1/planted"`, routeSeam},
		{scopes, scope, scopesList, "var RouteScopes = map[string]string{\n", "\t\"DELETE /api/v1/planted\": ScopeA,\n",
			`"DELETE /api/v1/planted"`, "extwire/<domain>_scopes.go"},
		{prompts, prompt, promptsList, "func init() {}\n", "func RenderPlanted() string { return \"\" }\n", "RenderPlanted", promptSeam},
		{prompts, prompt, promptsList, "func init() {}\n", "func (s Snapshot) Planted() int { return 0 }\n", "Snapshot.Planted", promptSeam},
		{prompts, prompt, promptsList, "func init() {}\n", "type Planted struct{}\n", "Planted", promptSeam},
	} {
		if failures, notices := check(c.l, c.src, c.list); len(failures)+len(notices) != 0 {
			t.Errorf("%s: unplanted source reports %q, %q", c.l.id, failures, notices)
		}
		planted := strings.Replace(c.src, c.at, c.at+c.planted, 1)
		failures, _ := check(c.l, planted, c.list)
		if len(failures) != 1 || !strings.Contains(failures[0], " "+c.name+" is a new ") ||
			!strings.Contains(failures[0], c.seam) || !strings.Contains(failures[0], centralListsDoc) {
			t.Errorf("%s: planting %s reports %q, want one failure naming it, %s and the doc", c.l.id, c.name, failures, c.seam)
		}
		if failures, _ := check(c.l, planted, c.list+c.name+" #4512\n"); len(failures) != 0 {
			t.Errorf("%s: %s, pending with its PR, still fails once its PR merges: %q", c.l.id, c.name, failures)
		}
	}

	// A name that left is a notice, never a failure; one that is pending but not in the source yet too.
	removed := strings.Replace(bus, "\t\"GET /api/v1/a\",\n", "", 1)
	failures, notices := check(routes, removed, routesList+"\"GET /api/v1/z\" #11 #12\n")
	const gone = `internal/assistantwire/bus.go no longer has "GET /api/v1/a": ` +
		"delete line 3 of testdata/central_lists/assistantwire_routes.txt"
	if len(failures) != 0 || len(notices) != 2 || notices[0] != gone || !strings.Contains(notices[1], `"GET /api/v1/z" (#11 #12)`) {
		t.Errorf("a removed route and a pending one report %q, %q", failures, notices)
	}

	// The format: sorted and unique in each part, pending ones last.
	for _, c := range []struct{ list, problem string }{
		{"B\nA\n", "line 2 (A) is out of order"},
		{"A\nA\n", "line 2 repeats A (line 1)"},
		{"X #1\nA\n", "line 2 (A) has no PR after the pending entries"},
		{"A\nY #2\nX #1\n", "line 3 (X) is out of order"},
	} {
		entries, problems := readCentralList(c.list)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, c.problem) }) {
			t.Errorf("%q: problems %q, want %q", c.list, problems, c.problem)
		}
		if len(entries) == 0 {
			t.Errorf("%q: no entries", c.list)
		}
	}
	entries, problems := readCentralList("# c\n\n\"GET /api/v1/a\"\n\"GET /api/v1/b\" #1 #22\n")
	if len(problems) != 0 || len(entries) != 2 || entries[1].text != `"GET /api/v1/b"` ||
		!slices.Equal(entries[1].prs, []string{"#1", "#22"}) {
		t.Errorf("a route and a pending one read as %+v, %q", entries, problems)
	}
}
