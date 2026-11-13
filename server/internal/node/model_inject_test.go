package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
)

// hostileModels are what a typed model id must never turn into: more than one argument, a flag, a command run by the
// shell on the way.
var hostileModels = []string{
	"x --dangerously-skip-permissions", "-p", "--model", "$(touch pwned)", "${HOME}", "`touch pwned`", "x; touch pwned",
	"x' ; touch pwned; '", `x" ; touch pwned; "`, "x\ntouch pwned", "x && touch pwned", "x | touch pwned", "x > pwned", "*",
	"~", `x\`, "x\x00y", "opus ",
}

// A model id the user typed (#1644) is refused before it reaches anything when it is not an id: a start's check (which
// every start, the assistant's included, runs first), the launcher check, and the `/model` line typed into a running
// Claude Code.
func TestHostileModelIsRefusedEverywhere(t *testing.T) {
	for _, m := range hostileModels {
		if err := (modelExecution{Model: m}).check("claude"); err == nil || err.Error() != "invalid model" {
			t.Errorf("check(%q) = %v, want invalid model", m, err)
		}
		if _, err := modelCommand(m); err == nil && strings.TrimSpace(m) != "opus" {
			t.Errorf("modelCommand(%q) built a line", m)
		}
		n, _ := testNode(t)
		if _, err := n.launcherModelCheck(context.Background(), "claude", m); err == nil {
			t.Errorf("launcherModelCheck(%q) passed", m)
		}
	}
	for _, m := range []string{"", "opus", "opus[1m]", "vendor/model-x", "qwen3:32b", "claude-x@20250101",
		"~anthropic/claude-opus-latest", "~anthropic/claude-sonnet-latest[1m]"} {
		if err := (modelExecution{Model: m}).check("claude"); err != nil {
			t.Errorf("check(%q) = %v", m, err)
		}
	}
}

// And the quoting holds on its own: the command herdr's pane runs for a start (shellQuote) and the resume command herdr
// runs after a restart (eval of dquote'd words) give the agent the model as exactly one argument after --model, with
// nothing else run, for real ids (a gateway's ~-alias is not expanded to a home directory) and for hostile ones that the
// rule would have refused.
func TestModelIsOneQuotedArgumentOnTheCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	withPaneShell(t, sh)
	models := append([]string{"opus[1m]", "vendor/model-x", "qwen3:32b", "claude-x@20250101",
		"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-x-v1:0",
		"~anthropic/claude-opus-latest", "~anthropic/claude-sonnet-latest[1m]", "~root"}, hostileModels...)
	for _, m := range models {
		if strings.ContainsRune(m, 0) {
			continue // no shell argument holds a NUL; the check refuses it above
		}
		for _, resume := range []bool{false, true} {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			fakeProgram(t, filepath.Join(bin, "claude"), `printf '%s\0' "$@" >"$HOME/ran"`)
			n, _ := testNode(t)
			n.dir = filepath.Join(home, "persona")
			spec := claudeLaunch{Launcher: "claude", Model: m}
			line := n.agentCommand(n.claudeCommandLine(spec, "--session-id "+startID, shellQuote), shellQuote, sh)
			if resume {
				argv, ok := n.claudeResumeArgv(spec, startID)
				if !ok {
					if strings.Contains(m, "'") || strings.IndexFunc(m, unicode.IsControl) >= 0 {
						continue // herdr refuses an apostrophe or a control character: no resume command is given at all
					}
					t.Fatalf("%q: no resume command", m)
				}
				line = argv[0] + " " + shellQuote(argv[1])
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd := exec.CommandContext(ctx, sh, "-c", line)
			cmd.Dir = home
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("%q (resume %v): %s: %v: %s", m, resume, line, err, out)
			}
			raw, err := os.ReadFile(filepath.Join(home, "ran"))
			if err != nil {
				t.Fatalf("%q (resume %v): claude did not run: %s", m, resume, line)
			}
			args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			i := slices.Index(args, "--model")
			if i < 0 || i+1 >= len(args) || args[i+1] != m {
				t.Errorf("%q (resume %v): claude got %q from %s", m, resume, args, line)
				continue
			}
			rest := slices.Delete(slices.Clone(args), i, i+2) // every other argument is Persona's own
			if slices.Contains(rest, "--model") || slices.Contains(rest, "--dangerously-skip-permissions") || slices.Contains(rest, "-p") {
				t.Errorf("%q (resume %v): part of the model became an argument: %q", m, resume, args)
			}
			if _, err := os.Stat(filepath.Join(home, "pwned")); err == nil {
				t.Errorf("%q (resume %v): the shell ran part of the model: %s", m, resume, line)
			}
		}
	}
}
