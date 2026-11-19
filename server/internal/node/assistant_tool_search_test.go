package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's claude has tool search on for the plain claude launcher on Anthropic's own API, as for a shell
// launcher there (TestAssistantShellLauncherCompaction): exactly true (#7317).
func TestAssistantToolSearchOn(t *testing.T) {
	f := cliTest(t)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(f.proc(id).cmd.Env, "ENABLE_TOOL_SEARCH"); got != "true" {
		t.Fatalf("claude has ENABLE_TOOL_SEARCH=%q, want \"true\"", got)
	}
}

// Each start decides by where its requests go (#10365): Anthropic's own API true (the hold-off lift of #7317), a known
// gateway false (any value lifts Claude Code's own rule that keeps tool search off there), a start that cannot tell
// nothing (Claude Code decides), a launcher that sets it itself nothing; a refusal on that endpoint off, in the
// start's settings too, whatever the launcher says.
func TestToolSearchFor(t *testing.T) {
	const gw = "https://gw.example.com"
	for _, tc := range []struct {
		name                string
		endpoint            string
		known, own, refused bool
		want                string
		forceOff            bool
	}{
		{"Anthropic's own API, no Base URL", "", true, false, false, "true", false},
		{"Anthropic's own API", "https://api.anthropic.com", true, false, false, "true", false},
		{"Anthropic's own API with /v1", "https://api.anthropic.com/v1/", true, false, false, "true", false},
		{"a gateway", gw, true, false, false, "false", false},
		{"a lookalike host", "https://api.anthropic.com.example.com", true, false, false, "false", false},
		{"cannot tell", gw, false, false, false, "", false},
		{"cannot tell, no Base URL seen", "", false, false, false, "", false},
		{"the launcher's own value", gw, true, true, false, "", false},
		{"the launcher's own value on Anthropic's API", "", true, true, false, "", false},
		{"refused", gw, true, false, true, "false", true},
		{"refused, over the launcher's own value", gw, true, true, true, "false", true},
		{"refused where it could not tell", "", false, false, true, "false", true},
	} {
		got := toolSearchFor(tc.endpoint, tc.known, tc.own, tc.refused)
		if got.env != tc.want || got.forceOff != tc.forceOff {
			t.Errorf("%s: %+v, want env %q forceOff %v", tc.name, got, tc.want, tc.forceOff)
		}
	}
}

