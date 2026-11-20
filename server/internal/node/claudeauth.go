package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// claude.auth_status (docs/assistant/onboarding.md 4.3): whether Claude Code is signed in, as the launcher runs it, without a
// model request. `claude auth status --json` answers from what Claude Code finds where it runs: the credentials in its
// config directory, and a key or gateway token in its environment or --settings. So it runs exactly as a start does:
// the launcher's command in the login shell (launcherCommand: plain claude with the machine's claude_start_dir, a
// ~/.zshrc function, `personad launch <name>` for a managed one) with the --settings a start gives it (claudeSettings:
// a managed launcher's gateway over the user's global settings env). Run elsewhere it would say signed in for a key
// the launcher does not use, or signed out for a login it does.

// claudeAuthMinVersion: `claude auth login / status / logout` came with Claude Code 2.1.41. An older claude would
// reject the arguments (or, without --json, take them for a prompt), so it is not asked.
var claudeAuthMinVersion = [3]int{2, 1, 41}

// claudeAuthTimeout bounds one status run (an interactive zsh, then Claude Code reading its credentials, which may
// refresh a token).
var claudeAuthTimeout = 20 * time.Second

// claudeAuthStatus answers claude.auth_status for launcher name ("" is plain claude). Anything it cannot read is
// unknown with a reason, never an error: the readiness takes unknown as "not known", not as a problem.
func (n *Node) claudeAuthStatus(ctx context.Context, name string) (proto.ClaudeAuthStatus, error) {
	if name == "" {
		name = "claude"
	}
	return n.claudeAuthStatusCommand(ctx, name, n.launcherCommand(name))
}

// claudeAuthStatusProfile checks the exact profile a new conversation selected, not the machine's default login.
func (n *Node) claudeAuthStatusProfile(ctx context.Context, name, selected string) (proto.ClaudeAuthStatus, error) {
	return n.claudeAuthStatusCommand(ctx, name, n.launcherCommandIn(name, n.assistantConfigDir(name, selected), scriptPath))
}

func (n *Node) claudeAuthStatusCommand(ctx context.Context, name, command string) (proto.ClaudeAuthStatus, error) {
	if name == "" {
		name = "claude"
	}
	if err := n.runnableLauncher(ctx, name, false); err != nil {
		return proto.ClaudeAuthStatus{}, err // a word that is no launcher here would run as a command
	}
	unknown := func(reason string) proto.ClaudeAuthStatus {
		return proto.ClaudeAuthStatus{State: proto.ClaudeAuthUnknown, Reason: reason}
	}
	if _, managed := n.managedLauncher(name); !managed && name != "claude" {
		if n.runtimes.launcherRuntime(name) != nil {
			return unknown(proto.ClaudeAuthNotClaude), nil
		}
		if d := n.launcherProbeCached(ctx, name); d.Agent != "" && d.Agent != "claude" {
			return unknown(proto.ClaudeAuthNotClaude), nil // Codex, a compatible CLI: they have no `claude auth`
		}
	}
	// The login shell's claude, as the self-check reads it (cached). An older known CLI is not asked because it
	// would reject `claude auth`.
	version := n.claudeV.get(ctx)
	if version != "" && !versionAtLeast(version, claudeAuthMinVersion) {
		st := unknown(proto.ClaudeAuthOld)
		st.Version = version
		return st, nil
	}
	dir, err := os.MkdirTemp("", "persona-auth-")
	if err != nil {
		return proto.ClaudeAuthStatus{}, err
	}
	defer os.RemoveAll(dir)
	ours := filepath.Join(dir, "auth-settings.json")
	if err := os.WriteFile(ours, []byte(`{"disableAllHooks":true}`), 0o600); err != nil {
		return proto.ClaudeAuthStatus{}, err
	}
	// --settings goes before the subcommand: a launcher appends its own arguments after "$@" no more than before it,
	// and `claude auth status` itself takes no --settings.
	cctx, cancel := context.WithTimeout(ctx, claudeAuthTimeout)
	defer cancel()
	cmd := loginShell(cctx, "cd "+scriptPath(dir)+" && "+command+
		scriptSettingsFlag(n.claudeSettings(ctx, name, ours, dir))+` auth status --json`+" </dev/null")
	killGroupOnCancel(cmd)
	out, _ := cmd.CombinedOutput() // not signed in exits 1, with the same JSON
	if cctx.Err() != nil {
		st := unknown(proto.ClaudeAuthTimeout)
		st.Version = version
		return st, nil
	}
	st, ok := parseClaudeAuthStatus(string(out))
	if !ok {
		st = unknown(proto.ClaudeAuthUnreadable)
	}
	st.Version = version
	return st, nil
}

