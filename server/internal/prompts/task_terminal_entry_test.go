package prompts

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The terminal entry's calls the task notes name: "`tool <name>` and `{…}`" or "`<name>` with `{…}`" (#12136).
var terminalCallRe = regexp.MustCompile("`(?:tool )?([a-z_]+)` (?:with|and) `(\\{[^`]*\\})`")

// A terminal Agent has no tool list to read the schemas from: the argument shapes its handoff and leader notes give are
// all it has. Each must be one JSON object the tool's own schema takes: its required keys, no key the schema does not
// have (the leader's tools refuse unknown fields), and enum values the schema allows.
func TestTerminalEntryArgumentShapesMatchTheTools(t *testing.T) {
	// A task leader's task_plan and task_review are listed under those names with the leader tools' schemas.
	schemaOf := map[string]string{"task_plan": "leader_task_plan", "task_review": "leader_task_review"}
	notes := RenderTaskHandoff(TaskHandoff{Executable: `"/opt/tools/persona-node"`}) + "\n\n" + RenderTaskLeader(TaskLeader{Terminal: true})
	var named []string
	for _, m := range terminalCallRe.FindAllStringSubmatch(notes, -1) {
		name, args := m[1], m[2]
		named = append(named, name)
		var value any
		if err := json.Unmarshal([]byte(args), &value); err != nil {
			t.Errorf("%s: %s is not JSON: %v", name, args, err)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(Tool(cmp.Or(schemaOf[name], name)).InputSchema, &schema); err != nil || schema == nil {
			t.Errorf("%s: no schema: %v", name, err)
			continue
		}
		for _, problem := range schemaProblems(schema, value, name) {
			t.Errorf("%s %s: %s", name, args, problem)
		}
	}
	for _, want := range []string{"todo_decisions", "todo_decision_add", "report_to_supervisor", "task_plan", "task_review"} {
		if !slices.Contains(named, want) {
			t.Errorf("the terminal notes give no arguments for %s: %v\n%s", want, named, notes)
		}
	}
}

// The terminal text appears only for a terminal entry: an MCP or unverified entry, and an MCP leader, keep theirs.
func TestTerminalEntryTextOnlyForTheTerminal(t *testing.T) {
	for _, h := range []TaskHandoff{{MCP: true}, {MCP: true, Executable: `"/opt/tools/persona-node"`}, {}, {Language: "English"}} {
		if got := RenderTaskHandoff(h); strings.Contains(got, "The same entry runs") || h.Terminal() {
			t.Errorf("%+v got the terminal entry: %s", h, got)
		}
	}
	if !(TaskHandoff{Executable: `"/x"`}).Terminal() {
		t.Error("a verified executable without the MCP is not the terminal entry")
	}
	if got := RenderTaskLeader(TaskLeader{}); strings.Contains(got, "`tool task_plan`") || strings.Contains(got, "from this terminal") {
		t.Errorf("an MCP leader was pointed at the terminal: %s", got)
	}
}

// schemaProblems checks value against the parts of a JSON schema the tools' schemas use: type, required, properties
// (with additionalProperties closed or not), items and enum.
func schemaProblems(schema map[string]any, value any, at string) []string {
	var out []string
	switch schema["type"] {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return []string{at + ": not an object"}
		}
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, r := range required {
			if _, ok := obj[r.(string)]; !ok {
				out = append(out, at+": lacks required "+r.(string))
			}
		}
		for key, v := range obj {
			sub, ok := props[key].(map[string]any)
			if !ok {
				out = append(out, at+": "+key+" is not in the schema")
				continue
			}
			out = append(out, schemaProblems(sub, v, at+"."+key)...)
		}
	case "array":
		list, ok := value.([]any)
		if !ok {
			return []string{at + ": not an array"}
		}
		items, _ := schema["items"].(map[string]any)
		for _, v := range list {
			out = append(out, schemaProblems(items, v, at+"[]")...)
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return []string{at + ": not a string"}
		}
		if enum, ok := schema["enum"].([]any); ok && !slices.Contains(enum, any(s)) {
			out = append(out, at+": "+s+" is not one of the schema's values")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			out = append(out, at+": not a boolean")
		}
	}
	return out
}
