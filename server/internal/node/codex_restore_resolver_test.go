package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The unmarked pane has a wrapper. Only the marked resolver's rc puts the CLI
// first on PATH. Exercise the effort decision, the real start helper and the
// command herdr will evaluate after a restart.
func TestCodexStartAndHerdrRestoreUseEffortProbeResolver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interactive shell resolution is Unix-only")
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			sh, err := exec.LookPath(shell)
			if err != nil {
				t.Skip("no " + shell)
			}
			withPaneShell(t, sh)
			n, home := testNode(t)
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			t.Setenv("SHELL", sh)
			t.Setenv("HERDR_ENV", "")
			t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
			t.Setenv("PERSONA_CODEX_START", "inherited")
			t.Setenv("PERSONA_AGENT_SANDBOX", "off")
			t.Setenv("PATH", filepath.Dir(sh)+":/usr/bin:/bin")
			cwd := mkdir(t, filepath.Join(home, "project"))
			writeFile(t, filepath.Join(cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
			writeFile(t, n.codexConfigPath(), "model_reasoning_effort = \"high\"\n")

			// codexCLIExecutable recognizes npm's official launcher by its resolved path.
			cli := writeExecutable(t, filepath.Join(home, "node_modules/@openai/codex/bin/codex.js"), `#!/bin/sh
printf '%s\0' "$PERSONA_CODEX_START" "${HERDR_AGENT-unset}" "${ALIAS_SETTING-unset}" "$@" > "$HOME/received"
`)
			if err := os.Symlink(cli, filepath.Join(mkdir(t, filepath.Join(home, "cli")), "codex")); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(home, "wrapper/codex"), "#!/bin/sh\nexit 45\n")
			writeFile(t, filepath.Join(home, "."+shell+"rc"), `
if [ "${PERSONA_CODEX_START-}" = 1 ]; then
  export PATH="$HOME/cli:$PATH"
else
  export PATH="$HOME/wrapper:$PATH"
fi
if [ "${HERDR_ENV-}" != 1 ] || [ "${PERSONA_CODEX_START-}" = 1 ]; then
  alias cx='ALIAS_SETTING=alias-value PERSONA_CODEX_START=alias-default codex --model alias-model'
fi
export PERSONA_CODEX_START=changed-by-rc
`)
			n.bin = writeExecutable(t, filepath.Join(home, "test bin/personad"), `#!/bin/sh
if [ "$1" = launch ]; then exec "$HOME/cli/codex" "$@"; fi
exec `+shellQuote(os.Args[0])+" -test.run=^TestCodexStartHelper$ -- \"$@\"\n")
			n.setManaged([]proto.ManagedLauncher{{Name: "managed", Agent: "codex"}, {Name: "cx", Agent: "codex"}})
			t.Setenv("PERSONA_CODEX_START_HELPER", "1")
			for _, pane := range [][]string{nil, codexPaneEnv(&herdrHost{})} {
				if !n.codexEffortInherited(t.Context(), []string{"codex"}, pane, cwd, "high") {
					t.Fatalf("effort probe selected the wrapper with pane env %q", pane)
				}
			}

			runPane := func(line string, want []string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, sh, "-ic", line)
				cmd.Dir = cwd
				cmd.Env = append(cmd.Environ(), "HERDR_ENV=1", "TERM_PROGRAM=herdr")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("pane command failed: %v\n%s", err, output)
				}
				file := filepath.Join(home, "received")
				got := strings.Split(strings.TrimSuffix(readFile(t, file), "\x00"), "\x00")
				if !slices.Equal(got, want) {
					t.Fatalf("CLI received %q, want %q", got, want)
				}
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			out, err := scanLaunchers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, launchers, _, _ := mergeLaunchers(string(out))
			withCodexLaunchers(t, launchers)
			for _, tc := range []struct {
				name string
				want []string
			}{
				{"codex", []string{"1", "codex", "unset"}},
				{"cx", []string{"1", "codex", "alias-value", "--model", "alias-model"}},
				{"managed", []string{"1", "codex", "unset", "launch", "managed"}},
			} {
				words, ok := n.codexArgv(tc.name)
				if !ok {
					t.Fatalf("missing launcher %s", tc.name)
				}
				file := filepath.Join(home, "start.json")
				b, err := json.Marshal(codexStartFile{Argv: words, Prompt: "first message", Agent: "codex", Dir: cwd})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, file, string(b))
				runPane(dquote(n.bin)+" codex-start "+dquote(file), append(slices.Clone(tc.want), "--", "first message"))

				const rules = "Keep the user's \"$HOME\" and `pwd` literal."
				argv, ok := n.agentResumeArgv(agentLaunch{Agent: "codex", Launcher: tc.name, TaskInstructions: rules}, startID)
				if !ok || len(argv) != 2 || argv[0] != "eval" || strings.Contains(argv[1], "alias-value") {
					t.Fatalf("invalid restore command for %s: %q", tc.name, argv)
				}
				runPane(argv[1], append(tc.want, "-c", codexTaskConfig(rules), "resume", startID))
			}
			// A node can rebuild the persisted command before its first launcher scan.
			withCodexLaunchers(t, nil)
			argv, ok := n.agentResumeArgv(agentLaunch{Agent: "codex", Launcher: "codex"}, startID)
			if !ok || len(argv) != 2 || !strings.Contains(argv[1], "codex-start --herdr-resume codex resume "+startID) {
				t.Fatalf("cold stock restore depends on discovery: %q", argv)
			}
			runPane(argv[1], []string{"1", "codex", "unset", "resume", startID})

		})
	}
}
