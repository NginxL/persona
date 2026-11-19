package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const scriptKey = "sk-test-0123456789abcdefghij"

// demoScript is the issue's wrapper: exports for the gateway and key, the proxies taken away, its own settings, and
// `touch ran` before claude, which a probe that ran it would leave behind.
const demoScript = `#!/bin/sh
# claude through the demo gateway
export ANTHROPIC_BASE_URL="https://gw.example.com"
export ANTHROPIC_AUTH_TOKEN="` + scriptKey + `"
export ANTHROPIC_MODEL=glm-5
unset HTTP_PROXY HTTPS_PROXY
export NO_PROXY="gw.example.com"
touch "$HOME/ran"
exec claude --settings "$HOME/.claude/providers/demo.settings.json" \
  --effort max "$@"
`

func writeScript(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func launcherNames(list []proto.Launcher) []string {
	var out []string
	for _, l := range list {
		out = append(out, l.Name+":"+l.Source)
	}
	return out
}

// Only a wrapper counts, the first file of a name on PATH (as the shell finds it), one a function or alias does not
// shadow; nothing of its key comes out.
func TestPathLaunchersFindWrapperScripts(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeScript(t, filepath.Join(a, "claude-demo"), demoScript, 0o755)
	writeScript(t, filepath.Join(a, "notes"), "#!/bin/sh\necho hi\n", 0o755)
	writeScript(t, filepath.Join(a, "summarize"), "#!/bin/sh\nclaude -p \"summarize this\" < \"$1\"\n", 0o755)
	writeScript(t, filepath.Join(a, "claude-shadow"), "#!/bin/sh\necho something else\n", 0o755)
	writeScript(t, filepath.Join(b, "claude-shadow"), "#!/bin/sh\nexec claude \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(a, "claude-off"), "#!/bin/sh\nexec claude \"$@\"\n", 0o644) // not executable: no command
	writeScript(t, filepath.Join(b, "claude-off"), "#!/bin/sh\nexec claude --model later \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(b, "claude-fn"), "#!/bin/sh\nexec claude \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(b, "claude"), "#!/bin/sh\nexec /opt/claude/bin/claude \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(b, "claude-else"), "#!/bin/sh\nexec codex \"$@\"\nexec claude \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(b, "claude-bin"), "\x7fELF\x00\x00exec claude \"$@\"", 0o755)

	list, settings, files := pathLaunchers(strings.Join([]string{a, ".", "", b, a}, string(os.PathListSeparator)), map[string]bool{"claude-fn": true})
	if got := launcherNames(list); !slices.Equal(got, []string{"claude-demo:path", "claude-off:path"}) {
		t.Fatalf("launchers %v", got)
	}
	if l := list[0]; l.BaseURL != "https://gw.example.com" || l.Model != "glm-5" {
		t.Errorf("claude-demo: %+v", l)
	}
	if list[1].Model != "later" {
		t.Errorf("--model: %+v", list[1])
	}
	if settings["claude-demo"] != "$HOME/.claude/providers/demo.settings.json" || len(settings) != 1 {
		t.Errorf("settings %v", settings)
	}
	if files["claude-demo"] != filepath.Join(a, "claude-demo") || files["claude-off"] != filepath.Join(b, "claude-off") {
		t.Errorf("files %v", files)
	}
	if out, _ := json.Marshal(list); strings.Contains(string(out), scriptKey) {
		t.Error("the key is in the list")
	}
}

// What the text says, read as the shell would: variables the script set before, an env / command / path in front of
// claude, a line going on after a backslash; what it cannot say is left empty, never passed on as written.
func TestWrapperScriptReading(t *testing.T) {
	for _, c := range []struct {
		name, text      string
		ok              bool
		base, model, st string
	}{
		{"variables", "#!/bin/sh\nGW=https://gw.example.com\nexport ANTHROPIC_BASE_URL=\"${GW}/api\"\nexec claude \"$@\"", true, "https://gw.example.com/api", "", ""},
		{"set in front", "#!/bin/sh\nANTHROPIC_BASE_URL=https://a.example.com ANTHROPIC_MODEL=m1 exec claude \"$@\"", true, "https://a.example.com", "m1", ""},
		{"env and path", "#!/bin/bash\nset -euo pipefail\nexec env -u HTTP_PROXY ANTHROPIC_BASE_URL=https://b.example.com \"$HOME/.local/bin/claude\" --model=m2 \"$@\"", true, "https://b.example.com", "m2", ""},
		{"not exported", "#!/bin/sh\nANTHROPIC_BASE_URL=https://c.example.com\nexec claude \"$@\"", true, "", "", ""},
		{"from a command", "#!/bin/sh\nexport ANTHROPIC_BASE_URL=\"$(cat ~/.gw)\"\nexec claude \"$@\"", true, "", "", ""},
		{"settings inline", "#!/bin/sh\nexec claude --settings=\"$HOME/s.json\" \"$@\"", true, "", "", "$HOME/s.json"},
		{"inside an if", "#!/bin/sh\nif [ -n \"$X\" ]; then\n  exec claude --model a \"$@\"\nfi", true, "", "a", ""},
		{"no arguments passed on", "#!/bin/sh\nexec claude --model a", false, "", "", ""},
		{"becomes another program", "#!/bin/sh\nexec node cli.js \"$@\"\nexec claude \"$@\"", false, "", "", ""},
		{"past the first screen", "#!/bin/sh\n" + strings.Repeat(":\n", scriptHeadLines) + "exec claude \"$@\"", false, "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w")
			writeScript(t, path, c.text, 0o755)
			head, err := scriptHead(path)
			if err != nil {
				t.Fatal(err)
			}
			w, ok := wrapperScript(head)
			if ok != c.ok || ok && (w.shown("ANTHROPIC_BASE_URL") != c.base || w.model() != c.model || w.settings != c.st) {
				t.Fatalf("ok %v base %q model %q settings %q", ok, w.shown("ANTHROPIC_BASE_URL"), w.model(), w.settings)
			}
		})
	}
}

// withScripts is the launcher list's wrapper scripts for the test, put back after it.
func withScripts(t *testing.T, scripts map[string]string) {
	t.Helper()
	launcherCache.Lock()
	old, oldAt := launcherCache.scripts, launcherCache.at
	launcherCache.scripts, launcherCache.at = scripts, time.Now()
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.scripts, launcherCache.at = old, oldAt
		launcherCache.Unlock()
	})
}

