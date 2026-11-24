package node

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// instructionsDigest is a digest of the complete instructions the agent reads in dir at start: the bounded
// CLAUDE.md / AGENTS.md entry plus every generated native-rule part, "" when there are none.
func instructionsDigest(dir string) string {
	if dir == "" {
		return ""
	}
	h := sha256.New()
	found := false
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			found = true
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
		h.Write(b)
	}
	remainder, err := assistantInstructionRemainder(dir)
	if err != nil {
		fmt.Fprintf(h, "remainder error\x00%s\x00", err)
	} else {
		fmt.Fprintf(h, "remainder\x00%d\x00", len(remainder))
		h.Write([]byte(remainder))
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// instructionsStale: the instructions in its directory changed since it started (the hub wrote a new CLAUDE.md, e.g.
// after an upgrade with new prompts, #3235): it has to resume in a fresh process to read them. Claude only: Codex's
// app-server is left as it is.
func (p *assistantProc) instructionsStale() bool {
	return p.agent == "claude" && p.cwd != "" && instructionsDigest(p.cwd) != p.instructions
}
