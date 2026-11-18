package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// machO is the start of a 64-bit Mach-O program, enough for nativeExecutable.
const machO = "\xcf\xfa\xed\xfe\x07\x00\x00\x01"

// codexShim is the /bin/sh launcher a desktop app bundles in codex-cli/bin.
const codexShim = `#!/bin/sh
set -eu
entry="$0"
while [ -L "$entry" ]; do
  parent=$(CDPATH= cd -P -- "$(dirname -- "$entry")" && pwd)
  entry=$(readlink "$entry")
  case "$entry" in /*) ;; *) entry="$parent/$entry" ;; esac
done
bin_dir=$(CDPATH= cd -P -- "$(dirname -- "$entry")" && pwd)
exec "$bin_dir/../CodexCLI.app/Contents/MacOS/codex" "$@"
`

func writeExecutable(t *testing.T, path, body string) string {
	t.Helper()
	writeFile(t, path, body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// codexOnPath puts a native codex first on PATH, as an installed Codex CLI.
func codexOnPath(t *testing.T, home string) string {
	t.Helper()
	bin := filepath.Join(home, "native-bin")
	exe := writeExecutable(t, filepath.Join(bin, "codex"), machO)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return exe
}

// The effort is inherited only when the codex the pane's shell runs is the Codex CLI or an official launcher that only
// execs it; a wrapper script may set its own CODEX_HOME or profile (#10432). The app-bundled shim stays inherited, or
// every start through it would leave the shared app-server (#4860).
func TestCodexEffortInheritedRunsTheCodexCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Codex starts do not resolve shell functions on Windows")
	}
	app := filepath.Join("Apps", "Agent.app", "Contents", "Resources", "codex-cli")
	shim, cli := filepath.Join(app, "bin", "codex"), filepath.Join(app, "CodexCLI.app", "Contents", "MacOS", "codex")
	npm := "npm/lib/node_modules/@openai/codex/bin/codex.js"
	wrapper := "#!/bin/sh\nCODEX_HOME=\"$HOME/.other-codex\" exec \"$HOME/native/codex\" --profile other \"$@\"\n"
	inPanes := `if [ "${HERDR_ENV-}" = 1 ]; then PATH="$HOME/wrap:$PATH"; fi` + "\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string // executables, relative to the temp home
		links   map[string]string // link → target, relative to the temp home
		rc      string
		pane    []string
		inherit bool
	}{
		{name: "a native codex", files: map[string]string{"bin/codex": machO}, inherit: true},
		{name: "a symlink to a native codex", files: map[string]string{"native/codex": machO}, links: map[string]string{"bin/codex": "native/codex"}, inherit: true},
		{name: "the app-bundled shim", files: map[string]string{shim: codexShim, cli: machO}, links: map[string]string{"bin/codex": shim}, inherit: true},
		{name: "npm's launcher", files: map[string]string{npm: "#!/usr/bin/env node\n"}, links: map[string]string{"bin/codex": npm}, inherit: true},
		{name: "a wrapper script", files: map[string]string{"bin/codex": wrapper, "native/codex": machO}},
		{name: "a symlink to a wrapper script", files: map[string]string{"wrap/codex": wrapper, "native/codex": machO}, links: map[string]string{"bin/codex": "wrap/codex"}},
		{name: "a script dressed as the shim", files: map[string]string{
			shim: "#!/bin/sh\nexport CODEX_HOME=/elsewhere\n" + codexShim[len("#!/bin/sh\n"):], cli: machO,
		}, links: map[string]string{"bin/codex": shim}},
		{name: "a shim without the CLI", files: map[string]string{shim: codexShim}, links: map[string]string{"bin/codex": shim}},
		{name: "no codex on PATH"},
		{name: "a wrapper only in the engine's panes, elsewhere", files: map[string]string{"bin/codex": machO, "wrap/codex": wrapper}, rc: inPanes, inherit: true},
		{name: "a wrapper the engine's panes put first", files: map[string]string{"bin/codex": machO, "wrap/codex": wrapper}, rc: inPanes,
			pane: codexPaneEnv(&herdrHost{})},
	} {
		for _, shell := range []string{"bash", "zsh"} {
			t.Run(tc.name+"/"+shell, func(t *testing.T) {
				if _, err := exec.LookPath(shell); err != nil {
					t.Skip("no " + shell)
				}
				n, home := testNode(t)
				t.Setenv("HOME", home)
				t.Setenv("ZDOTDIR", home)
				t.Setenv("SHELL", "/bin/"+shell)
				t.Setenv("HERDR_ENV", "") // as when the node runs outside the engine
				t.Setenv("PATH", filepath.Join(home, "bin")+string(os.PathListSeparator)+"/usr/bin:/bin")
				writeFile(t, filepath.Join(home, "."+shell+"rc"), tc.rc)
				writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_reasoning_effort = \"high\"\n")
				writeFile(t, filepath.Join(home, "repo", ".git", "HEAD"), "ref: refs/heads/main\n")
				for p, body := range tc.files {
					writeExecutable(t, filepath.Join(home, p), body)
				}
				for link, target := range tc.links {
					mkdir(t, filepath.Dir(filepath.Join(home, link)))
					if err := os.Symlink(filepath.Join(home, target), filepath.Join(home, link)); err != nil {
						t.Fatal(err)
					}
				}
				if got := n.codexEffortInherited(context.Background(), []string{"codex"}, tc.pane, filepath.Join(home, "repo"), "high"); got != tc.inherit {
					t.Fatalf("inherited %v, want %v", got, tc.inherit)
				}
			})
		}
	}
}

// A launcher that names codex by its path is checked the same way, without a shell.
func TestCodexEffortInheritedChecksACodexByItsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not checked on Windows")
	}
	n, home := testNode(t)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_reasoning_effort = \"high\"\n")
	writeFile(t, filepath.Join(home, "repo", ".git", "HEAD"), "ref: refs/heads/main\n")
	native := writeExecutable(t, filepath.Join(home, "native", "codex"), machO)
	script := writeExecutable(t, filepath.Join(home, "wrap", "codex"), "#!/bin/sh\nexec "+native+" --profile other \"$@\"\n")
	for exe, want := range map[string]bool{native: true, script: false, filepath.Join(home, "missing", "codex"): false} {
		if got := n.codexEffortInherited(context.Background(), []string{exe}, nil, filepath.Join(home, "repo"), "high"); got != want {
			t.Errorf("%s: inherited %v, want %v", exe, got, want)
		}
	}
}
