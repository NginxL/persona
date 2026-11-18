package node

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A fake claude that writes what it was started with (the environment the assistant sets, one argument a line, its
// --settings file) into the home directory, then answers RESULT_LINE (a claude -p JSON line).
const checkRecorder = `printf 'ENABLE_TOOL_SEARCH=%s\nPERSONA_ASSISTANT=%s\nCLAUDE_CONFIG_DIR=%s\n' "${ENABLE_TOOL_SEARCH-unset}" "$PERSONA_ASSISTANT" "${CLAUDE_CONFIG_DIR-unset}" > "$HOME/check-env"
: > "$HOME/check-args"
prev=""
for a in "$@"; do
  printf '%s\n' "$a" >> "$HOME/check-args"
  [ "$prev" = --settings ] && cp "$a" "$HOME/check-settings"
  prev=$a
done
printf '%s\n' "$RESULT_LINE"
`

const resultOK = `{"type":"result","subtype":"success","is_error":false,"result":"ok","modelUsage":{"opus":{}}}`

// checkNode is a node over the fake claude's home, with an extension MCP server whose command is on this machine.
func checkNode(t *testing.T, result string) (*Node, string) {
	t.Helper()
	fakeClaudeHome(t, checkRecorder)
	t.Setenv("RESULT_LINE", result)
	home := os.Getenv("HOME")
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), state: newState(), logger: log.New(io.Discard, "", 0)}
	n.larkCfg = proto.AssistantLark{MCPServers: []proto.AssistantMCPServer{{ID: "ext", Command: "sh"}}}
	return n, home
}

func checkRun(t *testing.T, n *Node, params map[string]any) proto.LauncherCheck {
	t.Helper()
	res, err := nodeCall(t, n, "launchers.check", params)
	if err != nil {
		t.Fatal(err)
	}
	var check proto.LauncherCheck
	if err := json.Unmarshal(res, &check); err != nil {
		t.Fatalf("%s: %v", res, err)
	}
	return check
}

