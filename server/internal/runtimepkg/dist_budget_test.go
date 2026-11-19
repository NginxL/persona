package runtimepkg

import (
	"os"
	"testing"
)

// TestDistBudget bounds what personad, persona-node and persona-manager embed of the runtime packages: every file
// Generate writes from integrations/. Measured 166,998 B on 2026-11-19 for the DSH, pi, Antigravity and Paseo runtimes
// and the Claude Code plugin, after the Antigravity runtime grew for MCP enums, plan refreshes, tool names, supervision
// and final view blocks (#9983, #10388, #10633, #10579, #10802) and the runtime SDK for token usage (#10074); the budget
// is that plus the same 5,244 B of headroom.
// Raise it only for code a runtime needs, never to embed the packages raw again.
func TestDistBudget(t *testing.T) {
	const budget = 172_242
	_, out := generateIntegrations(t)
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	if size > budget {
		t.Fatalf("generated runtime packages are %d bytes, over the budget of %d", size, budget)
	}
	t.Logf("generated runtime packages: %d bytes (budget %d)", size, budget)
}
