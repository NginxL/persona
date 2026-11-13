package node

import "testing"

// countClaudeArgv counts every claude command line whatever its config directory, so the hub's process cap matches
// the guard script's pgrep semantics rather than scanClaudeProcesses' env-bearing subset.
func TestCountClaudeArgv(t *testing.T) {
	procs := map[int][]string{
		101: {"claude", "--resume", "abc"},              // native binary
		102: {"node", "/opt/claude/cli.js"},             // node-launched CLI
		103: {"bun", "/home/u/bin/claude"},              // bun-launched CLI
		104: {"node", "/opt/codex/bin/codex.js", "x"},   // not claude
		105: {"/usr/bin/bash"},                          // shell
		106: {"node", "/app/other.js", "claude-helper"}, // path without a claude command component
		107: {},                                         // gone between walk and read
	}
	if got := countClaudeArgv(procs); got != 3 {
		t.Fatalf("countClaudeArgv = %d, want 3 (the native, node and bun claude invocations)", got)
	}
	if got := countClaudeArgv(nil); got != 0 {
		t.Fatalf("countClaudeArgv(nil) = %d, want 0", got)
	}
}
