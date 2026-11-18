package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// "default" records Claude's unset-directory behavior (~/.claude/projects but
// ~/.claude.json). An absolute ~/.claude is not equivalent: its global config
// is ~/.claude/.claude.json. "legacy" is an input only, resolved once and saved.
func (n *Node) assistantProfileSelection(ctx context.Context, id, launcher, asked string, resume bool) (string, error) {
	normalize := func(dir string) (string, error) {
		if dir == "legacy" {
			dir = n.legacyAssistantProfile(ctx, launcher)
		}
		if dir == "default" {
			return dir, nil
		}
		dir = proto.ExpandHome(dir, n.home)
		if !filepath.IsAbs(dir) {
			return "", errors.New("config_dir must be absolute, start with ~/, or be default or legacy")
		}
		return filepath.Clean(dir), nil
	}
	if n.sessionScope != nil {
		if asked == "legacy" {
			asked = n.legacyAssistantProfile(ctx, launcher) // the dedicated node's own existing profile
		}
		if asked != "" && asked != n.sessionScope.claude {
			return "", errors.New("config_dir must stay in the node's session scope")
		}
		return n.sessionScope.claude, nil
	}
	itself := resume && n.claudeItself(id, launcher) // the Agent SDK started it: no launcher's profile applies
	// The launcher's own profile when it sets one (a wrapper), normalized; "" when any profile goes.
	ownProfile := func() (string, error) {
		if _, managed := n.managedLauncher(launcher); managed || itself {
			return "", nil
		}
		if own := n.assistantLauncherProfile(ctx, launcher); own != "" {
			return normalize(own)
		}
		return "", nil
	}
	selected := ""
	if resume {
		st, _ := n.state.get(id)
		selected = st.ConfigDir
		n.mu.Lock()
		p := n.procs[id]
		n.mu.Unlock()
		running := false
		if p != nil {
			p.mu.Lock()
			if selected == "" {
				selected = p.model.ConfigDir
			}
			running = p.alive() && p.launcher == launcher && sameAssistantProfile(p.model.ConfigDir, selected)
			p.mu.Unlock()
		}
		// When one id has several copies, the newest is the conversation (transcriptPath).
		// A recorded profile holding no copy (a launcher switch) would make Claude answer
		// "No conversation found": resume where the newest copy is. One holding only an
		// older copy (a rollback continued the conversation elsewhere) would resume from
		// the old point and drop the later turns, so it moves too, unless:
		//   - a live process of the conversation runs in it: the next message goes to
		//     that process, and the record keeps naming the profile it writes to;
		//   - the launcher sets its own profile and would refuse the newest copy's: an
		//     older copy in that profile resumes, whichever profile was recorded, as a
		//     conflict would refuse every message. With no copy there, nothing this
		//     launcher can open holds the conversation and the conflict stays.
		// A profile whose copy is the newest, its projects/ shared through a symlink
		// included, keeps the conversation. Copies with the same modification time are
		// a tie the recorded profile keeps.
		if path := n.transcriptPath(id); path != "" {
			project := filepath.Dir(path)
			projects := filepath.Dir(project)
			mine := filepath.Join(n.assistantConfigPath(selected), "projects", filepath.Base(project), filepath.Base(path))
			copied := selected != "" && fileExists(mine)
			if filepath.Base(projects) == "projects" && (!copied || !running && !holdsNewestCopy(mine, path)) {
				newest := filepath.Dir(projects)
				// The pre-change resolution keeps the conversation when its
				// projects/ holds the transcript by real path. The path found
				// need not name it: ~/.claude/projects (or ~/.claude) may be
				// a symlink to a wrapper's profile, which is the one in use.
				legacy := n.legacyAssistantProfile(ctx, launcher)
				if sameScopePath(projects, filepath.Join(n.assistantConfigPath(legacy), "projects")) {
					newest = legacy
				} else if sameScopePath(newest, filepath.Join(n.home, ".claude")) {
					// Preserve the old unset-profile global config.
					newest = "default"
				}
				own, err := ownProfile()
				if err != nil {
					return "", err
				}
				selected = newest
				if to, err := normalize(newest); own != "" && (err != nil || !sameAssistantProfile(to, own)) &&
					fileExists(filepath.Join(n.assistantConfigPath(own), "projects", filepath.Base(project), filepath.Base(path))) {
					// The wrapper's own profile holds an older copy: the only one it can resume. With no copy
					// there, every copy is where the wrapper cannot go, and the conflict below says so (#4310).
					selected = own
				}
			}
		}
		if selected == "" {
			selected = n.legacyAssistantProfile(ctx, launcher)
		}
	} else if asked != "" {
		selected = asked
	} else {
		selected = n.assistantLauncherProfile(ctx, launcher)
		if selected == "" {
			root := n.dir
			if root == "" {
				root = identity.DataDir(n.home)
			}
			selected = filepath.Join(root, "profiles", launcher)
		}
	}
	selected, err := normalize(selected)
	if err != nil {
		return "", err
	}
	if resume && asked != "" {
		explicit, err := normalize(asked)
		if err != nil {
			return "", err
		}
		if !sameAssistantProfile(selected, explicit) {
			return "", errors.New("config_dir cannot move an existing conversation; start a new conversation instead")
		}
	}
	// A wrapper can override a prefix assignment. Refuse a conflicting wrapper
	// rather than claim to resume one profile while actually opening another.
	own, err := ownProfile()
	if err != nil {
		return "", err
	}
	if own != "" && !sameAssistantProfile(selected, own) {
		return "", errors.New("config_dir conflicts with the launcher's own profile; restore that profile or use a compatible launcher")
	}
	return selected, nil
}

