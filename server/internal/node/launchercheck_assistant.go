package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The readiness probe's check ("Try it", docs/assistant/onboarding.md "Main-chat readiness") asks launchers.check with
// assistant_turn: a Claude Code launcher then runs the one-word check as the assistant runs a turn of the conversation
// (assistant_start.go), so the check meets what the chat meets. The plain check said the launcher's default model
// answered in the login shell's profile, with no tools, while the chat ran another model in its own profile with
// Persona's tools and tool search, and failed (#10365). It keeps what makes it a check: the one-word prompt, one turn, nothing saved, no
// hooks, an empty directory. Settings › Models' "Test" and the launcher health checks never ask for it.

// launcherCheckOpts is what launchers.check asks for besides the launcher.
type launcherCheckOpts struct {
	model string // --model; "" the launcher's default
	// assistantTurn: shaped as an assistant turn of conversation; effort and configDir count only with it.
	assistantTurn bool
	effort        string
	conversation  string // the assistant conversation whose profile the check runs in
	configDir     string // the profile when the conversation has none recorded: the main chat's for new conversations
}

// assistantCheckToolSearch is the tool search an assistant turn on launcher/model starts with: the start's own
// decision (assistantToolSearch), with the endpoint read as the start reads it from the check's directory and profile
// (selected, a profile name as assistantProfileSelection gives it). model is the one asked for, before claudeModelArg:
// a refusal is remembered by the start's.
var assistantCheckToolSearch = func(n *Node, launcher, model, dir, selected string) toolSearchStart {
	return n.assistantToolSearch(launcher, model, n.assistantCompactionPolicy(launcher, dir, "", modelExecution{Model: model, ConfigDir: selected}))
}

// assistantCheck is what an assistant-turn check of a Claude Code launcher adds to the plain one (the zero value: the
// plain check).
type assistantCheck struct {
	on         bool
	args       string   // after --model: the effort and Persona's MCP server with the chat's tool flags, quoted
	env        []string // the process's environment
	configDir  string   // CLAUDE_CONFIG_DIR, "" for Claude Code's default
	configPath string   // the profile's directory, for the managed settings merge
	// toolSearchOff: the start would write ENABLE_TOOL_SEARCH=false into its settings (toolSearchStart.forceOff), over
	// the provider's and the launcher's own value; withSettings does the same to the check's.
	toolSearchOff bool
	done          func()
}

// assistantCheckFor is the assistant-turn shape of a check of launcher with o's model in dir.
func (n *Node) assistantCheckFor(ctx context.Context, launcher string, o launcherCheckOpts, dir string) (assistantCheck, error) {
	selected := n.checkProfile(ctx, launcher, o.conversation, o.configDir)
	c := assistantCheck{on: true, configDir: n.assistantConfigDir(launcher, selected), configPath: n.assistantConfigPath(selected)}
	if slices.Contains(effortLevels, o.effort) {
		c.args = modeFlags("", o.effort) // an effort the chat could not run is left to the model's default
	}
	// Its personad mcp names the check's directory, no conversation of the assistant's: the node answers its tool
	// list as an Agent's, which loads every Persona tool, while Claude Code still defers its own built-ins.
	flags := personaMCPOnly(n.claudeAssistantMCPAndToolsFor(filepath.Base(dir), dir, !n.hubSupportsPersonaContracts(), ""))
	flags, cleanup, err := assistantCLIMCPFlags(flags)
	if err != nil {
		return assistantCheck{}, err
	}
	c.done = cleanup
	for _, f := range flags {
		c.args += " " + shellQuote(f)
	}
	env := []string{"PERSONA_ASSISTANT=1"}
	toolSearch := assistantCheckToolSearch(n, launcher, o.model, dir, selected)
	if toolSearch.env != "" {
		env = append(env, toolSearchEnv+"="+toolSearch.env)
	}
	c.toolSearchOff = toolSearch.forceOff
	// As the start: a value the node inherited never reaches claude.
	c.env = n.claudeProcessEnv(identity.Environment(withoutEnv(os.Environ(), []string{toolSearchEnv}), env...), c.configDir)
	return c, nil
}

