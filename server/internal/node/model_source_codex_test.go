package node

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/pelletier/go-toml/v2"
)

func sourceCodexFixture(t *testing.T, config string) (string, string, []string) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), config)
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := modelSourceEnvironment(modelhttp.Source{Role: "task_agent", Todo: "todo_123"}, "/example/personad")
	return home, cwd, env
}

const sourceCodexProvider = `model_provider = "custom"
[model_providers.custom]
name = "Example"
base_url = "https://gw.example.com/v1"
wire_api = "responses"
http_headers = { "Authorization" = "example-credential", "X-Team" = "blue", "x-persona-todo" = "todo_old" }
env_http_headers = { "X-Extra" = "EXAMPLE_EXTRA", "x-persona-routine" = "OLD_ROUTINE" }
`

func TestCodexModelSourcePreservesHeadersWithoutExposingValues(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	home, cwd, env := sourceCodexFixture(t, sourceCodexProvider)
	env = append(env, "EXAMPLE_EXTRA=existing-value", "OLD_ROUTINE=old")
	for _, role := range []string{"main", "leader", "routine", "task_agent", "background"} {
		source := modelhttp.Source{Role: role, Todo: "todo_123", Session: "session_456"}
		raw, _ := json.Marshal(source)
		inputEnv := append(withoutEnv(env, []string{modelSourceEnv}), modelSourceEnv+"="+string(raw))
		argv, outEnv := codexModelSource([]string{"codex", "--model", "model", "--", "prompt"}, inputEnv, cwd, home)
		if strings.Contains(strings.Join(argv, " "), "example-credential") || strings.Contains(strings.Join(argv, " "), "existing-value") {
			t.Fatal("credential exposed in argv")
		}
		if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "prompt" {
			t.Fatal("prompt moved into config arguments")
		}
		cfg, err := codexSourceConfig(argv, outEnv, cwd, home)
		if err != nil {
			t.Fatal(err)
		}
		provider := cfg["model_providers"].(map[string]any)["custom"].(map[string]any)
		bindings := provider["env_http_headers"].(map[string]any)
		want := map[string]string{"Authorization": "example-credential", "X-Team": "blue", "X-Extra": "existing-value", modelhttp.RoleHeader: role, modelhttp.TodoHeader: "todo_123", modelhttp.SessionHeader: "session_456"}
		if len(bindings) != len(want) {
			t.Fatalf("bindings=%v", bindings)
		}
		for name, value := range want {
			variable, _ := bindings[name].(string)
			if envValue(outEnv, variable) != value {
				t.Fatalf("lost header %s", name)
			}
		}
		if provider["base_url"] != "https://gw.example.com/v1" || provider["wire_api"] != "responses" {
			t.Fatal("provider routing changed")
		}
	}
	before, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if string(before) != sourceCodexProvider {
		t.Fatal("user provider config changed")
	}
}

