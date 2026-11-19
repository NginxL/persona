package node

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// withWindowsRestore makes the test node resolve herdr's restore as classic herdr on Windows does, with
// `[terminal] default_shell` set to config ("" writes a config without the key; noConfig leaves no file at all), and
// Git Bash at powerShellTestBash.
func withWindowsRestore(t *testing.T, config string) {
	t.Helper()
	withPaneShell(t, "") // herdrstart_windows.go
	old := herdrRestoreShells
	herdrRestoreShells = windowsRestoreShells
	t.Cleanup(func() { herdrRestoreShells = old })
	withPowerShellTestBash(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("HERDR_CONFIG_PATH", path)
	if config == noConfig {
		return
	}
	body := "[terminal]\n"
	if config != "" {
		body += "default_shell = " + strconv.Quote(config) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const noConfig = "\x00no config file"

// herdrTypes is what herdr v0.9.5 types into the restored pane for argv (shell_command_from_argv in
// src/app/agent_resume.rs): each element as it is when every byte is one of its safe ones, else in apostrophes.
func herdrTypes(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:@%+=", r))
		}) < 0 {
			words[i] = a
		} else {
			words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(words, " ")
}

// herdrTakes is herdr v0.9.5's validate_resume_argv (src/agent_resume.rs): up to 64 elements and 8192 bytes, no control
// character, no apostrophe, and a plain command name first.
func herdrTakes(argv []string) bool {
	size := 0
	for _, a := range argv {
		if strings.Contains(a, "'") || strings.IndexFunc(a, unicode.IsControl) >= 0 {
			return false
		}
		size += len(a)
	}
	return len(argv) > 0 && len(argv) <= 64 && size <= maxResumeArgv && argv[0] != "" && argv[0][0] != '-' &&
		strings.IndexFunc(argv[0], func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.')
		}) < 0
}

// decodedResume is the command a PowerShell resume_argv hands Git Bash.
func decodedResume(t *testing.T, argv []string) string {
	t.Helper()
	if len(argv) != 4 || argv[0] != "." || argv[1] != powerShellTestBash || argv[2] != "-ic" {
		t.Fatalf("resume_argv = %q, want . <Git Bash> -ic <source>", argv)
	}
	b64, ok := strings.CutPrefix(argv[3], ". <(base64 -d <<< ")
	b64, ok2 := strings.CutSuffix(b64, ")")
	if !ok || !ok2 {
		t.Fatalf("source = %q", argv[3])
	}
	cmd, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 %q: %v", b64, err)
	}
	return string(cmd)
}

// windowsResumeCmd is the command a PowerShell resume hands Git Bash for the r3 launch: main's direct start command
// (r3MainDirect, captured from origin/main) with --resume, behind the start file's PERSONA_HOME / unset prefix.
func windowsResumeCmd() string {
	return "unset " + strings.Join(agentUnsetNames(), " ") + ` && PERSONA_HOME='C:'\\'node dir' ` + agentMarkEnv + " " +
		strings.Replace(r3MainDirect, "--session-id ", "--resume ", 1)
}

// herdr's default pane shell on Windows is PowerShell (pwsh.exe from PATH, else powershell.exe), and so is a
// default_shell naming either: the registered resume_argv hands the command to Git Bash in a form herdr accepts and
// PowerShell reads, instead of the eval line PowerShell cannot run (#9853).
func TestWindowsHerdrRestorePowerShellHandsOffToGitBash(t *testing.T) {
	for _, config := range []string{
		"powershell.exe",
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		"pwsh",
		"PWSH.EXE",
		`C:\Program Files\PowerShell\7\pwsh.exe`,
		"", // [terminal] without default_shell: herdr's default
		noConfig,
	} {
		t.Run(config, func(t *testing.T) {
			withWindowsRestore(t, config)
			n := &Node{dir: `C:\node dir`, home: t.TempDir()}
			argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Settings: `C:\Users\u\settings.json`}, startID)
			b64 := base64.StdEncoding.EncodeToString([]byte(windowsResumeCmd()))
			want := []string{".", powerShellTestBash, "-ic", ". <(base64 -d <<< " + b64 + ")"}
			if !ok || !reflect.DeepEqual(argv, want) {
				t.Fatalf("resume_argv = %q, %v\nwant %q", argv, ok, want)
			}
			if got := decodedResume(t, argv); got != windowsResumeCmd() {
				t.Fatalf("Git Bash gets %q\nwant %q", got, windowsResumeCmd())
			}
			if !herdrTakes(argv) {
				t.Fatalf("herdr would refuse %q", argv)
			}
			typed := `. 'C:\Program Files\Git\bin\bash.exe' -ic '. <(base64 -d <<< ` + b64 + `)'`
			if got := herdrTypes(argv); got != typed {
				t.Fatalf("herdr types %q\nwant %q", got, typed)
			}
		})
	}
}