// claudeEndpoint reads the layers as Claude Code does: a settings env over the process environment (the launcher's
// --settings, the project's local then shared settings, the user's), then what the launcher exports, then the shell's
// unless the launcher unset it. The first that names ANTHROPIC_BASE_URL counts, empty too.
func TestClaudeEndpointLayers(t *testing.T) {
	managed := claudeManagedSettingsFiles // not the machine's own policy (TestClaudeEndpointManagedPolicy)
	claudeManagedSettingsFiles = func() []string { return nil }
	t.Cleanup(func() { claudeManagedSettingsFiles = managed })
	home, cwd, profile := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, "own.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://own.example.com"}}`)
	writeFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://local.example.com"}}`)
	writeFile(t, filepath.Join(profile, "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://user.example.com","ENABLE_TOOL_SEARCH":true}}`)
	shell := map[string]string{"ANTHROPIC_BASE_URL": "https://shell.example.com"}
	for _, tc := range []struct {
		name    string
		p       Probe
		own     string
		profile string
		local   bool
		want    string
		ownTS   bool
	}{
		{"the launcher's --settings", Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://env.example.com"}, Shell: shell}, filepath.Join(home, "own.json"), profile, true, "https://own.example.com", true},
		{"the project's local settings", Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://env.example.com"}, Shell: shell}, "", profile, true, "https://local.example.com", true},
		{"the user's settings", Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://env.example.com"}, Shell: shell}, "", profile, false, "https://user.example.com", true},
		{"what the launcher exports", Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://env.example.com"}, Shell: shell}, "", t.TempDir(), false, "https://env.example.com", false},
		{"an empty value is Anthropic's API", Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": ""}, Shell: shell}, "", t.TempDir(), false, "", false},
		{"the shell's", Probe{Env: map[string]string{}, Shell: shell}, "", t.TempDir(), false, "https://shell.example.com", false},
		{"the shell's, unset by the launcher", Probe{Env: map[string]string{}, Shell: shell, Unset: []string{"ANTHROPIC_BASE_URL"}}, "", t.TempDir(), false, "", false},
		{"the launcher exports ENABLE_TOOL_SEARCH", Probe{Env: map[string]string{"ENABLE_TOOL_SEARCH": "true"}, Shell: map[string]string{}}, "", t.TempDir(), false, "", true},
	} {
		dir := cwd
		if !tc.local {
			dir = t.TempDir()
		}
		got, ownTS := claudeEndpoint(tc.p, tc.own, tc.profile, dir, home)
		if got != tc.want || ownTS != tc.ownTS {
			t.Errorf("%s: %q own %v, want %q own %v", tc.name, got, ownTS, tc.want, tc.ownTS)
		}
	}
}

// An administrator's managed settings sit above every other layer (#10365): their ANTHROPIC_BASE_URL is where claude's
// requests go, a managed-settings.d drop-in over managed-settings.json (the last in alphabetical order first, hidden
// ones skipped), and their ENABLE_TOOL_SEARCH is not Persona's to override. A start of plain claude behind such a
// policy's gateway loads every tool up front, as behind any other gateway.
func TestClaudeEndpointManagedPolicy(t *testing.T) {
	old := claudeManagedSettingsFiles
	t.Cleanup(func() { claudeManagedSettingsFiles = old })
	dir := t.TempDir()
	policy := filepath.Join(dir, "managed-settings.json")
	claudeManagedSettingsFiles = func() []string { return []string{filepath.Join(dir, "absent", "managed-settings.json"), policy} }
	writeFile(t, policy, `{"env":{"ANTHROPIC_BASE_URL":"https://policy.example.com"}}`)
	own := filepath.Join(dir, "own.json")
	writeFile(t, own, `{"env":{"ANTHROPIC_BASE_URL":"https://own.example.com"}}`)
	p := Probe{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://env.example.com"}, Shell: map[string]string{}}
	endpoint := func() (string, bool) { return claudeEndpoint(p, own, t.TempDir(), t.TempDir(), dir) }
	if got, ownTS := endpoint(); got != "https://policy.example.com" || ownTS {
		t.Fatalf("managed-settings.json: %q own %v", got, ownTS)
	}
	dropIns := filepath.Join(dir, "managed-settings.d")
	writeFile(t, filepath.Join(dropIns, "10-gateway.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://dropin.example.com"}}`)
	writeFile(t, filepath.Join(dropIns, "20-tools.json"), `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`)
	writeFile(t, filepath.Join(dropIns, ".30-hidden.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://hidden.example.com"}}`)
	writeFile(t, filepath.Join(dropIns, "30-notes.txt"), `{"env":{"ANTHROPIC_BASE_URL":"https://notes.example.com"}}`)
	if got, ownTS := endpoint(); got != "https://dropin.example.com" || !ownTS {
		t.Fatalf("drop-ins: %q own %v", got, ownTS)
	}
	writeFile(t, filepath.Join(dropIns, "40-anthropic.json"), `{"env":{"ANTHROPIC_BASE_URL":""}}`)
	if got, _ := endpoint(); got != "" {
		t.Fatalf("the last drop-in's empty Base URL (Anthropic's own API): %q", got)
	}

	f := cliTest(t)
	claudeManagedSettingsFiles = func() []string { return []string{filepath.Join(dir, "gw", "managed-settings.json")} }
	writeFile(t, filepath.Join(dir, "gw", "managed-settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9"}}`)
	if _, env, settings := toolSearchStartOf(t, f, "claude", cliExec()); env != "false" || settings != "-" {
		t.Fatalf("plain claude behind the policy's gateway: env %q, settings env %q", env, settings)
	}
}

