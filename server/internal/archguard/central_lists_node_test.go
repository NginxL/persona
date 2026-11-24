package archguard

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The freeze of Node.handleMethod's labels: the node_handle_method row of centralLists (#12920). A method that is not
// bound to a session registers from its domain's own file through nodeRPC (internal/node/node_methods.go), so
// handleMethod takes no new label for one. The labels the node's method table refuses stay where they are and are not
// frozen: session-bound methods, delivery aliases, and the methods under a prefix with a dispatcher of its own.

// The row is appended from this file, as a domain's entries are, so that the rows of other lists' freezes, added to the
// literal in central_lists_test.go, never meet it in one hunk.
func init() {
	centralLists = append(centralLists, centralList{
		id:     "node_handle_method",
		source: "internal/node/node.go",
		what:   "label of Node.handleMethod for a method that is not bound to a session",
		names:  handleMethodLabels,
		seam: "register the method from an init() in its domain's own file, func init() { nodeRPC(name, (*Node).x) } " +
			"(node/node_methods.go), as sessions_history.go does",
	})
}

// handleMethodLabels reads the dispatch's method labels in a node.go (nodeDispatch) by their source text: the cases of each
// switch on method, what method is compared with (method == x), and the prefixes it routes on (strings.HasPrefix and
// the like, as `prefix "plugin.node."`). It leaves out the labels the method table refuses (nodeRefusals); a label whose
// value it cannot read, such as a constant node.go's packages do not have yet, stays in.
func handleMethodLabels(f *ast.File) []centralName {
	refusals := nodeRefusalsOnce()
	isMethod := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "method" }
	var out []centralName
	exact := func(e ast.Expr) {
		if v, ok := refusals.value(e); !ok || refusals.tableMayHold(v) {
			out = append(out, centralName{types.ExprString(e), e})
		}
	}
	for _, fn := range nodeDispatch(f) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SwitchStmt:
				if n.Tag != nil && isMethod(n.Tag) {
					for _, c := range n.Body.List {
						for _, e := range c.(*ast.CaseClause).List {
							exact(e)
						}
					}
				}
			case *ast.BinaryExpr:
				switch {
				case n.Op != token.EQL:
				case isMethod(n.X):
					exact(n.Y)
				case isMethod(n.Y):
					exact(n.X)
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || len(n.Args) != 2 || !isMethod(n.Args[0]) || !slices.Contains([]string{"HasPrefix", "CutPrefix", "TrimPrefix"}, sel.Sel.Name) {
					return true
				}
				if v, ok := refusals.value(n.Args[1]); !ok || !refusals.sessionBound(v) {
					out = append(out, centralName{"prefix " + types.ExprString(n.Args[1]), n.Args[1]})
				}
			}
			return true
		})
	}
	return out
}

// nodeDispatch is the method that dispatches in a node.go: handleMethod, or handle in one from before handleMethod
// was split from it (#11687), so that an open PR that started before then still has its labels read.
func nodeDispatch(f *ast.File) []*ast.FuncDecl {
	byName := map[string][]*ast.FuncDecl{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Body != nil {
			byName[fn.Name.Name] = append(byName[fn.Name.Name], fn)
		}
	}
	if fns := byName["handleMethod"]; len(fns) > 0 {
		return fns
	}
	return byName["handle"]
}

// nodeRefusals are the names the node's method table refuses, as internal/node/node_methods.go lists them, and the
// string constants node.go's labels may name, by the name node.go uses ("x" in package node, "proto.X" in another).
type nodeRefusals struct {
	consts                                             map[string]string
	sessionPrefixes, sessionMethods, aliases, reserved []string
}

// nodeMethodsFile holds the refusal lists, and nodeLabelPackages are the packages whose constants node.go's labels name.
const nodeMethodsFile = "internal/node/node_methods.go"

var nodeLabelPackages = []string{"internal/node", "internal/proto", "internal/nodeproto", "internal/devicewire", "internal/assistantwire"}

