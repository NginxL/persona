package node

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

// An assistant conversation reads and writes its own directory without a card (the main conversation's, a space's, a
// track's). Claude Code asks for every write in the default mode, even inside its working directory, and Persona gives
// it no rule for the directory (assistant_start.go claudeAssistantMCPAndToolsFor), so each Write went to the phone
// (ix_2d0efa1b033728ef). The directory is compared as it resolves on both sides: the node starts the conversation in
// ~/.persona/assistant, and ~/.persona may be a link to the state directory of an older install, while Claude Code
// resolves its working directory and names the files it writes by the resolved path (<that directory>/assistant/...).

// workspaceConfigNames are the entries at the top of the directory that change what the conversation's own agent may do
// (its settings and hooks, its MCP servers, the repository's history): writing them still asks.
var workspaceConfigNames = []string{".claude", ".codex", ".agents", ".git", ".mcp.json"}

// workspaceToolPath is the file or directory a file tool's call names: the file of Read, Edit, Write, MultiEdit and
// NotebookEdit, the directory Glob and Grep search. "" for any other tool, or a relative path.
func workspaceToolPath(name string, input json.RawMessage) string {
	var a struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
	}
	_ = json.Unmarshal(input, &a)
	var p string
	switch name {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		p = firstNonEmpty(a.FilePath, a.NotebookPath)
	case "Glob", "Grep":
		p = a.Path
	}
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	return p
}

// inWorkspace: path is inside dir, both as they resolve (realPath: the longest existing ancestor, so a file about to
// be written compares too), and not one of the directory's own configuration (workspaceConfigNames). A link inside
// dir that leads out of it is out of it. A path in another space than dir's (identity.SpaceAt) is not dir's either.
func inWorkspace(path, dir string) bool {
	if path == "" || dir == "" || !filepath.IsAbs(path) || !filepath.IsAbs(dir) {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	rel, err := filepath.Rel(realPath(dir), realPath(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); slices.Contains(workspaceConfigNames, first) {
		return false
	}
	return identity.SpaceAt(path) == identity.SpaceAt(dir)
}

// ownWorkspaceCall: a PermissionRequest of assistant conversation in.SessionID for a file in the directory it started
// in (assistantOriginCwd: never where the model's last `cd` left the hook), which it may make without a card. Not in
// plan mode (it writes nothing), nor for a guest's conversation or the Inbox's (what strangers sent keeps its
// confirmations).
func (n *Node) ownWorkspaceCall(in hookInput) bool {
	if in.PermissionMode == "plan" {
		return false
	}
	path := workspaceToolPath(in.ToolName, in.ToolInput)
	if path == "" || n.guestOf(in.SessionID) != nil {
		return false
	}
	origin, cwd := n.assistantOriginCwd(in.SessionID)
	if cwd == "" { // no process here (an external start, a round's fork): where its record says it started
		cwd = n.assistantHome(in.SessionID, "")
	}
	return origin != originInbox && inWorkspace(path, cwd)
}
