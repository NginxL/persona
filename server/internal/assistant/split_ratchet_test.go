package assistant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The assistant package is being split into subpackages (#7891, #7896): pure logic moves out, Service methods stay as
// thin glue. These two numbers only go down: when you move code out, lower them to the new totals; never raise them.
// Put new logic in a subpackage (chatsearch is the first) instead of here.
const (
	maxNonTestLines     = 143632 // lines of the package's non-test files
	maxServiceMethods   = 2643   // methods on *Service across those files
	splitRatchetHowToDo = "move the logic into a subpackage of internal/assistant (see chatsearch) and keep only thin Service glue here (#7891)"
)

func TestAssistantPackageOnlyShrinks(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var lines, methods int
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines += strings.Count(string(b), "\n")
		af, err := parser.ParseFile(fset, f, b, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			typ := fd.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok && id.Name == "Service" {
				methods++
			}
		}
	}
	if lines > maxNonTestLines {
		t.Errorf("assistant's non-test files have %d lines, ratchet is %d: %s", lines, maxNonTestLines, splitRatchetHowToDo)
	}
	if methods > maxServiceMethods {
		t.Errorf("*Service has %d methods, ratchet is %d: %s", methods, maxServiceMethods, splitRatchetHowToDo)
	}
	if lines < maxNonTestLines || methods < maxServiceMethods {
		t.Logf("lower the ratchet: lines %d (now %d), methods %d (now %d)", maxNonTestLines, lines, maxServiceMethods, methods)
	}
}