// An assistant conversation an older node started on the Agent SDK with the plain claude launcher ran Claude Code's
// executable itself, never a `claude` alias or shell function, in the profile the node chose for it (Persona's own,
// <data>/profiles/claude, unless one was asked for). Through such an alias or function that sets a profile of its own,
// claude would open that profile instead and not find the conversation, or the profile check would refuse it on every
// message. So the conversation keeps running claude as the SDK did: the executable past the alias (`command claude`,
// assistantCLICommand) in the profile it recorded, resuming it there, with no launcher's own profile applied. A
// conversation the CLI started keeps the launcher's rules (#4310).
//
// The hub never kept which runtime ran a conversation; its transcript says so. Claude Code writes the entrypoint it was
// started through on its lines: "sdk-ts" under the Agent SDK, "sdk-cli" for claude -p. The first one is the start's,
// and stays the SDK's once the CLI goes on with the conversation.

// claudeItself says whether conversation id, resumed on launcher, runs Claude Code's executable itself (above).
func (n *Node) claudeItself(id, launcher string) bool {
	if id == "" || launcher != "claude" || n.sessionScope != nil {
		return false
	}
	if _, managed := n.managedLauncher(launcher); managed {
		return false
	}
	return startedOnAgentSDK(n.transcriptPath(id))
}

// firstEntrypointReach bounds how far into a transcript startedOnAgentSDK looks for its first entrypoint: a first
// message with images or a long snapshot runs to a few hundred KB.
const firstEntrypointReach = 8 << 20

// startedOnAgentSDK says whether the first entrypoint on the transcript at path is the Agent SDK's.
func startedOnAgentSDK(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	key := []byte(`"entrypoint":"`)
	var head []byte
	buf := make([]byte, 64<<10)
	at := -1 // where the first entrypoint's value starts, once read
	for len(head) < firstEntrypointReach {
		got, err := f.Read(buf)
		from := max(0, len(head)-len(key)+1) // a key cut by the last read is whole now
		head = append(head, buf[:got]...)
		if at < 0 {
			if i := bytes.Index(head[from:], key); i >= 0 {
				at = from + i + len(key)
			}
		}
		if at >= 0 {
			if j := bytes.IndexByte(head[at:], '"'); j >= 0 {
				return string(head[at:at+j]) == "sdk-ts"
			}
		}
		if err != nil {
			return false
		}
	}
	return false
}

// holdsNewestCopy reports whether copy exists and is no older than newest: the same file reached through a symlink, or
// an equally recent copy. Comparing times rather than paths also holds when newest came from a stale remembered path.
func holdsNewestCopy(copy, newest string) bool {
	c, err := os.Stat(copy)
	if err != nil {
		return false
	}
	m, err := os.Stat(newest)
	return err != nil || !m.ModTime().After(c.ModTime())
}

