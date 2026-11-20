package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Node.handle's parameters are typed per method namespace (#7891 stage 2, #8089): a namespace listed here decodes each
// method's request into its own struct (internal/node/handle_<namespace>.go) and names its methods with constants. The
// shared parameter struct may not get back a field only that namespace read, and node.go may not spell one of its
// method names as a string literal again. Add a row in every PR that types a namespace; never remove one.
//
// handle is the reload gate, and handleMethod dispatches and declares the shared struct (since #11687; started task
// workspaces and backend Git calls dispatch again without passing the gate). The guard reads the dispatch.
var typedHandleNamespaces = []struct {
	prefix string   // the method-name prefix, "file." for file.read
	fields []string // the shared struct's fields only this namespace read
}{
	{"file.", []string{"Offset", "Stream"}},
	{"fs.", []string{"Paths", "Detail"}},
	{"git.", []string{"IdentityOnly", "Staged", "Hash", "Skip"}},
	{"worktree.", []string{"Branch", "ParentWorkspaceID", "AutoName"}},
	{"clipboard.", nil}, // reads none of the shared fields
	{"preview.", nil},   // the same
}

func TestTypedHandleNamespacesStayTyped(t *testing.T) {
	const path = "../node/node.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	const dispatch = "handleMethod"
	shared := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != dispatch || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "p" {
				return true
			}
			if st, ok := vs.Type.(*ast.StructType); ok {
				for _, fld := range st.Fields.List {
					for _, name := range fld.Names {
						shared[name.Name] = true
					}
				}
			}
			return true
		})
	}
	if len(shared) == 0 {
		t.Fatalf("%s's shared parameter struct (var p struct) not found in %s: if the dispatch moved, point the guard at it", dispatch, path)
	}
	for _, ns := range typedHandleNamespaces {
		for _, field := range ns.fields {
			if shared[field] {
				t.Errorf("%s's shared parameter struct has %s again: %s methods decode into their own struct (internal/node/handle_*.go, #8089)", dispatch, field, ns.prefix)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, ns := range typedHandleNamespaces {
			if strings.HasPrefix(s, ns.prefix) {
				t.Errorf("node.go spells %q as a literal: use the %s method constant (internal/node/handle_*.go, #8089)", s, ns.prefix)
			}
		}
		return true
	})
}
