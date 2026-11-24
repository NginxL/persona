package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The Inbox is becoming the first vertical domain slice (docs/plans/plan-inbox-vertical-slice.md, #11309). While its
// code moves behind a module boundary (the plan's steps S4-S9), these tests keep code from flowing the other way: no
// other file of internal/assistant starts reaching the Inbox's in-memory state, and the central lists the moves empty
// gain no Inbox entry. They parse source only, like the rest of archguard.

const (
	assistantDir = serverDir + "/internal/assistant"
	inboxPlan    = "docs/plans/plan-inbox-vertical-slice.md"
)

// inboxBridge says whether a file of internal/assistant is the task bridge's (the plan's section 2): To-dos code that
// consumes Inbox events and writes task links onto items. An inbox-prefixed file that is the bridge's is not the Inbox
// core's. S7 renames the bridge's files out of the inbox prefix; drop the names and the prefix here then.
func inboxBridge(name string) bool {
	return strings.HasPrefix(name, "inbox_task_") ||
		slices.Contains([]string{"inbox_no_output.go", "inbox_todo.go", "supervise_inbox.go", "tasks_inbox.go"}, name)
}

// inboxCore says whether a file of internal/assistant is the Inbox core's: inbox*.go, less the bridge's.
func inboxCore(name string) bool { return strings.HasPrefix(name, "inbox") && !inboxBridge(name) }

// inboxBookBridge are the bridge's files that reach the book, by name: they read items and write task links onto them
// under the book's lock (the plan's section 3.1). S7 renames them (task_inbox_*, todo_inbox): rename the entry with its
// file and add none. They leave the list when S9 gives the bridge the Links surface. Never add one.
var inboxBookBridge = []string{
	"inbox_task_links.go",
	"inbox_task_recheck.go",
	"inbox_todo.go",
	"supervise_inbox.go",
	"tasks_inbox.go",
}

// inboxBookReaders are the other files that reach the book today (the plan's section 1), each with how many places it
// does and the seam that replaces them (the plan's section 3.1). Shrink-only: never add an entry or raise a count. A
// file with fewer places than its count passes with a note; lowering the count (or dropping the entry at zero) is
// tidying anyone can do, then or later (docs/operations/testing.md, "Count ratchets").
var inboxBookReaders = map[string]struct {
	sites int
	seam  string
}{
	"context.go":               {3, "followingInboxItems: the Reader's following items"},
	"feishu_workbench.go":      {1, "workbenchItem: the Reader's item copy, and Actions for the workbench's writes"},
	"origins.go":               {1, "gatherOriginEvidence: the Reader's origin evidence"},
	"service.go":               {2, "the inbox field, and s.inbox.lang until the #8584 repair PRs land (step S4)"},
	"tasks_draft_decisions.go": {5, "the Reader's card-send lock and item copy (deferred from step S3)"},
}

// inboxBookOpenPRs are files open PRs add that reach the book, with their counts, so that those PRs merge without
// touching this file; review each in its PR. When its PR lands, move an entry to inboxBookReaders (a reader) or
// inboxBookBridge (the bridge's); drop it when the PR closes or stops reaching the book.
var inboxBookOpenPRs = map[string]struct {
	sites int
	seam  string
}{
	"outbound_messages.go":    {1, "#9782: autoSendSuggestion reads an item under the book's lock; the Reader's item copy"},
	"tasks_inbox_existing.go": {2, "#9744: the task bridge links an existing task's item, as tasks_inbox.go does"},
}

const inboxBookHint = "reach the Inbox through a method of the Inbox core (an inbox*.go file) instead: the Reader " +
	"(following items, an item copy, the card-send lock, origin evidence), Actions for the Lark workbench, or Policy; " +
	"the task bridge's own reads and writes wait for S9's Links (" + inboxPlan + ", sections 3.1 and 4)"

// The Inbox's in-memory state (inboxBook, the Service's inbox field) is reached only from the Inbox core, the task
// bridge's named files and the readers' shrink-only list. Test files are out of scope: S9 moves those with a fake Host.
func TestInboxBookStaysInTheInbox(t *testing.T) {
	fset := token.NewFileSet()
	files := assistantFiles(t, fset)
	fields, funcs := inboxBookNames(slices.Collect(maps.Values(files)))
	reached := map[string]int{}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		sites := inboxBookSites(files[name], fields, funcs)
		if len(sites) == 0 {
			continue
		}
		reached[name] = len(sites)
		r, reader := inboxBookReaders[name]
		if pr, ok := inboxBookOpenPRs[name]; ok && !reader {
			r, reader = pr, true
		}
		switch {
		case inboxCore(name) || slices.Contains(inboxBookBridge, name):
		case reader && len(sites) <= r.sites:
		case reader:
			for _, s := range sites {
				t.Errorf("%s: %s reaches the Inbox's book; %s has %d such places, inboxBookReaders allows %d (%s): %s",
					pos(fset, s.node), s.what, name, len(sites), r.sites, r.seam, inboxBookHint)
			}
		case inboxBridge(name):
			for _, s := range sites {
				t.Errorf("%s: %s reaches the Inbox's book from a task bridge file inboxBookBridge does not name: %s",
					pos(fset, s.node), s.what, inboxBookHint)
			}
		default:
			for _, s := range sites {
				t.Errorf("%s: %s reaches the Inbox's book from outside the Inbox: %s", pos(fset, s.node), s.what, inboxBookHint)
			}
		}
	}
	for _, name := range inboxBookBridge {
		if reached[name] == 0 {
			t.Errorf("inboxBookBridge names %s, which no longer reaches the book (renamed or moved?): rename or drop the entry", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(inboxBookReaders)) {
		if r, n := inboxBookReaders[name], reached[name]; n < r.sites {
			t.Logf("inboxBookReaders allows %s %d places, and it has %d now: lowering the count (dropping the entry at "+
				"zero) is tidying anyone can do", name, r.sites, n)
		}
	}
}

// assistantFiles are internal/assistant's production files, parsed, by name.
func assistantFiles(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(assistantDir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no files in %s: %v", assistantDir, err)
	}
	files := map[string]*ast.File{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(p)] = f
	}
	return files
}

// inboxBookNames are the package's names that hold or hand out the book: the struct fields of type inboxBook or a
// pointer to it (the Service's inbox), and the functions and methods that return one.
func inboxBookNames(files []*ast.File) (fields, funcs map[string]bool) {
	fields, funcs = map[string]bool{}, map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.StructType:
				for _, fl := range n.Fields.List {
					if namesBook(fl.Type) {
						for _, id := range fl.Names {
							fields[id.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if n.Type.Results != nil && slices.ContainsFunc(n.Type.Results.List, func(r *ast.Field) bool { return namesBook(r.Type) }) {
					funcs[n.Name.Name] = true
				}
			}
			return true
		})
	}
	return fields, funcs
}

// namesBook says whether a type expression mentions inboxBook.
func namesBook(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "inboxBook" {
			found = true
		}
		return !found
	})
	return found
}

