package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/toolschema"
)

// An extension tool with an empty string in an enum (enkeep_seed_import's expiring, 2026-11-23) made Gemini refuse
// every request of an Agy session; the list an agent gets carries no such enum, whoever declared the tool.
func TestToolsListLeavesNoEmptyEnumForGemini(t *testing.T) {
	t.Setenv("PERSONA_ASSISTANT", "0")
	fakeNode(t, func(int) ([]byte, error) {
		return []byte(`{"tools":[{"name":"enkeep_seed_import","description":"seeds","input_schema":{"type":"object","properties":{` +
			`"items":{"type":"array","items":{"type":"object","properties":{"expiring":{"type":"string","enum":["","recent","role"]}}}}}}}]}`), nil
	})
	res, rerr := handle(context.Background(), "test", listRequest())
	if rerr != nil {
		t.Fatal(rerr)
	}
	tools := res.(map[string]any)["tools"].([]map[string]any)
	schema, _ := tools[0]["inputSchema"].(json.RawMessage)
	if len(schema) == 0 {
		t.Fatalf("no schema: %+v", tools[0])
	}
	if p := toolschema.Problems(schema); len(p) != 0 {
		t.Fatalf("the listed schema still has %q: %s", p, schema)
	}
}