// toolSearchStartOf starts launcher's claude for a new conversation and says what its start sets: ENABLE_TOOL_SEARCH
// in the process environment and in its settings env ("-": not set).
func toolSearchStartOf(t *testing.T, f *cliFixture, launcher string, execution modelExecution) (*assistantProc, string, string) {
	t.Helper()
	p, err := f.n.spawnClaude(newUUID(), t.TempDir(), launcher, false, false, execution)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.n.stopAssistant(p.id) })
	env := "-"
	if i := slices.IndexFunc(p.cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, toolSearchEnv+"=") }); i >= 0 {
		env = strings.TrimPrefix(p.cmd.Env[i], toolSearchEnv+"=")
	}
	settings := "-"
	if s, err := readLauncherSettings(settingsArg(p.cmd.Args), p.cwd, os.Getenv("HOME")); err == nil {
		if v, ok := settingsEnv(s)[toolSearchEnv]; ok {
			settings = v
		}
	}
	return p, env, settings
}

// End to end on the start: plain claude and launchers in ~/.zshrc as the probe sees them, and managed providers (their
// settings env pins the provider's value, #10365).
func TestAssistantToolSearchFollowsTheEndpoint(t *testing.T) {
	const rc = `function anth { ANTHROPIC_BASE_URL=https://api.anthropic.com command claude "$@"; }
function gw { ( export ANTHROPIC_BASE_URL=http://127.0.0.1:9; command claude "$@" ) }
function gwsettings { command claude "$@" --settings "$HOME/gw-settings.json"; }
function own { ( export ANTHROPIC_BASE_URL=http://127.0.0.1:9 ENABLE_TOOL_SEARCH=true; command claude "$@" ) }
`
	for _, tc := range []struct {
		launcher          string
		managed           *proto.ManagedLauncher
		env, settingsWant string
	}{
		{"claude", nil, "true", "-"},
		{"anth", nil, "true", "-"},
		{"gw", nil, "false", "-"},
		{"gwsettings", nil, "false", "-"},
		{"own", nil, "-", "-"},
		{"gwm", &proto.ManagedLauncher{Name: "gwm", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1"}, "false", "false"},
		{"gwmopt", &proto.ManagedLauncher{Name: "gwmopt", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", Env: "ENABLE_TOOL_SEARCH=true"}, "-", "true"},
		{"anthm", &proto.ManagedLauncher{Name: "anthm", Kind: "api", APIKey: "sk-test-k"}, "true", "-"},
	} {
		t.Run(tc.launcher, func(t *testing.T) {
			f := cliTest(t)
			writeFile(t, filepath.Join(os.Getenv("HOME"), ".zshrc"), rc)
			writeFile(t, filepath.Join(os.Getenv("HOME"), "gw-settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9"}}`)
			if tc.managed != nil {
				f.fakeLaunch(t)
				f.n.setManaged([]proto.ManagedLauncher{*tc.managed})
			}
			_, env, settings := toolSearchStartOf(t, f, tc.launcher, cliExec())
			if env != tc.env || settings != tc.settingsWant {
				t.Fatalf("start env %q, settings env %q; want %q, %q", env, settings, tc.env, tc.settingsWant)
			}
		})
	}
}

// What the node remembers of a refusal: per launcher and model (one gateway refused tool search for some upstream
// models and carried it for others), for the endpoint that refused, shared by the Nodes of one node process (a
// reconnection) and by no other node directory.
func TestToolSearchRefusalIsRememberedPerLauncherAndModel(t *testing.T) {
	const gw = "https://gw.example.com"
	dir := t.TempDir()
	n := &Node{dir: dir}
	if !n.noteToolSearchRefused("gw", "vendor-model", gw) || n.noteToolSearchRefused("gw", "vendor-model", gw) {
		t.Fatal("news: the first note only")
	}
	for _, tc := range []struct {
		n                         *Node
		launcher, model, endpoint string
		want                      bool
	}{
		{n, "gw", "vendor-model", gw, true},
		{&Node{dir: dir}, "gw", "vendor-model", gw, true}, // the next connection's Node
		{n, "gw", "claude-opus-5-5", gw, false},
		{n, "other", "vendor-model", gw, false},
		{n, "gw", "", gw, false},
		{n, "gw", "vendor-model", "https://gw2.example.com", false}, // the provider's Base URL changed
		{&Node{dir: t.TempDir()}, "gw", "vendor-model", gw, false},
	} {
		if got := tc.n.toolSearchRefused(tc.launcher, tc.model, tc.endpoint); got != tc.want {
			t.Errorf("%s/%s at %s: %v, want %v", tc.launcher, tc.model, tc.endpoint, got, tc.want)
		}
	}
	c := assistantCompaction{endpoint: gw, endpointKnown: true, ownToolSearch: true}
	if got := n.assistantToolSearch("gw", "vendor-model", c); got != (toolSearchStart{env: "false", forceOff: true}) {
		t.Errorf("a remembered pair: %+v", got)
	}
	if got := n.assistantToolSearch("gw", "claude-opus-5-5", c); got != (toolSearchStart{}) {
		t.Errorf("another model of the same launcher: %+v", got)
	}

	// A new conversation on a remembered pair starts with every tool loaded: off in its environment and its settings.
	f := cliTest(t)
	f.n.noteToolSearchRefused("claude", "", "")
	p, env, settings := toolSearchStartOf(t, f, "claude", cliExec())
	if env != "false" || settings != "false" || !p.toolSearch.forceOff {
		t.Fatalf("start env %q, settings env %q, forced %v", env, settings, p.toolSearch.forceOff)
	}
}

// toolSearchCLI is the machine's claude for the refusal tests: it refuses every turn the way a gateway edge that cannot
// carry tool search does unless its start turned tool search off in both its environment and its --settings env
// (always-refuse: whatever its start did), and answers otherwise, a 503 first when 503-first exists. It says it has
// no background task (tasks names one when that file exists) and keeps what each start had in tool-search.
const toolSearchCLI = fakeCLIAuthStatus + `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
settings=; prev=
for a in "$@"; do [ "$prev" = --settings ] && settings=$a; prev=$a; done
off=no
if [ "$ENABLE_TOOL_SEARCH" = false ] && [ -n "$settings" ] && grep -q '"ENABLE_TOOL_SEARCH":"false"' "$settings"; then off=yes; fi
printf '%s %s\n' "${ENABLE_TOOL_SEARCH-unset}" "$off" >> "$CLI_FAKE_DIR/tool-search"
echo '{"type":"system","subtype":"init"}'
if [ -e "$CLI_FAKE_DIR/tasks" ]; then
  echo '{"type":"system","subtype":"background_tasks_changed","tasks":[{"id":"t1","description":"deploy"}]}'
else
  echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
fi
while IFS= read -r line; do
  case "$line" in *'"type":"user"'*)
    printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
    if [ "$off" = yes ] && [ ! -e "$CLI_FAKE_DIR/always-refuse" ]; then
      if [ -e "$CLI_FAKE_DIR/503-first" ] && [ ! -e "$CLI_FAKE_DIR/503-sent" ]; then
        : > "$CLI_FAKE_DIR/503-sent"
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 503 no eligible upstream account is currently available"}'
      else
        echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered"}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"success","is_error":false,"result":"answered"}'
      fi
    else
      echo '{"type":"result","subtype":"success","is_error":true,"result":"API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded"}'
    fi ;;
  esac
done
`

func toolSearchCLITest(t *testing.T, flags ...string) *cliFixture {
	t.Helper()
	f := cliTest(t)
	writeFile(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+toolSearchCLI)
	if err := os.Chmod(filepath.Join(os.Getenv("HOME"), "bin", "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, flag := range flags {
		writeFile(t, filepath.Join(f.dir, flag), "")
	}
	return f
}

// A turn the endpoint refused for tool search is resent once, unseen, in a new process with every tool loaded (off in
// its environment and settings), the launcher and model remembered. A 503 after that resend is left to the manager's
// durable scheduler rather than spending another immediate same-model retry (#10365, #10444).
func TestToolSearchRefusalRetriesOnceWithoutToolSearch(t *testing.T) {
	for _, then503 := range []bool{false, true} {
		t.Run(map[bool]string{false: "answers", true: "then a 503"}[then503], func(t *testing.T) {
			var flags []string
			if then503 {
				flags = append(flags, "503-first")
			}
			f := toolSearchCLITest(t, flags...)
			id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "", cliExec())
			if err != nil {
				t.Fatal(err)
			}
			users := 2 // the original and the tool-search resend; a later 503 waits for manager recovery
			cliWait(t, "the resend", func() bool { return strings.Count(f.file("user.jsonl"), `"type":"user"`) == users })
			f.idle(t, id)
			if got := strings.Split(strings.TrimSpace(f.file("tool-search")), "\n"); !slices.Equal(got, []string{"true no", "false yes"}) {
				t.Fatalf("starts' tool search %q, want one with it on, then one restart with it off", got)
			}
			retry, _ := json.Marshal(prompts.RenderModelRetry())
			if got := f.file("user.jsonl"); strings.Count(got, "original request") != 1 || strings.Count(got, strings.Trim(string(retry), `"`)) != users-1 {
				t.Fatalf("the turn was not resent as the retry prompt: %s", got)
			}
			if sess := f.session(t, id); !then503 && sess.TurnError != "" || then503 && !strings.Contains(sess.TurnError, "503") {
				t.Fatalf("unexpected turn error after tool-search recovery: %q", sess.TurnError)
			}
			if p := f.proc(id); p == nil || !p.toolSearch.forceOff {
				t.Fatalf("the conversation does not run with tool search off: %+v", p)
			}
			if !f.n.toolSearchRefused("claude", "", "") {
				t.Fatal("the refusal was not remembered for the launcher and model")
			}
			if r := f.n.modelRecovery(id); r != nil {
				t.Fatalf("a model recovery marker for a tool-search resend: %+v", r)
			}
		})
	}
}

// A process already started with tool search off that is refused again ends the turn on the refusal: no second resend,
// nothing loops, the error is the turn's. With background tasks running the restart would kill, the resend does not
// happen either and the turn ends, the refusal remembered for the next start.
func TestToolSearchRefusalIsRetriedOnlyOnce(t *testing.T) {
	t.Run("refused again", func(t *testing.T) {
		f := toolSearchCLITest(t, "always-refuse")
		id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "", cliExec())
		if err != nil {
			t.Fatal(err)
		}
		// The turn error is published only once recovery has decided against another resend, so the count is final.
		cliWait(t, "the turn error", func() bool { return f.turnOver(id) && f.session(t, id).TurnError != "" })
		if got := strings.Count(f.file("cli-argv"), "\n"); got != 2 {
			t.Fatalf("%d starts, want the first and one restart", got)
		}
		if got := strings.Count(f.file("user.jsonl"), `"type":"user"`); got != 2 {
			t.Fatalf("%d messages written, want the original and one resend", got)
		}
		if sess := f.session(t, id); !strings.Contains(sess.TurnError, "ToolSearch is not supported") {
			t.Fatalf("turn error %q", sess.TurnError)
		}
	})
	t.Run("background tasks", func(t *testing.T) {
		f := toolSearchCLITest(t, "tasks")
		id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "", cliExec())
		if err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)
		if got := strings.Count(f.file("cli-argv"), "\n"); got != 1 {
			t.Fatalf("%d starts: a restart would have killed the running task", got)
		}
		if !f.n.toolSearchRefused("claude", "", "") {
			t.Fatal("the refusal was not remembered")
		}
	})
}

