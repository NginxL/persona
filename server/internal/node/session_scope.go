package node

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
)

// PERSONA_NODE_SESSION_DIR opts a second resident or stdio node into discovering
// only its assistant's sessions. Each agent profile must be inside its own
// PERSONA_HOME; the user's terminal hosts and global hooks stay with the primary node.
const nodeSessionDirEnv = "PERSONA_NODE_SESSION_DIR"

type nodeSessionScope struct {
	dir    string
	claude string
	codex  string
}

func readNodeSessionScope(home, root string) (*nodeSessionScope, error) {
	dir := os.Getenv(nodeSessionDirEnv)
	if dir == "" {
		return nil, nil
	}
	if !filepath.IsAbs(os.Getenv("PERSONA_HOME")) || !filepath.IsAbs(root) || sameScopePath(root, home) || sameScopePath(root, filepath.Join(home, ".persona")) || sameScopePath(root, filepath.Join(home, compat.StateDir)) {
		return nil, fmt.Errorf("%s requires an independent absolute PERSONA_HOME", nodeSessionDirEnv)
	}
	s := &nodeSessionScope{dir: dir, claude: os.Getenv("CLAUDE_CONFIG_DIR"), codex: os.Getenv("CODEX_HOME")}
	for _, item := range []struct{ name, path string }{{nodeSessionDirEnv, s.dir}, {"CLAUDE_CONFIG_DIR", s.claude}, {"CODEX_HOME", s.codex}} {
		if !filepath.IsAbs(item.path) || !sessionScopeWithin(root, item.path) || sameScopePath(root, item.path) {
			return nil, fmt.Errorf("%s must be an absolute directory inside the independent PERSONA_HOME", item.name)
		}
	}
	if sessionScopeWithin(s.dir, s.claude) || sessionScopeWithin(s.dir, s.codex) || sessionScopeWithin(s.claude, s.dir) || sessionScopeWithin(s.codex, s.dir) || sessionScopeWithin(s.claude, s.codex) || sessionScopeWithin(s.codex, s.claude) {
		return nil, fmt.Errorf("assistant, Claude and Codex directories must be separate")
	}
	return s, nil
}

// Resolve existing ancestors too: a not-yet-created profile beneath a symlink
// must not escape the independent root.
func sessionScopePath(p string) string {
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(sessionScopePath(parent), filepath.Base(p))
}

func sameScopePath(a, b string) bool { return sessionScopePath(a) == sessionScopePath(b) }

func sessionScopeWithin(root, p string) bool {
	if p == "" {
		return false
	}
	rel, err := filepath.Rel(sessionScopePath(root), sessionScopePath(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (n *Node) sessionScopeAllows(cwd string) bool {
	return n.sessionScope == nil || sessionScopeWithin(n.sessionScope.dir, cwd)
}

// assistantFallbackDir is where an assistant conversation runs when it has no folder of its own: a scoped node's
// assistant directory (PERSONA_NODE_SESSION_DIR, the only one it lists), else the data directory's assistant directory.
func (n *Node) assistantFallbackDir() string {
	if n.sessionScope != nil && n.sessionScope.dir != "" {
		return n.sessionScope.dir
	}
	return identity.AssistantDir(n.home)
}

// unlistedAssistantDir: dir is the built-in assistant's directory in a scoped node's state root, which that node never
// lists. Nothing starts a conversation there on purpose: it is the folder storedCwd fell back to before it knew the
// scope, and a conversation resumed there again dropped out of every list, so the manager ended each channel message
// to it as gone after channel.MissingAfter.
func (n *Node) unlistedAssistantDir(dir string) bool {
	return n.sessionScope != nil && dir != "" && sameScopePath(dir, identity.AssistantDir(n.home)) && !n.sessionScopeAllows(dir)
}

func (n *Node) scopedTranscript(path string) bool {
	if n.sessionScope == nil {
		return true
	}
	return sessionScopeWithin(n.sessionScope.claude, path) || sessionScopeWithin(n.sessionScope.codex, path)
}

// Scope checks need only the transcript's initial cwd, not its reply text or
// activity tail. Match the metadata scanner's header bound without its tail scan.
const scopeTranscriptHeaderLimit = 256 * 1024

func scopeTranscriptCwd(reader io.Reader) string {
	r := bufio.NewReader(io.LimitReader(reader, scopeTranscriptHeaderLimit))
	for {
		line, err := r.ReadBytes('\n')
		var header struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &header) == nil {
			if header.Cwd != "" {
				return header.Cwd
			}
			if (header.Type == "session_meta" || header.Type == "turn_context") && header.Payload.Cwd != "" {
				return header.Payload.Cwd
			}
		}
		if err != nil {
			return "" // missing, unreadable or incomplete metadata fails closed.
		}
	}
}

func (n *Node) scopedTranscriptAllows(path string) bool {
	if n.sessionScope == nil {
		return true
	}
	if !n.scopedTranscript(path) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return n.sessionScopeAllows(scopeTranscriptCwd(f))
}

func (n *Node) offlineCacheKey() string {
	if n.sessionScope != nil {
		return n.home + "\x00" + n.dir + "\x00" + n.sessionScope.dir
	}
	return n.home
}