// assistantNewProfileAuth refuses a new automatically selected private profile only when Claude Code explicitly says
// it is signed out. Explicit or launcher-owned profiles, managed providers and resumes keep their existing behavior.
// Unknown status (old Claude Code, an unreadable answer or a timeout) fails open; the turn retains its own error handling.
func (n *Node) assistantNewProfileAuth(ctx context.Context, launcher, asked, selected string) error {
	if asked != "" || n.sessionScope != nil {
		return nil
	}
	if _, managed := n.managedLauncher(launcher); managed {
		return nil
	}
	if n.assistantLauncherProfile(ctx, launcher) != "" ||
		!sameAssistantProfile(selected, n.assistantConfigDir(launcher, "")) {
		return nil
	}
	st, err := n.claudeAuthStatusProfile(ctx, launcher, selected)
	if err != nil || st.State != proto.ClaudeNotLoggedIn {
		return nil
	}
	dir := n.assistantConfigDir(launcher, selected)
	message := "the new conversation's dedicated Claude Code profile is not signed in; on this computer run " +
		"CLAUDE_CONFIG_DIR=" + shellQuote(dir) + " " + launcher +
		` and sign in, or start a new conversation with config_dir "legacy" to reuse an existing login`
	data, _ := json.Marshal(map[string]any{"delivered": "none", "retryable": false, "launcher": launcher})
	return &rpc.Error{Code: proto.TurnErrorNotLoggedIn, Message: message, Data: data}
}

// parseClaudeAuthStatus reads `claude auth status --json`'s object out of what the shell printed (an interactive zsh
// may print banners and escape codes first): the last JSON object with a boolean loggedIn. Only the fields
// ClaudeAuthStatus carries are read; the account's email and organization stay here.
func parseClaudeAuthStatus(out string) (proto.ClaudeAuthStatus, bool) {
	out = terminalEscapes.ReplaceAllString(out, "")
	var found *proto.ClaudeAuthStatus
	for i := strings.IndexByte(out, '{'); i >= 0; {
		var r struct {
			LoggedIn     *bool  `json:"loggedIn"`
			AuthMethod   string `json:"authMethod"`
			APIProvider  string `json:"apiProvider"`
			APIKeySource string `json:"apiKeySource"`
		}
		dec := json.NewDecoder(strings.NewReader(out[i:]))
		if dec.Decode(&r) == nil && r.LoggedIn != nil {
			st := proto.ClaudeAuthStatus{State: proto.ClaudeNotLoggedIn, AuthMethod: r.AuthMethod, APIProvider: r.APIProvider,
				KeySource: keySourceName(r.APIKeySource)}
			if *r.LoggedIn {
				st.State = proto.ClaudeLoggedIn
			}
			found = &st
			i += int(dec.InputOffset())
		} else {
			i++
		}
		next := strings.IndexByte(out[i:], '{')
		if next < 0 {
			break
		}
		i += next
	}
	if found == nil {
		return proto.ClaudeAuthStatus{}, false
	}
	return *found, true
}

// keySourceName keeps a key's source only when it is a name (ANTHROPIC_API_KEY, apiKeyHelper, "/login managed key"),
// never something that could be a key itself.
func keySourceName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 40 || tokenLike.MatchString(s) {
		return ""
	}
	return s
}
