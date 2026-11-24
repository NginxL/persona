package node

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/assistantskills"
)

const agySkillWalkDepth = 64

// agySkillRuntime resolves whether in targets a runtime-backed assistant and verifies that it is a
// connected, assistant-capable Antigravity (agy) runtime. Non-agy runtimes, non-assistant runtime
// sessions, disconnected runtimes, and launcher/session mismatches are rejected rather than falling
// through to the Claude/Codex branches.
//
// Connected-runtime rejection applies only to runtime-backed sessions or an explicitly supplied
// launcher; a non-runtime (UUID) session with an omitted launcher stays on the built-in Claude/Codex
// path even when an agy runtime is connected.
func (n *Node) agySkillRuntime(in skillCatalogRequest) (launcher string, isRuntime bool, err error) {
	if n.runtimeAssistant(in.SessionID) {
		var st sessionState
		if n.state != nil {
			st, _ = n.state.get(in.SessionID)
		}
		if in.Launcher != "" && n.assistantRuntime(in.Launcher) == nil {
			return "", true, errors.New("skill selection is unavailable for this runtime")
		}
		rl := launcherOr(in.Launcher, st.Launcher)
		h := n.assistantRuntime(rl)
		owner, _, _ := n.runtimes.belongs(in.SessionID)
		if h == nil {
			h = owner
			if h != nil {
				rl = h.id()
			}
		}
		if owner != nil && h != owner {
			return "", true, errors.New("skill selection is unavailable for this runtime")
		}
		if h == nil || !h.assistant() || h.agent() != agyAgent {
			return "", true, errors.New("skill selection is unavailable for this runtime")
		}
		if rl == "" {
			rl = h.id()
		}
		return rl, true, nil
	}
	if runtimeSessionID(in.SessionID) {
		return "", true, errors.New("skill selection is unavailable for this runtime")
	}
	if in.Launcher == "" {
		return "", false, nil
	}
	h := n.assistantRuntime(in.Launcher)
	if h == nil {
		// Reject launchers owned by a non-assistant runtime or a disconnected managed agy launcher
		// instead of letting an empty shell probe fall back to Claude.
		if n.runtimeOf(in.Launcher) != nil {
			return "", true, errors.New("skill selection is unavailable for this runtime")
		}
		if m, ok := n.managedLauncher(in.Launcher); ok && m.AgentType() == agyAgent {
			return "", true, errors.New("skill selection is unavailable for this runtime")
		}
		return "", false, nil
	}
	if in.SessionID != "" || !h.assistant() || h.agent() != agyAgent {
		return "", true, errors.New("skill selection is unavailable for this runtime")
	}
	return in.Launcher, true, nil
}

// agySkillCatalog discovers bounded local skill roots for an Antigravity (agy) assistant runtime
// in deterministic precedence order (workspace ancestors -> user -> builtin) and deduplicates by
// canonical path and skill name.
func (n *Node) agySkillCatalog(in skillCatalogRequest) ([]assistantskills.Entry, error) {
	if n.assistantDirectoryPrepared(in.Cwd) {
		n.assistantRuntimeSkills(in.Cwd)
	}
	entries, err := assistantskills.LocalCatalog(n.agySkillRoots(in.Cwd))
	if err != nil {
		return nil, err
	}
	return dedupeSkillsByName(entries), nil
}

func (n *Node) agySkillRoots(cwd string) []assistantskills.Root {
	roots := []assistantskills.Root{}
	seen := map[string]bool{}
	addRoot := func(path, source string) {
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		real := clean
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			real = resolved
			if seen[real] {
				return
			}
		}
		seen[clean] = true
		seen[real] = true
		roots = append(roots, assistantskills.Root{Path: clean, Source: source})
	}

	home, realHome := "", ""
	if n.home != "" {
		home = filepath.Clean(n.home)
		if real, err := filepath.EvalSymlinks(home); err == nil {
			realHome = real
		}
	}
	isHome := func(dir string) bool {
		if home == "" {
			return false
		}
		if dir == home {
			return true
		}
		if realHome != "" {
			if real, err := filepath.EvalSymlinks(dir); err == nil && real == realHome {
				return true
			}
		}
		return false
	}

	cleanCwd := filepath.Clean(cwd)
	for dir, depth := cleanCwd, 0; depth < agySkillWalkDepth; depth++ {
		if isHome(dir) {
			break
		}
		rel, _ := filepath.Rel(cleanCwd, dir)
		addRoot(filepath.Join(dir, ".agents", "skills"), "project · "+filepath.ToSlash(rel))
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if home != "" {
		addRoot(filepath.Join(home, ".agents", "skills"), "user")
		addRoot(filepath.Join(home, ".gemini", "config", "skills"), "user")
	}
	if os.Getenv("ANTIGRAVITY_CLI_CONFIG_DIR") != "" || home != "" {
		if dataRoot := n.agyDataRoot(); dataRoot != "" {
			addRoot(filepath.Join(dataRoot, "builtin", "skills"), "builtin")
		}
	}
	return roots
}

func dedupeSkillsByName(entries []assistantskills.Entry) []assistantskills.Entry {
	out := make([]assistantskills.Entry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}