// nodeRefusalsOnce reads them once per test binary. It panics when it cannot: the freeze would compare nothing.
var nodeRefusalsOnce = sync.OnceValue(func() nodeRefusals {
	r, err := readNodeRefusals(serverDir)
	if err != nil {
		panic(fmt.Sprintf("the node_handle_method freeze cannot read the method table's refusals: %v (point it at %s)", err, nodeMethodsFile))
	}
	return r
})

func readNodeRefusals(server string) (nodeRefusals, error) {
	r := nodeRefusals{consts: map[string]string{}}
	var methodsFile *ast.File
	for _, dir := range nodeLabelPackages {
		paths, err := filepath.Glob(filepath.Join(server, dir, "*.go"))
		if err != nil {
			return r, err
		}
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return r, err
			}
			isMethodsFile := filepath.ToSlash(p) == filepath.ToSlash(filepath.Join(server, nodeMethodsFile))
			if !isMethodsFile && !bytes.Contains(src, []byte("\nconst")) {
				continue // no top-level constant: half of package node's files, not parsed
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
			if err != nil {
				return r, err
			}
			if isMethodsFile {
				methodsFile = f
			}
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, s := range g.Specs {
					v := s.(*ast.ValueSpec)
					for i, name := range v.Names {
						if i >= len(v.Values) {
							continue
						}
						if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							key := name.Name
							if dir != "internal/node" {
								key = filepath.Base(dir) + "." + key
							}
							r.consts[key], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}
	if methodsFile == nil {
		return r, fmt.Errorf("%s not found", nodeMethodsFile)
	}
	lists := map[string]*[]string{"sessionMethodPrefixes": &r.sessionPrefixes, "sessionMethods": &r.sessionMethods,
		"deliveryAliases": &r.aliases, "reservedMethodPrefixes": &r.reserved}
	for _, d := range methodsFile.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			v := s.(*ast.ValueSpec)
			for i, name := range v.Names {
				list, ok := lists[name.Name]
				if !ok || i >= len(v.Values) {
					continue
				}
				cl, ok := v.Values[i].(*ast.CompositeLit)
				if !ok {
					return r, fmt.Errorf("%s is not a literal", name.Name)
				}
				for _, e := range cl.Elts {
					value, ok := r.value(e)
					if !ok {
						return r, fmt.Errorf("%s's %s is neither a string literal nor a constant", name.Name, types.ExprString(e))
					}
					*list = append(*list, value)
				}
				delete(lists, name.Name)
			}
		}
	}
	for name := range lists {
		return r, fmt.Errorf("%s does not declare %s", nodeMethodsFile, name)
	}
	return r, nil
}

// value is the string a label is: a literal's, or a constant's that nodeLabelPackages declare.
func (r nodeRefusals) value(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		}
	case *ast.Ident:
		v, ok := r.consts[e.Name]
		return v, ok
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			v, ok := r.consts[x.Name+"."+e.Sel.Name]
			return v, ok
		}
	}
	return "", false
}

// sessionBound is whether a method, or a prefix, is bound to a session.
func (r nodeRefusals) sessionBound(v string) bool {
	return slices.Contains(r.sessionMethods, v) || slices.ContainsFunc(r.sessionPrefixes, func(p string) bool { return strings.HasPrefix(v, p) })
}

// tableMayHold is whether the method table may answer the method v, as node_methods.go's tableRefusal decides.
func (r nodeRefusals) tableMayHold(v string) bool {
	return v != "" && !slices.Contains(r.aliases, v) && !r.sessionBound(v) &&
		!slices.ContainsFunc(r.reserved, func(p string) bool { return strings.HasPrefix(v, p) })
}

