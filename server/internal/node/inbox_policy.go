package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	originInbox = "inbox"
	// inboxLarkRefusal is deliberately actionable: the model has a supported way to finish its turn without sending.
	inboxLarkRefusal = "Inbox conversations cannot make outbound Lark calls directly; use inbox_suggest so the owner can confirm the action."
)

// assistantOrigin is the manager-recorded origin for a model call. Session state covers Claude from before process
// start and every resumed conversation; the process record covers Codex while its proc-* key is becoming a thread id.
func (n *Node) assistantOrigin(id string) string {
	if n.state != nil {
		if st, ok := n.state.get(id); ok && st.Origin != "" {
			return st.Origin
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	read := func(p *assistantProc) string {
		if p == nil {
			return ""
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.origin
	}
	if p := n.procs[id]; p != nil {
		return read(p)
	}
	if p := n.akeys[id]; p != nil {
		return read(p)
	}
	if p := n.spare; p != nil && p.id == id {
		return read(p)
	}
	for _, p := range n.akeys {
		if p != nil && (p.id == id || p.cx != nil && p.cx.key == id) {
			return read(p)
		}
	}
	return ""
}

// inboxReadRules lets an Inbox investigation read the owner's installed Skills and knowledge base. Claude Code checks
// both a requested symlink path and its resolved target, so a linked Skills root or direct child Skill needs both rules.
// Nested links keep the usual permission flow. A comma cannot be carried by the comma-joined --allowedTools value.
func (n *Node) inboxReadRules() []string {
	var skillRoots []string
	if n.home != "" {
		skillRoots = append(skillRoots, filepath.Join(n.home, ".claude", "skills"))
	}
	if n.dir != "" {
		skillRoots = append(skillRoots, filepath.Join(n.dir, "skills"))
	}
	roots := slices.Clone(skillRoots)
	if n.dir != "" {
		roots = append(roots, filepath.Join(n.dir, "data"))
	}
	var rules []string
	add := func(path string) {
		if path == "" || strings.Contains(path, ",") {
			return
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if path[0] != '/' {
			path = "/" + path // a Windows drive path
		}
		rule := "Read(/" + path + "/**)"
		if !slices.Contains(rules, rule) {
			rules = append(rules, rule)
		}
	}
	for _, root := range roots {
		if strings.Contains(root, ",") {
			continue
		}
		add(root)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			add(resolved)
		}
	}
	// Installed Skills are commonly one symlink per Skill. Resolve just this bounded level rather than walking a Skill's
	// contents: its files may contain unrelated links, which retain the normal approval boundary.
	for _, root := range skillRoots {
		if strings.Contains(root, ",") {
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			resolved, err := filepath.EvalSymlinks(filepath.Join(root, entry.Name()))
			if err != nil {
				continue
			}
			if info, err := os.Stat(resolved); err == nil && info.IsDir() {
				add(resolved)
			}
		}
	}
	return rules
}

// inboxLarkOutbound says whether a parsed command can make a Lark change or is too opaque to prove read-only. It runs
// before configured allows, bypass-safe bot send/reply, and phone confirmation: none of those is an Inbox confirmation.
func inboxLarkOutbound(cfg proto.AssistantLark, v larkVerdict, command string) bool {
	for _, c := range v.calls {
		if !larkRead(c) {
			return true
		}
	}
	if v.cliWrite || v.unknown {
		return true
	}
	if _, write := gateWrite(cfg.Gates, command); write {
		return true
	}
	// A command substitution or unparsed fragment associated with a gated call cannot be shown to be read-only.
	return (v.unsure || v.unparsed) && (len(v.calls) > 0 || larkCLIRe.MatchString(command))
}