// recorded is what the fake claude was started with: its environment's three values and its arguments.
func recorded(t *testing.T, home string) (map[string]string, []string) {
	t.Helper()
	env := map[string]string{}
	b, err := os.ReadFile(filepath.Join(home, "check-env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}
	b, err = os.ReadFile(filepath.Join(home, "check-args"))
	if err != nil {
		t.Fatal(err)
	}
	return env, strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// The readiness probe's check runs as the assistant runs the conversation's turn: in its recorded profile, with the
// assistant's environment and tool search, Persona's own MCP server alone (strict) with the chat's tool flags, the
// model and effort the chat runs; and still as a check: one word, one turn, nothing kept, no hooks.
func TestLauncherCheckAssistantTurnShape(t *testing.T) {
	n, home := checkNode(t, resultOK)
	profile := filepath.Join(home, "profiles", "work")
	n.state.put("c1", sessionState{ConfigDir: profile})
	check := checkRun(t, n, map[string]any{"name": "claude", "model": "opus", "effort": "high", "purpose": "assistant",
		"assistant_turn": true, "conversation": "c1", "config_dir": "~/elsewhere"})
	if !check.OK || !check.AssistantTurn || check.Model != "opus" {
		t.Fatalf("check: %+v", check)
	}
	env, args := recorded(t, home)
	if env["ENABLE_TOOL_SEARCH"] != "true" || env["PERSONA_ASSISTANT"] != "1" || env["CLAUDE_CONFIG_DIR"] != profile {
		t.Fatalf("environment: %v", env)
	}
	if args[0] != "-p" || argAfter(args, "--max-turns") != "1" || !slices.Contains(args, "--no-session-persistence") ||
		argAfter(args, "--output-format") != "json" || argAfter(args, "--model") != "opus" || argAfter(args, "--effort") != "high" {
		t.Fatalf("arguments: %q", args)
	}
	if argAfter(args, "--tools") != assistantBuiltinTools || !strings.HasPrefix(argAfter(args, "--allowedTools"), assistantAllowedTools) ||
		!slices.Contains(args, "--strict-mcp-config") {
		t.Fatalf("tool flags: %q", args)
	}
	var mcp struct {
		Servers map[string]struct {
			Args []string          `json:"args"`
			Env  map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(argAfter(args, "--mcp-config")), &mcp); err != nil {
		t.Fatalf("--mcp-config: %v %q", err, args)
	}
	persona, ok := mcp.Servers["persona"]
	if _, ext := mcp.Servers["ext"]; ext || !ok || !slices.Equal(persona.Args, []string{"mcp"}) ||
		!strings.HasPrefix(persona.Env["PERSONA_SESSION_ID"], launcherCheckDir) {
		t.Fatalf("MCP servers: %+v", mcp.Servers)
	}
	settings, err := os.ReadFile(filepath.Join(home, "check-settings"))
	if err != nil || !strings.Contains(string(settings), `"disableAllHooks":true`) {
		t.Fatalf("settings: %s %v", settings, err)
	}
	// A conversation with no profile recorded runs in the one asked for, else Persona's for the launcher.
	checkRun(t, n, map[string]any{"name": "claude", "assistant_turn": true, "conversation": "new", "config_dir": "~/elsewhere"})
	if env, args := recorded(t, home); env["CLAUDE_CONFIG_DIR"] != filepath.Join(home, "elsewhere") || slices.Contains(args, "--model") ||
		slices.Contains(args, "--effort") {
		t.Fatalf("asked profile: %v %q", env, args)
	}
	checkRun(t, n, map[string]any{"name": "claude", "assistant_turn": true})
	if env, _ := recorded(t, home); env["CLAUDE_CONFIG_DIR"] != filepath.Join(n.dir, "profiles", "claude") {
		t.Fatalf("the launcher's profile: %v", env)
	}
	if _, err := os.Stat(filepath.Join(n.dir, "profiles", "claude", "settings.json")); err == nil {
		t.Fatal("the check wrote into the profile")
	}
}

// Without assistant_turn (Settings › Models' Test, the launcher health checks) the check is the plain one, argument
// for argument: no tools, no profile, no assistant environment, and no effort even when one is sent.
func TestLauncherCheckPlainShapeUnchanged(t *testing.T) {
	n, home := checkNode(t, resultOK)
	check := checkRun(t, n, map[string]any{"name": "claude", "model": "opus", "effort": "high", "purpose": "assistant", "conversation": "c1"})
	if !check.OK || check.AssistantTurn {
		t.Fatalf("check: %+v", check)
	}
	env, args := recorded(t, home)
	if env["ENABLE_TOOL_SEARCH"] != "unset" || env["PERSONA_ASSISTANT"] != "" || env["CLAUDE_CONFIG_DIR"] != "unset" {
		t.Fatalf("environment: %v", env)
	}
	want := []string{"-p", args[1], "--output-format", "json", "--max-turns", "1", "--no-session-persistence", "--model", "opus",
		"--settings", argAfter(args, "--settings")}
	if !slices.Equal(args, want) {
		t.Fatalf("arguments: %q, want %q", args, want)
	}
}

// With tools the model may answer the one word with a tool call, which the one turn allowed does not run: the request
// went through, so an assistant-turn check passes. The plain check, which has no tools, keeps reading it as a failure.
func TestLauncherCheckAssistantTurnMaxTurnsIsOK(t *testing.T) {
	n, _ := checkNode(t, `{"type":"result","subtype":"error_max_turns","is_error":true,"modelUsage":{"opus":{}}}`)
	if check := checkRun(t, n, map[string]any{"name": "claude", "assistant_turn": true}); !check.OK || check.Error != "" || check.Answer != "" {
		t.Fatalf("assistant turn: %+v", check)
	}
	if check := checkRun(t, n, map[string]any{"name": "claude"}); check.OK || !strings.Contains(check.Error, "error_max_turns") {
		t.Fatalf("plain: %+v", check)
	}
}

// The check's tool search is the assistant start's decision for the launcher and model (assistantCheckToolSearch): none
// leaves Claude Code to decide.
func TestLauncherCheckAssistantTurnToolSearch(t *testing.T) {
	n, home := checkNode(t, resultOK)
	old := assistantCheckToolSearch
	t.Cleanup(func() { assistantCheckToolSearch = old })
	for _, want := range []string{"false", ""} {
		var asked []string
		assistantCheckToolSearch = func(_ *Node, launcher, model, _, _ string) toolSearchStart {
			asked = append(asked, launcher+"/"+model)
			return toolSearchStart{env: want}
		}
		checkRun(t, n, map[string]any{"name": "claude", "model": "opus", "assistant_turn": true})
		env, _ := recorded(t, home)
		got := env["ENABLE_TOOL_SEARCH"]
		if want == "" && got != "unset" || want != "" && got != want || !slices.Equal(asked, []string{"claude/opus"}) {
			t.Fatalf("tool search %q: env %q, asked %v", want, got, asked)
		}
	}
}

// A launcher and model whose endpoint refused tool search are checked as the chat now starts them (#10365): off in the
// check's settings too, over a provider that opts in with its own ENABLE_TOOL_SEARCH=true (a settings env wins over the
// process's), the refusal found by the model asked for (a tier alias the start remembers it by, not the provider's
// model --model maps it to); and a value the node inherited does not reach claude when the start sets none.
func TestLauncherCheckAssistantTurnAfterAToolSearchRefusal(t *testing.T) {
	n, home := checkNode(t, resultOK)
	bin := filepath.Join(home, "bin", "personad")
	writeFile(t, bin, "#!/bin/sh\nshift 2\nexec claude \"$@\"\n") // personad launch <name> …: the machine's claude
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	n.bin = bin
	l := proto.ManagedLauncher{Name: "gwopt", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", OpusModel: "o1", Env: "ENABLE_TOOL_SEARCH=true"}
	n.setManaged([]proto.ManagedLauncher{l})
	settingsToolSearch := func() string {
		t.Helper()
		s, err := readLauncherSettings(filepath.Join(home, "check-settings"), home, home)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := settingsEnv(s)[toolSearchEnv]; ok {
			return v
		}
		return "-"
	}
	check := checkRun(t, n, map[string]any{"name": "gwopt", "model": "opus", "assistant_turn": true})
	env, args := recorded(t, home)
	if !check.OK || env[toolSearchEnv] != "unset" || settingsToolSearch() != "true" || argAfter(args, "--model") != "o1" {
		t.Fatalf("before a refusal: %+v, env %v, settings %q, args %q", check, env, settingsToolSearch(), args)
	}
	n.noteToolSearchRefused("gwopt", "opus", managedBaseURL(l))
	check = checkRun(t, n, map[string]any{"name": "gwopt", "model": "opus", "assistant_turn": true})
	env, args = recorded(t, home)
	if !check.OK || env[toolSearchEnv] != "false" || settingsToolSearch() != "false" || argAfter(args, "--model") != "o1" {
		t.Fatalf("after a refusal: %+v, env %v, settings %q, args %q", check, env, settingsToolSearch(), args)
	}
	if !strings.Contains(settingsArg(args), launcherCheckDir) {
		t.Fatalf("the check's own settings are not in its directory: %q", settingsArg(args))
	}

	old := assistantCheckToolSearch
	t.Cleanup(func() { assistantCheckToolSearch = old })
	assistantCheckToolSearch = func(*Node, string, string, string, string) toolSearchStart { return toolSearchStart{} }
	t.Setenv(toolSearchEnv, "true")
	checkRun(t, n, map[string]any{"name": "claude", "assistant_turn": true})
	if env, _ := recorded(t, home); env[toolSearchEnv] != "unset" {
		t.Fatalf("the node's own ENABLE_TOOL_SEARCH reached the check: %v", env)
	}
}
