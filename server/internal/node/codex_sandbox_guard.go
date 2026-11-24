package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// Codex protects these children as read-only within each workspace-write root.
// A symlink here makes the Linux sandbox fail before any command can execute.
func guardCodexWritableSymlinks(ctx context.Context, argv, env []string, cwd, home string) error {
	if codexOnRemote(argv) {
		return nil
	}
	roots := []string{cwd}
	var overrideRoots []string
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		flag, value, hasValue := strings.Cut(argv[i], "=")
		if flag == "-c" || flag == "--config" {
			if !hasValue && i+1 < len(argv) {
				i++
				value = argv[i]
			}
			if key, raw, ok := strings.Cut(value, "="); ok && strings.TrimSpace(key) == "sandbox_workspace_write.writable_roots" {
				overrideRoots = tomlPathArray(raw)
			}
			continue
		}
		if flag != "--add-dir" && flag != "--cd" && flag != "-C" {
			if codexValueFlags[flag] && !hasValue {
				i++
			}
			continue
		}
		if !hasValue && i+1 < len(argv) {
			i++
			value = argv[i]
		}
		if flag == "--add-dir" {
			roots = append(roots, value)
		} else {
			roots[0] = value
		}
	}
	effective := proto.ExpandHome(roots[0], home)
	if !filepath.IsAbs(effective) {
		effective = filepath.Join(cwd, effective)
	}
	if resolved, err := filepath.EvalSymlinks(effective); err == nil {
		effective = resolved
	}
	roots[0] = effective
	configs := codexGuardConfigPaths(env, effective, home)
	reason := codexSandboxModeReason(argv, env, home, configs...)
	if reason == nil || reason.Key != "node-doctor.sandbox_codex_own" || reason.Args["mode"] != "workspace-write" {
		return nil
	}
	var configuredRoots []string
	for _, path := range configs {
		if body, err := os.ReadFile(path); err == nil {
			if parsed := codexConfigWritableRoots(string(body)); parsed != nil {
				configuredRoots = parsed
			}
		}
	}
	if overrideRoots != nil {
		configuredRoots = overrideRoots
	}
	roots = append(roots, configuredRoots...)
	for _, root := range roots {
		root = proto.ExpandHome(root, home)
		if root == "" {
			root = cwd
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(effective, root)
		}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		for _, name := range []string{".aws", ".codex", ".agents", ".git"} {
			info, err := os.Lstat(filepath.Join(root, name))
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return refusal("codex_writable_symlink", i18n.T(i18n.From(ctx), "rpc-errors.codex_writable_symlink", i18n.Args{"name": name}), nil)
			}
		}
	}
	return nil
}

func (n *Node) checkCodexWritableSymlinks(ctx context.Context, launch host.Launcher, launcher string, argv []string, cwd string) error {
	env := append(os.Environ(), codexPaneEnv(launch)...)
	env = append(withoutEnv(env, []string{"CODEX_HOME"}), "CODEX_HOME="+n.codexHome())
	for len(argv) > 0 && shellcmd.AssignRe.MatchString(argv[0]) {
		key, _, _ := strings.Cut(argv[0], "=")
		env = append(withoutEnv(env, []string{key}), argv[0])
		argv = argv[1:]
	}
	if managed, ok := n.managedLauncher(launcher); ok && len(argv) >= 3 && argv[0] == n.bin && argv[1] == "launch" {
		if managed.Env != "" {
			return nil
		} // final provider environment is checked by Launch
		argv = append(append([]string{"codex"}, managed.Args...), argv[3:]...)
		if envValue(env, codexBypassEnv) == "1" {
			argv = codexBypassArgs(argv)
		}
	}
	// Use the node's scoped config home for this preflight. The native launch
	// repeats it with the shell/provider's final environment before exec.
	return guardCodexWritableSymlinks(ctx, argv, env, cwd, n.home)
}

// Apply ordinary config layers from the user home to the closest project directory.
func codexGuardConfigPaths(env []string, cwd, home string) []string {
	codexHome := proto.ExpandHome(envValue(env, "CODEX_HOME"), home)
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	paths := []string{filepath.Join(codexHome, "config.toml")}
	var project []string
	for dir := cwd; dir != ""; dir = filepath.Dir(dir) {
		file := filepath.Join(dir, ".codex", "config.toml")
		if file != paths[0] {
			project = append(project, file)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for i := len(project) - 1; i >= 0; i-- {
		paths = append(paths, project[i])
	}
	return paths
}

func codexConfigWritableRoots(body string) []string {
	inWorkspace := false
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inWorkspace = line == "[sandbox_workspace_write]"
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && inWorkspace && strings.TrimSpace(key) == "writable_roots" {
			for j := i + 1; ; j++ {
				if roots := tomlPathArray(value); roots != nil {
					return roots
				}
				if j >= len(lines) {
					break
				}
				value += "\n" + lines[j]
			}
		}
	}
	return nil
}

// Common TOML path arrays use basic or literal strings, optionally trailing commas.
func tomlPathArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") {
		return nil
	}
	raw = strings.TrimSpace(raw[1:])
	paths := []string{}
	skip := func() {
		raw = strings.TrimSpace(raw)
		for strings.HasPrefix(raw, "#") {
			_, rest, ok := strings.Cut(raw, "\n")
			if !ok {
				raw = ""
				return
			}
			raw = strings.TrimSpace(rest)
		}
	}
	skip()
	for !strings.HasPrefix(raw, "]") {
		if raw == "" || raw[0] != '\'' && raw[0] != '"' {
			return nil
		}
		quote, end := raw[0], 1
		for end < len(raw) {
			if quote == '"' && raw[end] == '\\' {
				end += 2
				continue
			}
			if raw[end] == quote {
				break
			}
			end++
		}
		if end >= len(raw) {
			return nil
		}
		value, ok := tomlScalar(raw[:end+1])
		path, isString := value.(string)
		if !ok || !isString {
			return nil
		}
		paths = append(paths, path)
		raw = strings.TrimSpace(raw[end+1:])
		skip()
		if strings.HasPrefix(raw, ",") {
			raw = strings.TrimSpace(raw[1:])
			skip()
		} else if !strings.HasPrefix(raw, "]") {
			return nil
		}
	}
	return paths
}
