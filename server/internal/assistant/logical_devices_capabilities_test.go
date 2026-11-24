package assistant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/closeout"
)

// New wraps the devices in logicalDevices whenever the store keeps logical conversations, which the hub's database
// does. The wrapper promotes only the Devices interface's methods, so the optional capabilities the assistant looks
// for must be asked of the physical devices, or the deployed assistant never finds them (#12928).

func TestMachineRedeployThroughLogicalDevices(t *testing.T) {
	s, d := newMachineRedeployService(t, Device{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true})
	s.devs = &logicalDevices{Devices: d, s: s}
	out, err := callMachineRedeploy(t, s, "box-1")
	if err != nil {
		t.Fatalf("redeploy through the logical devices: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "success") {
		t.Fatalf("the result does not read back a successful redeploy: %q", out)
	}
	if got := d.startedIDs(); len(got) != 1 || got[0] != "box-1" {
		t.Fatalf("started deployments on %v, want only box-1", got)
	}
}

func TestNodeCapabilitiesThroughLogicalDevices(t *testing.T) {
	s, _ := newTaskService(t)
	s.devs = &logicalDevices{Devices: closeoutDevices{newFleetDevs()}, s: s}
	if got := s.closeoutForm("box", "en"); got != closeout.MarkdownExample("en") {
		t.Fatalf("a node announcing markdown closeouts got the XML form through the logical devices: %q", got)
	}
}

func TestOptionalDeviceCapabilitiesAreAskedOfThePhysicalDevices(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			a, ok := n.(*ast.TypeAssertExpr)
			if !ok {
				return true
			}
			sel, ok := a.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "devs" {
				return true
			}
			if star, ok := a.Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "logicalDevices" {
					return true
				}
			}
			t.Errorf("%s: a type assertion on devs misses what logicalDevices does not promote: assert on s.physicalDevices()", fset.Position(a.Pos()))
			return true
		})
	}
}
