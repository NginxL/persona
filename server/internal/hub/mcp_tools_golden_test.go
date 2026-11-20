package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/mcptoolgolden"
	"github.com/nwverse/persona/server/internal/proto"
)

// The MCP tools the hub offers, as they go over the wire (json.Marshal compacts the schemas; the goldens are indented
// for reading, which is a function of the same bytes). One golden a tool, an index a list, and for the assistant's
// variants only their difference from its list (mcptoolgolden, #11252), so a change to one tool rewrites one small
// file. docs/assistant/prompts.md: regenerate with UPDATE_GOLDEN=1, which also deletes the goldens of removed tools.
// The connectors', Cua's and ego's are the manager's (extmgr's connector_tools goldens, its answer to
// extensions.hub_call connector_tools); the hub lists them with its own and the assistant's, in the same order.
func TestGoldenMCPTools(t *testing.T) {
	checked := func(tools []proto.Tool) []proto.Tool {
		assertNoEmptyToolEnums(t, tools)
		assertToolSchemaTypes(t, tools)
		return tools
	}
	dir := mcptoolgolden.Dir
	h := newTestHub(t)
	// The manager answers as its golden says (extmgr TestGoldenConnectorTools), for the case at hand.
	manager := func(name string) {
		f := fakeExtOf(h)
		f.on(fakeConnectorTools, func(raw json.RawMessage) (any, error) {
			var a struct {
				Assistant bool `json:"assistant"`
			}
			_ = json.Unmarshal(raw, &a)
			file := name
			if !a.Assistant {
				file = "agent"
			}
			b, err := os.ReadFile(filepath.Join("..", "extmgr", "testdata", "prompts", "connector_tools."+file+".golden"))
			if err != nil {
				return nil, err
			}
			var out json.RawMessage = b
			return out, nil
		})
	}
	manager("assistant")
	connectAssistant(t, h) // its tools are its to list (assistant.tools)
	assistantTools := checked(h.mcpTools(true))
	mcptoolgolden.CheckList(t, dir, "assistant", assistantTools)
	mcptoolgolden.CheckList(t, dir, "agent", checked(h.mcpTools(false)))
	// With GitHub signed in: its read tools after the hub's own, for the assistant only.
	manager("assistant_with_github")
	mcptoolgolden.CheckVariant(t, dir, "assistant_with_github", "assistant", assistantTools, checked(h.mcpTools(true)))
	mcptoolgolden.CheckList(t, dir, "agent", checked(h.mcpTools(false)))
	// With iMessage handed to Persona (the switch is on by default): its read tools too.
	manager("assistant_with_imessage")
	mcptoolgolden.CheckVariant(t, dir, "assistant_with_imessage", "assistant", assistantTools, checked(h.mcpTools(true)))
	mcptoolgolden.CheckList(t, dir, "agent", checked(h.mcpTools(false)))
}

// The whole-list goldens the per-tool files replaced (#11252). A branch written before the split that merges main can
// keep or recreate them while the per-tool files stay stale (#11475), so their return fails here with the fix.
func TestNoWholeListMCPToolGoldens(t *testing.T) {
	old, err := filepath.Glob(filepath.Join("testdata", "prompts", "mcp_tools.*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range old {
		t.Errorf("%s is a whole-list golden replaced by %s/<list>/<tool>.golden (#11252): delete it, then rerun UPDATE_GOLDEN=1 go test ./internal/hub -run TestGoldenMCPTools so the tool your branch changed is in its own file", f, mcptoolgolden.Dir)
	}
}

func assertNoEmptyToolEnums(t *testing.T, tools []proto.Tool) {
	t.Helper()
	var walk func(node any, path string)
	walk = func(node any, path string) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		if raw, has := obj["enum"]; has {
			vals, ok := raw.([]any)
			if !ok || len(vals) == 0 {
				t.Errorf("%s.enum must be a non-empty array, got %#v", path, raw)
			}
			for _, v := range vals {
				if s, ok := v.(string); ok && s == "" {
					t.Errorf(`%s.enum contains an empty string ""`, path)
				}
			}
		}
		for k, v := range obj {
			switch k {
			case "enum":
				continue
			case "properties", "$defs", "definitions", "patternProperties":
				if m, ok := v.(map[string]any); ok {
					for ck, cv := range m {
						walk(cv, path+"."+k+"."+ck)
					}
				}
			default:
				switch x := v.(type) {
				case map[string]any:
					walk(x, path+"."+k)
				case []any:
					for _, item := range x {
						walk(item, path+"."+k+"[]")
					}
				}
			}
		}
	}
	for _, tl := range tools {
		if len(tl.InputSchema) == 0 {
			continue
		}
		var schema any
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Fatalf("%s: invalid input_schema: %v", tl.Name, err)
		}
		walk(schema, tl.Name)
	}
}

// assertToolSchemaTypes checks the complete wire lists, including connector tools added to the embedded prompts.
// A property or array item needs an explicit type; nested schemas with properties, items, enum or const need one too.
// A reference is the explicit exception: its target is checked in $defs.
func assertToolSchemaTypes(t *testing.T, tools []proto.Tool) {
	t.Helper()
	var walk func(node any, path string, requireType bool)
	walk = func(node any, path string, requireType bool) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		_, hasType := obj["type"]
		_, hasProperties := obj["properties"]
		_, hasItems := obj["items"]
		_, hasEnum := obj["enum"]
		_, hasConst := obj["const"]
		_, hasRef := obj["$ref"]
		if !hasType && (requireType || hasProperties || hasItems || hasEnum || hasConst) &&
			(!hasRef || hasProperties || hasItems || hasEnum || hasConst) {
			t.Errorf("%s: type is not defined", path)
		}
		for k, v := range obj {
			switch k {
			case "enum", "const":
				continue
			case "properties", "$defs", "definitions", "patternProperties", "dependentSchemas":
				if children, ok := v.(map[string]any); ok {
					for ck, cv := range children {
						walk(cv, path+"."+k+"."+ck, k == "properties")
					}
				}
			case "items":
				switch items := v.(type) {
				case []any:
					for i, item := range items {
						walk(item, fmt.Sprintf("%s.items[%d]", path, i), true)
					}
				default:
					walk(items, path+".items", true)
				}
			default:
				switch x := v.(type) {
				case map[string]any:
					walk(x, path+"."+k, false)
				case []any:
					for i, item := range x {
						walk(item, fmt.Sprintf("%s.%s[%d]", path, k, i), false)
					}
				}
			}
		}
	}
	for _, tl := range tools {
		if len(tl.InputSchema) == 0 {
			continue
		}
		var schema any
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Fatalf("%s: invalid input_schema: %v", tl.Name, err)
		}
		walk(schema, tl.Name, false)
	}
}
