// Package toolschema keeps MCP tool input schemas within what every model API Persona's agents use accepts. Gemini
// (Agy) checks each function declaration more strictly than Claude or Codex: one tool with an empty string in an
// enum ("enum": ["", "recent", "role"]) makes every request of the session fail with INVALID_ARGUMENT, so an Agy
// agent with Persona's MCP tools cannot call its model at all. Problems names what a schema must not contain (the
// tool tests assert none); Portable removes it from a schema on its way out (internal/mcpserver), so a tool an
// extension declares this way still cannot take an Agy session down.
package toolschema

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// Problems lists the places in schema a Gemini function declaration rejects: an enum with an empty string, or an
// empty enum. A schema that is not JSON is one problem. (A boolean enum such as task_review's ready: [true] is left
// alone: it is in the assistant's list only, which no Gemini session gets.)
func Problems(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(schema, &v); err != nil {
		return []string{"not JSON: " + err.Error()}
	}
	var out []string
	walk(v, "", func(path string, enum []any) {
		if len(enum) == 0 {
			out = append(out, path+".enum is empty")
		}
		for i, e := range enum {
			if s, ok := e.(string); ok && s == "" {
				out = append(out, fmt.Sprintf("%s.enum[%d] is an empty string", path, i))
			}
		}
	})
	return out
}

// Portable returns schema without what Problems names: empty strings leave an enum, and an enum left empty is
// dropped (the property takes any value of its type). A schema with nothing to
// change, or that is not JSON, comes back as it was.
func Portable(schema json.RawMessage) json.RawMessage {
	if len(Problems(schema)) == 0 {
		return schema
	}
	var v any
	if json.Unmarshal(schema, &v) != nil {
		return schema
	}
	fix(v)
	b, err := json.Marshal(v)
	if err != nil {
		return schema
	}
	return b
}

func walk(v any, path string, enum func(string, []any)) {
	switch n := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(n) {
			if k == "enum" {
				if list, ok := n[k].([]any); ok {
					enum(path, list)
					continue
				}
			}
			walk(n[k], path+"."+k, enum)
		}
	case []any:
		for i, e := range n {
			walk(e, fmt.Sprintf("%s[%d]", path, i), enum)
		}
	}
}

func fix(v any) {
	switch n := v.(type) {
	case map[string]any:
		for k, child := range n {
			if k == "enum" {
				if list, ok := child.([]any); ok {
					kept := slices.DeleteFunc(slices.Clone(list), func(e any) bool { s, ok := e.(string); return ok && s == "" })
					if len(kept) == 0 {
						delete(n, k)
					} else {
						n[k] = kept
					}
					continue
				}
			}
			fix(child)
		}
	case []any:
		for _, e := range n {
			fix(e)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