type inboxBookSite struct {
	node ast.Node
	what string // as written: s.inbox.items, &s.inbox, inboxBook, s.book()
}

// inboxBookSites are the places f reaches the book: a selector through one of fields (s.inbox.items, s.inbox.mu.Lock(),
// a method value s.inbox.add), the address of one (b := &s.inbox), a call of one of funcs, and the name inboxBook itself
// (a method on *inboxBook; a parameter, field, variable or conversion of it). Parse-only, so a field of another type
// that shares a name with one in fields counts too when used the same way; an index or a plain assignment
// (ev.inbox[id], r.inbox = …) never does.
func inboxBookSites(f *ast.File, fields, funcs map[string]bool) []inboxBookSite {
	var sites []inboxBookSite
	add := func(n ast.Node, e ast.Expr) { sites = append(sites, inboxBookSite{n, types.ExprString(e)}) }
	book := func(e ast.Expr) bool {
		sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
		return ok && fields[sel.Sel.Name]
	}
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.UnaryExpr:
			if e.Op == token.AND && book(e.X) {
				add(e, e)
				return false
			}
		case *ast.SelectorExpr:
			if book(e.X) {
				add(e, e)
				return false
			}
		case *ast.CallExpr:
			switch fn := ast.Unparen(e.Fun).(type) {
			case *ast.Ident:
				if funcs[fn.Name] {
					add(e, e.Fun)
				}
			case *ast.SelectorExpr:
				if funcs[fn.Sel.Name] {
					add(e, e.Fun)
				}
			}
		case *ast.Ident:
			if e.Name == "inboxBook" {
				add(e, e)
			}
		}
		return true
	}
	for _, d := range f.Decls {
		ast.Inspect(d, visit)
	}
	return sites
}

