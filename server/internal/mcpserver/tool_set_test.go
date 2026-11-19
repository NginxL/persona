package mcpserver

import (
	"context"
	"testing"
)

// A background supervisor's round starts its Persona server with PERSONA_TOOL_SET (node/assistant_round.go): mcp.tools
// carries it to the node, which passes it on for the manager to narrow the list. Without it nothing is sent, as before.
func TestListToolsSendsToolSet(t *testing.T) {
	old := dialNode
	t.Cleanup(func() { dialNode = old })
	var asked map[string]any
	dialNode = func(_ context.Context, req any) ([]byte, error) {
		asked, _ = req.(map[string]any)
		return []byte(`{"tools":[{"name":"task_review"}]}`), nil
	}
	t.Setenv("PERSONA_ASSISTANT", "1")
	t.Setenv("PERSONA_SESSION_ID", "11111111-2222-3333-4444-555555555555")

	t.Setenv("PERSONA_TOOL_SET", "supervise")
	tools, err := listToolsOnce(context.Background(), toolSnapshot{})
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("tools %v, %v", tools, err)
	}
	if asked["kind"] != "mcp.tools" || asked["tool_set"] != "supervise" || asked["assistant"] != true {
		t.Fatalf("asked %v", asked)
	}

	t.Setenv("PERSONA_TOOL_SET", "")
	if _, err := listToolsOnce(context.Background(), toolSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, set := asked["tool_set"]; set {
		t.Fatalf("a tool set was sent without one: %v", asked)
	}
}
