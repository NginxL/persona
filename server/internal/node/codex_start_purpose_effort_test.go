package node

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexStartUsesThePurposeEffort(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex", "-c", "model_reasoning_effort=low"}})
	n, _, _, _ := codexStartHerdr(t, 0)
	_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "cx", "prompt": "hello", "effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "-c", "model_reasoning_effort=low", "-c", "model_reasoning_effort=high"}
	if strings.Join(ran, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("runs %q, want %q", ran, want)
	}
}

func TestCodexStartRejectsInvalidPurposeEffort(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	n, _, _, cmds := codexStartHerdr(t, 0)
	_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "hello", "effort": "invalid"})
	if err == nil || !strings.Contains(err.Error(), "unknown effort") {
		t.Fatalf("invalid effort: %v", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("started with invalid effort: %q", *cmds)
	}
}

// An effort Codex's own config already sets is not passed again: the -c would only take the start off the shared
// app-server, into a process with the pane's environment (#4860). A different effort is still passed.
func TestCodexStartLeavesOutAnEffortCodexAlreadyRuns(t *testing.T) {
	for _, tc := range []struct {
		effort string
		model  string
		want   []string
	}{
		{"high", "", []string{"codex"}},
		{"max", "", []string{"codex", "-c", "model_reasoning_effort=max"}},
		{"high", "gpt-6", []string{"codex", "--model", "gpt-6"}},
		{"max", "gpt-6", []string{"codex", "--model", "gpt-6", "-c", "model_reasoning_effort=max"}},
	} {
		t.Run(tc.effort+"/"+tc.model, func(t *testing.T) {
			withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
			n, _, cwd, _ := codexStartHerdr(t, 0)
			t.Setenv("HOME", n.home)
			t.Setenv("ZDOTDIR", n.home)
			writeFile(t, filepath.Join(cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
			writeFile(t, filepath.Join(n.codexHome(), "config.toml"), "model = \"gpt-6\"\nmodel_reasoning_effort = \"high\"\n\n[features]\nhooks = true\n")
			_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "hello", "effort": tc.effort, "model": tc.model})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(ran, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("runs %q, want %q", ran, tc.want)
			}
		})
	}
}

func TestCodexStartKeepsTierEffortOverNativeShellFunction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Codex starts do not resolve shell functions on Windows")
	}
	for _, shell := range []string{"bash", "zsh"} {
		for _, flags := range []string{"-p fast", "-c model_reasoning_effort=low"} {
			t.Run(shell+"/"+flags, func(t *testing.T) {
				if _, err := exec.LookPath(shell); err != nil {
					t.Skip("no " + shell)
				}
				withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
				n, _, cwd, _ := codexStartHerdr(t, 0)
				t.Setenv("HOME", n.home)
				t.Setenv("ZDOTDIR", n.home)
				t.Setenv("SHELL", "/bin/"+shell)
				writeFile(t, filepath.Join(n.home, "."+shell+"rc"),
					`codex() { HTTPS_PROXY=http://proxy.example:8080 command codex `+flags+` "$@"; }`+"\n")
				writeFile(t, filepath.Join(cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
				writeFile(t, filepath.Join(n.codexHome(), "config.toml"), "model_reasoning_effort = \"high\"\n")
				_, err := request(t, n, "session.start", map[string]any{
					"cwd": "~/proj", "launcher": "codex", "prompt": "hello", "effort": "high",
				})
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"codex", "-c", "model_reasoning_effort=high"}
				if strings.Join(ran, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("runs %q, want tier effort after the shell function's flags: %q", ran, want)
				}
			})
		}
	}
}

func TestCodexEffortInheritedKeepsTheFlagWhenShellResolutionIsUncertain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Codex starts do not resolve shell functions on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	for _, tc := range []struct {
		name, rc string
		cancel   bool
	}{
		{name: "shell startup failed", rc: "exit 7\n"},
		{name: "caller canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, home := testNode(t)
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			t.Setenv("SHELL", "/bin/bash")
			writeFile(t, filepath.Join(home, ".bashrc"), tc.rc)
			writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_reasoning_effort = \"high\"\n")
			cwd := mkdir(t, filepath.Join(home, "repo"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if n.codexEffortInherited(ctx, []string{"codex"}, cwd, "high") {
				t.Fatal("uncertain shell resolution dropped the tier effort")
			}
		})
	}
}

func TestCodexEffortInheritedChecksTheLaunchEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Codex starts do not resolve shell functions on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	n, home := testNode(t)
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/bash")
	writeFile(t, filepath.Join(home, ".bashrc"), `
if [ "${PERSONA_TEST_CODEX_FUNCTION-}" = yes ] && [ "$PWD" = "$HOME/repo" ]; then
  codex() { command codex -p fast "$@"; }
fi
`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_reasoning_effort = \"high\"\n")
	cwd := mkdir(t, filepath.Join(home, "repo"))
	for _, on := range []bool{false, true} {
		argv := []string{"codex"}
		if on {
			argv = append([]string{"PERSONA_TEST_CODEX_FUNCTION=yes"}, argv...)
		}
		if inherited := n.codexEffortInherited(context.Background(), argv, cwd, "high"); inherited == on {
			t.Fatalf("function enabled %v: inherited %v", on, inherited)
		}
	}
}

