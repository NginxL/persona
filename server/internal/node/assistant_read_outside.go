package node

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

// An assistant conversation reads outside its own directory without a card (decision rights, 2026-11-24): Read, Glob
// and Grep change nothing, and a card for each look at a file elsewhere on the computer (ix_a03337f7) asked the user
// what the assistant may simply do. What stays behind a card: the health space and every other space (a track's, the
// health space's, another manager's), Persona's state directory as a whole (the hub's credentials, other spaces'
// conversations), and credentials (keys, tokens, .env files, launcher settings, the agents' own settings).

// readOnlyTools are the file tools that only read.
var readOnlyTools = []string{"Read", "Glob", "Grep"}

// credentialHomeDirs are directories under the home directory that hold credentials: nothing in them is read without
// a card.
var credentialHomeDirs = []string{".ssh", ".aws", ".gnupg", ".docker", ".kube", filepath.Join(".config", "gh"), filepath.Join(".config", "gcloud")}

// credentialHomeFiles are files under the home directory that hold credentials or decide what an agent may do.
var credentialHomeFiles = []string{".netrc", ".npmrc", ".pypirc", ".git-credentials",
	filepath.Join(".claude", "settings.json"), filepath.Join(".claude", "settings.local.json"), filepath.Join(".claude", ".credentials.json"),
	filepath.Join(".claude.json"), filepath.Join(".codex", "auth.json"), filepath.Join(".codex", "config.toml")}

// credentialName: a file whose name says it holds a secret, wherever it is.
func credentialName(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == ".env", strings.HasPrefix(n, ".env."), strings.HasSuffix(n, ".env"),
		strings.HasSuffix(n, ".cred"), strings.HasSuffix(n, ".creds"), strings.HasSuffix(n, ".pem"), strings.HasSuffix(n, ".key"),
		strings.HasSuffix(n, ".p12"), strings.HasSuffix(n, ".pfx"), strings.HasSuffix(n, ".keychain"), strings.HasSuffix(n, ".keychain-db"),
		strings.HasPrefix(n, "id_rsa"), strings.HasPrefix(n, "id_ed25519"), strings.HasPrefix(n, "id_ecdsa"),
		n == "hub.json", n == "credentials.json", n == "auth.json", n == "secrets.json", n == "admin.cred":
		return true
	}
	return false
}

// readGuarded: path (resolved) is one a read outside the workspace never takes without a card: a space other than
// own's (own: the conversation's directory's space, "" for the main one), Persona's state directory, a credential
// directory or file, a launcher's settings, or a file named like a credential.
func readGuarded(path, own, home string, stateDirs []string) bool {
	if space := identity.SpaceAt(path); space != "" && space != own || identity.InHealthSpace(path) && own != identity.SpaceHealth {
		return true
	}
	for _, d := range stateDirs {
		if d != "" && within(path, realPath(filepath.Clean(d))) {
			return true
		}
	}
	if home != "" {
		h := realPath(filepath.Clean(home))
		for _, d := range credentialHomeDirs {
			if within(path, filepath.Join(h, d)) {
				return true
			}
		}
		for _, f := range credentialHomeFiles {
			if path == filepath.Join(h, f) {
				return true
			}
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.Contains(strings.ToLower(part), "launcher-settings") {
			return true
		}
	}
	return credentialName(filepath.Base(path))
}

// searchCovers: a Glob or Grep of dir reaches a guarded place: dir holds Persona's state directory, the home
// directory's credentials or another space (searching the home directory reads them too).
func searchCovers(dir, home string, stateDirs []string) bool {
	var roots []string
	for _, d := range stateDirs {
		if d != "" {
			roots = append(roots, realPath(filepath.Clean(d)))
		}
	}
	if home != "" {
		h := realPath(filepath.Clean(home))
		roots = append(roots, filepath.Join(h, ".claude"), filepath.Join(h, ".codex"))
		for _, d := range credentialHomeDirs {
			roots = append(roots, filepath.Join(h, d))
		}
		for _, f := range credentialHomeFiles {
			roots = append(roots, filepath.Join(h, f))
		}
	}
	return slices.ContainsFunc(roots, func(r string) bool { return within(r, dir) })
}

// grepScanLimit bounds the look for credential files under a directory Grep would read: a tree bigger than this is
// not searched without a card.
const grepScanLimit = 5000

// grepReadsCredentials: Grep over dir would read a file named like a credential (a .env in a project) or a space's
// directory, or the tree is too big to tell (more than grepScanLimit entries).
func grepReadsCredentials(dir string) bool {
	n, found := 0, false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > grepScanLimit || credentialName(d.Name()) || d.IsDir() && p != dir && identity.SpaceOf(p) != "" {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// readOutsideAllowed: tool (Read, Glob or Grep) on path, by a conversation whose directory is cwd, may go without a
// card: not guarded (readGuarded), and a search does not reach a guarded place (searchCovers, and for Grep the files
// it would read).
func readOutsideAllowed(tool, path, cwd, home string, stateDirs []string) bool {
	if !slices.Contains(readOnlyTools, tool) || path == "" || !filepath.IsAbs(path) || cwd == "" {
		return false
	}
	p := realPath(filepath.Clean(path))
	if readGuarded(p, identity.SpaceAt(cwd), home, stateDirs) {
		return false
	}
	if tool == "Read" {
		return true
	}
	if searchCovers(p, home, stateDirs) {
		return false
	}
	return tool == "Glob" || !grepReadsCredentials(p)
}

// readOutsideCall: a PermissionRequest of assistant conversation in.SessionID to read outside its own directory that
// it may make without a card (readOutsideAllowed). Not for a guest's conversation or the Inbox's (what strangers sent
// keeps its confirmations).
func (n *Node) readOutsideCall(in hookInput) bool {
	path := workspaceToolPath(in.ToolName, in.ToolInput)
	if path == "" || n.guestOf(in.SessionID) != nil {
		return false
	}
	origin, cwd := n.assistantOriginCwd(in.SessionID)
	if cwd == "" {
		cwd = n.assistantHome(in.SessionID, "")
	}
	if origin == originInbox {
		return false
	}
	return readOutsideAllowed(in.ToolName, path, cwd, n.home, []string{n.dir, identity.DataDir(n.home)})
}