// A restored pane of Git Bash keeps the exact eval line main registers, and so do cmd.exe (which runs neither line)
// and a shell the node does not know.
func TestWindowsHerdrRestoreGitBashKeepsMainBytes(t *testing.T) {
	for _, config := range []string{
		"C:/Program Files/Git/bin/bash.exe",
		`C:\Program Files\Git\bin\bash.exe`,
		"bash",
		"BASH.EXE",
		`C:\Windows\System32\cmd.exe`,
		"nu.exe",
	} {
		t.Run(config, func(t *testing.T) {
			withWindowsRestore(t, config)
			n := &Node{dir: `C:\node dir`, home: t.TempDir()}
			argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Settings: `C:\Users\u\settings.json`}, startID)
			if !ok || !reflect.DeepEqual(argv, []string{"eval", r3MainResume}) {
				t.Fatalf("resume_argv = %q, %v, want main %q", argv, ok, []string{"eval", r3MainResume})
			}
		})
	}
}

// macOS and Linux never look at default_shell for the restore: a config naming pwsh changes nothing there.
func TestHerdrRestoreOutsideWindowsIgnoresDefaultShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the restore shell on Windows is herdr's default_shell")
	}
	withPaneShell(t, "/bin/zsh")
	withPowerShellTestBash(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[terminal]\ndefault_shell = \"pwsh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_CONFIG_PATH", path)
	n, home := testNode(t)
	n.dir = filepath.Join(home, ".persona")
	argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude"}, startID)
	want := inAgentEnv(n.dir, "HERDR_AGENT=claude claude --resume "+startID+" --mcp-config "+filepath.Join(n.dir, "mcp.json")+
		" --allowedTools "+ownToolsRule, dquote)
	if !ok || !reflect.DeepEqual(argv, []string{"eval", want}) {
		t.Fatalf("resume_argv = %q, %v, want eval %q", argv, ok, want)
	}
}

// Codex, a compatible CLI and the work agents get the same handoff, with their own commands: agy keeps its model, its
// effort and its verified conversation, and a managed agy's personad path is quoted for Git Bash, not for eval.
func TestWindowsHerdrRestorePowerShellOtherAgents(t *testing.T) {
	withWindowsRestore(t, "pwsh.exe")
	n := &Node{dir: `C:\node dir`, home: t.TempDir(), bin: `C:\Program Files\Persona\personad.exe`}
	n.managed.byID = map[string]proto.ManagedLauncher{"work-agy": {Name: "work-agy"}}
	prefix := "unset " + strings.Join(agentUnsetNames(), " ") + ` && PERSONA_HOME='C:'\\'node dir' ` + agentMarkEnv + " "
	for _, tc := range []struct{ kind, launcher, model, effort, conv, want string }{
		{"codex", "cx", "", "", "", "cx resume " + startID},
		{traexAgent, "tx", "", "", "", "tx resume " + startID},
		{piAgent, "pi", "", "", "", "pi --approve --session " + startID},
		{cursorAgent, "", "gpt 5", "", "", "cursor-agent --model 'gpt 5'"},
		{agyAgent, agyAgent, "test model", "high", agyTestID, "agy --model 'test model' --effort high --conversation " + agyTestID},
		{agyAgent, "work-agy", "m", "xhigh", "", `'C:'\\'Program Files'\\'Persona'\\'personad.exe' launch work-agy --model m --effort xhigh`},
	} {
		argv, ok := n.agentResumeArgv(agentLaunch{Agent: tc.kind, Launcher: tc.launcher, Model: tc.model, Effort: tc.effort, AgyConversation: tc.conv}, startID)
		if !ok || !herdrTakes(argv) {
			t.Fatalf("%s (%s): resume_argv = %q, %v", tc.kind, tc.launcher, argv, ok)
		}
		if got := decodedResume(t, argv); got != prefix+tc.want {
			t.Errorf("%s (%s): Git Bash gets %q, want %q", tc.kind, tc.launcher, got, prefix+tc.want)
		}
	}
}

