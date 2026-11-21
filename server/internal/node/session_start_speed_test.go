package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Faster speed is admitted only for a new Codex or a new Claude Code (fast
// only). Validation precedes the existing filesystem, resume and adapter errors;
// the missing directory in these cases also proves that no process was needed.
func TestSessionStartSpeedRefusalsPrecedeStartup(t *testing.T) {
	external := proto.ExternalLauncherName("adapter", "target")
	for _, tc := range []struct {
		name, speed, launcher, agent, resume, want string
	}{
		{name: "invalid speed", speed: "ludicrous", launcher: "codex", want: "agent_speed_invalid"},
		{name: "speed is case sensitive", speed: "FAST", launcher: "codex", want: "agent_speed_invalid"},
		{name: "Claude ultrafast", speed: "ultrafast", launcher: "claude", want: "agent_speed_unsupported"},
		{name: "Codex resume", speed: "fast", launcher: "codex", resume: startThread, want: "agent_speed_start_only"},
		{name: "Claude resume", speed: "fast", launcher: "claude", resume: startID, want: "agent_speed_start_only"},
		{name: "traex", speed: "fast", launcher: traexAgent, want: "agent_speed_unsupported"},
		{name: "omp", speed: "fast", launcher: ompAgent, want: "agent_speed_unsupported"},
		{name: "pi work agent", speed: "fast", launcher: piAgent, want: "agent_speed_unsupported"},
		{name: "cursor work agent", speed: "fast", launcher: cursorAgent, want: "agent_speed_unsupported"},
		{name: "agy work agent", speed: "fast", launcher: agyAgent, want: "agent_speed_unsupported"},
		{name: "external adapter", speed: "fast", launcher: external, want: "agent_speed_unsupported"},
		{name: "Claude desktop app", speed: "fast", launcher: claudeDesktopLauncher, want: "agent_speed_unsupported"},
		{name: "Codex desktop app", speed: "fast", launcher: codexAppLauncher, want: "agent_speed_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, home := testNode(t)
			shellLaunchers(t)
			params := map[string]any{
				"cwd": filepath.Join(home, "missing"), "launcher": tc.launcher,
				"agent_type": tc.agent, "prompt": "hello", "speed": tc.speed,
			}
			if tc.resume != "" {
				params["resume_session_id"] = tc.resume
			}
			_, err := request(t, n, "session.start", params)
			if code := rpcCode(err); code != tc.want {
				t.Errorf("refusal = %q (%v), want %q", code, err, tc.want)
			}
			n.mu.Lock()
			processes := len(n.procs)
			n.mu.Unlock()
			if processes != 0 {
				t.Errorf("refused start spawned %d processes", processes)
			}
		})
	}
}

