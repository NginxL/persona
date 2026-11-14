package archguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodeMaxLines is the ceiling for the non-test Go lines directly in internal/node. It only goes down: the zero-coupling
// clusters move into subpackages (#7891, #7894), and every PR that moves one lowers it to the new size. Lower it after
// shrinking the package; never raise it, put new code in a subpackage instead.
const nodeMaxLines = 105698

func TestNodePackageDoesNotGrow(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "node", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no node files: %v", err)
	}
	lines := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines += bytes.Count(b, []byte("\n"))
	}
	switch {
	case lines > nodeMaxLines:
		t.Errorf("internal/node has %d non-test lines, ceiling %d: put new code in a subpackage under internal/node (#7894)", lines, nodeMaxLines)
	case lines < nodeMaxLines:
		t.Errorf("internal/node shrank to %d non-test lines: lower nodeMaxLines from %d to %d in this file", lines, nodeMaxLines, lines)
	}
}