// Paths with spaces, apostrophes, double quotes, dollars, backticks, PowerShell's other single quotes and a newline:
// none of them reaches herdr or PowerShell, and Git Bash reads each back exactly. The eval line still cannot say an
// apostrophe or a newline, so a Git Bash pane gets no command for those, as before.
func TestWindowsHerdrRestorePowerShellQuoting(t *testing.T) {
	for _, settings := range []string{
		`C:\Users\Jane Doe\settings.json`,
		`C:\Users\O'Brien\settings.json`,
		`C:\Users\u\"quoted"\settings.json`,
		`C:\Users\u\$HOME ` + "`id`" + ` $(id)\settings.json`,
		`C:\Users\u\it’s ‘here’ ‚x‛\settings.json`,
		"C:\\Users\\u\\two\nlines\\settings.json",
		`C:\Users\u\ends with backslash\`,
	} {
		t.Run(settings, func(t *testing.T) {
			withWindowsRestore(t, "powershell.exe")
			n := &Node{dir: `C:\node dir`, home: t.TempDir()}
			argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Settings: settings}, startID)
			if !ok || !herdrTakes(argv) {
				t.Fatalf("resume_argv = %q, %v", argv, ok)
			}
			for _, a := range argv[2:] {
				if strings.ContainsAny(a, "'\"\\\u2018\u2019\u201a\u201b") || strings.IndexFunc(a, unicode.IsControl) >= 0 {
					t.Fatalf("%q crosses PowerShell", a)
				}
			}
			if got := decodedResume(t, argv); !strings.Contains(got, " --settings "+shellQuote(settings)+" ") {
				t.Fatalf("Git Bash gets %q, want --settings %s", got, shellQuote(settings))
			}
		})
	}
	for _, settings := range []string{`C:\Users\O'Brien\settings.json`, "C:\\Users\\u\\two\nlines\\settings.json"} {
		withWindowsRestore(t, "C:/Program Files/Git/bin/bash.exe")
		n := &Node{dir: `C:\node dir`, home: t.TempDir()}
		if argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Settings: settings}, startID); ok {
			t.Fatalf("%q: Git Bash pane got %q", settings, argv)
		}
	}
}

// The Git Bash path is the one element herdr wraps in apostrophes that PowerShell then reads: one with an apostrophe
// (herdr refuses it) or another of PowerShell's single quotes (it would end PowerShell's string) gets no command, and
// neither does a node without Git Bash.
func TestWindowsHerdrRestorePowerShellGitBashPath(t *testing.T) {
	withWindowsRestore(t, "powershell.exe")
	n := &Node{dir: `C:\node dir`, home: t.TempDir()}
	for _, bash := range []string{`C:\Users\O'Brien\Git\bin\bash.exe`, `C:\Users\it’s\Git\bin\bash.exe`, `C:\Users\‛x\Git\bin\bash.exe`} {
		powerShellStartShell = func() (string, error) { return bash, nil }
		if argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude"}, startID); ok {
			t.Errorf("%q: resume_argv %q", bash, argv)
		}
	}
	powerShellStartShell = func() (string, error) { return "", errors.New("no Git Bash") }
	if argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude"}, startID); ok {
		t.Fatalf("without Git Bash: resume_argv %q", argv)
	}
	powerShellStartShell = func() (string, error) { return "C:/Git/bin/bash.exe", nil }
	argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude"}, startID)
	if !ok || !strings.HasPrefix(herdrTypes(argv), ". C:/Git/bin/bash.exe -ic '. <(base64 -d <<< ") {
		t.Fatalf("plain Git Bash path: herdr types %q", herdrTypes(argv))
	}
}

