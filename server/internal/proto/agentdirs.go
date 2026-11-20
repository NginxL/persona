package proto

import (
	"path/filepath"
	"strings"
)

// Agent folder rules: per computer, two parallel lists of folder patterns with one syntax and one matcher
// (DirPatternMatchedBy). Blocked Claude config folders (claudedirs.go) keep a CLAUDE_CONFIG_DIR out of collection:
// the node never scans it. Agent folder rules (HiddenAgentDir) act on the sessions whose working folder matches, for
// every runtime: a Block rule keeps them out of collection (the node lists nothing and sends nothing of them), a Hide
// rule leaves collection alone and only takes them out of Agents and the assistant's view
// (docs/protocol/protocol.md "Agent directory visibility").

// ValidDirPattern: a pattern filepath.Match understands, rooted at /, ~/ (that computer's home), or starting with *
// (a folder name or name glob anywhere in the path).
func ValidDirPattern(p string) bool {
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "*") {
		return false
	}
	_, err := filepath.Match(p, "")
	return err == nil
}

// CleanDirPattern trims spaces and trailing slashes; false when nothing is left or the pattern would cover
// everything (home, the root, or above home, however it is spelled, or a glob every folder matches: "*", "**", or
// "/*", which matches the top folder of every path).
func CleanDirPattern(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if blocksEverything(p) || strings.Trim(strings.TrimPrefix(filepath.Clean(p), "/"), "*") == "" {
		return "", false
	}
	return strings.TrimRight(p, "/"), true
}

// DirPatternMatchedBy is the first pattern ("" when none) that matches dir or a folder above it, as the user wrote
// it. dir is "~/…" or absolute. A pattern is a path ("~/work"), a filepath.Match glob ("~/.automation/*") or a
// folder name / name glob ("*workspaces*").
func DirPatternMatchedBy(dir, home string, patterns []string) string {
	if dir == "" || len(patterns) == 0 {
		return ""
	}
	d := filepath.Clean(ExpandHome(dir, home))
	for _, raw := range patterns {
		p := filepath.Clean(ExpandHome(raw, home))
		for c := d; ; {
			if ok, _ := filepath.Match(p, c); ok {
				return raw
			}
			if ok, _ := filepath.Match(p, filepath.Base(c)); ok {
				return raw
			}
			parent := filepath.Dir(c)
			if parent == c || parent == "." || parent == "/" || parent == "~" {
				break
			}
			c = parent
		}
	}
	return ""
}

// DirPatternMatchedOn is DirPatternMatchedBy on a machine whose home may be reached through a link (real, the home
// with its links followed; "" when it is none): a dir or pattern written under either name of the home is the same
// folder. Runtimes record a session's working folder either way (Codex resolves links, a shell's $PWD keeps them).
func DirPatternMatchedOn(dir, home, real string, patterns []string) string {
	d := HomeNamed(dir, home, real)
	for _, p := range patterns {
		if DirPatternMatchedBy(d, home, []string{HomeNamed(p, home, real)}) != "" {
			return p
		}
	}
	return ""
}

// HiddenAgentDir is one Agent folder rule of a computer: its sessions are blocked (Action AgentDirBlock: not collected,
// nothing about them reaches the hub) or hidden (AgentDirHide: collected, left out of the views). A rule switched off
// stays listed and does nothing. The list keeps the name it had when hiding was its only action (#8578, #10053).
type HiddenAgentDir struct {
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
	// Action is AgentDirBlock or AgentDirHide. A rule saved without one (before blocking, or by an older client) is
	// AgentDirHide: what it did then. A new rule is AgentDirBlock unless Hide is chosen (the apps, persona_settings).
	Action string `json:"action,omitempty"`
}

// The actions of an Agent folder rule.
const (
	AgentDirBlock = "block"
	AgentDirHide  = "hide"
)

// Blocks: the rule blocks its sessions (switched on, action block).
func (r HiddenAgentDir) Blocks() bool { return r.Enabled && r.Action == AgentDirBlock }

// CleanAgentDirAction is action as stored: "" is AgentDirHide; false when it is neither.
func CleanAgentDirAction(action string) (string, bool) {
	switch action {
	case "", AgentDirHide:
		return AgentDirHide, true
	case AgentDirBlock:
		return AgentDirBlock, true
	}
	return "", false
}

// HiddenAgentDirs is what GET / PUT /api/v1/machines/{mid}/agent-dirs/hidden answer: the computer's rules, in the
// order the user added them, and its home for showing "~" (empty before its node first said).
type HiddenAgentDirs struct {
	Rules []HiddenAgentDir `json:"rules"`
	Home  string           `json:"home,omitempty"`
}

// MaxHiddenAgentDirs bounds one computer's rules.
const MaxHiddenAgentDirs = 100

// BlockedAgentDirPatterns are the patterns of the rules that block (Blocks): what the node is told to keep out
// (node.configure agent_dirs_blocked).
func BlockedAgentDirPatterns(rules []HiddenAgentDir) []string {
	out := []string{}
	for _, r := range rules {
		if r.Blocks() {
			out = append(out, r.Pattern)
		}
	}
	return out
}

// HiddenAgentDirPatterns are the patterns of the rules switched on, blocking ones included: the manager leaves all of
// their sessions out of the views, so a node that does not take agent_dirs_blocked yet only hides what it should block.
func HiddenAgentDirPatterns(rules []HiddenAgentDir) []string {
	var out []string
	for _, r := range rules {
		if r.Enabled {
			out = append(out, r.Pattern)
		}
	}
	return out
}