func TestModelSourceLaunchKeepsCodexGuard(t *testing.T) {
	const childEnv = "PERSONA_TEST_SOURCE_LAUNCH_GUARD"
	if os.Getenv(childEnv) == "1" {
		err := ModelSourceLaunch([]string{"codex", "--sandbox", "workspace-write"})
		if rpcCode(err) != "codex_writable_symlink" {
			t.Fatalf("source wrapper lost the native launch guard: %v", err)
		}
		return
	}
	home, cwd, sourceEnv := sourceCodexFixture(t, sourceCodexProvider)
	if err := os.Symlink(t.TempDir(), filepath.Join(cwd, ".aws")); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	fake := filepath.Join(bin, "codex")
	writeFile(t, fake, "#!/bin/sh\nexit 73\n")
	if err := os.Chmod(fake, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestModelSourceLaunchKeepsCodexGuard$")
	cmd.Dir = cwd
	cmd.Env = append(withoutEnv(os.Environ(), []string{"HOME", "USERPROFILE", "CODEX_HOME", "PATH", childEnv, modelhttp.SourceHostsEnv, modelSourceEnv, modelSourceBinEnv}),
		"HOME="+home, "USERPROFILE="+home, "CODEX_HOME="+filepath.Join(home, ".codex"), "PATH="+bin,
		childEnv+"=1", modelhttp.SourceHostsEnv+"=*.example.com")
	cmd.Env = append(cmd.Env, sourceEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("source launch guard: %v: %s", err, out)
	}
}

func TestCodexModelSourceUsesEffectiveDestination(t *testing.T) {
	for _, tc := range []struct {
		name, hosts, config string
		args                []string
		inject              bool
	}{
		{"empty allowlist", "", sourceCodexProvider, nil, false},
		{"custom provider", "*.example.com", sourceCodexProvider, nil, true},
		{"official provider URL", "*.example.com", strings.ReplaceAll(sourceCodexProvider, "https://gw.example.com/v1", "https://api.openai.com/v1"), nil, false},
		{"CLI overrides to official", "*.example.com", sourceCodexProvider, []string{"-c", `model_providers.custom.base_url="https://api.openai.com/v1"`}, false},
		{"CLI overrides to gateway", "*.example.com", strings.ReplaceAll(sourceCodexProvider, "https://gw.example.com/v1", "https://api.openai.com/v1"), []string{"-c", `model_providers.custom.base_url="https://gw.example.com/v1"`}, true},
		{"compact config override", "*.example.com", sourceCodexProvider, []string{`-cmodel_providers.custom.base_url="https://api.openai.com/v1"`}, false},
		{"compact profile option", "*.example.com", sourceCodexProvider, []string{"-pother"}, false},
		{"built-in provider", "*.example.com", `model_provider="openai"`, nil, false},
		{"remote app-server", "*.example.com", sourceCodexProvider, []string{"--remote", "ws://localhost:1234"}, false},
		{"profile selects official", "*.example.com", sourceCodexProvider + "\n[profiles.official]\nmodel_provider='openai'\n", []string{"--profile", "official"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(modelhttp.SourceHostsEnv, tc.hosts)
			home, cwd, env := sourceCodexFixture(t, tc.config)
			input := append([]string{"codex"}, tc.args...)
			out, _ := codexModelSource(input, env, cwd, home)
			if (!reflect.DeepEqual(input, out)) != tc.inject {
				t.Fatalf("injected=%v, want %v", !reflect.DeepEqual(input, out), tc.inject)
			}
		})
	}
}

func TestCodexModelSourceDoesNotGuessProjectTrust(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	home, cwd, env := sourceCodexFixture(t, sourceCodexProvider)
	writeFile(t, filepath.Join(cwd, ".codex", "config.toml"), "model_provider='openai'\n")
	in := []string{"codex", "app-server"}
	if got, _ := codexModelSource(in, env, cwd, home); !reflect.DeepEqual(got, in) {
		t.Fatal("injected with unknown project trust")
	}
}

func TestCodexSourceOverrideReplacesInlineTables(t *testing.T) {
	config := map[string]any{}
	for _, override := range []string{`model_providers."provider.name"={base_url="https://gw.example.com",http_headers={"X-Team"="blue"}}`, `model_providers."provider.name".http_headers={}`} {
		if err := setSourceOverride(config, override); err != nil {
			t.Fatal(err)
		}
	}
	provider := config["model_providers"].(map[string]any)["provider.name"].(map[string]any)
	if len(provider["http_headers"].(map[string]any)) != 0 || provider["base_url"] != "https://gw.example.com" {
		t.Fatal("inline override did not replace the selected leaf")
	}
}

func TestCodexSourceShellRunsAfterLauncherEnvironment(t *testing.T) {
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	home := t.TempDir()
	out := filepath.Join(home, "seen")
	helper := filepath.Join(home, "helper")
	writeFile(t, helper, "#!/bin/sh\nprintf '%s\\n' \"$OPENAI_BASE_URL\" \"$@\" > \"$SOURCE_OUT\"\n")
	if err := os.Chmod(helper, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".zshrc"), "provider() { OPENAI_BASE_URL=https://gw.example.com command codex -c 'model_provider=custom' \"$@\"; }\n")
	cmd := exec.Command(shell, "-ic", codexShell("provider"), "persona", "-c", "features.apps=false")
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "PATH=/usr/bin:/bin", "SOURCE_OUT=" + out, modelhttp.SourceHostsEnv + "=*.example.com", modelSourceBinEnv + "=" + helper}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell failed: %v: %s", err, output)
	}
	seen, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(seen) != "https://gw.example.com\nmodel-source\ncodex\n-c\nmodel_provider=custom\napp-server\n-c\nfeatures.apps=false\n" {
		t.Fatalf("final helper arguments=%s", seen)
	}
}