// herdr's 8192 bytes count every element; base64 makes the handed-off command a third longer than the eval line's.
func TestWindowsHerdrRestorePowerShellLength(t *testing.T) {
	model := strings.Repeat("m", 5500)
	withWindowsRestore(t, "C:/Program Files/Git/bin/bash.exe")
	n := &Node{dir: `C:\node dir`, home: t.TempDir()}
	if _, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Model: model}, startID); !ok {
		t.Fatal("Git Bash pane: no eval line")
	}
	withWindowsRestore(t, "pwsh")
	if argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Model: model}, startID); ok {
		t.Fatalf("over herdr's limit: %d bytes", len(strings.Join(argv, "")))
	}
	// 1 + 33 (Git Bash) + 3 + 19 + 8136 base64 bytes of 6102 = 8192; 6103 bytes take 8140, which only the elements
	// before the base64 one push over the limit.
	if argv, ok := powerShellResumeArgv(strings.Repeat("m", 6102)); !ok || len(strings.Join(argv, "")) != maxResumeArgv {
		t.Fatalf("at herdr's limit: %v, %d bytes", ok, len(strings.Join(argv, "")))
	}
	if argv, ok := powerShellResumeArgv(strings.Repeat("m", 6103)); ok {
		t.Fatalf("over herdr's limit: %d bytes", len(strings.Join(argv, "")))
	}
}

// What herdr is told for a PowerShell pane: the handoff, through the same two reports as everywhere.
func TestWindowsHerdrRestorePowerShellIsReported(t *testing.T) {
	n, f, home := herdrNode(t)
	withWindowsRestore(t, "powershell.exe")
	launch := n.hosts.Get(host.Herdr).(*herdrHost)
	n.holdResume(context.Background(), launch, "w1:p2", startID, claudeLaunch{Launcher: "claude"})
	want, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude"}, startID)
	if !ok {
		t.Fatal("no resume_argv")
	}
	reported := false
	for _, c := range f.calls() {
		if c.Method != "pane.report_agent_session" {
			continue
		}
		got, _ := c.Params["resume_argv"].([]any)
		if len(got) != len(want) {
			t.Fatalf("reported %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("reported %q, want %q", got, want)
			}
		}
		reported = true
	}
	if !reported {
		t.Fatal("no session report")
	}
	if cmd := decodedResume(t, want); !strings.Contains(cmd, "PERSONA_HOME="+shellQuote(personaDir(home))+" "+agentMarkEnv+" claude --resume "+startID) {
		t.Fatalf("Git Bash gets %q", cmd)
	}
}

// Git Bash, which reads the handoff, sources exactly the command: run with a bash that can source a process
// substitution (4.0 and later; Git for Windows ships 5.x).
func TestPowerShellResumeSourceRunsInBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	v, err := exec.Command(bash, "-c", "echo ${BASH_VERSINFO[0]}").Output()
	if major, _ := strconv.Atoi(strings.TrimSpace(string(v))); err != nil || major < 4 {
		t.Skipf("bash %q cannot source a process substitution", strings.TrimSpace(string(v)))
	}
	withPowerShellTestBash(t)
	args := []string{`C:\Users\Jane Doe\s.json`, `O'Brien`, `"q" $HOME ` + "`id`" + ` $(id) \`, "it’s", "two\nlines", "  spaced  "}
	cmd := "printf '<%s>'"
	want := ""
	for _, a := range args {
		cmd += " " + shellQuote(a)
		want += "<" + a + ">"
	}
	argv, ok := powerShellResumeArgv(cmd)
	if !ok {
		t.Fatal("no resume_argv")
	}
	out, err := exec.Command(bash, "-c", argv[3]).Output()
	if err != nil || string(out) != want {
		t.Fatalf("bash printed %q (%v), want %q", out, err, want)
	}
}