// The node_handle_method freeze on a planted node.go: a new case, comparison or prefix for a method the table may hold
// fails and names nodeRPC; a session-bound one, a delivery alias and one under a reserved prefix pass, since they stay
// in the switch; the same method registered through nodeRPC from its own file passes; a pending one passes.
func TestNodeHandleMethodFreeze(t *testing.T) {
	i := slices.IndexFunc(centralLists, func(l centralList) bool { return l.id == "node_handle_method" })
	if i < 0 {
		t.Fatal("centralLists has no node_handle_method row")
	}
	l := centralLists[i]
	const src = `package node

const methodLocal = "local.method"

func (n *Node) handleMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if strings.HasPrefix(method, "plugin.node.") {
		return nil, nil
	}
	if strings.HasPrefix(method, "session.") {
		return nil, nil
	}
	switch method {
	case nodeproto.SourceSend:
		method = "session.send"
	}
	switch method {
	case "machine.a", proto.MethodMachinePorts:
		return nil, nil
	case methodLocal:
		return nil, nil
	case "session.keys", "terminal.watch", "assistant.send", "codex.answer":
		return nil, nil
	}
	if method == "sessions.history" {
		return nil, nil
	}
	return nil, nil
}
`
	const list = "\"machine.a\"\n\"sessions.history\"\nmethodLocal\nprefix \"plugin.node.\"\nproto.MethodMachinePorts\n"
	const at = "\tswitch method {\n\tcase \"machine.a\""
	check := func(src, list string) ([]string, []string) {
		t.Helper()
		failures, notices, err := checkCentralList(l, []byte(src), list)
		if err != nil {
			t.Fatal(err)
		}
		return failures, notices
	}
	if failures, notices := check(src, list); len(failures)+len(notices) != 0 {
		t.Fatalf("unplanted source reports %q, %q", failures, notices)
	}
	for _, c := range []struct{ planted, name string }{
		{"\tswitch method {\n\tcase \"machine.planted\":\n\t\treturn nil, nil\n\t}\n", `"machine.planted"`},
		{"\tif method == proto.MethodMachinePlanted {\n\t\treturn nil, nil\n\t}\n", "proto.MethodMachinePlanted"},
		{"\tif strings.HasPrefix(method, \"planted.\") {\n\t\treturn nil, nil\n\t}\n", `prefix "planted."`},
	} {
		planted := strings.Replace(src, at, c.planted+at, 1)
		failures, _ := check(planted, list)
		if len(failures) != 1 || !strings.Contains(failures[0], " "+c.name+" is a new ") ||
			!strings.Contains(failures[0], "nodeRPC") || !strings.Contains(failures[0], centralListsDoc) {
			t.Errorf("planting %s reports %q, want one failure naming it, nodeRPC and the doc", c.name, failures)
		}
		if failures, _ := check(planted, list+c.name+" #4512\n"); len(failures) != 0 {
			t.Errorf("%s, pending with its PR, still fails: %q", c.name, failures)
		}
	}
	for _, planted := range []string{
		"\tswitch method {\n\tcase \"session.planted\", \"terminal.planted\", assistantwire.DeviceAssistantAnswerAgain:\n\t\treturn nil, nil\n\t}\n",
		"\tswitch method {\n\tcase nodeproto.OwnerCardAnswer:\n\t\tmethod = \"codex.answer\"\n\t}\n",
		"\tif method == \"node.update.planted\" || method == \"task.workspace.planted\" {\n\t\treturn nil, nil\n\t}\n",
		"\tif strings.HasPrefix(method, \"terminal.planted.\") {\n\t\treturn nil, nil\n\t}\n",
	} {
		if failures, _ := check(strings.Replace(src, at, planted+at, 1), list); len(failures) != 0 {
			t.Errorf("a label the table refuses fails the freeze: %q", failures)
		}
	}
	registered := src + "\nfunc init() { nodeRPC(\"machine.planted\", (*Node).machinePlanted) }\n"
	if failures, _ := check(registered, list); len(failures) != 0 {
		t.Errorf("a method registered through nodeRPC fails the freeze: %q", failures)
	}
	// A node.go from before handleMethod was split from handle: its labels are handle's.
	if names := handleMethodLabels(parseGo(t, strings.Replace(src, ") handleMethod(", ") handle(", 1))); len(names) != 5 {
		t.Errorf("handle's labels in a node.go without handleMethod: %d, want 5", len(names))
	}
}

func parseGo(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "node.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