func TestCodexSourceResolvesLauncherHomeAndWorkingDirectory(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	home, cwd, env := sourceCodexFixture(t, sourceCodexProvider)
	other := t.TempDir()
	writeFile(t, filepath.Join(other, ".codex", "config.toml"), strings.ReplaceAll(sourceCodexProvider, "https://gw.example.com/v1", "https://api.openai.com/v1"))
	input := []string{"codex", "app-server"}
	if got, _ := codexModelSource(input, append(env, "HOME="+other), cwd, home); !reflect.DeepEqual(got, input) {
		t.Fatal("used the node home instead of the launcher home")
	}
	if got, _ := codexModelSource(input, append(env, "HOME="+home, "HOME="+other), cwd, home); !reflect.DeepEqual(got, input) {
		t.Fatal("injected with an ambiguous provider home")
	}
	project := t.TempDir()
	writeFile(t, filepath.Join(project, ".codex", "config.toml"), "model_provider='openai'\n")
	for _, args := range [][]string{{"-C", project}, {"--cd=" + project}, {"-C" + project}} {
		input := append([]string{"codex"}, args...)
		if got, _ := codexModelSource(input, env, cwd, home); !reflect.DeepEqual(got, input) {
			t.Fatal("ignored the launcher's working directory")
		}
	}
}

// A pane's shell has its engine's environment, not the node's: the node's allowlist reaches codex-start through its
// file, and personad launch through launcher.env. Once adopted, codexModelSource injects the source as the node would.
func TestCodexModelSourceAdoptsTheNodesHosts(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "")
	home, cwd, env := sourceCodexFixture(t, sourceCodexProvider)
	if argv, _ := codexModelSource([]string{"codex"}, env, cwd, home); len(argv) != 1 {
		t.Fatalf("injected without an allowlist: %q", argv)
	}
	if got := adoptSourceHosts("  "); got != "" || os.Getenv(modelhttp.SourceHostsEnv) != "" {
		t.Fatalf("an empty allowlist was adopted: %q", got)
	}
	if got := adoptSourceHosts(" *.example.com "); got != modelhttp.SourceHostsEnv+"=*.example.com" {
		t.Fatalf("adopted %q", got)
	}
	argv, outEnv := codexModelSource([]string{"codex"}, env, cwd, home)
	cfg, err := codexSourceConfig(argv, outEnv, cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	bindings := cfg["model_providers"].(map[string]any)["custom"].(map[string]any)["env_http_headers"].(map[string]any)
	for name, value := range map[string]string{modelhttp.RoleHeader: "task_agent", modelhttp.TodoHeader: "todo_123"} {
		variable, _ := bindings[name].(string)
		if envValue(outEnv, variable) != value {
			t.Fatalf("header %s not injected after adopting the allowlist: %v", name, bindings)
		}
	}
}

// The node writes its allowlist into a start that carries a source, and only into one.
func TestSourceHostsForAStart(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "gw.example.com")
	if got := sourceHostsFor(modelhttp.Source{Role: "task_agent", Todo: "todo_1"}); got != "gw.example.com" {
		t.Fatalf("with a source: %q", got)
	}
	if got := sourceHostsFor(modelhttp.Source{}); got != "" {
		t.Fatalf("without a source: %q", got)
	}
	t.Setenv(modelhttp.SourceHostsEnv, "")
	if got := sourceHostsFor(modelhttp.Source{Role: "task_agent"}); got != "" {
		t.Fatalf("no allowlist on the node: %q", got)
	}
}

// codex-start hands the file's allowlist to what it runs, with the source; a pane that had none still gets it.
func TestCodexStartPassesTheNodesHosts(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env(1)")
	}
	for _, tc := range []struct{ name, hosts, want string }{
		{"from the file", "*.example.com", modelhttp.SourceHostsEnv + "=*.example.com"},
		{"none", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "start.json")
			source := modelhttp.Source{Role: "task_agent", Todo: "todo_1"}
			b, _ := json.Marshal(codexStartFile{Source: source, SourceHosts: tc.hosts, Argv: []string{envBin}})
			if err := os.WriteFile(file, b, 0o600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal([]string{file})
			cmd := exec.Command(os.Args[0], "-test.run=^TestCodexStartHelper$")
			cmd.Env = append(withoutEnv(os.Environ(), []string{modelhttp.SourceHostsEnv}), "PERSONA_CODEX_START_HELPER=1", "PERSONA_CODEX_START_ARGS="+string(args))
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var hosts []string
			sourced := false
			for _, kv := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(kv, modelhttp.SourceHostsEnv+"=") {
					hosts = append(hosts, kv)
				}
				sourced = sourced || strings.HasPrefix(kv, modelSourceEnv+"=")
			}
			want := []string{}
			if tc.want != "" {
				want = []string{tc.want}
			}
			if !reflect.DeepEqual(append([]string{}, hosts...), want) || !sourced {
				t.Errorf("allowlist %q (source passed: %v), want %q", hosts, sourced, want)
			}
		})
	}
}