func TestSessionStartSpeedRefusesExternalAndPaseoRuntimes(t *testing.T) {
	t.Run("external runtime", func(t *testing.T) {
		n, _ := runtimeNode(t)
		host := dialRuntime(t, n, dshHello())
		host.answer = func(method string, _ json.RawMessage) (any, error) {
			if method == "session.start" {
				return map[string]any{"session": map[string]any{"session_id": "runtime-speed", "cwd": "/w", "status": "idle"}}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		name := n.runtimes.launchers()[0].Name
		_, err := request(t, n, "session.start", map[string]any{
			"cwd": t.TempDir(), "launcher": name, "prompt": "hello", "speed": "fast",
		})
		if code := rpcCode(err); code != "agent_speed_unsupported" {
			t.Errorf("refusal = %q (%v), want agent_speed_unsupported", code, err)
		}
		if got := host.got("session.start"); len(got) != 0 {
			t.Errorf("speed reached the external runtime: %s", got)
		}
	})

	t.Run("Paseo provider", func(t *testing.T) {
		n, _ := runtimeNode(t)
		host := dialRuntime(t, n, paseoEngineHello())
		host.answer = func(method string, _ json.RawMessage) (any, error) {
			if method == "session.start" {
				return map[string]any{"session": map[string]any{"session_id": "paseo-speed", "cwd": "/w", "status": "idle"}}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		enablePaseo(n)
		_, err := request(t, n, "session.start", map[string]any{
			"cwd": t.TempDir(), "launcher": "paseo-codex", "prompt": "hello", "speed": "fast",
		})
		if code := rpcCode(err); code != "agent_speed_unsupported" {
			t.Errorf("refusal = %q (%v), want agent_speed_unsupported", code, err)
		}
		if got := host.got("session.start"); len(got) != 0 {
			t.Errorf("speed reached the Paseo provider: %s", got)
		}
	})
}

var startSpeedSettingsRE = regexp.MustCompile(` --settings '?([^' ]+?)'?(?: |$)`)

func startSpeedSettingsPath(t *testing.T, command string) string {
	t.Helper()
	match := startSpeedSettingsRE.FindStringSubmatch(command)
	if match == nil {
		t.Fatalf("no effective --settings file in %q", command)
	}
	return match[1]
}

// Fast mode belongs only to this process's effective --settings. It must not
// rewrite Persona's reusable settings, user-global defaults, or the command and
// settings herdr stores for an automatic resume.
func TestClaudeFastSpeedIsEffectiveStartOnlySettings(t *testing.T) {
	n, f, home := herdrNode(t)
	if err := n.writeClaudeConfig(); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(n.dir, "claude-settings.json")
	base := readSettings(t, basePath)
	base["model"] = "preserve-this-model"
	base["env"] = map[string]any{"PERSONA_TEST_KEEP": "yes"}
	encoded, _ := json.MarshalIndent(base, "", "  ")
	writeFile(t, basePath, string(encoded)+"\n")
	baseBytes, _ := os.ReadFile(basePath)
	globalPath := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, globalPath, `{"fastMode":false,"model":"user-global-model"}`+"\n")
	globalBytes, _ := os.ReadFile(globalPath)

	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"))
	_, err := request(t, n, "session.start", map[string]any{
		"session_id": startID, "cwd": cwd, "launcher": "claude", "speed": "fast", "async": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCalls(t, f, "pane.report_agent_session", 1)

	var startCommand string
	for _, call := range f.calls() {
		if call.Method == "pane.send_input" {
			startCommand, _ = call.Params["text"].(string)
		}
	}
	if startCommand == "" {
		t.Fatal("Claude start was not typed")
	}
	if got := strings.Count(startCommand, " --settings "); got != 1 {
		t.Errorf("start has %d effective --settings files, want one: %s", got, startCommand)
	}
	effectivePath := startSpeedSettingsPath(t, startCommand)
	effective := readSettings(t, effectivePath)
	if effective["fastMode"] != true {
		t.Errorf("effective start settings fastMode = %v, want true (%s)", effective["fastMode"], effectivePath)
	}
	if effective["model"] != "preserve-this-model" || effective["hooks"] == nil {
		t.Errorf("effective settings did not preserve model and hooks: %v", effective)
	}
	if env, _ := effective["env"].(map[string]any); env["PERSONA_TEST_KEEP"] != "yes" {
		t.Errorf("effective settings did not preserve env: %v", effective)
	}
	if got, _ := os.ReadFile(basePath); string(got) != string(baseBytes) {
		t.Errorf("fast start rewrote reusable settings: before %s, after %s", baseBytes, got)
	}
	if got, _ := os.ReadFile(globalPath); string(got) != string(globalBytes) {
		t.Errorf("fast start rewrote user-global defaults: before %s, after %s", globalBytes, got)
	}

	var resumeCommand string
	for _, call := range f.calls() {
		argv, _ := call.Params["resume_argv"].([]any)
		if len(argv) == 2 {
			resumeCommand, _ = argv[1].(string)
		}
	}
	if resumeCommand == "" {
		t.Fatal("herdr stored no resume_argv")
	}
	resumePath := startSpeedSettingsPath(t, resumeCommand)
	if resume := readSettings(t, resumePath); resume["fastMode"] == true {
		t.Errorf("fastMode leaked into herdr's automatic resume settings %s: %v", resumePath, resume)
	}
}

func startSpeedFunction(t *testing.T, name, invocation string) {
	t.Helper()
	if loginShellProgram() == "" {
		t.Skip("no login shell")
	}
	shellLaunchers(t, name)
	launcherCache.Lock()
	oldSettings, oldScripts := launcherCache.settings, launcherCache.scripts
	launcherCache.settings, launcherCache.scripts = map[string]string{}, map[string]string{}
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.settings, launcherCache.scripts = oldSettings, oldScripts
		launcherCache.Unlock()
	})
	home := os.Getenv("HOME")
	zdotdir := os.Getenv("ZDOTDIR")
	if zdotdir == "" {
		zdotdir = home
	}
	// loginShellProgram runs zsh where available ($ZDOTDIR/.zshrc) and falls back to bash ($HOME/.bashrc) where zsh is
	// absent, so define the launcher in both rc files.
	def := "function " + name + " { " + invocation + "; }\n"
	writeFile(t, filepath.Join(zdotdir, ".zshrc"), def)
	writeFile(t, filepath.Join(home, ".bashrc"), def)
}

// A shell function can append its own --settings after Persona's. With plugins
// enabled there is no base settings path to merge, so fast mode must refuse
// rather than discard the function's credentials and model.
func TestClaudeFastSpeedRefusesHiddenFunctionSettings(t *testing.T) {
	n, f, home := herdrNode(t)
	n.claudeV.set("99.0.0")
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	startSpeedFunction(t, "speedwork", `claude --settings "$HOME/work-settings.json" "$@"`)
	writeFile(t, filepath.Join(home, "work-settings.json"), `{"model":"must-not-lose"}`)
	if err := n.writeClaudeConfig(); err != nil {
		t.Fatal(err)
	}
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"))
	_, err := request(t, n, "session.start", map[string]any{
		"session_id": startID, "cwd": cwd, "launcher": "speedwork", "speed": "fast",
	})
	if code, _ := refusalOf(t, err); code != "agent_speed_unsupported" {
		t.Errorf("refusal = %q (%v), want agent_speed_unsupported", code, err)
	}
	if calls := f.trace("tab.create", "pane.send_input"); len(calls) != 0 {
		t.Errorf("hidden settings reached startup: %v", calls)
	}
}

func TestClaudeFastSpeedAllowsFunctionWithoutSettings(t *testing.T) {
	n, _, home := herdrNode(t)
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	startSpeedFunction(t, "speedplain", `claude "$@"`)
	path, err := n.claudeFastModeSettings(context.Background(), "speedplain", "", home)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, path)["fastMode"]; got != true {
		t.Fatalf("fastMode = %v, want true", got)
	}
}

// Machines without zsh run the launcher probe through bash (loginShellProgram). Keep the bash fallback covered even on
// hosts with zsh installed, while preserving the refusal for genuinely unseen launchers.
func TestClaudeFastSpeedFunctionWithoutZsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PATH fallback only")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("no bash on PATH: %v", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(bash, filepath.Join(bin, "bash")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := loginShellProgram(); got != "bash" {
		t.Fatalf("loginShellProgram() = %q, want bash", got)
	}

	t.Run("allows function without settings", func(t *testing.T) {
		n, _, home := herdrNode(t)
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", home)
		startSpeedFunction(t, "speedplain", `claude "$@"`)
		path, err := n.claudeFastModeSettings(context.Background(), "speedplain", "", home)
		if err != nil {
			t.Fatal(err)
		}
		if got := readSettings(t, path)["fastMode"]; got != true {
			t.Fatalf("fastMode = %v, want true", got)
		}
	})

	t.Run("refuses hidden function settings", func(t *testing.T) {
		n, _, home := herdrNode(t)
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", home)
		startSpeedFunction(t, "speedwork", `claude --settings "$HOME/work-settings.json" "$@"`)
		_, err := n.claudeFastModeSettings(context.Background(), "speedwork", "", home)
		if err == nil {
			t.Fatal("expected refusal for hidden --settings")
		}
		if errors.Is(err, errProbeUnseen) {
			t.Fatalf("probe did not see bash function: %v", err)
		}
		if !strings.Contains(err.Error(), "--settings") {
			t.Fatalf("error = %v, want hidden --settings refusal", err)
		}
	})

	t.Run("still refuses unseen launcher", func(t *testing.T) {
		n, _, home := herdrNode(t)
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", home)
		shellLaunchers(t, "speedmissing")
		_, err := n.claudeFastModeSettings(context.Background(), "speedmissing", "", home)
		if !errors.Is(err, errProbeUnseen) {
			t.Fatalf("error = %v, want errProbeUnseen", err)
		}
	})
}