// The probe of a wrapper script reads it and never runs it (the real claude would start): the model a send passes goes
// where the script's own arguments go, the gateway, key and settings env are what it sets, and its page masks the key.
func TestScriptLauncherIsReadNotRun(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	writeScript(t, filepath.Join(bin, "claude-demo"), demoScript, 0o755)
	writeScript(t, filepath.Join(bin, "claude-fixed"), "#!/bin/sh\nexec claude --model pinned \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(home, ".claude", "providers", "demo.settings.json"), `{"env":{"API_TIMEOUT_MS":"600000"}}`)
	withScripts(t, map[string]string{"claude-demo": filepath.Join(bin, "claude-demo"), "claude-fixed": filepath.Join(bin, "claude-fixed")})
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	ctx := context.Background()

	p, err := n.launcherProbe(ctx, "claude-demo", "--model", "glm-5-air")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "ran")); err == nil {
		t.Fatal("the probe ran the script")
	}
	want := []string{"--settings", "$HOME/.claude/providers/demo.settings.json", "--effort", "max", "--model", "glm-5-air"}
	if p.Agent != "claude" || !slices.Equal(p.Args, want) {
		t.Fatalf("probe %+v", p)
	}
	if p.Env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" || p.Env["ANTHROPIC_AUTH_TOKEN"] != scriptKey ||
		p.Env["NO_PROXY"] != "gw.example.com" || p.Env["API_TIMEOUT_MS"] != "600000" {
		t.Fatalf("env %v", p.Env)
	}
	if _, err := n.launcherModelCheck(ctx, "claude-demo", "glm-5-air"); err != nil {
		t.Errorf("a script passing its arguments on: %v", err)
	}
	if _, err := n.launcherModelCheck(ctx, "claude-fixed", "glm-5-air"); err == nil {
		t.Error("a script with its own --model took another")
	}
	d := LauncherDetail(p)
	if d.Auth != "ANTHROPIC_AUTH_TOKEN" || d.BaseURL != "https://gw.example.com" {
		t.Errorf("detail %+v", d)
	}
	if out, _ := json.Marshal(d); strings.Contains(string(out), scriptKey) {
		t.Error("the key is in the detail")
	}
	text, err := n.launcherDefinition(ctx, "claude-demo")
	if err != nil || !strings.HasPrefix(text, "# ~/.local/bin/claude-demo\n#!/bin/sh") || strings.Contains(text, scriptKey) ||
		!strings.Contains(text, "exec claude --settings") {
		t.Fatalf("definition %q (%v)", text, err)
	}
}

