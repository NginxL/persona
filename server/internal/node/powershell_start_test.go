package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

const powerShellTestBash = `C:\Program Files\Git\bin\bash.exe`

func withPowerShellTestBash(t *testing.T) {
	t.Helper()
	old := powerShellStartShell
	powerShellStartShell = func() (string, error) { return powerShellTestBash, nil }
	t.Cleanup(func() { powerShellStartShell = old })
}

// A Windows herdr pane runs PowerShell unless its configuration says otherwise. The start must not type the
// POSIX-quoted command itself there: PowerShell splits shellQuote's backslash escapes into separate arguments.
func TestPowerShellClaudeStartUsesAStartFile(t *testing.T) {
	n, _ := testNode(t)
	n.dir = `C:\Users\u\.persona`
	shells := []string{windowsPowerShellPane}
	cmd := n.agentStartCommand(n.claudeCommandLine(claudeLaunch{
		Launcher: "claude",
		Settings: `C:\Users\u\.persona\settings.json`,
		Hooks:    `C:\Users\u\.persona\claude-hooks`,
	}, "--session-id "+startID, shellQuote), shellQuote, shells...)
	if !n.typesStartFile(&herdrHost{}, shells...) {
		t.Fatalf("PowerShell would type the POSIX command directly: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "unset ") || !strings.Contains(cmd, "PERSONA_HOME="+shellQuote(n.dir)+" ") {
		t.Fatalf("start file lost the PERSONA_HOME / unset prefix: %s", cmd)
	}
	file, err := writeStartScript(t.TempDir(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	typed := startLineChoice(cmd, file, startID, powerShellTestBash, true, shells...)
	if typed != powerShellStartLine(powerShellTestBash, file, startID) {
		t.Fatalf("typed line = %q", typed)
	}
	if strings.Contains(typed, `'\\'`) || strings.Contains(typed, "&&") {
		t.Fatalf("PowerShell line contains POSIX quoting or &&: %q", typed)
	}
}

func TestPowerShellSecretTicketUsesAStartFile(t *testing.T) {
	n, _ := testNode(t)
	n.dir = `C:\Users\u\.persona`
	n.bin = `C:\Program Files\Persona\personad.exe`
	line := n.claudeCommandLine(claudeLaunch{Launcher: "claude"}, "--session-id "+startID, shellQuote)
	line = n.withSecretTicket(line, "claude", "ticket with spaces", shellQuote)
	cmd := n.agentStartCommand(line, shellQuote, windowsPowerShellPane)
	wrap := shellQuote(n.bin) + " launch --ticket " + shellQuote("ticket with spaces") + " -- claude "
	if !strings.Contains(cmd, wrap) || !strings.Contains(cmd, "PERSONA_HOME="+shellQuote(n.dir)+" ") {
		t.Fatalf("secret-ticket start file = %q", cmd)
	}
	typed := startLineChoice(cmd, `C:\Users\u\.persona\agent-start.sh`, startID, powerShellTestBash, true, windowsPowerShellPane)
	if strings.Contains(typed, `'\\'`) || !strings.HasPrefix(typed, "& ") {
		t.Fatalf("secret-ticket PowerShell line = %q", typed)
	}
}

func TestPowerShellQuote(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", "''"},
		{"plain", "plain", "'plain'"},
		{"spaces", `C:\a path\start.sh`, "'C:\\a path\\start.sh'"},
		{"apostrophe", `C:\O'Brien\start.sh`, "'C:\\O''Brien\\start.sh'"},
		{"smart quotes", `C:\‘’‚‛“”\start.sh`, "'C:\\‘‘’’‚‚‛‛“”\\start.sh'"},
		{"dollar", `$HOME\start.sh`, "'$HOME\\start.sh'"},
		{"backtick", "`HOME\\start.sh", "'`HOME\\start.sh'"},
		{"operators", `a&b;c`, "'a&b;c'"},
		{"non-ASCII", `雪😀`, "'雪😀'"},
		{"UNC", `\\server\share\start.sh`, "'\\\\server\\share\\start.sh'"},
		{"drive root", `C:\`, "'C:\\'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := powerShellQuote(tc.in); got != tc.want {
				t.Fatalf("powerShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPowerShellStartLineExact(t *testing.T) {
	path := `C:\Users\O'Brien\agent start.sh`
	want := `& 'C:\Program Files\Git\bin\bash.exe' -ic '. $''C:/Users/O\''Brien/agent start.sh'''; $null = '` + startID + `'`
	got := powerShellStartLine(powerShellTestBash, path, startID)
	if got != want {
		t.Fatalf("line = %q\nwant = %q", got, want)
	}
	if strings.Contains(got, `"`) {
		t.Fatalf("an embedded double quote would not survive PowerShell 5.1's native boundary: %q", got)
	}
}

func TestPowerShellStartLines(t *testing.T) {
	for _, tc := range []struct{ name, path, script string }{
		{"spaces", `C:\Users\a b\agent start.sh`, ". $'C:/Users/a b/agent start.sh'"},
		{"apostrophe", `C:\Users\O'Brien\start.sh`, `. $'C:/Users/O\'Brien/start.sh'`},
		{"smart quotes", `C:\Users\‘O’Brien‚‛“”\start.sh`, ". $'C:/Users/‘O’Brien‚‛“”/start.sh'"},
		{"dollar", `C:\Users\$HOME\start.sh`, ". $'C:/Users/$HOME/start.sh'"},
		{"backtick", "C:\\Users\\`HOME\\start.sh", ". $'C:/Users/`HOME/start.sh'"},
		{"operators", `C:\a&b;c\start.sh`, ". $'C:/a&b;c/start.sh'"},
		{"non-ASCII", `C:\Users\雪😀\start.sh`, ". $'C:/Users/雪😀/start.sh'"},
		{"UNC", `\\server\share\a b\start.sh`, ". $'//server/share/a b/start.sh'"},
		{"drive root", `C:\`, ". $'C:/'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := "& " + powerShellQuote(powerShellTestBash) + " -ic " + powerShellQuote(tc.script) +
				"; $null = " + powerShellQuote(startID)
			got := powerShellStartLine(powerShellTestBash, tc.path, startID)
			if got != want {
				t.Fatalf("line = %q\nwant = %q", got, want)
			}
			if strings.Contains(got, `'\\'`) || strings.Contains(got, "&&") {
				t.Fatalf("POSIX quoting or && in PowerShell line: %q", got)
			}
		})
	}
	if got := powerShellStartLine(powerShellTestBash, `C:\start.sh`, ""); strings.Contains(got, "$null") {
		t.Fatalf("an empty anchor added a no-op: %q", got)
	}
}

func TestWindowsPaneShellDecision(t *testing.T) {
	bash := `C:\Program Files\Git\bin\bash.exe`
	for _, tc := range []struct {
		name       string
		classic    bool
		host       string
		foreground []string
		config     string
		want       []string
	}{
		{"host PowerShell wins", false, "POWERSHELL.EXE", []string{bash}, "cmd.exe", []string{windowsPowerShellPane}},
		{"host Bash stays as reported", false, "bash", []string{"pwsh.exe"}, "cmd.exe", []string{"bash"}},
		{"host Bash path stays as reported", false, bash, nil, "", []string{bash}},
		{"host cmd keeps main behavior", false, "cmd.exe", nil, "", []string{"cmd.exe"}},
		{"nonclassic unnamed keeps main fallback", false, "", nil, "", []string{""}},
		{"foreground PowerShell wins over config", true, "", []string{`C:\Program Files\PowerShell\7\pwsh.exe`}, bash, []string{windowsPowerShellPane}},
		{"foreground innermost Bash keeps direct main bytes", true, "", []string{"powershell.exe", bash}, "cmd.exe", []string{""}},
		{"foreground POSIX shell keeps direct main bytes", true, "", []string{"zsh"}, "powershell.exe", []string{""}},
		{"foreground cmd", true, "", []string{"powershell.exe", `C:\Windows\System32\CMD.EXE`}, bash, []string{windowsCmdPane}},
		{"foreground error falls through to config", true, "", nil, bash, []string{""}},
		{"empty foreground falls through to config", true, "", []string{}, "powershell.exe", []string{windowsPowerShellPane}},
		{"unknown foreground falls through to config", true, "", []string{"powershell.exe", "node.exe"}, bash, []string{""}},
		{"config PowerShell", true, "", nil, "PWSh.ExE", []string{windowsPowerShellPane}},
		{"config cmd", true, "", nil, `C:\Windows\System32\cmd.exe`, []string{windowsCmdPane}},
		{"unknown config keeps direct main bytes", true, "", nil, "unknown-program.exe", []string{""}},
		{"PowerShell default", true, "", nil, "", []string{windowsPowerShellPane}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsPaneShellDecision(tc.classic, tc.host, tc.foreground, tc.config); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decision = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWindowsPaneShellDecisionGitBashNames(t *testing.T) {
	for _, shell := range []string{
		"bash", "BASH.EXE", "-bash.exe",
		`C:\Program Files\Git\bin\bash.exe`,
		`C:/Program Files/Git/BIN/BaSh.ExE`,
	} {
		if got := windowsPaneShellDecision(true, "", nil, shell); !reflect.DeepEqual(got, []string{""}) {
			t.Errorf("%q: decision = %q, want main's unnamed-shell direct behavior", shell, got)
		}
	}
}

// Actual origin/main output (plus the Agent mark PERSONA_AGENT=1 after PERSONA_HOME, #10111, #10365's startup
// variables and #13472's PERSONA_TRACK_KIND in the host bash file's unset), not branch-derived; the direct golden uses a Windows node directory and
// omits its prefix.
const (
	r3MainDirect       = `claude --session-id 0e000000-0000-4000-8000-0000000000aa --settings 'C:'\\'Users'\\'u'\\'settings.json' --mcp-config 'C:'\\'node dir/mcp.json' --allowedTools 'mcp__persona__report_to_supervisor,mcp__jarvis__report_to_supervisor,mcp__persona__todo_decisions,mcp__jarvis__todo_decisions,mcp__persona__todo_decision_add,mcp__jarvis__todo_decision_add,mcp__persona__todo_bind_thread,mcp__jarvis__todo_bind_thread,mcp__persona__task_plan,mcp__jarvis__task_plan,mcp__persona__task_review,mcp__jarvis__task_review'`
	r3MainStartPath    = `C:\node dir\agent start.sh`
	r3MainAnchor       = "session id"
	r3MainHostBashFile = `command rm -f -- 'C:'\\'node dir'\\'agent start.sh'
unset PERSONA_ASSISTANT JARVIS_ASSISTANT PERSONA_ASSISTANT_BIN JARVIS_ASSISTANT_BIN PERSONA_SESSION_ID JARVIS_SESSION_ID PERSONA_SPACE JARVIS_SPACE PERSONA_TRACK_KIND JARVIS_TRACK_KIND PERSONA_GUEST JARVIS_GUEST CLAUDE_CODE_AUTO_COMPACT_WINDOW ENABLE_TOOL_SEARCH CLAUDECODE CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_EXECPATH CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_SESSION_ATTENDED CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_EFFORT CLAUDE_PID CLAUDE_PLUGIN_DATA DISABLE_AUTO_UPDATE DISABLE_UPDATE_PROMPT ZSH_DISABLE_COMPFIX POWERLEVEL9K_DISABLE_CONFIGURATION_WIZARD && PERSONA_HOME='C:'\\'node dir' PERSONA_AGENT=1 claude
`
	r3MainHostBashTyped = `. 'C:'\\'node dir'\\'agent start.sh' && true 'session id'`
	r3MainResume        = `claude --resume 0e000000-0000-4000-8000-0000000000aa --settings "C:\\Users\\u\\settings.json" --mcp-config "C:\\node dir/mcp.json" --allowedTools mcp__persona__report_to_supervisor,mcp__jarvis__report_to_supervisor,mcp__persona__todo_decisions,mcp__jarvis__todo_decisions,mcp__persona__todo_decision_add,mcp__jarvis__todo_decision_add,mcp__persona__todo_bind_thread,mcp__jarvis__todo_bind_thread,mcp__persona__task_plan,mcp__jarvis__task_plan,mcp__persona__task_review,mcp__jarvis__task_review`
	r3MainOrca          = "cd -- /tmp/r3-main-orca && " + r3MainDirect + "\r"
)

func TestWindowsUnchangedDirectStartsUseMainBytes(t *testing.T) {
	n := &Node{dir: `C:\node dir`}
	direct := n.claudeCommandLine(claudeLaunch{Launcher: "claude", Settings: `C:\Users\u\settings.json`}, "--session-id "+startID, shellQuote)
	bash := `C:\Program Files\Git\bin\bash.exe`
	for _, tc := range []struct {
		name       string
		classic    bool
		host       string
		foreground []string
		config     string
		launch     host.Launcher
	}{
		{"classic foreground Git Bash", true, "", []string{"powershell.exe", bash}, "", &herdrHost{}},
		{"classic config Git Bash", true, "", nil, bash, &herdrHost{}},
		{"classic foreground POSIX shell", true, "", []string{"zsh"}, "", &herdrHost{}},
		{"host-named unknown", false, "unknown-program.exe", nil, "", &herdrHost{}},
		{"host-named cmd", false, "cmd.exe", nil, "", &herdrHost{}},
		{"config unknown", true, "", nil, "unknown-program.exe", &herdrHost{}},
		{"Orca unnamed", false, "", nil, "", &orcaHost{}},
		{"Tern unnamed", false, "", nil, "", &ternHost{}},
		{"herdr-core unnamed", false, "", nil, "", &herdrCoreHost{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shells := windowsPaneShellDecision(tc.classic, tc.host, tc.foreground, tc.config)
			if err := windowsPaneShellError(shells...); err != nil {
				t.Fatalf("main-compatible start was refused: %v", err)
			}
			useStartFile := n.typesStartFile(tc.launch, shells...)
			if useStartFile {
				t.Fatal("main's direct start moved to a start file")
			}
			cmd := n.agentStartCommand(direct, shellQuote, shells...)
			if cmd != r3MainDirect {
				t.Fatalf("payload = %q, want main %q", cmd, r3MainDirect)
			}
			if got := startLineChoice(cmd, r3MainStartPath, r3MainAnchor, powerShellTestBash, useStartFile, shells...); got != r3MainDirect {
				t.Fatalf("typed = %q, want main %q", got, r3MainDirect)
			}
		})
	}
}

func TestWindowsHostNamedBashKeepsMainBytesAndClearsModelSource(t *testing.T) {
	n := &Node{dir: `C:\node dir`}
	shells := windowsPaneShellDecision(false, "bash", nil, "")
	payload := n.agentStartCommand("claude", shellQuote, shells...)
	got := startScript(r3MainStartPath, payload)
	// Clear inherited attribution before applying this Agent's source. Apart from
	// these additional unset fields, preserve the established Windows shell bytes.
	for _, key := range []string{modelSourceEnv, modelSourceBinEnv} {
		current, legacy := compat.EnvNames(key)
		for _, name := range []string{current, legacy} {
			if !strings.Contains(got, " "+name+" ") {
				t.Fatalf("start file retained inherited %s", name)
			}
			got = strings.Replace(got, name+" ", "", 1)
		}
	}
	if got != r3MainHostBashFile {
		t.Fatalf("start file = %q, want main %q", got, r3MainHostBashFile)
	}
	if !n.typesStartFile(&herdrHost{}, shells...) {
		t.Fatal("host-reported bash lost main's start file")
	}
	if got := startLineChoice(payload, r3MainStartPath, r3MainAnchor, powerShellTestBash, true, shells...); got != r3MainHostBashTyped {
		t.Fatalf("typed = %q, want main %q", got, r3MainHostBashTyped)
	}
}

func TestWindowsHostNamedPowerShellUsesHandoff(t *testing.T) {
	for _, launch := range []host.Launcher{&herdrCoreHost{}, &ternHost{}} {
		shells := windowsPaneShellDecision(false, `C:\Program Files\PowerShell\7\pwsh.exe`, nil, "")
		if !(&Node{dir: `C:\node dir`}).typesStartFile(launch, shells...) {
			t.Fatalf("%T did not make a host-named PowerShell start-file eligible", launch)
		}
		got, want := startLineChoice("claude", r3MainStartPath, r3MainAnchor, powerShellTestBash, true, shells...), powerShellStartLine(powerShellTestBash, r3MainStartPath, r3MainAnchor)
		if got != want {
			t.Fatalf("%T typed = %q, want %q", launch, got, want)
		}
	}
}

// withWindowsAdoptedPanes makes startPaneShells read an adopted pane as the Windows build does (herdrstart_windows.go).
func withWindowsAdoptedPanes(t *testing.T) {
	t.Helper()
	old := adoptedPaneShells
	adoptedPaneShells = windowsAdoptedPaneShells
	t.Cleanup(func() { adoptedPaneShells = old })
}

// Off Windows an adopted pane keeps its raw programs: a pwsh there gets the command typed as before (#9854 is
// Windows only).
func TestAdoptedPowerShellOffWindowsUsesMainBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows turns an innermost PowerShell into the handoff")
	}
	n := &Node{dir: `C:\node dir`}
	created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p1"}}
	ctx := withAdoptedPane(context.Background(), created, []string{"bash", "pwsh"})
	shells := n.startPaneShells(ctx, &herdrHost{}, created)
	if !reflect.DeepEqual(shells, []string{"bash", "pwsh"}) {
		t.Fatalf("adopted shells = %q, want main's raw stack", shells)
	}
	cmd := n.agentStartCommand(r3MainDirect, shellQuote, shells...)
	useStartFile := n.typesStartFile(&herdrHost{}, shells...)
	if useStartFile {
		t.Fatal("adopted pwsh stack moved to a start file off Windows")
	}
	if got := startLineChoice(cmd, r3MainStartPath, r3MainAnchor, powerShellTestBash, useStartFile, shells...); got != r3MainDirect {
		t.Fatalf("typed = %q, want main %q", got, r3MainDirect)
	}
}

func TestWindowsAdoptedPaneShells(t *testing.T) {
	gitBash := `C:\Program Files\Git\bin\bash.exe`
	for _, tc := range []struct {
		name  string
		names []string
		want  []string
	}{
		{"Windows PowerShell", []string{"powershell.exe"}, []string{windowsPowerShellPane}},
		{"PowerShell 7", []string{"pwsh.exe"}, []string{windowsPowerShellPane}},
		{"upper case", []string{"PWSH.EXE"}, []string{windowsPowerShellPane}},
		{"mixed case path", []string{`C:\Windows\System32\WindowsPowerShell\v1.0\PowerShell.EXE`}, []string{windowsPowerShellPane}},
		{"bare pwsh", []string{"pwsh"}, []string{windowsPowerShellPane}},
		{"PowerShell started from Git Bash", []string{"bash", "pwsh"}, []string{windowsPowerShellPane}},
		{"Git Bash", []string{"bash"}, []string{"bash"}},
		{"Git Bash path", []string{gitBash}, []string{gitBash}},
		{"Git Bash started from PowerShell", []string{"pwsh", "bash"}, []string{"pwsh", "bash"}},
		{"Git Bash started from Windows PowerShell", []string{"POWERSHELL.EXE", gitBash}, []string{"POWERSHELL.EXE", gitBash}},
		{"cmd stays as it is", []string{"cmd.exe"}, []string{"cmd.exe"}},
		{"nothing", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsAdoptedPaneShells(tc.names); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("shells = %q, want %q", got, tc.want)
			}
		})
	}
}

// Run Agent Here, a stopped session's own terminal and a switch adopt a pane. On Windows, one whose innermost program
// is PowerShell or pwsh now takes the handoff a new PowerShell pane takes: the POSIX command, with its PERSONA_HOME /
// unset prefix, in the start file, and only the & line typed (#9854). The list the shell-prompt checks read is
// still the raw programs.
func TestWindowsAdoptedPowerShellPaneUsesHandoff(t *testing.T) {
	withWindowsAdoptedPanes(t)
	withPowerShellTestBash(t)
	newPane := windowsPaneShellDecision(true, "", []string{"powershell.exe"}, "")
	for _, front := range [][]string{
		{"powershell.exe"},
		{"pwsh.exe"},
		{"PWSH.EXE"},
		{`C:\Windows\System32\WindowsPowerShell\v1.0\PowerShell.EXE`},
		{"pwsh"},
		{"bash", "pwsh"},
	} {
		t.Run(strings.Join(front, ">"), func(t *testing.T) {
			n, f, _ := herdrNode(t)
			n.dir = t.TempDir()
			launch := n.hosts.Get(host.Herdr).(*herdrHost)
			created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p2"}, SlotID: "w1:t2", SpaceID: "w1"}
			ctx := withAdoptedPane(context.Background(), created, front)
			shells := n.startPaneShells(ctx, launch, created)
			if !reflect.DeepEqual(shells, newPane) {
				t.Fatalf("adopted shells = %q, want a new PowerShell pane's %q", shells, newPane)
			}
			if got := paneShells(ctx, created); !reflect.DeepEqual(got, front) {
				t.Fatalf("the adopted list became %q, want the raw %q", got, front)
			}
			cmd := n.agentStartCommand(withHerdrAgent(typedHerdrAgent(launch, "claude", shells...), "claude --session-id "+startID), shellQuote, shells...)
			if want := n.agentStartCommand("claude --session-id "+startID, shellQuote, newPane...); cmd != want {
				t.Fatalf("start file command = %q, want a new PowerShell pane's %q", cmd, want)
			}
			if !strings.HasPrefix(cmd, "unset ") || !strings.Contains(cmd, "PERSONA_HOME="+shellQuote(n.dir)+" "+agentMarkEnv+" claude ") {
				t.Fatalf("start file lost the PERSONA_HOME / unset prefix: %q", cmd)
			}
			typed, err := n.typeStart(ctx, launch, created, "w1", startID, cmd, shells...)
			if err != nil {
				t.Fatal(err)
			}
			defer typed.done()
			if want := powerShellStartLine(powerShellTestBash, typed.path, startID); typed.line != want {
				t.Fatalf("typed = %q, want %q", typed.line, want)
			}
			b, err := os.ReadFile(typed.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != startScript(typed.path, cmd) {
				t.Fatalf("start file = %q", b)
			}
			var inputs []string
			for _, c := range f.calls() {
				if c.Method == "pane.send_input" {
					inputs = append(inputs, str(c.Params["text"]))
				}
			}
			if len(inputs) != 1 || inputs[0] != typed.line {
				t.Fatalf("pane input = %q, want only %q", inputs, typed.line)
			}
		})
	}
}

// An adopted Git Bash pane, Git Bash started from PowerShell included, keeps main's bytes on Windows.
func TestWindowsAdoptedBashPaneUsesMainBytes(t *testing.T) {
	withWindowsAdoptedPanes(t)
	withPowerShellTestBash(t)
	for _, tc := range []struct {
		name  string
		front []string
		file  bool // main typed `. <start file>`; otherwise the command itself
	}{
		{"Git Bash", []string{"bash"}, true},
		{"login Git Bash", []string{"-bash"}, true},
		{"Git Bash started from pwsh", []string{"pwsh", "bash"}, false},
		{"Git Bash started from Windows PowerShell", []string{"PowerShell.EXE", "bash"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _ := herdrNode(t)
			n.dir = t.TempDir()
			launch := n.hosts.Get(host.Herdr).(*herdrHost)
			created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p2"}, SlotID: "w1:t2", SpaceID: "w1"}
			ctx := withAdoptedPane(context.Background(), created, tc.front)
			shells := n.startPaneShells(ctx, launch, created)
			if !reflect.DeepEqual(shells, tc.front) {
				t.Fatalf("adopted shells = %q, want main's raw stack %q", shells, tc.front)
			}
			cmd := n.agentStartCommand("claude --session-id "+startID, shellQuote, shells...)
			typed, err := n.typeStart(ctx, launch, created, "w1", startID, cmd, shells...)
			if err != nil {
				t.Fatal(err)
			}
			defer typed.done()
			want := cmd
			if tc.file {
				want = ". " + shellQuote(typed.path) + " && true " + shellQuote(startID)
			}
			if typed.line != want || (typed.path != "") != tc.file {
				t.Fatalf("typed = %q (start file %q), want main %q", typed.line, typed.path, want)
			}
			if strings.HasPrefix(typed.line, "& ") {
				t.Fatalf("a Git Bash pane got the PowerShell line: %q", typed.line)
			}
			for _, c := range f.calls() {
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), powerShellTestBash) {
					t.Fatalf("a Git Bash pane was typed the PowerShell handoff: %v", c)
				}
			}
		})
	}
}

// Run Agent Here end to end in a pane whose front program is pwsh, the one PowerShell name the shell-prompt check
// knows: the check takes the pane as it did, and the start types the handoff instead of the POSIX command.
func TestWindowsRunAgentHereInPowerShellUsesHandoff(t *testing.T) {
	withWindowsAdoptedPanes(t)
	withPowerShellTestBash(t)
	n, f, home := actionNode(t)
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": 1, "name": "pwsh", "argv0": "pwsh"}}}})
	f.reply("agent.prompt", map[string]any{})
	var mu sync.Mutex
	var files []string
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		// Whatever was typed, the agent comes up, so a start that typed the wrong line fails below instead of
		// waiting for an agent that never comes.
		left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*"))
		mu.Lock()
		for _, path := range left {
			if b, err := os.ReadFile(path); err == nil {
				files = append(files, string(b))
			}
		}
		mu.Unlock()
		f.reply("session.snapshot", snapshot(nil, nil, []any{
			agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, cursorAgent),
		}, nil))
	})
	s, err := n.runAgentInPane(context.Background(), "w1:p2", cursorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != cursorAgent || s.PaneID != "w1:p2" {
		t.Fatalf("session: %+v", s)
	}
	var inputs []string
	for _, c := range f.calls() {
		if c.Method == "pane.send_input" {
			inputs = append(inputs, str(c.Params["text"]))
		}
	}
	prefix := "& " + powerShellQuote(powerShellTestBash) + " -ic '. $''"
	if len(inputs) != 1 || !strings.HasPrefix(inputs[0], prefix) || strings.Contains(inputs[0], "cursor-agent") {
		t.Fatalf("pane input = %q, want only the PowerShell handoff", inputs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(files) != 1 || !strings.Contains(files[0], "PERSONA_HOME="+shellQuote(n.dir)+" "+agentMarkEnv+" cursor-agent") ||
		strings.Contains(files[0], "HERDR_AGENT=") {
		t.Fatalf("start files = %q, want the POSIX cursor-agent command a new PowerShell pane gets", files)
	}
	if left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*")); len(left) != 0 {
		t.Fatalf("start files left behind: %v", left)
	}
	closesNothing(t, f)
}

func TestWindowsHerdrRestoreUsesMainBytes(t *testing.T) {
	old := herdrPaneShell
	herdrPaneShell = func() string { return "" }
	t.Cleanup(func() { herdrPaneShell = old })
	n := &Node{dir: `C:\node dir`}
	argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Settings: `C:\Users\u\settings.json`}, startID)
	if !ok || !reflect.DeepEqual(argv, []string{"eval", r3MainResume}) {
		t.Fatalf("resume_argv = %q, %v, want main %q", argv, ok, []string{"eval", r3MainResume})
	}
}

func TestWindowsOrcaUnnamedShellUsesMainBytes(t *testing.T) {
	const cwd = "/tmp/r3-main-orca"
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cwd) })
	fake := orcarttest.New(t)
	fake.SetWorktrees(orcarttest.Worktree("wt-r3", cwd, "refs/heads/main"))
	h := newOrcaHost(&orcart.Client{MetaPath: fake.MetaPath})
	created, err := h.CreateSpace(context.Background(), cwd, "tab")
	if err != nil {
		t.Fatal(err)
	}
	shells := windowsPaneShellDecision(false, created.Shell, nil, "")
	n := &Node{dir: `C:\node dir`}
	cmd := n.agentStartCommand(r3MainDirect, shellQuote, shells...)
	useStartFile := n.typesStartFile(h, shells...)
	typed := startLineChoice(cmd, r3MainStartPath, r3MainAnchor, powerShellTestBash, useStartFile, shells...)
	if typed != r3MainDirect {
		t.Fatalf("Orca command = %q, want main %q", typed, r3MainDirect)
	}
	if err := h.Run(context.Background(), created.Pane, typed); err != nil {
		t.Fatal(err)
	}
	if writes := fake.Writes(); len(writes) != 1 || writes[0].Text != r3MainOrca {
		t.Fatalf("Orca writes = %v, want main %q", writes, r3MainOrca)
	}
}

// The existing Unix choices stay byte-identical: direct commands are unchanged, and POSIX start files keep their
// source operator, quoting and && anchor. The PowerShell choice is the only new one.
func TestStartLineChoiceKeepsPosixBytes(t *testing.T) {
	const cmd = "claude --session-id 0e000000-0000-4000-8000-0000000000aa"
	if got := startLineChoice(cmd, "unused", "anchor", powerShellTestBash, false, "/bin/zsh"); got != cmd {
		t.Fatalf("direct command = %q", got)
	}
	for _, tc := range []struct{ shell, want string }{
		{"/bin/zsh", `. '/tmp/node dir/agent start.sh' && true 'session id'`},
		{"-fish", `source '/tmp/node dir/agent start.sh' && true 'session id'`},
		{"/bin/tcsh", `. '/tmp/node dir/agent start.sh' && true 'session id'`},
	} {
		if got := startLineChoice(cmd, `/tmp/node dir/agent start.sh`, "session id", powerShellTestBash, true, tc.shell); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.shell, got, tc.want)
		}
	}
	n, _ := testNode(t)
	n.dir = "/tmp/node dir"
	for _, shells := range [][]string{{"/bin/zsh"}, {"/opt/homebrew/bin/fish"}, {"/bin/tcsh"}, {"pwsh"}, {}} {
		if got, want := n.agentStartCommand(cmd, shellQuote, shells...), n.agentCommand(cmd, shellQuote, shells...); got != want {
			t.Errorf("%v: agentStartCommand = %q, want existing %q", shells, got, want)
		}
	}
}

// The POSIX file, not the PowerShell outer line, carries Claude's arguments. Run it with a fake claude that records
// every argument and the environment to prove that each Windows path survives as one argument.
func TestWindowsClaudeStartFileArguments(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	fakeProgram(t, filepath.Join(bin, "claude"), `printf '%s\n' "${PERSONA_HOME-unset}" "${PERSONA_ASSISTANT-unset}" "$@" >"$HOME/args"`)
	n, _ := testNode(t)
	n.dir = `C:\Users\u\.persona`
	spec := claudeLaunch{Launcher: "claude", Settings: `C:\Users\u\settings with space.json`, Hooks: `C:\Users\u\hook's`}
	cmd := n.agentStartCommand(n.claudeCommandLine(spec, "--session-id "+startID, shellQuote), shellQuote, windowsPowerShellPane)
	file, err := writeStartScript(t.TempDir(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", file)
	c.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "PERSONA_ASSISTANT=from-assistant"}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("start file: %v: %s", err, out)
	}
	got := readArgumentLines(t, filepath.Join(home, "args"))
	want := []string{n.dir, "unset", "--session-id", startID, "--settings", spec.Settings,
		"--mcp-config", filepath.Join(n.dir, "mcp.json"), "--allowedTools", n.agentAllowedTools(false), "--plugin-dir", spec.Hooks}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude received\n%q\nwant\n%q", got, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("start file left behind: %v", err)
	}
}

func TestWindowsOtherAgentStartFileArguments(t *testing.T) {
	home := t.TempDir()
	n, _ := testNode(t)
	n.dir = `C:\Users\u\.persona`
	prompt := "a 'quoted' $HOME ` & ; 雪"
	resume := "00000000-0000-4000-8000-000000000001"
	cmd := otherAgentCommand("wrapper", resume, prompt, windowsPowerShellPane)
	cmd = n.agentStartCommand(cmd, shellQuote, windowsPowerShellPane)
	file, err := writeStartScript(t.TempDir(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	script := `wrapper() { printf '%s\n' "${PERSONA_HOME-unset}" "$#" "$@" >"$HOME/args"; }; . "$1"`
	c := exec.Command("sh", "-c", script, "sh", file)
	c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("start file: %v: %s", err, out)
	}
	want := []string{n.dir, "3", "resume", resume, prompt}
	if got := readArgumentLines(t, filepath.Join(home, "args")); !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapper received %q, want %q", got, want)
	}
	if got := otherAgentCommand("wrapper", "", "one\ntwo", windowsPowerShellPane); !strings.Contains(got, "one\ntwo") {
		t.Fatalf("start-file prompt lost its line break: %q", got)
	}
}

// Git Bash reads ~/.bashrc in the same -ic mode as loginShell, so a launcher defined there works in the start file.
func TestPowerShellStartFileReadsBashrc(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	fakeProgram(t, filepath.Join(bin, "claude"), `printf '%s\n' "${PERSONA_HOME-unset}" "${PERSONA_ASSISTANT-unset}" "$@" >"$HOME/args"`)
	writeFile(t, filepath.Join(home, ".bashrc"), "ccf() { claude --model from-function \"$@\"; }\n")
	n, _ := testNode(t)
	n.dir = filepath.Join(home, "O'Brien ‘’‚‛“” $HOME ` & ; 雪")
	cmd := n.agentStartCommand(n.claudeCommandLine(claudeLaunch{Launcher: "ccf"}, "--session-id "+startID, shellQuote), shellQuote, windowsPowerShellPane)
	file, err := writeStartScript(n.dir, cmd)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bash, "-ic", powerShellSourceCommand(file))
	c.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "PERSONA_ASSISTANT=from-assistant"}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Git Bash start command: %v: %s", err, out)
	}
	want := []string{n.dir, "unset", "--model", "from-function", "--session-id", startID,
		"--mcp-config", filepath.Join(n.dir, "mcp.json"), "--allowedTools", n.agentAllowedTools(false)}
	if got := readArgumentLines(t, filepath.Join(home, "args")); !reflect.DeepEqual(got, want) {
		t.Fatalf("rc-file launcher received %q, want %q", got, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("start file left behind: %v", err)
	}
}

func TestPowerShellStartParseErrorIsRetyped(t *testing.T) {
	line := powerShellStartLine(powerShellTestBash, `C:\node dir\agent-start.sh`, startID)
	probe := &startProbe{anchor: startID}
	probe.typed(&typedStart{line: line})
	for _, tc := range []struct{ name, screen, want string }{
		{"PowerShell 5.1", "PS> " + line + "\r\nAt line:1 char:72\r\n" +
			"+ CategoryInfo          : ParserError: (:) [], ParentContainsErrorRecordException\r\n" +
			"+ FullyQualifiedErrorId : UnexpectedToken\r\n",
			"+ CategoryInfo          : ParserError: (:) [], ParentContainsErrorRecordException"},
		{"PowerShell 7", "PS> " + line + "\nParserError:\nLine |\n   1 | " + line +
			"\n     | Unexpected token '-ic' in expression or statement.\n", "ParserError:"},
		{"unterminated string", "PS> " + line + "\nParserError:\nLine |\n   1 | " + line +
			"\n     | The string is missing the terminator: '.\n     + FullyQualifiedErrorId : TerminatorExpectedAtEndOfString\n", "ParserError:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !probe.mayBeRejected(tc.screen) {
				t.Fatal("the parser error did not trigger a full-screen check")
			}
			if got := probe.rejectedLine(tc.screen); got != tc.want {
				t.Fatalf("rejected line = %q, want %q", got, tc.want)
			}
		})
	}
	if got := probe.rejectedLine("ParserError:\nPS> " + line); got != "" {
		t.Fatalf("an error before the typed line was accepted: %q", got)
	}
}

func TestPowerShellParseTriggerIsLimitedToPowerShellStarts(t *testing.T) {
	for _, screen := range []string{"SyntaxError: Unexpected token }", "ParserError: ordinary agent output"} {
		if mayBeRejected(screen) {
			t.Fatalf("main's POSIX predicate changed for %q", screen)
		}
		probe := &startProbe{anchor: startID}
		probe.typed(&typedStart{line: `. '/tmp/agent start.sh' && true '` + startID + `'`})
		if probe.mayBeRejected(screen) {
			t.Fatalf("a POSIX start would make an extra full-screen read for %q", screen)
		}
		probe.typed(&typedStart{line: powerShellStartLine(powerShellTestBash, `C:\start.sh`, startID)})
		if !probe.mayBeRejected(screen) {
			t.Fatalf("a PowerShell start did not recognize its parser error %q", screen)
		}
	}
	if !mayBeRejected("zsh: parse error near `done'") {
		t.Fatal("the existing POSIX parse-error predicate was lost")
	}
}

func readArgumentLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestTypeStartPowerShellLineStaysShort(t *testing.T) {
	withPowerShellTestBash(t)
	n, _, _ := herdrNode(t)
	n.dir = filepath.Join(t.TempDir(), strings.Repeat("long-directory/", 8))
	created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p2"}, SlotID: "w1:t2", SpaceID: "w1"}
	cmd := strings.Repeat("x", 2048)
	typed, err := n.typeStartMode(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), created, "w1", startID, cmd, false, windowsPowerShellPane)
	if err != nil {
		t.Fatal(err)
	}
	defer typed.done()
	if len(typed.line)+1 >= 1024 || strings.Contains(typed.line, cmd) {
		t.Fatalf("typed %d bytes: %q", len(typed.line)+1, typed.line)
	}
	b, err := os.ReadFile(typed.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != startScript(typed.path, cmd) {
		t.Fatalf("start file lost the long command (%d bytes)", len(b))
	}
	if strings.Contains(typed.line, `'\\'`) || !strings.HasPrefix(typed.line, "& ") {
		t.Fatalf("PowerShell line = %q", typed.line)
	}
}

func TestTypeStartPowerShellMissingGitBash(t *testing.T) {
	n, f, _ := herdrNode(t)
	n.dir = t.TempDir()
	old := powerShellStartShell
	powerShellStartShell = func() (string, error) { return "", errors.New("Git Bash (Git for Windows) is missing") }
	t.Cleanup(func() { powerShellStartShell = old })
	created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p2"}, SlotID: "w1:t2", SpaceID: "w1"}
	typed, err := n.typeStartMode(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), created, "w1", startID, "claude", false, windowsPowerShellPane)
	if err == nil || err.Error() != "Git Bash (Git for Windows) is missing" || typed != nil {
		t.Fatalf("typed = %#v, err = %v", typed, err)
	}
	if left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*")); len(left) != 0 {
		t.Fatalf("start files left behind: %v", left)
	}
	if calls := f.trace("pane.send_input"); len(calls) != 0 {
		t.Fatalf("typed without Git Bash: %v", calls)
	}
}

func TestTypeStartWindowsCmdIsRefusedBeforeFileOrInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shells []string
	}{
		{"foreground", windowsPaneShellDecision(true, "", []string{"powershell.exe", "cmd.exe"}, "")},
		{"config", windowsPaneShellDecision(true, "", nil, `C:\Windows\System32\cmd.exe`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _ := herdrNode(t)
			n.dir = t.TempDir()
			created := &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: "w1:p2"}, SlotID: "w1:t2", SpaceID: "w1"}
			typed, err := n.typeStartMode(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), created, "w1", startID, "claude", false, tc.shells...)
			const want = "the pane shell cmd.exe cannot start an Agent; PowerShell or Git Bash is required"
			if err == nil || err.Error() != want || typed != nil {
				t.Fatalf("typed = %#v, err = %v, want %q", typed, err, want)
			}
			if left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*")); len(left) != 0 {
				t.Fatalf("cmd.exe created a start file: %v", left)
			}
			if calls := f.trace("pane.send_input"); len(calls) != 0 {
				t.Fatalf("cmd.exe received pane input: %v", calls)
			}
		})
	}
}
