package node

import (
	"errors"
	"strings"
)

// windowsPowerShellPane identifies a pane that reads PowerShell while its Agent start file is read by Git Bash.
// windowsCmdPane identifies cmd.exe, which cannot read either the POSIX line or the PowerShell handoff.
// The Windows platform files produce these values; other platforms never produce them.
const (
	windowsPowerShellPane = "windows-powershell"
	windowsCmdPane        = "windows-cmd"
)

// windowsShellName normalizes a Windows shell program without depending on the test machine's path semantics.
func windowsShellName(shell string) string {
	shell = strings.ReplaceAll(shell, `\`, "/")
	shell = shell[strings.LastIndex(shell, "/")+1:]
	return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(shell), "-"), ".exe")
}

// windowsPowerShellProgram reports whether a reported shell program is Windows PowerShell or PowerShell 7.
func windowsPowerShellProgram(shell string) bool {
	shell = windowsShellName(shell)
	return shell == "powershell" || shell == "pwsh"
}

// windowsShellProgram recognizes a shell only for a new classic-herdr pane's decision. The shared shellProgram and
// takesAssign retain main's behavior; case, slashes and .exe are normalized only here.
func windowsShellProgram(shell string) bool {
	name := windowsShellName(shell)
	return name == "cmd" || windowsPowerShellProgram(shell) || shells[name]
}

// windowsClassicShellKind maps only the two failing classic-pane cases to new behavior. A POSIX or unknown shell gets
// main's unnamed-shell value, so its directly typed command and environment stay byte-for-byte unchanged.
func windowsClassicShellKind(shell string) string {
	name := windowsShellName(shell)
	switch {
	case name == "powershell" || name == "pwsh":
		return windowsPowerShellPane
	case name == "cmd":
		return windowsCmdPane
	default:
		return ""
	}
}

// windowsForegroundUsable says whether the foreground answer can select the pane shell: its innermost/last program is
// a shell the node knows. An error or empty answer reaches this as nil and therefore falls through to config.
func windowsForegroundUsable(foreground []string) bool {
	return len(foreground) > 0 && windowsShellProgram(foreground[len(foreground)-1])
}

// windowsPaneShellDecision narrows the change to a host that names PowerShell and to a new classic-herdr pane. Every
// unnamed nonclassic pane returns main's empty fallback. A host-named non-PowerShell shell stays exactly as reported.
func windowsPaneShellDecision(classic bool, hostShell string, foreground []string, configShell string) []string {
	if hostShell != "" {
		if windowsPowerShellProgram(hostShell) {
			return []string{windowsPowerShellPane}
		}
		return []string{hostShell}
	}
	if !classic {
		return []string{""}
	}
	if windowsForegroundUsable(foreground) {
		return []string{windowsClassicShellKind(foreground[len(foreground)-1])}
	}
	if configShell != "" {
		return []string{windowsClassicShellKind(configShell)}
	}
	return []string{windowsPowerShellPane}
}

// windowsPaneShellError refuses cmd.exe before a start file or pane input can be produced.
func windowsPaneShellError(shells ...string) error {
	if len(shells) > 0 && shells[len(shells)-1] == windowsCmdPane {
		return errors.New("the pane shell cmd.exe cannot start an Agent; PowerShell or Git Bash is required")
	}
	return nil
}

// windowsAdoptedPaneShells is what a start in an adopted pane (Run Agent Here, a stopped session's own terminal, a
// launcher or engine switch) reads on Windows. names are the programs in front of the pane, innermost last. Only an
// innermost PowerShell or pwsh, under any of its Windows names, becomes the sentinel a new PowerShell pane gets, so
// the start takes the same Git Bash handoff (#9854). Every other stack, Git Bash started from PowerShell included,
// stays the raw programs and keeps main's bytes. The list itself is not changed: the shell-prompt checks before a
// start still read it with the shared shellProgram, and takesAssign is not taught these names.
func windowsAdoptedPaneShells(names []string) []string {
	if len(names) > 0 && windowsPowerShellProgram(names[len(names)-1]) {
		return []string{windowsPowerShellPane}
	}
	return names
}

// powerShellPane reports only the singleton sentinel made by windowsPaneShellDecision or windowsAdoptedPaneShells.
// A shell's name never selects the handoff by itself.
func powerShellPane(shells ...string) bool {
	return len(shells) == 1 && shells[0] == windowsPowerShellPane
}

// startCommandShells are the shells that read the command inside a start file. A Windows PowerShell pane only reads
// the outer line; Git Bash reads the file interactively, just as loginShell reads launcher definitions on Windows.
func startCommandShells(shells []string) []string {
	if powerShellPane(shells...) {
		return []string{"bash"}
	}
	return shells
}

// powerShellQuote makes s one single-quoted PowerShell string. Backslashes, dollars, backticks and operators are
// literal inside single quotes. PowerShell recognizes ASCII apostrophes and four Unicode single-quote variants as
// delimiters; double each one to keep it literal. Double-quote variants need no escape inside a single-quoted string.
func powerShellQuote(s string) string {
	return "'" + strings.NewReplacer(
		"'", "''",
		"‘", "‘‘",
		"’", "’’",
		"‚", "‚‚",
		"‛", "‛‛",
	).Replace(s) + "'"
}

// powerShellSourceCommand is the Bash -c argument that sources path. It uses Bash's ANSI-C quote form rather than
// windowsScriptPath's '"'"' concatenation: embedded double quotes do not survive Windows PowerShell 5.1's legacy
// native-argument reconstruction, and PowerShell 7's MSVC escaping is not Git Bash's MSYS escaping. This form contains
// no double quote at all. PowerShell still applies its own single-quote doubling when the argument is typed.
func powerShellSourceCommand(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	path = strings.NewReplacer(`'`, `\'`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(path)
	return ". $'" + path + "'"
}

// powerShellStartLine is the one line typed into a Windows PowerShell pane. & invokes the quoted Git Bash path
// instead of evaluating it as a string expression. -ic is loginShell's mode on Windows: ~/.bashrc supplies launcher
// aliases, functions and PATH. The Bash command sources the start file without an embedded native-boundary quote.
// The anchor is a PowerShell 5.1-compatible no-op after the command; && is not available there before PowerShell 7.
func powerShellStartLine(bash, path, anchor string) string {
	line := "& " + powerShellQuote(bash) + " -ic " + powerShellQuote(powerShellSourceCommand(path))
	if anchor != "" {
		line += "; $null = " + powerShellQuote(anchor)
	}
	return line
}

// powerShellStartShell is the Git Bash used by loginShell on Windows. A variable keeps typed-start tests independent
// of the test machine's operating system and installed shells.
var powerShellStartShell = interactiveShell
