package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/pelletier/go-toml/v2"
)

const modelSourceBinEnv = "PERSONA_MODEL_SOURCE_BIN"

func modelSourceEnvironment(source modelhttp.Source, bin string) []string {
	raw, _ := json.Marshal(source.Safe())
	return []string{modelSourceEnv + "=" + string(raw), modelSourceBinEnv + "=" + bin}
}

// sourceHostsFor is the node's allowlist for a start that carries source, "" for one that does not.
func sourceHostsFor(source modelhttp.Source) string {
	if source.Role == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(modelhttp.SourceHostsEnv))
}

// adoptSourceHosts makes hosts, the node's allowlist handed to a process in a pane (codex-start's file, personad
// launch's answer), this process's own: the pane's shell has its engine's environment, which need not carry it, and
// codexModelSource reads it from the process. It answers the NAME=value to pass on to what runs next, "" for none.
func adoptSourceHosts(hosts string) string {
	hosts = strings.TrimSpace(hosts)
	if hosts == "" {
		return ""
	}
	if err := os.Setenv(modelhttp.SourceHostsEnv, hosts); err != nil {
		return ""
	}
	return modelhttp.SourceHostsEnv + "=" + hosts
}

// ModelSourceLaunch is the final CLI hop, after shell functions set their environment and provider flags.
// Provider credentials remain in memory/environment; only header-to-environment bindings reach argv.
func ModelSourceLaunch(args []string) error {
	if len(args) == 0 || args[0] != "codex" {
		return errors.New("model-source: expected codex arguments")
	}
	exe, err := agentExecutable("codex")
	if err != nil {
		return err
	}
	env := os.Environ()
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	argv, env := codexModelSource(args, env, cwd, home)
	if err := guardCodexWritableSymlinks(context.Background(), argv, env, cwd, home); err != nil {
		return err
	}
	exe, env, err = codexStartProgram(exe, true, env)
	if err != nil {
		return err
	}
	return osproc.Exec(exe, argv, env)
}

func codexModelSource(argv, env []string, cwd, home string) ([]string, []string) {
	var source modelhttp.Source
	if os.Getenv(modelhttp.SourceHostsEnv) == "" || json.Unmarshal([]byte(envValue(env, modelSourceEnv)), &source) != nil || source.Role == "" || codexOnRemote(argv) {
		return argv, env
	}
	config, err := codexSourceConfig(argv, env, cwd, home)
	if err != nil {
		return argv, env // unknown effective destination: never inject speculatively
	}
	provider, _ := config["model_provider"].(string)
	if provider == "" || provider == "openai" {
		// Codex ignores ordinary header overrides for its built-in provider. Keep its authentication/routing intact.
		return argv, env
	}
	// Codex splits a -c key at every "." and takes each part as written, quotes and all: `model_providers."custom"`
	// is another provider, named `"custom"` with its quotes, that has no name, and Codex 0.171 will not start
	// (`provider name must not be empty`). So the headers' flags name the provider bare, and one whose id is no bare
	// key gets none.
	if !codexBareKey(provider) {
		return argv, env
	}
	providers, _ := config["model_providers"].(map[string]any)
	selected, _ := providers[provider].(map[string]any)
	base, _ := selected["base_url"].(string)
	if !modelhttp.SourceAllowed(base) {
		return argv, env
	}
	bindings := map[string]string{}
	if headers, ok := selected["env_http_headers"].(map[string]any); ok {
		for name, value := range headers {
			if variable, ok := value.(string); ok && !modelhttp.SourceHeader(name) {
				bindings[http.CanonicalHeaderKey(name)] = variable
			}
		}
	}
	// Move literal headers into environment bindings so an existing credential never enters argv.
	literals, _ := selected["http_headers"].(map[string]any)
	values := map[string]string{}
	for name, value := range literals {
		if text, ok := value.(string); ok && !modelhttp.SourceHeader(name) {
			// Codex's environment bindings take precedence over literal headers.
			if variable := bindings[http.CanonicalHeaderKey(name)]; variable != "" && envValue(env, variable) != "" {
				continue
			}
			values[http.CanonicalHeaderKey(name)] = text
		}
	}
	source = source.Safe()
	values[modelhttp.RoleHeader] = source.Role
	if source.Todo != "" {
		values[modelhttp.TodoHeader] = source.Todo
	}
	if source.Routine != "" {
		values[modelhttp.RoutineHeader] = source.Routine
	}
	if source.Session != "" {
		values[modelhttp.SessionHeader] = source.Session
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		variable := "PERSONA_SOURCE_HEADER_" + strconv.Itoa(i)
		for slices.Contains(envNames(env), variable) {
			variable += "_"
		}
		env = append(env, variable+"="+values[name])
		bindings[http.CanonicalHeaderKey(name)] = variable
	}
	names = names[:0]
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	var entries []string
	for _, name := range names {
		entries = append(entries, tomlString(name)+"="+tomlString(bindings[name]))
	}
	prefix := "model_providers." + provider
	flags := []string{"-c", prefix + ".http_headers={}", "-c", prefix + ".env_http_headers={" + strings.Join(entries, ",") + "}"}
	// These global options also force a local CLI off a shared daemon, keeping attribution process-specific.
	at := len(argv)
	for i, arg := range argv {
		if arg == "--" || arg == "-" {
			at = i
			break
		}
	}
	out := append(slices.Clone(argv[:at]), flags...)
	return append(out, argv[at:]...), env
}

