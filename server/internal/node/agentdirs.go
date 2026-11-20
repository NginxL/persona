package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Blocked Agent folders (node.configure agent_dirs_blocked, nodeproto.AgentDirsBlocked, #10053): the Agent folder rules
// set to Block. A session whose working folder matches is kept out of Persona on this computer, whatever runtime runs
// it: it is in no list (sessions.go, sessions_offline.go), and nothing of it is sent (events, interaction cards,
// notifications); a permission, question or plan it asks for gets no decision at once, so the agent asks in its own
// interface. The assistant's own conversations and the sessions the hub keeps are never blocked. The rules set to Hide
// are the manager's alone: the node is not told of them.

// setBlockedAgentDirs takes the hub's patterns; the next list leaves their sessions out.
func (n *Node) setBlockedAgentDirs(patterns []string) {
	clean := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p, ok := proto.CleanDirPattern(p); ok && proto.ValidDirPattern(p) && !slices.Contains(clean, p) {
			clean = append(clean, p)
		}
	}
	n.mu.Lock()
	before := n.agentDirsBlocked
	n.agentDirsBlocked = clean
	n.mu.Unlock()
	if slices.Equal(before, clean) {
		return
	}
	n.saveBlockedAgentDirs(clean)
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
}

// agentDirBlocked: a session working in cwd is kept out (a blocked Agent folder). A cwd is matched as written and
// with its links followed, and the home under either of its names (/home/me -> /data/home/me).
func (n *Node) agentDirBlocked(cwd string) bool {
	if cwd == "" {
		return false
	}
	n.mu.Lock()
	patterns := n.agentDirsBlocked
	n.mu.Unlock()
	return n.dirMatches(cwd, patterns)
}

// dirMatches: dir, or dir with its links followed, matches one of patterns, the home under either of its names.
func (n *Node) dirMatches(dir string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	homeReal := realHome(n.home)
	if proto.DirPatternMatchedOn(dir, n.home, homeReal, patterns) != "" {
		return true
	}
	real, err := filepath.EvalSymlinks(proto.ExpandHome(dir, n.home))
	return err == nil && proto.DirPatternMatchedOn(real, n.home, homeReal, patterns) != ""
}

// sessionBlocked: the session is kept out by a blocked Agent folder, with the exemptions the list makes (the assistant's
// conversations, the sessions the hub keeps). cwd is the session's working folder as the caller knows it.
func (n *Node) sessionBlocked(sid, cwd string) bool {
	if !n.agentDirBlocked(cwd) {
		return false
	}
	return sid == "" || (!n.isAssistantSession(sid) && !n.keptSessions()[sid])
}

// restBlocked: a session at rest (a transcript or rollout on disk, listed or in history) working in cwd is left out.
// A blocked Agent folder spares the assistant's conversations and the sessions the hub keeps (keep); a blocked Claude
// config folder does not.
func (n *Node) restBlocked(id, cwd string, keep map[string]bool) bool {
	return cwd != "" && n.cwdBlocked(cwd) && (n.claudeDirBlocks(cwd) || !keep[id] && !n.isAssistantSession(id))
}

// runtimeSessionBlocked is sessionBlocked for a session a runtime package runs: cwd as the runtime gave it, else its row's.
func (n *Node) runtimeSessionBlocked(sid, cwd string) bool {
	if cwd == "" && n.runtimes != nil {
		if row, _, ok := n.runtimes.rowOf(sid); ok {
			cwd = row.Cwd
		}
	}
	return n.sessionBlocked(sid, cwd)
}

// sessionCwd is the working folder of session sid as the node knows it without reading a transcript: its live agent's,
// its runtime row's, or what the node keeps of it; "" when none says.
func (n *Node) sessionCwd(sid string) string {
	if sid == "" {
		return ""
	}
	if a, ok := n.agent(sid); ok && a.Cwd != "" {
		return a.Cwd
	}
	if n.runtimes != nil {
		if row, _, ok := n.runtimes.rowOf(sid); ok && row.Cwd != "" {
			return row.Cwd
		}
	}
	if st, ok := n.state.get(sid); ok {
		return st.Cwd
	}
	return ""
}

// blockedAgentDirsFile keeps the last patterns the hub gave (~/.persona/agent-dirs-blocked.json): a hook, a runtime's
// card or a replay of what standby kept that comes before the hub's node.configure is held to them too.
const blockedAgentDirsFile = "agent-dirs-blocked.json"

func (n *Node) loadBlockedAgentDirs() {
	if n.dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(n.dir, blockedAgentDirsFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			n.logger.Printf("blocked agent folders: %v", err)
		}
		return
	}
	var patterns []string
	if err := json.Unmarshal(b, &patterns); err != nil {
		n.logger.Printf("blocked agent folders: %v", err)
		return
	}
	n.mu.Lock()
	n.agentDirsBlocked = patterns
	n.mu.Unlock()
}

func (n *Node) saveBlockedAgentDirs(patterns []string) {
	if n.dir == "" {
		return
	}
	b, _ := json.Marshal(patterns)
	if err := writeFileAtomic(filepath.Join(n.dir, blockedAgentDirsFile), b); err != nil {
		n.logger.Printf("blocked agent folders: %v", err)
	}
}