// inboxBookSites finds every way of reaching the book and nothing else.
func TestInboxBookSites(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
	}{
		{`func (s *Service) f() { s.inbox.mu.Lock(); defer s.inbox.mu.Unlock(); for range s.inbox.items {} }`,
			[]string{"s.inbox.mu", "s.inbox.mu", "s.inbox.items"}},
		{`func (s *Service) f() { b := &s.inbox; b.mu.Lock() }`, []string{"&s.inbox"}},
		{`func (h *Handler) f() { _ = h.svc.inbox.cardSends; _ = (&h.svc.inbox).add }`, []string{"h.svc.inbox.cardSends", "&h.svc.inbox"}},
		{`func (b *inboxBook) fileTodo() {}`, []string{"inboxBook"}},
		{`func f(b *inboxBook) {}`, []string{"inboxBook"}},
		{`type Service struct { inbox inboxBook }; func (s *Service) book() *inboxBook { return &s.inbox }; func g(s *Service) { _ = s.book().items }`,
			[]string{"inboxBook", "inboxBook", "&s.inbox", "s.book"}},
		{`func f(ev *originEvidence, r *fleetRead) { ev.inbox[k] = v; r.inbox = map[string]bool{}; _ = r.inbox[k] }`, nil},
		{`func f(s *Service) { s.followingInboxItems(); _ = s.inboxReport() }`, nil},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package assistant; "+c.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		fields, funcs := inboxBookNames([]*ast.File{f})
		fields["inbox"] = true // the Service's field, declared in another file
		var got []string
		for _, s := range inboxBookSites(f, fields, funcs) {
			got = append(got, s.what)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: found %q, want %q", c.src, got, c.want)
		}
	}
}

// inboxTools are the Inbox's tools toolNames (internal/assistant/tools.go) may list: main's, and those open PRs add.
// An Inbox tool is one named inbox_* or described in prompts/mcp/inbox.
var inboxTools = []string{
	"inbox_draft", "inbox_folders", "inbox_move", "inbox_post", "inbox_suggest",
	"inbox_replay", // #6539
	"message_send", // #9782: described in prompts/mcp/inbox, it stays in toolNames when S5 moves the others
}

// inboxScopes are the Inbox routes RouteScopes' literal (internal/extwire/scopes.go) may still give a scope: the ones
// open PRs add there. The Inbox's other routes get theirs in its own file, extwire/inbox_scopes.go (step S6). When such
// a PR lands, move its lines there and drop them here; drop them too when the PR closes. Never add one. An Inbox route
// is under /api/v1/inbox or /api/v1/translate, or needs an Inbox scope.
var inboxScopes = []string{
	"POST /api/v1/inbox/{id}/tier", "GET /api/v1/inbox/rules", "PUT /api/v1/inbox/rules", // #4512
}

// inboxScopesFile is the Inbox's own file in internal/extwire, which gives the Inbox's routes their scopes through
// addRouteScopes.
const inboxScopesFile = "inbox_scopes.go"

// toolNames and RouteScopes gain no Inbox entry. S5 registers the Inbox's tools with the Inbox, and toolNames keeps only
// what is there now and what open PRs add. The Inbox's scopes are decided in inbox_scopes.go (S6): RouteScopes' literal
// keeps only what open PRs add, that file decides only Inbox routes, and another domain's file none. An entry the move
// took (or a PR that closed) leaves its allowance with it.
func TestCentralListsGainNoInboxEntry(t *testing.T) {
	described, err := filepath.Glob(filepath.Join(serverDir, "internal", "prompts", "mcp", "inbox", "*.md"))
	if err != nil || len(described) == 0 {
		t.Fatalf("no Inbox tool descriptions in prompts/mcp/inbox: %v", err)
	}
	for i, p := range described {
		described[i] = strings.TrimSuffix(filepath.Base(p), ".md")
	}
	fset := token.NewFileSet()
	for _, e := range stringListVar(t, fset, filepath.Join(assistantDir, "tools.go"), "toolNames") {
		name, _ := strconv.Unquote(e.Value)
		if (strings.HasPrefix(name, "inbox_") || slices.Contains(described, name)) && !slices.Contains(inboxTools, name) {
			t.Errorf("%s: toolNames gains the Inbox tool %s: register it with the Inbox's own tools (step S5 of %s); "+
				"before S5 lands, list it in inboxTools with its PR, so that the move takes it along", pos(fset, e), name, inboxPlan)
		}
	}
	for _, kv := range mapVar(t, fset, filepath.Join(serverDir, "internal", "extwire", "scopes.go"), "RouteScopes") {
		route, inbox, ok := inboxRouteScope(kv)
		if !ok {
			t.Errorf("%s: a RouteScopes key that is no string literal: this guard reads only those", pos(fset, kv))
		} else if inbox && !slices.Contains(inboxScopes, route) {
			t.Errorf("%s: RouteScopes gains the Inbox route %q: give it its scope in extwire/%s, the Inbox's own file "+
				"(step S6 of %s)", pos(fset, kv), route, inboxScopesFile, inboxPlan)
		}
	}
	added := addedRouteScopes(t, fset)
	if len(added[inboxScopesFile]) == 0 {
		t.Errorf("extwire/%s gives no route a scope: this guard reads addRouteScopes(map[string]string{…}) calls",
			inboxScopesFile)
	}
	for _, file := range slices.Sorted(maps.Keys(added)) {
		for _, kv := range added[file] {
			route, inbox, ok := inboxRouteScope(kv)
			switch {
			case !ok:
				t.Errorf("%s: an addRouteScopes key that is no string literal: this guard reads only those", pos(fset, kv))
			case file == inboxScopesFile && !inbox:
				t.Errorf("%s: %q is no Inbox route: give it its scope in its own domain's file, extwire/<domain>_scopes.go",
					pos(fset, kv), route)
			case file != inboxScopesFile && inbox:
				t.Errorf("%s: the Inbox route %q gets its scope outside the Inbox's file: move it to extwire/%s",
					pos(fset, kv), route, inboxScopesFile)
			}
		}
	}
}