func sameAssistantProfile(a, b string) bool {
	if a == "default" || b == "default" {
		return a == b
	}
	return sameScopePath(a, b)
}

func (n *Node) assistantLauncherProfile(ctx context.Context, launcher string) string {
	var env map[string]string
	if l, managed := n.managedLauncher(launcher); managed {
		env = providerEnv(l, "", "", true)
	} else if e, ok := n.launcherProfileEnv(ctx, launcher); ok { // not a login shell per message (#8947)
		env = e
	}
	if dir, set := env["CLAUDE_CONFIG_DIR"]; set {
		if launcher == "claude" {
			if d := n.claudeStartDir.Load(); d != nil && proto.ExpandHome(*d, n.home) == dir {
				return "" // the probe's machine assignment is not a launcher-owned profile
			}
		}
		if dir == "" {
			return "default"
		}
		return dir
	}
	return ""
}

// Capture the pre-change rules, only for a pre-existing unrecorded session (or
// an explicit new-session "legacy" request). Never guess the new private path.
func (n *Node) legacyAssistantProfile(ctx context.Context, launcher string) string {
	if own := n.assistantLauncherProfile(ctx, launcher); own != "" {
		return own
	}
	if launcher == "claude" {
		if d := n.claudeStartDir.Load(); d != nil && *d != "" {
			return proto.ExpandHome(*d, n.home)
		}
	}
	// Neither names one: the service's CLAUDE_CONFIG_DIR, or the one a login shell sets.
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := loginShell(ctx, `printf '\n__PERSONA_LEGACY_PROFILE__%s\n' "$CLAUDE_CONFIG_DIR"`)
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if value, ok := strings.CutPrefix(line, "__PERSONA_LEGACY_PROFILE__"); ok {
				dir = value
			}
		}
	}
	if dir == "" {
		return "default"
	}
	return proto.ExpandHome(dir, n.home)
}

func (n *Node) assistantConfigDir(launcher, selected string) string {
	if n.sessionScope != nil {
		return n.sessionScope.claude
	}
	if selected == "" {
		root := n.dir
		if root == "" {
			root = identity.DataDir(n.home)
		}
		selected = filepath.Join(root, "profiles", launcher)
	}
	if selected == "default" {
		return ""
	}
	return proto.ExpandHome(selected, n.home)
}

func (n *Node) assistantConfigPath(selected string) string {
	if selected == "default" {
		return filepath.Join(n.home, ".claude")
	}
	return proto.ExpandHome(selected, n.home)
}

func (n *Node) claudeProcessEnv(base []string, dir string) []string {
	if n.sessionScope != nil {
		dir = n.sessionScope.claude
	}
	if dir == "" {
		// "default" is Claude's unset directory. Claude takes an empty CLAUDE_CONFIG_DIR as a directory of its own (a
		// resume then finds no conversation), so the variable is removed, not set empty.
		out := make([]string, 0, len(base))
		for _, entry := range base {
			if !strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
				out = append(out, entry)
			}
		}
		return out
	}
	return identity.Environment(base, "CLAUDE_CONFIG_DIR="+dir)
}

// Terminal Agents keep launcherCommand unchanged; only this assistant start
// overrides a shell's default after startup, using its saved selection.
func (n *Node) launcherCommandIn(name, dir string, q func(string) string) string {
	if proto.IsExternalLauncher(name) {
		return "false" // external starts bypass shell command construction entirely
	}
	cmd := name
	if _, managed := n.managedLauncher(name); managed {
		cmd = q(n.bin) + " launch " + name
	}
	return commandInProfile(cmd, dir, q)
}

// commandInProfile is shell command cmd run in Claude profile dir ("" for "default").
func commandInProfile(cmd, dir string, q func(string) string) string {
	if dir == "" {
		// "default": unset after the shell's rc files, not an empty value (claudeProcessEnv)
		return "unset CLAUDE_CONFIG_DIR; " + cmd
	}
	return "CLAUDE_CONFIG_DIR=" + q(dir) + " " + cmd
}