// End to end through the login shell: a script on the PATH ~/.zshrc sets is listed after the functions and aliases,
// one of the same name as an alias or a function is not (the shell runs those), and plain claude is not added.
func TestLauncherListTakesScriptsOnPath(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	for _, name := range []string{"claude-demo", "cc_alias", "cc_fn"} {
		writeScript(t, filepath.Join(bin, name), demoScript, 0o755)
	}
	defs := `export PATH="$HOME/bin:$PATH"
alias cc_alias="ls"
cc_fn() { echo not claude; }
`
	list, _, settings, scripts := mergeLaunchers(asNodeIn(t, home, defs, launcherScript))
	// pi, cursor-agent or agy installed on this machine are reported too; this test is about the script on PATH.
	list = withoutInstalledWorkAgents(list)
	if got := launcherNames(list); !slices.Equal(got, []string{"claude-demo:path"}) {
		t.Fatalf("launchers %v", got)
	}
	if scripts["claude-demo"] != filepath.Join(bin, "claude-demo") || settings["claude-demo"] == "" {
		t.Fatalf("scripts %v settings %v", scripts, settings)
	}
}

// A launcher the shell finds as a file is reported by the probe, never run: whether the list has it as a script or
// not (a ~/.zshrc helper still printing the name of a function since replaced by a script, a list not read yet).
func TestProbeReportsAFileWithoutRunningIt(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	writeScript(t, filepath.Join(bin, "claude-demo"), "#!/bin/sh\ntouch "+ran+"\nexec claude \"$@\"\n", 0o755)
	defs := `export PATH="` + bin + `:$PATH"
_claude_launchers() { printf '%-26s%-40s%s\n' claude-demo https://gw.example.com glm-5; }
`
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			p, ok := parseProbe(inLoginShell(t, shell, defs, probeScript("claude-demo", "--model", "m-1")))
			if ok || p.File != filepath.Join(bin, "claude-demo") {
				t.Fatalf("probe: %+v (%v)", p, ok)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Fatal("the probe ran the script")
			}
			// A function still runs with the stubs.
			p, ok = parseProbe(inLoginShell(t, shell, `envcc() { env FOO=bar claude "$@"; }`+"\n", probeScript("envcc", "--model", "m-1")))
			if !ok || p.File != "" || p.Env["FOO"] != "bar" {
				t.Fatalf("function: %+v (%v)", p, ok)
			}
		})
	}
}

// Scripts that use claude for something are no wrappers: one that writes a wrapper with a here-document, a pipeline,
// -p, a subcommand, a prompt; and a ${…} form other than ${NAME} is not guessed at.
func TestWrapperScriptRefusals(t *testing.T) {
	for name, text := range map[string]string{
		"here-document":  "#!/bin/sh\ncat > ~/.local/bin/claude-x <<'EOF'\n#!/bin/sh\nexport ANTHROPIC_BASE_URL=https://gw.example.com\nexec claude \"$@\"\nEOF\nchmod +x ~/.local/bin/claude-x",
		"piped into":     "#!/bin/sh\ngit diff | claude -p \"review\" \"$@\"",
		"piped out":      "#!/bin/sh\nclaude --verbose \"$@\" | tee /tmp/log",
		"print":          "#!/bin/sh\nexec claude -p \"summarize\" \"$@\"",
		"subcommand":     "#!/bin/sh\nexec claude mcp add \"$@\"",
		"prompt":         "#!/bin/sh\nexec claude \"fix the tests\" \"$@\"",
		"comment escape": "#!/bin/sh\n# run claude \\\nexec claude --model a",
	} {
		if _, ok := wrapperScript(text); ok {
			t.Errorf("%s: taken for a wrapper", name)
		}
	}
	for _, c := range []struct{ text, base string }{
		{"#!/bin/sh\nGW=https://gw.example.com/\nexport ANTHROPIC_BASE_URL=\"${GW%/}/api\"\nexec claude \"$@\"", ""},
		{"#!/bin/sh\nGW=https://gw.example.com\nexport ANTHROPIC_BASE_URL=\"${GW:-https://other.example.com}\"\nexec claude \"$@\"", ""},
		{"#!/bin/sh\nGW=https://gw.example.com\nexport ANTHROPIC_BASE_URL='$GW'\nexec claude \"$@\"", ""},
		{"#!/bin/sh\nGW=https://gw.example.com\nexport ANTHROPIC_BASE_URL=\"$GW/v1\"\nexec /usr/bin/env claude \"$@\"", "https://gw.example.com/v1"},
		{"#!/bin/sh\nexport ANTHROPIC_BASE_URL=https://gw.example.com\nexec env -i PATH=/bin claude \"$@\"", ""},
	} {
		w, ok := wrapperScript(c.text)
		if !ok || w.shown("ANTHROPIC_BASE_URL") != c.base {
			t.Errorf("%q: ok %v base %q, want %q", c.text, ok, w.shown("ANTHROPIC_BASE_URL"), c.base)
		}
	}
}