// inboxRouteScope reads one entry of a route scope table: its route, and whether that is an Inbox route (under
// /api/v1/inbox or /api/v1/translate, or needing an Inbox scope); ok is false for a key that is no string literal.
func inboxRouteScope(kv *ast.KeyValueExpr) (route string, inbox, ok bool) {
	key, isLit := kv.Key.(*ast.BasicLit)
	if !isLit || key.Kind != token.STRING {
		return "", false, false
	}
	route, _ = strconv.Unquote(key.Value)
	_, p, hasMethod := strings.Cut(route, " ")
	if !hasMethod {
		p = route
	}
	inbox = strings.HasPrefix(types.ExprString(kv.Value), "ScopeInbox")
	for _, under := range []string{"/api/v1/inbox", "/api/v1/translate"} {
		inbox = inbox || p == under || strings.HasPrefix(p, under+"/")
	}
	return route, inbox, true
}

// addedRouteScopes are the entries of every addRouteScopes(map[string]string{…}) call in internal/extwire's non-test
// files, by file name. An argument that is no map literal fails, since its entries cannot be read.
func addedRouteScopes(t *testing.T, fset *token.FileSet) map[string][]*ast.KeyValueExpr {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(serverDir, "internal", "extwire", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]*ast.KeyValueExpr{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := ast.Unparen(call.Fun).(*ast.Ident); !ok || fn.Name != "addRouteScopes" {
				return true
			}
			var lit *ast.CompositeLit
			if len(call.Args) == 1 {
				lit, _ = ast.Unparen(call.Args[0]).(*ast.CompositeLit)
			}
			if lit == nil {
				t.Errorf("%s: addRouteScopes takes no map literal: this guard reads only those", pos(fset, call))
				return true
			}
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					out[filepath.Base(path)] = append(out[filepath.Base(path)], kv)
				}
			}
			return true
		})
	}
	return out
}

// stringListVar is the string literals of the package-level variable name in the file at path.
func stringListVar(t *testing.T, fset *token.FileSet, path, name string) []*ast.BasicLit {
	t.Helper()
	var out []*ast.BasicLit
	ast.Inspect(varValue(t, fset, path, name), func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			out = append(out, lit)
		}
		return true
	})
	return out
}

// mapVar is the entries of the map literal the package-level variable name in the file at path is set to.
func mapVar(t *testing.T, fset *token.FileSet, path, name string) []*ast.KeyValueExpr {
	t.Helper()
	lit, ok := varValue(t, fset, path, name).(*ast.CompositeLit)
	if !ok {
		t.Fatalf("%s: %s is no map literal", path, name)
	}
	var out []*ast.KeyValueExpr
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			out = append(out, kv)
		}
	}
	return out
}

// varValue is the expression the package-level variable name in the file at path is set to.
func varValue(t *testing.T, fset *token.FileSet, path, name string) ast.Expr {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
			for _, s := range g.Specs {
				v := s.(*ast.ValueSpec)
				for i, n := range v.Names {
					if n.Name == name && i < len(v.Values) {
						return v.Values[i]
					}
				}
			}
		}
	}
	t.Fatalf("%s: no package-level var %s = …", path, name)
	return nil
}