// codexEffortInherited holds only when nothing between the user's config.toml and a -c override could choose another
// effort, and the launcher would otherwise run on the shared app-server; any doubt keeps the flag.
func TestCodexEffortInherited(t *testing.T) {
	const high = "model_reasoning_effort = \"high\"\n"
	low := map[string]string{"repo/.codex/config.toml": "model_reasoning_effort = \"low\"\n"}
	for _, tc := range []struct {
		name    string
		argv    []string
		user    string            // ~/.codex/config.toml; "-" for none
		files   map[string]string // more files, relative to the temp home
		cwd     string            // relative to the temp home; "" for repo, a Git checkout
		inherit bool
	}{
		{name: "plain codex", argv: []string{"codex"}, user: high, inherit: true},
		{name: "an alias with a model and its environment", argv: []string{"HTTPS_PROXY=http://proxy.example:8080", "codex", "--model", "gpt-6"}, user: high, inherit: true},
		{name: "codex by its path", argv: []string{"/usr/local/bin/codex"}, user: high, inherit: true},
		{name: "a project config without an effort", argv: []string{"codex"}, user: high, files: map[string]string{"repo/.codex/config.toml": "[features]\nhooks = true\n"}, inherit: true},
		{name: "a config above the project's root", argv: []string{"codex"}, user: high, cwd: "outer/repo2",
			files: map[string]string{"outer/.codex/config.toml": "model_reasoning_effort = \"low\"\n", "outer/repo2/.git": "gitdir: /elsewhere\n"}, inherit: true},
		{name: "another effort", argv: []string{"codex"}, user: "model_reasoning_effort = \"low\"\n"},
		{name: "no effort in config.toml", argv: []string{"codex"}, user: "model = \"gpt-6\"\n"},
		{name: "no config.toml", argv: []string{"codex"}, user: "-"},
		{name: "the effort only in a table", argv: []string{"codex"}, user: "[profiles.deep]\n" + high},
		{name: "a profile", argv: []string{"codex"}, user: "profile = \"deep\"\n" + high + "[profiles.deep]\nmodel_reasoning_effort = \"low\"\n"},
		{name: "its own project markers", argv: []string{"codex"}, user: high + "project_root_markers = [\".hg\"]\n"},
		{name: "a -c of the launcher", argv: []string{"codex", "-c", "model=\"gpt-6\""}, user: high},
		{name: "--no-daemon", argv: []string{"codex", "--no-daemon"}, user: high},
		{name: "--profile", argv: []string{"codex", "--profile", "deep"}, user: high},
		{name: "--remote", argv: []string{"codex", "--remote", "ws://127.0.0.1:9"}, user: high},
		{name: "its own CODEX_HOME", argv: []string{"CODEX_HOME=/elsewhere", "codex"}, user: high},
		{name: "another home", argv: []string{"HOME=/elsewhere", "codex"}, user: high},
		{name: "other shell startup files", argv: []string{"ZDOTDIR=/elsewhere", "codex"}, user: high},
		{name: "another shell", argv: []string{"SHELL=/elsewhere", "codex"}, user: high},
		{name: "another executable path", argv: []string{"PATH=/elsewhere", "codex"}, user: high},
		{name: "another working directory environment", argv: []string{"PWD=/elsewhere", "codex"}, user: high},
		{name: "a managed launcher", argv: []string{"/opt/persona/personad", "launch", "team-codex"}, user: high},
		{name: "another agent", argv: []string{"traex"}, user: high},
		{name: "the project's config sets an effort", argv: []string{"codex"}, user: high, files: low},
		{name: "a config between the directory and the root sets one", argv: []string{"codex"}, user: high, cwd: "repo/pkg/sub", files: low},
		{name: "the project's config selects a profile", argv: []string{"codex"}, user: high, files: map[string]string{"repo/.codex/config.toml": "profile = \"deep\"\n"}},
		{name: "an unreadable project config", argv: []string{"codex"}, user: high, files: map[string]string{"repo/.codex/config.toml/x": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, home := testNode(t)
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			writeFile(t, filepath.Join(home, "repo", ".git", "HEAD"), "ref: refs/heads/main\n")
			if tc.user != "-" {
				writeFile(t, filepath.Join(home, ".codex", "config.toml"), tc.user)
			}
			for p, body := range tc.files {
				writeFile(t, filepath.Join(home, p), body)
			}
			cwd := filepath.Join(home, "repo")
			if tc.cwd != "" {
				cwd = mkdir(t, filepath.Join(home, tc.cwd))
			}
			if got := n.codexEffortInherited(context.Background(), tc.argv, cwd, "high"); got != tc.inherit {
				t.Fatalf("inherited %v, want %v", got, tc.inherit)
			}
		})
	}
}

// Without a project root above it, Codex reads only the directory's own .codex/config.toml; the check reads every
// directory above as well, so a parent's effort keeps the flag (whether or not a .git above the temp directory ends
// the walk).
func TestCodexEffortInheritedWithoutAProjectRoot(t *testing.T) {
	n, home := testNode(t)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_reasoning_effort = \"high\"\n")
	writeFile(t, filepath.Join(home, "loose", ".codex", "config.toml"), "model_reasoning_effort = \"low\"\n")
	cwd := mkdir(t, filepath.Join(home, "loose", "dir"))
	if n.codexEffortInherited(context.Background(), []string{"codex"}, cwd, "high") {
		t.Fatal("a parent's effort did not keep the flag")
	}
}
