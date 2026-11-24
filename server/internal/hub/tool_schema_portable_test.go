package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/mcptoolgolden"
	"github.com/nwverse/persona/server/internal/toolschema"
)

// Every tool the hub offers stays within what Gemini accepts: one schema with an empty string in an enum makes an
// Agy (Gemini) session fail every model call (2026-11-23). The goldens are the hub's lists as sent (TestGoldenMCPTools
// keeps them current); internal/mcpserver also strips such enums on the way out, for tools extensions declare.
func TestHubToolSchemasAreAcceptedByGemini(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(mcptoolgolden.Dir, "*", "*.golden"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no tool goldens under %s (%v)", mcptoolgolden.Dir, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(b, &tool); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, p := range toolschema.Problems(tool.InputSchema) {
			t.Errorf("%s (%s): %s; Gemini refuses the whole session's requests", tool.Name, f, p)
		}
	}
}
