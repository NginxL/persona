package node

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The destination owns path resolution: the hub cannot resolve a remote symlink.
func (n *Node) guardAgentDirectory(ctx context.Context, cwd string) error {
	cwd = proto.ExpandHome(strings.TrimSpace(cwd), n.home)
	blocked := identity.IsAssistantDir(cwd)
	resolved, err := filepath.EvalSymlinks(cwd)
	if err == nil {
		cwd = resolved
	} else {
		cwd = filepath.Clean(cwd)
	}
	blocked = blocked || identity.IsAssistantDir(cwd)
	candidate, statErr := os.Stat(cwd)
	for _, state := range []string{n.dir, identity.DataDir(n.home)} {
		dirs, _ := os.ReadDir(state)
		for _, dir := range dirs {
			if identity.IsManagerDirName(strings.ToLower(dir.Name()), "tracks") {
				// A track's directory (tracks/<slug>, #13472), however its path is spelled.
				tracks := filepath.Join(state, dir.Name())
				if info, err := os.Stat(tracks); statErr == nil && err == nil && identity.ValidTrackSlug(strings.ToLower(filepath.Base(cwd))) {
					if parent, err := os.Stat(filepath.Dir(cwd)); err == nil && os.SameFile(parent, info) {
						blocked = true
					}
				}
				continue
			}
			if !identity.IsManagerDirName(dir.Name(), "assistant") && !identity.IsManagerDirName(dir.Name(), identity.SpaceHealth) {
				continue
			}
			expected := filepath.Join(state, dir.Name())
			if cwd == expected {
				blocked = true
			}
			info, err := os.Stat(expected)
			if statErr == nil && err == nil && os.SameFile(candidate, info) {
				blocked = true
			}
		}
	}
	if blocked {
		return refusal("agent_assistant_workspace", i18n.T(i18n.From(ctx), "rpc-errors.agent_assistant_workspace"), nil)
	}
	return nil
}

// agentRulesFile is the optional file in the node's data directory whose text goes with every new Agent started on
// this computer (#13642): its owner's standing rules for it, carried like the caller's task instructions (Claude Code's
// instruction file, Codex's developer instructions, else after the first message). Persona adds no rules of its own: a
// computer without the file starts each Agent with what its caller sent, on every platform.
const agentRulesFile = "agent-rules.md"

// agentStartRules is rules followed by this computer's agent-rules.md when it has text, else rules as they are (no
// file, an empty one, or one that cannot be read).
func (n *Node) agentStartRules(rules string) string {
	if n.dir == "" {
		return rules
	}
	raw, err := os.ReadFile(filepath.Join(n.dir, agentRulesFile))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("agent rules: %v", err)
		}
		return rules
	}
	own := strings.TrimSpace(string(raw))
	if own == "" {
		return rules
	}
	return strings.TrimSpace(rules + "\n\n" + own)
}