// withSettings is the check's --settings (path, what claudeSettings gave) as the start's are: with tool search forced
// off, ENABLE_TOOL_SEARCH=false over the env the provider or the launcher merged in (assistantCompactionSettings does
// the same), else path. The copy goes in the check's directory (dir), which goes with it.
func (c assistantCheck) withSettings(path, dir, home string) (string, error) {
	if !c.toolSearchOff {
		return path, nil
	}
	s, err := readLauncherSettings(path, dir, home)
	if err != nil {
		return "", err
	}
	env, _ := s["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env[toolSearchEnv] = "false"
	s["env"] = env
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "check-settings-tools.json")
	return out, os.WriteFile(out, b, 0o600)
}

// checkProfile is the profile an assistant-turn check of launcher runs in, as assistantProfileSelection would choose it
// but only read: the conversation's recorded profile (its running process's), else the one asked for, else the
// launcher's own, else Persona's for the launcher. Nothing is created or saved.
func (n *Node) checkProfile(ctx context.Context, launcher, conversation, asked string) string {
	if n.sessionScope != nil {
		return n.sessionScope.claude
	}
	selected := ""
	if conversation != "" {
		if st, ok := n.state.get(conversation); ok {
			selected = st.ConfigDir
		}
		n.mu.Lock()
		p := n.procs[conversation]
		n.mu.Unlock()
		if selected == "" && p != nil {
			p.mu.Lock()
			selected = p.model.ConfigDir
			p.mu.Unlock()
		}
	}
	if selected == "" {
		selected = asked
	}
	if selected == "legacy" {
		selected = n.legacyAssistantProfile(ctx, launcher)
	}
	if selected == "" {
		selected = n.assistantLauncherProfile(ctx, launcher)
	}
	if selected == "" {
		root := n.dir
		if root == "" {
			root = identity.DataDir(n.home)
		}
		selected = filepath.Join(root, "profiles", launcher)
	}
	if selected == "default" {
		return selected
	}
	return filepath.Clean(proto.ExpandHome(selected, n.home))
}

// personaMCPOnly narrows a conversation's MCP and tool flags (claudeAssistantMCPAndToolsFor) to Persona's own server,
// with --strict-mcp-config: the extensions' servers may be slow to start or act as they start, which a check must not
// wait for or set off. The tool flags stay the chat's.
func personaMCPOnly(flags []string) []string {
	out := slices.Clone(flags)
	if i := slices.Index(out, "--mcp-config"); i >= 0 && i+1 < len(out) {
		var cfg struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(out[i+1]), &cfg) == nil {
			for name := range cfg.Servers {
				if !compat.IsMCPServer(name) {
					delete(cfg.Servers, name)
				}
			}
			if b, err := json.Marshal(map[string]any{"mcpServers": cfg.Servers}); err == nil {
				out[i+1] = string(b)
			}
		}
	}
	if !slices.Contains(out, "--strict-mcp-config") {
		out = append(out, "--strict-mcp-config")
	}
	return out
}

// profile is the profile claudeSettings merges a managed launcher's settings for: none for the plain check.
func (c assistantCheck) profile() []string {
	if !c.on {
		return nil
	}
	return []string{c.configPath}
}

// inDirScript is launcherInDirScript with the launcher run in the check's profile, as the assistant runs it
// (launcherCommandIn).
func (c assistantCheck) inDirScript(n *Node) func(name, dir, args string) string {
	return func(name, dir, args string) string {
		return "cd " + scriptPath(dir) + " && " + n.launcherCommandIn(name, c.configDir, scriptPath) + args + " </dev/null"
	}
}

func (c assistantCheck) cleanup() {
	if c.done != nil {
		c.done()
	}
}