// The probe reads the arguments as the script expands them (--settings "$S" names the file), and a claude in a branch
// is not seen: a send with a model passes it on rather than being refused over the branch it may not take.
func TestScriptProbeExpandsAndBranches(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	writeScript(t, filepath.Join(bin, "claude-s"), "#!/bin/sh\nS=\"$HOME/s.json\"\nexec claude --settings \"$S\" \"$@\"\n", 0o755)
	writeScript(t, filepath.Join(bin, "claude-if"), "#!/bin/sh\nif [ -n \"$FAST\" ]; then\n  exec claude --model fast \"$@\"\nfi\nexec claude \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(home, "s.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://gw.example.com","ANTHROPIC_AUTH_TOKEN":"`+scriptKey+`"}}`)
	withScripts(t, map[string]string{"claude-s": filepath.Join(bin, "claude-s"), "claude-if": filepath.Join(bin, "claude-if")})
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	p, err := n.launcherProbe(context.Background(), "claude-s")
	if err != nil || !slices.Equal(p.Args, []string{"--settings", "$HOME/s.json"}) || p.Env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" {
		t.Fatalf("probe %+v (%v)", p, err)
	}
	if _, err := n.launcherProbe(context.Background(), "claude-if"); !errors.Is(err, errProbeUnseen) {
		t.Fatalf("a branch: %v", err)
	}
	if _, err := n.launcherModelCheck(context.Background(), "claude-if", "glm-5"); err != nil {
		t.Fatalf("a send with a model through a branch: %v", err)
	}
}

// What the page shows of a probe never holds a key: an inline --settings, a header, a proxy's password.
func TestLauncherDetailMasksKeys(t *testing.T) {
	d := LauncherDetail(Probe{Agent: "claude",
		Args: []string{"--settings", `{"env":{"ANTHROPIC_AUTH_TOKEN":"` + scriptKey + `"}}`, "--settings={\"apiKeyHelper\":\"x\"}", "--model", "claude-sonnet-4-5-20250929-long-model-id"},
		Env:  map[string]string{"ANTHROPIC_CUSTOM_HEADERS": "x-api-key: abc123short\nx-source: dogfood", "HTTPS_PROXY": "http://me:pw-secret@proxy.example:3128"}})
	out, _ := json.Marshal(d)
	for _, secret := range []string{scriptKey, "abc123short", "pw-secret", "me:", "dogfood"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("%s in %s", secret, out)
		}
	}
	for _, kept := range []string{"x-source: •••", "claude-sonnet-4-5-20250929-long-model-id", "proxy.example:3128"} {
		if !strings.Contains(string(out), kept) {
			t.Errorf("%s gone from %s", kept, out)
		}
	}
	if got := redactSecrets(`"ANTHROPIC_AUTH_TOKEN": "0123456789abcdef"` + "\nx-api-key: short1"); strings.Contains(got, "0123456789abcdef") || strings.Contains(got, "short1") {
		t.Errorf("redacted: %q", got)
	}
}

// A scan that takes too long (a network mount that hangs) gives the last scan's scripts, and while it still runs no
// other starts: the list gets the last scan's.
func TestPathScanTimesOut(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "claude-demo"), demoScript, 0o755)
	t.Cleanup(func() {
		lastPathScan.Lock()
		lastPathScan.got, lastPathScan.running = nil, false
		lastPathScan.Unlock()
	})
	if list, _, _ := pathLaunchers(bin, nil); len(list) != 1 {
		t.Fatalf("first scan: %v", list)
	}
	lastPathScan.Lock()
	lastPathScan.running = true // one that ran out of time and has not come back
	lastPathScan.Unlock()
	if list, _, files := pathLaunchers(t.TempDir(), nil); len(list) != 1 || files["claude-demo"] == "" {
		t.Fatalf("while one runs: %v %v", list, files)
	}
}

