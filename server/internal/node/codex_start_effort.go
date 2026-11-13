package node

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// A purpose's or tier's effort reaches a new Codex as `-c model_reasoning_effort=<effort>` (control.go), but any -c
// keeps a 0.163+ Codex off the shared app-server: it runs one of its own in the pane (codexConfigFlags), so Persona
// gets only the terminal route, and Codex starts up with the pane's environment instead of the daemon's. Where only
// the daemon had the proxy Codex needs, such a start failed before its first turn ("workspace routing discovery timed
// out", #4860). An effort Codex would run at anyway is therefore left out.

// codexProfileKey matches a line that selects a Codex profile (`profile = "…"`, quoted or not), in any table.
var codexProfileKey = lazyre.New(`(?m)^[ \t]*"?profile"?[ \t]*=`)

// codexEffortInherited: the Codex that argv starts in cwd already runs at effort without the flag. True only when sure:
//   - argv runs codex itself (not a managed launcher), changes no home or shell lookup settings, and would join the shared
//     app-server (no own-process flag, no --remote); a bare codex is not a shell function (#4876);
//   - this node's Codex home sets effort at the top level of config.toml, selects no profile and keeps Codex's own
//     project markers;
//   - no project .codex/config.toml Codex may read for cwd names an effort or a profile.
//
// Between the user's file and a -c override, Codex has only the profile and project layers; its managed layers come
// after both and apply either way (codex-rs config_layer_source.rs).
func (n *Node) codexEffortInherited(ctx context.Context, argv []string, cwd, effort string) bool {
	words := argv
	for len(words) > 0 && shellcmd.AssignRe.MatchString(words[0]) {
		key, _, _ := strings.Cut(words[0], "=")
		switch key {
		case "CODEX_HOME", "HOME", "ZDOTDIR", "SHELL", "PATH", "PWD":
			// These can change which config, executable or shell startup files the pane reads.
			return false
		}
		words = words[1:]
	}
	if len(words) == 0 || filepath.Base(words[0]) != "codex" || codexOnRemote(words) || codexOwnProcess(words) != "" {
		return false
	}
	b, err := os.ReadFile(n.codexConfigPath())
	if err != nil || codexProfileKey.Match(b) || bytes.Contains(b, []byte("project_root_markers")) {
		return false
	}
	if v, _ := tomlTopLevel(string(b))["model_reasoning_effort"].(string); v != effort {
		return false
	}
	dirs := []string{cwd}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil && resolved != cwd {
		dirs = append(dirs, resolved) // Codex walks up from the directory as the shell resolved it
	}
	for _, dir := range dirs {
		if !n.noProjectEffort(dir) {
			return false
		}
	}
	if words[0] == "codex" && runtime.GOOS != "windows" {
		// codex-start preserves a same-name shell function. Its flags are not in argv and may select another effort.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := loginShell(ctx, `
if [ -n "${ZSH_VERSION-}" ]; then
  (( $+functions[codex] )) && exit 0
elif [ -n "${BASH_VERSION-}" ]; then
  builtin declare -F codex >/dev/null && exit 0
else
  exit 0
fi
printf '\001persona-codex-unshadowed\001'
`)
		cmd.Dir = cwd
		cmd.Env = append(cmd.Environ(), argv[:len(argv)-len(words)]...)
		killGroupOnCancel(cmd)
		out, err := cmd.Output()
		if err != nil || !bytes.Contains(out, []byte("\x01persona-codex-unshadowed\x01")) {
			return false
		}
	}
	return true
}

// noProjectEffort: none of the project configs Codex may read for dir names an effort or a profile. Codex reads
// .codex/config.toml in dir and each directory above it up to the project's root, the nearest one holding a .git,
// leaving out the Codex home's own folder (codex-rs config/src/loader/mod.rs, discover_project_layers). Without a
// root Codex reads dir's alone; this then looks at every directory above too, which can only keep the flag more
// often. A file that cannot be read counts as naming one.
func (n *Node) noProjectEffort(dir string) bool {
	home := n.codexHome()
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if dot := filepath.Join(d, ".codex"); !sameDir(dot, home) {
			b, err := os.ReadFile(filepath.Join(dot, "config.toml"))
			switch {
			case err == nil && (bytes.Contains(b, []byte("model_reasoning_effort")) || codexProfileKey.Match(b)):
				return false
			case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
				return false
			}
		}
		if codexRootMarker(d) || filepath.Dir(d) == d {
			return true
		}
	}
}

// codexRootMarker: dir has Codex's default project marker, a .git directory holding HEAD or a .git file (a linked
// worktree or a submodule).
func codexRootMarker(dir string) bool {
	git := filepath.Join(dir, ".git")
	st, err := os.Stat(git)
	if err != nil {
		return false
	}
	if !st.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(git, "HEAD"))
	return err == nil
}