// A conversation on a launcher and model whose endpoint refused tool search since it started restarts with every tool
// loaded on its next message; a spare warmed before is not adopted (#10365).
func TestIdleConversationRestartsWhenItsLauncherRefusedToolSearch(t *testing.T) {
	f := cliTest(t)
	ctx := context.Background()
	id, err := f.n.assistantSend(ctx, "", t.TempDir(), "claude", "hello", nil, "", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	first := f.proc(id)
	if _, err := f.n.assistantSend(ctx, id, "", "claude", "again", nil, "", "", cliExec()); err != nil {
		t.Fatal(err)
	}
	if f.proc(id) != first {
		t.Fatal("restarted with nothing refused")
	}
	f.idle(t, id)
	f.n.noteToolSearchRefused("claude", first.model.Model, first.endpoint) // another conversation's turn was refused
	if _, err := f.n.assistantSend(ctx, id, "", "claude", "and again", nil, "", "", cliExec()); err != nil {
		t.Fatal(err)
	}
	p := f.proc(id)
	if p == first || !p.toolSearch.forceOff || envValue(p.cmd.Env, toolSearchEnv) != "false" {
		t.Fatalf("not restarted with tool search off: same %v, %+v", p == first, p.toolSearch)
	}
	f.idle(t, id)

	g := cliTest(t)
	cwd := t.TempDir()
	if err := g.n.assistantWarm(cwd, "claude", cliExec()); err != nil {
		t.Fatal(err)
	}
	g.n.mu.Lock()
	spare := g.n.spare
	g.n.mu.Unlock()
	t.Cleanup(spare.stop)
	g.n.noteToolSearchRefused("claude", spare.model.Model, spare.endpoint)
	if got := g.n.adoptSpare(cwd, "claude", cliExec()); got != nil {
		t.Fatal("a spare started with tool search on was adopted after its endpoint refused it")
	}
}

// The next warm replaces a spare its endpoint's refusal made stale since (#10365): it is stopped, and the one waiting
// instead starts with every tool loaded, which the next new conversation adopts rather than cold-starting.
func TestWarmReplacesASpareMadeStaleByAToolSearchRefusal(t *testing.T) {
	f := cliTest(t)
	cwd, execution := t.TempDir(), cliExec()
	execution.ConfigDir = t.TempDir() // the profile the hub warms and starts new conversations in
	if err := f.n.assistantWarm(cwd, "claude", execution); err != nil {
		t.Fatal(err)
	}
	spare := func() *assistantProc {
		f.n.mu.Lock()
		defer f.n.mu.Unlock()
		return f.n.spare
	}
	stale := spare()
	t.Cleanup(stale.stop)
	f.n.noteToolSearchRefused("claude", stale.model.Model, stale.endpoint)
	if got := f.n.adoptSpare(cwd, "claude", execution); got != nil {
		t.Fatal("a spare started with tool search on was adopted after its endpoint refused it")
	}
	if err := f.n.assistantWarm(cwd, "claude", execution); err != nil {
		t.Fatal(err)
	}
	fresh := spare()
	if fresh == nil || fresh == stale || !fresh.toolSearch.forceOff {
		t.Fatalf("the stale spare was kept (same %v), or its replacement has tool search on", fresh == stale)
	}
	t.Cleanup(fresh.stop)
	cliWait(t, "the stale spare to stop", func() bool { return !stale.alive() })
	if got := f.n.adoptSpare(cwd, "claude", execution); got != fresh {
		t.Fatal("the spare started with every tool loaded was not adopted")
	}
}

// --model on a managed provider behind a gateway is never a tier alias (#10365): the provider's model for that tier,
// or none; plain claude keeps the alias (its own environment resolves it).
func TestManagedAliasModelArg(t *testing.T) {
	for _, tc := range []struct {
		launcher string
		l        *proto.ManagedLauncher
		want     string // "": no --model
	}{
		{"gw", &proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com", Model: "route/claude-opus-5-5", OpusModel: "opus"}, "route/claude-opus-5-5"},
		{"bare", &proto.ManagedLauncher{Name: "bare", BaseURL: "https://gw.example.com", Model: "opus"}, ""},
		{"claude", nil, "opus"},
	} {
		t.Run(tc.launcher, func(t *testing.T) {
			f := cliTest(t)
			if tc.l != nil {
				f.fakeLaunch(t)
				f.n.setManaged([]proto.ManagedLauncher{*tc.l})
			}
			execution := cliExec()
			execution.Model = "opus"
			toolSearchStartOf(t, f, tc.launcher, execution)
			cliWait(t, "claude's arguments", func() bool { return f.file("cli-argv") != "" })
			argv := f.file("cli-argv")
			if model := modelArg(strings.Fields(argv)); model != tc.want {
				t.Fatalf("--model %q, want %q: %s", model, tc.want, argv)
			}
		})
	}
}
