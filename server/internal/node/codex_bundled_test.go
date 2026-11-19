package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// withBundledCodex makes path the ChatGPT app's bundled Codex CLI for the test ("" for none).
func withBundledCodex(t *testing.T, path string) {
	t.Helper()
	old := bundledCodexCLI
	bundledCodexCLI = func() string { return path }
	t.Cleanup(func() { bundledCodexCLI = old })
}

// fakeBundledCodex is an executable named codex in a directory of its own, as the app bundles it.
func fakeBundledCodex(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex-cli", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// launcherScriptOutput is what launcherScript prints with codexPath where codex is and shellNames the shell's
// functions and aliases, nothing else on the machine.
func launcherScriptOutput(aliases, codexPath, shellNames string) string {
	return launcherBegin + strings.Join([]string{"", aliases, codexPath, "", "", "", "", "", shellNames, ""}, "\x1d") + launcherEnd
}

// A Mac whose only Codex is the one the ChatGPT app bundles lists plain codex, run by that path (#10437): its base
// name is codex, so herdr, codex-start and the effort check take it for plain codex.
func TestBundledCodexIsTheCodexLauncher(t *testing.T) {
	cli := fakeBundledCodex(t)
	withBundledCodex(t, cli)
	list, codex, _, _ := mergeLaunchers(launcherScriptOutput("", "", ""))
	i := slices.IndexFunc(list, func(l proto.Launcher) bool { return l.Name == "codex" })
	if i < 0 || list[i].Agent != "codex" || list[i].Source != "zshrc" || !list[i].PromptOptional || !list[i].RunHere {
		t.Fatalf("no codex launcher: %+v", list)
	}
	if !slices.Equal(codex["codex"], []string{cli}) || filepath.Base(codex["codex"][0]) != "codex" {
		t.Fatalf("codex words %v, want [%s]", codex["codex"], cli)
	}
}

// The shell's codex wins over the app's, and so does a function or alias of that name; no bundled CLI, no launcher.
func TestBundledCodexOnlyWithoutTheShellsCodex(t *testing.T) {
	cli := fakeBundledCodex(t)
	withBundledCodex(t, cli)
	if _, codex, _, _ := mergeLaunchers(launcherScriptOutput("", "/usr/local/bin/codex", "")); !slices.Equal(codex["codex"], []string{"codex"}) {
		t.Fatalf("codex on PATH: words %v", codex["codex"])
	}
	if list, codex, _, _ := mergeLaunchers(launcherScriptOutput("", "", "codex\nll")); codex["codex"] != nil ||
		slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "codex" }) {
		t.Fatalf("a codex function: %+v %v", list, codex)
	}
	alias := "codex\x1fcodex\x1f-m\x1fgpt-6\x1f\x1e"
	if _, codex, _, _ := mergeLaunchers(launcherScriptOutput(alias, "", "codex")); !slices.Equal(codex["codex"], []string{"codex", "-m", "gpt-6"}) {
		t.Fatalf("a codex alias: words %v", codex["codex"])
	}
	withBundledCodex(t, "")
	if list, codex, _, _ := mergeLaunchers(launcherScriptOutput("", "", "")); codex["codex"] != nil ||
		slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "codex" }) {
		t.Fatalf("no codex at all: %+v %v", list, codex)
	}
}

// What the node runs for plain codex itself (the launcher check, model lists, a resume after herdr restarts) is the
// bundled CLI's path when that is what the launcher runs.
func TestBundledCodexLauncherCommand(t *testing.T) {
	n := &Node{}
	cli := filepath.Join(t.TempDir(), "Chat GPT", "codex")
	withCodexLaunchers(t, map[string][]string{"codex": {cli}})
	if got := n.launcherCommand("codex"); got != shellQuote(cli) {
		t.Fatalf("launcherCommand: %q, want %q", got, shellQuote(cli))
	}
	if argv, ok := n.codexArgv("codex"); !ok || !slices.Equal(argv, []string{cli}) {
		t.Fatalf("codexArgv: %v %v", argv, ok)
	}
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	if got := n.launcherCommand("codex"); got != "codex" {
		t.Fatalf("codex on PATH: %q", got)
	}
}

// agentExecutable finds the bundled CLI when nothing else is codex (a managed Codex launcher, the model catalog).
func TestAgentExecutableFallsBackToBundledCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	hideMachineAgents(t)
	withBundledCodex(t, "")
	if p, err := agentExecutable("codex"); err == nil {
		t.Fatalf("no codex anywhere, found %q", p)
	}
	cli := fakeBundledCodex(t)
	withBundledCodex(t, cli)
	if p, err := agentExecutable("codex"); err != nil || p != cli {
		t.Fatalf("got %q %v, want %q", p, err, cli)
	}
	if p, err := agentExecutable("claude"); err == nil {
		t.Fatalf("claude is not the bundled codex: %q", p)
	}
}
