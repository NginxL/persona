package assistant

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Claude clients drop a tool whose input schema has anyOf / oneOf / allOf at the top level (the Anthropic API does
// not accept them there), so no Claude session would see it. "At least one of" rules belong in the description and
// the handler's own checks, never at the top of the schema (todo_repository_links_add lost its links this way).
func TestToolSchemasHaveNoTopLevelCombinators(t *testing.T) {
	if len(toolNames) == 0 || len(agentToolNames) == 0 {
		t.Fatal("no tools to check")
	}
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, toolNames...), agentToolNames...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(prompts.Tool(name).InputSchema, &schema); err != nil {
			t.Fatalf("%s: bad JSON schema: %v", name, err)
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if _, has := schema[key]; has {
				t.Errorf("%s: input schema has top-level %s; Claude clients drop the whole tool, put the rule in the description", name, key)
			}
		}
	}
}