// A branch is only where the claude command is: a guard closed before it does not count, a claude after && / || or
// inside an open if does; a top-level exit ends the script.
func TestWrapperScriptBranches(t *testing.T) {
	for _, c := range []struct {
		name, text string
		ok, branch bool
		model      string
	}{
		{"guard closed before", "#!/bin/sh\nif ! command -v claude >/dev/null; then\n  echo missing; exit 1\nfi\nexec claude --model glm-5 \"$@\"", true, false, "glm-5"},
		{"one-line guard", "#!/bin/sh\nif [ -z \"$HOME\" ]; then exit 1; fi\nexec claude --model glm-5 \"$@\"", true, false, "glm-5"},
		{"after &&", "#!/bin/sh\n[ -n \"$FAST\" ] && exec claude --model fast \"$@\"\nexec claude \"$@\"", true, true, "fast"},
		{"inside a loop", "#!/bin/sh\nfor f in a; do\n  exec claude \"$@\"\ndone", true, true, ""},
		{"exit first", "#!/bin/sh\necho gone\nexit 0\nexec claude \"$@\"", false, false, ""},
		{"a sentence after a switch", "#!/bin/sh\nexec claude --dangerously-skip-permissions \"fix the tests\" \"$@\"", false, false, ""},
		{"a word after a switch", "#!/bin/sh\nexec claude --verbose review \"$@\"", false, false, ""},
		{"text for a text flag", "#!/bin/sh\nexec claude --append-system-prompt \"Answer in Chinese\" --effort max \"$@\"", true, false, ""},
		{"here-string", "#!/bin/sh\nread -r x <<< \"$1\"\nexec claude \"$@\"", true, false, ""},
		{"comment with <<", "#!/bin/sh\n# cat <<EOF is not here\nexec claude \"$@\"", true, false, ""},
	} {
		w, ok := wrapperScript(c.text)
		if ok != c.ok || ok && (w.branch != c.branch || w.model() != c.model) {
			t.Errorf("%s: ok %v branch %v model %q", c.name, ok, w.branch, w.model())
		}
	}
	// env in the command's place only; env - drops what the script exported.
	w, ok := wrapperScript("#!/bin/sh\nexport ANTHROPIC_AUTH_TOKEN=" + scriptKey + "\nANTHROPIC_BASE_URL=https://gw.example.com/env exec /usr/bin/env claude --settings \"$HOME/.claude/env\" \"$@\"")
	if !ok || w.settings != "$HOME/.claude/env" || w.env["ANTHROPIC_BASE_URL"] != "https://gw.example.com/env" || w.env["ANTHROPIC_AUTH_TOKEN"] != scriptKey {
		t.Errorf("env words: %+v (%v)", w, ok)
	}
	if w, ok := wrapperScript("#!/bin/sh\nexport ANTHROPIC_AUTH_TOKEN=" + scriptKey + "\nexec env - PATH=/bin claude \"$@\""); !ok || w.env["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Errorf("env -: %+v (%v)", w, ok)
	}
}

// The scan's fallback (one still running) leaves out a name the shell has as a function now.
func TestPathScanFallbackKeepsShadowing(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "claude-demo"), demoScript, 0o755)
	t.Cleanup(func() {
		lastPathScan.Lock()
		lastPathScan.got, lastPathScan.running = nil, false
		lastPathScan.Unlock()
	})
	pathLaunchers(bin, nil)
	lastPathScan.Lock()
	lastPathScan.running = true
	lastPathScan.Unlock()
	if list, settings, files := pathLaunchers(bin, map[string]bool{"claude-demo": true}); len(list) != 0 || len(settings) != 0 || len(files) != 0 {
		t.Fatalf("a shadowed script from the last scan: %v %v %v", list, settings, files)
	}
}

// Masking keeps paths and names that only look like a key's prefix, and finds a key in escaped JSON.
func TestMaskingKeepsPaths(t *testing.T) {
	for _, kept := range []string{"/Users/me/.claude/settings.ak-prod.json", "/Users/me/work/sk-tools-repository"} {
		if got := maskSecrets(kept); got != kept {
			t.Errorf("%q masked: %q", kept, got)
		}
	}
	if got := maskSecrets("sk-ant-api03-0123456789"); strings.Contains(got, "0123456789") {
		t.Errorf("a key kept: %q", got)
	}
	if got := redactSecrets(`--settings "{\"env\":{\"ANTHROPIC_AUTH_TOKEN\":\"abc123short\"}}"`); strings.Contains(got, "abc123short") {
		t.Errorf("escaped JSON: %q", got)
	}
}
