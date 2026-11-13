package node

import "runtime"

// claudeProcessCount counts the node user's running claude processes for the hub's admission gate. Linux reuses the
// cached /proc walk (userProcesses); other platforms cannot answer from the shared walk yet, so they report unknown
// (ok=false) rather than a guess. The count includes every claude command line (isClaudeCmd), whatever its config
// directory: unlike scanClaudeProcesses it must not drop default-config claudes, which the guard's process cap counts.
// On Linux the walk reads the node's own uid's /proc, which is readable in practice; a walk that cannot open /proc at
// all yields an empty map (count 0), the same best-effort semantics the shared snapshot already gives its other callers.
func claudeProcessCount() (n int, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	return countClaudeArgv(userProcesses()), true
}

// countClaudeArgv counts the pid -> argv entries whose command line runs Claude Code. Split out so tests do not need a
// real /proc.
func countClaudeArgv(procs map[int][]string) int {
	n := 0
	for _, argv := range procs {
		if isClaudeCmd(argv) {
			n++
		}
	}
	return n
}
