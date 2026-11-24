package prompts

import (
	"encoding/json"
	"strings"
	"testing"
)

// The per-start project/direction memory opt-out (#13538) is discoverable on both start tools, optional and off by
// default. The task_plan property lives on each task, not on the outer plan.
func TestSkipProjectMemoryToolSchemas(t *testing.T) {
	for _, name := range []string{"task_plan", "agent_start"} {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(Tool(name).InputSchema, &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw := schema.Properties["skip_project_memory"]
		if name == "task_plan" {
			var tasks struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			}
			if err := json.Unmarshal(schema.Properties["tasks"], &tasks); err != nil {
				t.Fatal(err)
			}
			raw = tasks.Items.Properties["skip_project_memory"]
		}
		var property struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &property); err != nil {
			t.Fatalf("%s skip_project_memory: %v", name, err)
		}
		if property.Type != "boolean" {
			t.Errorf("%s skip_project_memory type = %q, want boolean", name, property.Type)
		}
		for _, phrase := range []string{"default false", "project/direction memory", "prompt"} {
			if !strings.Contains(property.Description, phrase) {
				t.Errorf("%s skip_project_memory description lacks %q: %q", name, phrase, property.Description)
			}
		}
	}
}