// codexBareKey: key is a TOML bare key (letters, digits, "_" and "-"), the only kind of part Codex's -c key reads as
// TOML would.
func codexBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// codexSourceConfig resolves only local configuration layers. It never logs their contents or writes user files.
func codexSourceConfig(argv, env []string, cwd, home string) (map[string]any, error) {
	for _, key := range []string{"HOME", "USERPROFILE", "CODEX_HOME"} {
		seen, found := "", false
		for _, entry := range env {
			name, value, _ := strings.Cut(entry, "=")
			if name == key {
				if found && seen != value {
					return nil, errors.New("ambiguous provider configuration environment")
				}
				seen, found = value, true
			}
		}
	}

	if actual := envValue(env, "HOME"); actual != "" {
		home = actual
	}
	if actual := envValue(env, "USERPROFILE"); actual != "" && envValue(env, "HOME") == "" {
		home = actual
	}
	startDir := cwd
	for i := 1; i < len(argv); i++ {
		arg, selected := argv[i], ""
		if arg == "--" {
			break
		}
		switch {
		case (arg == "-C" || arg == "--cd") && i+1 < len(argv):
			i++
			selected = argv[i]
		case strings.HasPrefix(arg, "--cd="):
			selected = strings.TrimPrefix(arg, "--cd=")
		case strings.HasPrefix(arg, "-C") && len(arg) > 2:
			selected = strings.TrimPrefix(strings.TrimPrefix(arg, "-C"), "=")
		case codexValueFlags[arg] && i+1 < len(argv):
			i++
		}
		if selected != "" {
			if strings.HasPrefix(selected, "~") {
				return nil, errors.New("unknown provider working directory")
			}
			if !filepath.IsAbs(selected) {
				selected = filepath.Join(startDir, selected)
			}
			cwd = selected
		}
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	cwd = resolved
	// Legacy administrator overlays can change the endpoint after user/CLI configuration.
	// Their precedence is runtime-owned; do not attach attribution when one is present.
	if _, err := os.Stat("/etc/codex/managed_config.toml"); err == nil || !os.IsNotExist(err) {
		return nil, errors.New("administrator provider policy is runtime-owned")
	}
	config := map[string]any{}
	read := func(path string) error {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var layer map[string]any
		if err := toml.Unmarshal(raw, &layer); err != nil {
			return errors.New("unreadable model provider configuration")
		}
		mergeSourceConfig(config, layer)
		return nil
	}
	codexHome := envValue(env, "CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return nil, errors.New("relative provider configuration home")
	}
	if err := read("/etc/codex/config.toml"); err != nil {
		return nil, err
	}
	if err := read(filepath.Join(codexHome, "config.toml")); err != nil {
		return nil, err
	}
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		if filepath.Join(dir, ".codex") != codexHome {
			raw, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			if err == nil {
				var layer map[string]any
				if toml.Unmarshal(raw, &layer) != nil {
					return nil, errors.New("unknown project provider configuration")
				}
				for _, key := range []string{"model_provider", "model_providers", "profile", "profiles"} {
					if _, present := layer[key]; present {
						return nil, errors.New("project provider trust is runtime-owned")
					}
				}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil || filepath.Dir(dir) == dir {
			break
		}
	}
	profileFlag := ""
	var overrides []string
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		switch {
		case (arg == "-c" || arg == "--config") && i+1 < len(argv):
			i++
			overrides = append(overrides, argv[i])
		case strings.HasPrefix(arg, "--config="):
			overrides = append(overrides, strings.TrimPrefix(arg, "--config="))
		case (arg == "-p" || arg == "--profile") && i+1 < len(argv):
			i++
			profileFlag = argv[i]
		case strings.HasPrefix(arg, "--profile="):
			profileFlag = strings.TrimPrefix(arg, "--profile=")
		case strings.HasPrefix(arg, "-c") && arg != "-c", strings.HasPrefix(arg, "-p") && arg != "-p":
			return nil, errors.New("compact provider options are runtime-owned")
		}
	}
	for _, override := range overrides {
		if err := setSourceOverride(config, override); err != nil {
			return nil, err
		}
	}
	profile, _ := config["profile"].(string)
	if profileFlag != "" {
		profile = profileFlag
	}
	if profile != "" {
		profiles, _ := config["profiles"].(map[string]any)
		layer, _ := profiles[profile].(map[string]any)
		if layer == nil {
			return nil, errors.New("unknown model provider profile")
		}
		mergeSourceConfig(config, layer)
	}
	for _, override := range overrides {
		if err := setSourceOverride(config, override); err != nil {
			return nil, err
		}
	}
	return config, nil
}

func mergeSourceConfig(dst, src map[string]any) {
	for key, value := range src {
		if table, ok := value.(map[string]any); ok {
			current, _ := dst[key].(map[string]any)
			if current == nil {
				current = map[string]any{}
			}
			mergeSourceConfig(current, table)
			dst[key] = current
		} else {
			dst[key] = value
		}
	}
}

func setSourceOverride(config map[string]any, raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return errors.New("invalid model provider override")
	}
	// Let TOML parse quoted dotted keys, including provider names containing punctuation.
	var path map[string]any
	if toml.Unmarshal([]byte(key+"=0"), &path) != nil {
		return errors.New("invalid model provider override")
	}
	var wrapped struct{ Value any }
	if toml.Unmarshal([]byte("Value="+value), &wrapped) != nil {
		wrapped.Value = value // Codex accepts unquoted strings in -c values.
	}
	current := config
	for {
		for name, entry := range path {
			if nested, ok := entry.(map[string]any); ok {
				next, _ := current[name].(map[string]any)
				if next == nil {
					next = map[string]any{}
					current[name] = next
				}
				current, path = next, nested
			} else {
				current[name] = wrapped.Value
				return nil
			}
			break
		}
	}
}