// codexReadsOverrides is the providers Codex makes of argv's -c overrides: it splits a key at every "." and takes each
// part as written, quotes and all, and parses the value as TOML.
func codexReadsOverrides(t *testing.T, argv []string) map[string]any {
	t.Helper()
	config := map[string]any{}
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		if argv[i] != "-c" || i+1 == len(argv) {
			continue
		}
		i++
		key, value, _ := strings.Cut(argv[i], "=")
		var wrapped struct{ Value any }
		if err := toml.Unmarshal([]byte("Value="+value), &wrapped); err != nil {
			t.Fatalf("codex could not read %q: %v", argv[i], err)
		}
		parts := strings.Split(key, ".")
		current := config
		for _, part := range parts[:len(parts)-1] {
			next, _ := current[part].(map[string]any)
			if next == nil {
				next = map[string]any{}
				current[part] = next
			}
			current = next
		}
		current[parts[len(parts)-1]] = wrapped.Value
	}
	providers, _ := config["model_providers"].(map[string]any)
	return providers
}

// A managed Codex launcher's agent_start: the source headers go onto Persona's own provider, which keeps its name.
// They used to name it `model_providers."persona-launcher"`, which Codex reads as another provider with no name, and
// Codex 0.171 would not start: `provider name must not be empty`.
func TestCodexModelSourceKeepsTheLaunchersProviderNamed(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	home, cwd, env := sourceCodexFixture(t, "")
	launcher := []string{"OPENAI_BASE_URL=https://gw.example.com/v1", "OPENAI_API_KEY=example"}
	input := append([]string{"codex"}, codexProviderArgs(nil, launcher, "")...)
	input = append(input, "-m", "example-model", "--", "prompt")
	argv, outEnv := codexModelSource(input, append(env, launcher...), cwd, home)
	if reflect.DeepEqual(argv, input) {
		t.Fatal("no source headers for the launcher's provider")
	}
	for _, arg := range argv {
		if strings.HasPrefix(arg, `model_providers."`) {
			t.Fatalf("a quoted provider key Codex would read as another provider: %q", argv)
		}
	}
	providers := codexReadsOverrides(t, argv)
	if len(providers) != 1 {
		t.Fatalf("codex reads providers %v, want persona-launcher alone", providers)
	}
	provider, _ := providers["persona-launcher"].(map[string]any)
	if name, _ := provider["name"].(string); strings.TrimSpace(name) == "" {
		t.Fatalf("persona-launcher has no name: %v", provider)
	}
	bindings, _ := provider["env_http_headers"].(map[string]any)
	variable, _ := bindings[modelhttp.RoleHeader].(string)
	if envValue(outEnv, variable) != "task_agent" {
		t.Fatalf("the role header is not on persona-launcher: %v", bindings)
	}
	if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "prompt" {
		t.Fatalf("prompt moved: %q", argv)
	}
}

// A provider whose id is no bare key cannot be named in a -c key Codex reads: it gets no source headers rather than a
// provider of its own that has no name.
func TestCodexModelSourceSkipsAProviderCodexCannotName(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	config := strings.ReplaceAll(strings.ReplaceAll(sourceCodexProvider, `model_provider = "custom"`, `model_provider = "gw.custom"`),
		"[model_providers.custom]", `[model_providers."gw.custom"]`)
	home, cwd, env := sourceCodexFixture(t, config)
	input := []string{"codex", "app-server"}
	if got, gotEnv := codexModelSource(input, env, cwd, home); !reflect.DeepEqual(got, input) || !reflect.DeepEqual(gotEnv, env) {
		t.Fatalf("injected for a provider Codex cannot name: %q", got)
	}
	for _, key := range []string{"custom", "persona-launcher", "a_b-9"} {
		if !codexBareKey(key) {
			t.Fatalf("%q is a bare key", key)
		}
	}
	for _, key := range []string{"", "gw.custom", `"custom"`, "a b"} {
		if codexBareKey(key) {
			t.Fatalf("%q is no bare key", key)
		}
	}
}
