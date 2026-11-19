package node

import "testing"

// The node's hop of a round's tool set (#7315, assistant_round.go): `personad mcp` started with PERSONA_TOOL_SET asks
// mcp.tools with tool_set, and the node passes it on to the manager, which narrows the list (supervise_round.go). If it
// were dropped every round would silently get the whole list.
func TestMCPToolsForwardsToolSet(t *testing.T) {
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	req := mcpTools(false, "sess-round")
	req["tool_set"] = roundToolSet
	if got := ask(req); got["tool_set"] != roundToolSet {
		t.Fatalf("relayed %v, want tool_set %q", got, roundToolSet)
	}
	// Any other process asks for none, and none is passed on: the whole list.
	if got := ask(mcpTools(false, "sess-other")); got["tool_set"] != "" && got["tool_set"] != nil {
		t.Fatalf("relayed %v, want no tool set", got)
	}
}
