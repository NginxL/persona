package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// `personad node doctor` looks at what a node on this machine needs for the sessions Persona starts to reach the
// phone: the sockets hooks and MCP dial, the files claude loads (compared with what this binary would write now, the
// same bytes the node writes at startup) and the programs those files run. It only reads. --fix writes Persona's
// own generated files again (writeGeneratedConfig, the startup path) and, on a Mac and outside the node, the resident
// node's LaunchAgent's ProcessType (launchagent.go); nothing else: the user's own Claude Code settings are only ever
// looked at. The path-access check (preflight.go) reads what a claude start reads, in a child it kills when a read
// hangs. Background: a redeploy once deleted node.sock and every hook and MCP call fell back to the terminal without a
// word (docs/lessons/server.md).

// Check statuses.
const (
	DoctorOK   = "ok"
	DoctorWarn = "warn"
	DoctorFail = "fail"
)

// DoctorCheck is one line of the report; the names are stable for scripts.
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// State is current / stale / missing for a generated file or directory, "" for the other checks.
	State  string   `json:"state"`
	Detail string   `json:"detail"`
	Hint   string   `json:"hint"`
	Items  []string `json:"items"` // what differs, which commands, ...; never null
}

// DoctorReport is what `node doctor` prints (--json: as it is).
type DoctorReport struct {
	OK     bool          `json:"ok"` // no check failed
	Dir    string        `json:"dir"`
	Checks []DoctorCheck `json:"checks"`
	// Fixed names what --fix put right: the generated files it wrote again; on a Mac launchd-process-type when it
	// wrote ProcessType into the LaunchAgent and launchd-reload when it loaded the agent again.
	Fixed []string `json:"fixed"`
}

// DoctorOptions says where to look; DefaultDoctorOptions fills them in for this machine (tests give their own).
type DoctorOptions struct {
	// Lang words this run's human-readable fields. Empty keeps the legacy zh-Hans report; it never comes from the
	// resident node's environment.
	Lang i18n.Lang
	Dir  string // the node's directory (~/.persona)
	Bin  string // the program the generated files should run: this binary
	// Sockets: node.sock, runtime.sock and herdr's API socket.
	NodeSock, RuntimeSock, HerdrSock string
	// ClaudeConfigDir is where the user's own Claude Code settings are (read, never written).
	ClaudeConfigDir string
	// ClaudeVersion asks claude's version ("" when unknown); ClaudeProbe, when set, is asked instead and also says
	// whether the shell found no claude at all (claudeProbe), so a version that could not be read this time is not
	// reported as Claude Code missing.
	ClaudeVersion func(context.Context) string
	ClaudeProbe   func(context.Context) (version string, missing bool)
	// LaunchAgent is the resident node's launchd agent on a Mac (NodeLaunchAgentPath; pickNodeLaunchdLabel says which
	// when a Mac keeps its previously installed label); "" elsewhere, and the launchd-process-type check is
	// left out.
	LaunchAgent string
	// LaunchdTarget is that agent as launchctl names it (gui/<uid>/com.persona.node, or the older label).
	LaunchdTarget string
	// Launchctl runs launchctl and returns its output; nil: launchd is not asked (the check reads the plist only).
	Launchctl func(ctx context.Context, args ...string) (string, error)
	// InsideNode: the doctor runs in the resident node (node.doctor) or under it. --fix then leaves the LaunchAgent
	// alone: loading it again stops the node, and with it the doctor, half way.
	InsideNode bool
	// RestartWait caps the wait, after --fix loaded the agent again, for the new node to answer (0: nodeRestartWait).
	RestartWait time.Duration
	// Preflight is the path-access check (preflight.go); nil leaves it out. It receives this run's language so a
	// terminal --json run can keep the legacy bytes even when the terminal is not Chinese.
	Preflight func(context.Context, i18n.Lang) DoctorCheck
	// AgyHooksFile is agy's global hooks file (node/agyhooks.go); "" leaves the check out (agy is not here).
	AgyHooksFile string
	// Fix writes the generated files again when they are stale or missing, and (outside the node) writes
	// ProcessType=Interactive into the LaunchAgent and loads it again.
	Fix bool
}

// DefaultDoctorOptions is this machine's: the same places the node itself uses, without creating or moving any.
func DefaultDoctorOptions() (DoctorOptions, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return DoctorOptions{}, err
	}
	dir := identity.DataDir(home)
	// The node's own spelling of this program (selfprogram.go): the generated files name it, and another one would
	// read as stale here and --fix would write them back the other way.
	_, bin, err := selfProgram(home, dir)
	if err != nil {
		return DoctorOptions{}, err
	}
	opts := DoctorOptions{
		Dir: dir, Bin: bin,
		NodeSock: nodeclient.SocketPath(), RuntimeSock: RuntimeSocketPath(), HerdrSock: herdr.SocketPath(),
		ClaudeConfigDir: claudeDirOf(home), ClaudeVersion: claudeVersion, ClaudeProbe: claudeProbe,
	}
	if an := (&Node{home: home, dir: dir, bin: bin}); an.agyInstalled() {
		opts.AgyHooksFile = an.agyHooksFile()
	}
	opts.withLaunchd(home)
	// Under the node's agent (a claude the node started runs the doctor) a reload would stop the doctor too: under
	// either label, today's or the one a Mac set up before the rename keeps.
	opts.InsideNode = isNodeLaunchdLabel(os.Getenv("XPC_SERVICE_NAME"))
	if runtime.GOOS != "windows" {
		opts.Preflight = preflightFromNode(opts.NodeSock, home, opts.ClaudeConfigDir)
	}
	return opts, nil
}

// withLaunchd points the launchd-process-type check at the resident node's agent, on a Mac only.
func (o *DoctorOptions) withLaunchd(home string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// launchctl is asked only when both labels have a plist; bounded, as everything else here.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.withLaunchAgent(ctx, home, launchdDomain(), os.Getenv("XPC_SERVICE_NAME"), runLaunchctl)
}

// withLaunchAgent points the check at the agent pickNodeLaunchdLabel picks in domain, asking launchctl.
func (o *DoctorOptions) withLaunchAgent(ctx context.Context, home, domain, self string,
	launchctl func(context.Context, ...string) (string, error)) {
	label := pickNodeLaunchdLabel(ctx, home, domain, self, launchctl)
	o.LaunchAgent, o.LaunchdTarget, o.Launchctl = NodeLaunchAgentPath(home, label), domain+"/"+label, launchctl
}

// generatedFile is a file the node writes for the sessions it starts: Group is the unit the doctor reports on, Rel
// the path under the node's directory.
type generatedFile struct {
	Group   string
	Rel     string
	Content []byte
}

// generatedGroups are the groups of generatedFiles, then the skills plugin (agent-plugin, assistantskills).
var generatedGroups = []string{"claude-settings.json", "claude-hooks", "assistant-settings.json", "assistant-hooks", "mcp.json"}

const agentPluginGroup = "agent-plugin"

// cuaMCPMatcher is the PreToolUse matcher of Cua Driver's MCP tools, under whatever name with "cua" in it the
// user gave the server (cuaToolOf decides).
const cuaMCPMatcher = "mcp__.*cua.*__.*"

// hookSettings is the settings with Persona's hooks for a claude whose program is bin, of the node whose directory is
// dir: the agents' (assistant false) or the assistant's, which add the Feishu gate on Bash (larkgate.go). The hook
// names dir itself (rootAssign): Claude Code runs it with the claude's environment, which is its pane's (agentenv.go).
func hookSettings(bin, dir string, assistant bool) map[string]any {
	return hookSettingsForOS(runtime.GOOS, bin, dir, assistant)
}

func hookSettingsForOS(goos, bin, dir string, assistant bool) map[string]any {
	cmd := shellQuote(bin) + " hook"
	if dir != "" {
		cmd = rootAssign(dir, shellQuote) + cmd
	}
	hook := func(timeout int) map[string]any {
		h := map[string]any{"type": "command", "command": cmd, "timeout": timeout}
		if goos == "windows" {
			// Native hook argv avoids Git Bash consuming the quotes around Windows paths.
			// Keep the same node and gates, with its data directory passed explicitly.
			args := []string{"hook"}
			if dir != "" {
				args = append(args, nodeclient.HomeFlag, dir)
			}
			h["command"], h["args"] = bin, args
		}
		return h
	}
	blocking := func(matcher string) map[string]any {
		return map[string]any{"matcher": matcher, "hooks": []any{hook(86400)}}
	}
	notify := map[string]any{"hooks": []any{hook(10)}}
	postQuestion := map[string]any{"matcher": "AskUserQuestion", "hooks": notify["hooks"]}
	pre := []any{blocking("AskUserQuestion")}
	if assistant {
		pre = append(pre, blocking("Bash"))
	}
	if goos == "darwin" {
		// An Agent's own Cua Driver never operates System Settings or a permission prompt (cua_gate.go, #4349): its
		// MCP tools, and Bash commands running cua-driver. Decided at once, so a short timeout. The assistant's
		// Bash hook above takes its commands to the gate too; an Agent's goes to the node only when the command
		// mentions cua-driver (nodeclient.CuaOnlyFlag), so other Bash commands cost a process start and nothing else.
		quick := func(matcher, command string) map[string]any {
			h := hook(60)
			h["command"] = command
			return map[string]any{"matcher": matcher, "hooks": []any{h}}
		}
		pre = append(pre, quick(cuaMCPMatcher, cmd))
		if !assistant {
			pre = append(pre, quick("Bash", cmd+" "+nodeclient.CuaOnlyFlag))
		}
	}
	return map[string]any{"hooks": map[string]any{
		"PreToolUse":        pre,
		"PostToolUse":       []any{postQuestion},
		"PermissionRequest": []any{blocking("*")},
		"Elicitation":       []any{blocking("*")},
		"Notification":      []any{notify},
		"Stop":              []any{notify},
		"UserPromptSubmit":  []any{notify},
		"SessionStart":      []any{notify},
	}}
}

// displayHooks is the MessageDisplay hook of the Agents Persona starts, the live replies plugin's (hooksplugin.go,
// livereply.go, #1309), for the node whose directory is dir: Claude Code hands it each batch of whole lines of a reply
// as it shows them. It shows the lines only once the hook is done, whatever "async" says (2.1.319), so the hook just
// passes them to the node and prints nothing (nodeclient.Display, at most 150 ms after it started); the timeout
// (seconds, the shortest whole one) bounds one that hangs, after which Claude shows the lines as they are. It is in
// exec form ("command" the program, "args" its arguments): Claude Code starts it directly instead of through /bin/sh -c,
// which costs every batch 6–8 ms more on a Mac (docs/integrations/claude-integration.md "What the terminal pays").
// Every Claude Code with the event takes exec form (2.1.150 has exec form and not the event; 2.1.200 has both). With
// no shell to put PERSONA_HOME in front (hookSettings), the hook is told dir as an argument (nodeclient.HomeFlag).
// The headless assistant streams its replies itself and has none.
func displayHooks(bin, dir string) []any {
	args := []string{"hook"}
	if dir != "" {
		args = append(args, nodeclient.HomeFlag, dir)
	}
	return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": bin, "args": args, "async": true, "timeout": 1}}}}
}

// generatedFiles is every file writeGeneratedConfig writes under the node's directory dir (but the skills plugin),
// computed without writing. Each set of hooks is there twice: as a plugin for a claude that loads plugins, in a
// settings file for one that does not (hooksplugin.go). The hooks and the MCP server are told dir, as PERSONA_HOME.
// The Agents' live replies plugin is part of their hooks (claude-hooks), written whatever the setting says: a start
// loads it or not (liveRepliesPlugin).
func generatedFiles(bin, dir string) ([]generatedFile, error) {
	var out []generatedFile
	for _, kind := range []string{"claude", "assistant"} {
		settings := hookSettings(bin, dir, kind == "assistant")
		b, err := marshalJSON(settings)
		if err != nil {
			return nil, err
		}
		out = append(out, generatedFile{Group: kind + "-settings.json", Rel: kind + "-settings.json", Content: b})
		files, err := hooksPluginFiles(settings["hooks"])
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(files)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(kind+"-hooks", rel), Content: files[rel]})
		}
		if kind != "claude" {
			continue
		}
		live, err := liveRepliesPluginFiles(bin, dir)
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(live)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(liveRepliesDir, rel), Content: live[rel]})
		}
	}
	server := map[string]any{"command": bin, "args": []string{"mcp"}}
	if dir != "" {
		server["env"] = map[string]string{"PERSONA_HOME": dir}
	}
	// This shared file also serves automatic Agent resumes, before any hub handshake.
	// Keep both namespaces here so historical tool calls remain available. Assistant
	// starts choose their own per-process registration after capability negotiation.
	mcp, err := marshalJSON(map[string]any{"mcpServers": compat.MCPRegistrationServers(map[string]any{"persona": server}, true)})
	if err != nil {
		return nil, err
	}
	return append(out, generatedFile{Group: "mcp.json", Rel: "mcp.json", Content: mcp}), nil
}

// writeGeneratedConfig writes the hooks, MCP config and skills plugin that sessions Persona starts load into dir,
// for a claude whose hook program is bin: what the node does at each start, and what `node doctor --fix` does.
func writeGeneratedConfig(dir, bin string) error {
	files, err := generatedFiles(bin, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(path, f.Content); err != nil {
			return err
		}
	}
	return assistantskills.WritePlugin(filepath.Join(dir, agentPluginGroup)) // the skills the agents Persona starts load (agentplugin.go)
}

// RunDoctor runs the checks. With opts.Fix it first puts right what it may (the generated files; outside the node, the
// LaunchAgent's ProcessType), then checks again.
func RunDoctor(ctx context.Context, opts DoctorOptions) DoctorReport {
	if opts.Lang == "" {
		opts.Lang = i18n.Default
	} else {
		opts.Lang = i18n.Resolve(string(opts.Lang))
	}
	if pre := opts.Preflight; pre != nil { // read once: nothing a fix does changes what it reads
		var once sync.Once
		var c DoctorCheck
		opts.Preflight = func(ctx context.Context, lang i18n.Lang) DoctorCheck {
			once.Do(func() { c = pre(ctx, lang) })
			return c
		}
	}
	rep := doctorChecks(ctx, opts)
	if !opts.Fix {
		return rep
	}
	var fixed []string
	var failed []DoctorCheck
	var stale []string
	if fixAgyHooks(opts) {
		fixed = append(fixed, agyHooksCheck)
	}
	for _, c := range rep.Checks {
		if c.State == "stale" || c.State == "missing" {
			stale = append(stale, c.Name)
		}
	}
	if len(stale) > 0 {
		if err := writeGeneratedConfig(opts.Dir, opts.Bin); err != nil {
			failed = append(failed, DoctorCheck{Name: "fix", Status: DoctorFail,
				Detail: i18n.T(opts.Lang, "node-doctor.fix_regenerate_failed", i18n.Args{"error": err.Error()}),
				Hint:   i18n.T(opts.Lang, "node-doctor.fix_regenerate_hint", i18n.Args{"dir": opts.Dir}), Items: []string{}})
		} else {
			fixed = append(fixed, stale...)
		}
	}
	var notBack []string
	logPath := ""
	if opts.LaunchAgent != "" && !opts.InsideNode {
		a := inspectLaunchAgent(ctx, opts)
		wrote, reloaded, err := fixLaunchAgent(ctx, opts, a)
		if err != nil {
			failed = append(failed, DoctorCheck{Name: "fix", Status: DoctorFail, Detail: err.Error(),
				Hint: i18n.T(opts.Lang, "node-doctor.fix_reinstall_hint"), Items: []string{opts.LaunchAgent}})
		}
		if wrote {
			fixed = append(fixed, launchdCheck)
		}
		if reloaded {
			fixed = append(fixed, launchdReloaded)
			// bootstrap returns before the new node listens: checked at once, its sockets would look dead.
			notBack, logPath = waitForRestart(ctx, opts, rep), a.logPath
		}
	}
	if len(fixed) == 0 && len(failed) == 0 {
		return rep
	}
	rep = doctorChecks(ctx, opts)
	notBackYet(&rep, notBack, opts.RestartWait, logPath, opts.Lang)
	rep.Fixed = append(rep.Fixed, fixed...)
	if len(failed) > 0 {
		rep.Checks = append(rep.Checks, failed...)
		rep.OK = false
	}
	return rep
}

func doctorChecks(ctx context.Context, opts DoctorOptions) DoctorReport {
	rep := DoctorReport{Dir: opts.Dir, Fixed: []string{}}
	add := func(c DoctorCheck) {
		if c.Items == nil {
			c.Items = []string{}
		}
		rep.Checks = append(rep.Checks, c)
	}
	var pre chan DoctorCheck
	if opts.Preflight != nil { // reads in a child of its own, alongside the other checks
		pre = make(chan DoctorCheck, 1)
		go func() { pre <- opts.Preflight(ctx, opts.Lang) }()
	}
	add(checkNodeSocketIn(ctx, opts.NodeSock, opts.Lang))
	add(checkRuntimeSocketIn(ctx, opts.RuntimeSock, opts.Lang))
	for _, c := range checkGenerated(opts) {
		add(c)
	}
	add(checkCommandPathsIn(opts.Dir, opts.Lang))
	add(checkClaudeVersionIn(ctx, opts.claudeProbe(), opts.Lang))
	add(checkHerdrIn(ctx, opts.HerdrSock, opts.Lang))
	add(checkGlobalPermissionHooksIn(opts.ClaudeConfigDir, opts.Dir, opts.Lang))
	if opts.AgyHooksFile != "" {
		add(checkAgyHooks(opts))
	}
	add(checkAgentSandboxIn(opts.Dir, opts.Lang))
	if opts.LaunchAgent != "" {
		add(checkLaunchAgent(inspectLaunchAgent(ctx, opts), opts))
	}
	if pre != nil {
		add(<-pre)
	}
	rep.OK = !slices.ContainsFunc(rep.Checks, func(c DoctorCheck) bool { return c.Status == DoctorFail })
	return rep
}

const doctorDialTimeout = 3 * time.Second

func checkNodeSocket(ctx context.Context, path string) DoctorCheck {
	return checkNodeSocketIn(ctx, path, i18n.Default)
}

func checkNodeSocketIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "node.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorFail, i18n.T(lang, "node-doctor.socket_missing")
		c.Hint = i18n.T(lang, "node-doctor.node_socket_missing_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	line, err := nodeclient.DialAt(ctx, path, sockRequest{Kind: "ping"})
	if err != nil {
		c.Status = DoctorFail
		c.Detail = i18n.T(lang, "node-doctor.node_socket_unanswered", i18n.Args{"error": shorten(err.Error(), 80)})
		c.Hint = i18n.T(lang, "node-doctor.socket_stale_hint")
		return c
	}
	if !json.Valid(line) {
		c.Status, c.Detail = DoctorFail, i18n.T(lang, "node-doctor.node_socket_invalid")
		c.Hint = i18n.T(lang, "node-doctor.node_socket_invalid_hint")
		return c
	}
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.node_socket_ok")
	return c
}

func checkRuntimeSocket(ctx context.Context, path string) DoctorCheck {
	return checkRuntimeSocketIn(ctx, path, i18n.Default)
}

func checkRuntimeSocketIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "runtime.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.socket_missing")
		c.Hint = i18n.T(lang, "node-doctor.runtime_socket_missing_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		c.Status = DoctorFail
		c.Detail = i18n.T(lang, "node-doctor.runtime_socket_unavailable", i18n.Args{"error": shorten(err.Error(), 80)})
		c.Hint = i18n.T(lang, "node-doctor.socket_stale_hint")
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.runtime_socket_ok")
	return c
}

// checkGenerated compares each generated file with what this binary would write: current, stale (differs, or part
// of a plugin is missing) or missing.
func checkGenerated(opts DoctorOptions) []DoctorCheck {
	files, err := generatedFiles(opts.Bin, opts.Dir)
	if err != nil {
		return []DoctorCheck{{Name: "generated-files", Status: DoctorFail, Detail: err.Error(), Items: []string{}}}
	}
	var out []DoctorCheck
	for _, g := range generatedGroups {
		var issues []string
		total := 0
		for _, f := range files {
			if f.Group != g {
				continue
			}
			total++
			got, err := os.ReadFile(filepath.Join(opts.Dir, f.Rel))
			switch {
			case err != nil:
				issues = append(issues, "missing "+filepath.ToSlash(f.Rel))
			case !bytes.Equal(got, f.Content):
				issues = append(issues, "differs "+filepath.ToSlash(f.Rel))
			}
		}
		allMissing := len(issues) == total && allPrefix(issues, "missing")
		out = append(out, generatedCheck(g, issues, allMissing, true, opts.Lang))
	}
	issues := assistantskills.PluginIssues(filepath.Join(opts.Dir, agentPluginGroup))
	allMissing := len(issues) == 1 && issues[0] == "missing"
	return append(out, generatedCheck(agentPluginGroup, issues, allMissing, false, opts.Lang))
}

// generatedCheck turns what differs in one generated file or directory into its check. critical: the sessions
// lose their hooks or MCP without it (missing fails), otherwise only the skills (missing warns).
func generatedCheck(name string, issues []string, allMissing, critical bool, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: name, Items: issues}
	if c.Items == nil {
		c.Items = []string{}
	}
	switch {
	case len(issues) == 0:
		c.State, c.Status, c.Detail = "current", DoctorOK, i18n.T(lang, "node-doctor.generated_current")
	case allMissing:
		c.State, c.Status, c.Detail = "missing", DoctorWarn, i18n.T(lang, "node-doctor.generated_missing")
		if critical {
			c.Status = DoctorFail
			c.Hint = i18n.T(lang, "node-doctor.generated_missing_critical_hint")
		} else {
			c.Hint = i18n.T(lang, "node-doctor.generated_missing_plugin_hint")
		}
	default:
		c.State, c.Status = "stale", DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.generated_stale", i18n.Args{"count": len(issues), "issue": shorten(issues[0], 60)})
		c.Hint = i18n.T(lang, "node-doctor.generated_stale_hint")
	}
	return c
}

func allPrefix(items []string, prefix string) bool {
	return !slices.ContainsFunc(items, func(s string) bool { return !strings.HasPrefix(s, prefix) })
}

// configCommands are the programs a settings file or hooks plugin (top-level "hooks") runs: a shell-form hook's first
// word, an exec-form one's (with "args") command as it is.
func configCommands(b []byte) []string {
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []string
	for _, matchers := range f.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				w := h.Command
				if h.Args == nil {
					w = shellFirstWord(h.Command)
				}
				if w != "" {
					out = append(out, w)
				}
			}
		}
	}
	for _, s := range f.MCPServers {
		if s.Command != "" {
			out = append(out, s.Command)
		}
	}
	return out
}

// shellFirstWord is the program a command line written with shellQuote runs: its first word past the VAR=value ones
// in front (rootAssign). A word is bare, or single-quoted with each embedded quote or backslash closed,
// backslash-escaped and reopened. Other tools' hooks in the user's settings.json (hookProgram) may double-quote a
// word instead: read the same way, nothing expanded inside. shellQuote never writes a bare ", so its lines read as before.
func shellFirstWord(cmd string) string {
	for rest := strings.TrimSpace(cmd); rest != ""; {
		var w strings.Builder
		i := 0
		for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' {
			switch q := rest[i]; {
			case q == '\'' || q == '"':
				j := strings.IndexByte(rest[i+1:], q)
				if j < 0 {
					return "" // not a command line shellQuote writes
				}
				w.WriteString(rest[i+1 : i+1+j])
				i += j + 2
			case rest[i] == '\\' && i+1 < len(rest):
				w.WriteByte(rest[i+1])
				i += 2
			default:
				w.WriteByte(rest[i])
				i++
			}
		}
		if !assignmentRe.MatchString(rest[:i]) {
			return w.String()
		}
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return ""
}

// assignmentRe is a shell word that sets a variable for the command after it.
var assignmentRe = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*=`)

// checkCommandPaths: the program each generated file runs exists and can be run. A file moved or removed under a
// running node (a redeploy to another path) leaves hooks that fail on every call.
func checkCommandPaths(dir string) DoctorCheck {
	return checkCommandPathsIn(dir, i18n.Default)
}

func checkCommandPathsIn(dir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "command-paths", Items: []string{}}
	files := []string{"claude-settings.json", "assistant-settings.json", "mcp.json"}
	for _, kind := range []string{"claude", "assistant"} {
		files = append(files, filepath.Join(kind+"-hooks", "hooks", "hooks.json"))
	}
	files = append(files, filepath.Join(liveRepliesDir, "hooks", "hooks.json"))
	seen := map[string]bool{}
	var bad []string
	read := 0
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		read++
		for _, p := range configCommands(b) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if err := runnable(p); err != nil {
				bad = append(bad, fmt.Sprintf("%s: %v", p, err))
			}
		}
	}
	switch {
	case len(bad) > 0:
		slices.Sort(bad)
		c.Status, c.Items = DoctorFail, bad
		c.Detail = i18n.T(lang, "node-doctor.command_paths_bad", i18n.Args{"count": len(bad), "issue": shorten(bad[0], 80)})
		c.Hint = i18n.T(lang, "node-doctor.command_paths_bad_hint")
	case read == 0:
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.command_paths_none")
		c.Hint = i18n.T(lang, "node-doctor.command_paths_none_hint")
	default:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.command_paths_ok", i18n.Args{"count": len(seen)})
	}
	return c
}

func runnable(path string) error {
	if !filepath.IsAbs(path) { // a bare name: found on PATH like claude would
		if _, err := exec.LookPath(path); err != nil {
			return errors.New("not on PATH")
		}
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return errors.New("not found")
	}
	if st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		return errors.New("not executable")
	}
	return nil
}

// ClaudeVersionUnanswered is the claude-version check's state when no version was read but the shell did find claude
// (it ran out of time, or printed none): the assistant's readiness then says unknown, not claude_missing. The hub
// matches the literal (assistant/readiness.go); "" is every other outcome, as with nodes from before it.
const ClaudeVersionUnanswered = "unanswered"

// claudeProbe is ClaudeProbe, or ClaudeVersion read as it always was: no version is no claude.
func (o DoctorOptions) claudeProbe() func(context.Context) (string, bool) {
	if o.ClaudeProbe != nil {
		return o.ClaudeProbe
	}
	if o.ClaudeVersion == nil {
		return nil
	}
	return func(ctx context.Context) (string, bool) {
		v := o.ClaudeVersion(ctx)
		return v, v == ""
	}
}

func checkClaudeVersion(ctx context.Context, ask func(context.Context) (string, bool)) DoctorCheck {
	return checkClaudeVersionIn(ctx, ask, i18n.Default)
}

func checkClaudeVersionIn(ctx context.Context, ask func(context.Context) (string, bool), lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "claude-version"}
	v, missing := "", true
	if ask != nil {
		v, missing = ask(ctx)
	}
	switch {
	case v == "" && !missing:
		c.Status, c.State, c.Detail = DoctorWarn, ClaudeVersionUnanswered, i18n.T(lang, "node-doctor.claude_unanswered")
		c.Hint = i18n.T(lang, "node-doctor.claude_unanswered_hint")
	case v == "":
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.claude_missing")
		c.Hint = i18n.T(lang, "node-doctor.claude_missing_hint")
	case !versionAtLeast(v, pluginMinVersion):
		c.Status = DoctorWarn
		min := fmt.Sprintf("%d.%d.%d", pluginMinVersion[0], pluginMinVersion[1], pluginMinVersion[2])
		c.Detail = i18n.T(lang, "node-doctor.claude_too_old", i18n.Args{"version": v, "minimum": min})
		c.Hint = i18n.T(lang, "node-doctor.claude_too_old_hint")
	default:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.claude_ok", i18n.Args{"version": v})
	}
	return c
}

// checkHerdr dials herdr's API socket. The engine is chosen by the hub when it connects and is not kept on this
// machine, so a socket that is not there is a warning: it only matters when herdr is this machine's engine.
func checkHerdr(ctx context.Context, path string) DoctorCheck {
	return checkHerdrIn(ctx, path, i18n.Default)
}

func checkHerdrIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "herdr"}
	if path == "" {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.herdr_no_path")
		c.Hint = i18n.T(lang, "node-doctor.herdr_no_path_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	conn, err := herdr.Dial(ctx, path)
	if err != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.herdr_unavailable")
		c.Hint = i18n.T(lang, "node-doctor.herdr_unavailable_hint")
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.herdr_ok")
	return c
}

// checkAgentSandbox is the doctor's line for the agent sandbox: whether it is on and where from, whether this machine
// can run it, and each kind of start's last status.
func checkAgentSandbox(dir string) DoctorCheck {
	return checkAgentSandboxIn(dir, i18n.Default)
}

func checkAgentSandboxIn(dir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "agent-sandbox", Status: DoctorOK, Items: []string{}}
	on, from := agentSandboxOn(dir)
	support := agentSandboxProbe() // macOS runs sandbox-exec true; Linux runs a real default-allow bwrap probe
	sup := sandbox.AgentSupport(support)
	supWhy := sup.WhyIn(lang)
	last := readAgentSandboxStatus(dir)
	wheres := make([]string, 0, len(last))
	for w := range last {
		wheres = append(wheres, w)
	}
	slices.Sort(wheres)
	for _, w := range wheres {
		r := last[w]
		key := "node-doctor.sandbox_status_item"
		args := i18n.Args{"where": w, "status": agentSandboxWord(lang, r.State), "at": r.At.Local().Format("01-02 15:04")}
		if r.Why != "" {
			key = "node-doctor.sandbox_status_item_why"
			args["why"] = r.WhyIn(lang)
		}
		c.Items = append(c.Items, i18n.T(lang, key, args))
	}
	switch {
	case !on:
		if from == "" {
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_default")
		} else {
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_from", i18n.Args{"from": from})
		}
		switch sup.State {
		case sandbox.AgentActive:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_available", i18n.Args{"detail": c.Detail})
			c.Hint = i18n.T(lang, "node-doctor.sandbox_enable_hint", i18n.Args{"path": filepath.Join(dir, sandbox.AgentOnFile)})
		case sandbox.AgentUnsupported:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_unsupported", i18n.Args{"detail": c.Detail, "why": supWhy})
		default:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_unavailable", i18n.Args{"detail": c.Detail, "why": supWhy})
		}
	case sup.State == sandbox.AgentUnsupported:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on_unsupported", i18n.Args{"why": supWhy})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_runs_outside_hint")
	case sup.State == sandbox.AgentUnavailable:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on_unavailable", i18n.Args{"why": supWhy})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_runs_outside_hint")
	default:
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on", i18n.Args{"from": from, "limits": agentSandboxLimitsIn(support.OS, lang)})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_environment_hint", i18n.Args{"env": agentSandboxEnv})
		for _, w := range wheres {
			if s := last[w].State; s == sandbox.AgentSkipped || s == sandbox.AgentUnavailable {
				c.Status = DoctorWarn
				c.Detail = i18n.T(lang, "node-doctor.sandbox_on_skipped", i18n.Args{"detail": c.Detail})
				c.Hint = i18n.T(lang, "node-doctor.sandbox_skipped_hint")
				break
			}
		}
	}
	if len(c.Items) > 0 {
		c.Detail = i18n.T(lang, "node-doctor.sandbox_with_recent", i18n.Args{
			"detail": c.Detail, "items": strings.Join(c.Items, i18n.T(lang, "node-doctor.item_separator")),
		})
	}
	return c
}

// agentSandboxLimits is what the doctor reminds of while the sandbox is on (docs/server/agent-sandbox.md): what it does not
// stop, which agents it does not cover, and platform-specific behavior that needs a restart or stops working.
func agentSandboxLimits(goos string) string {
	return agentSandboxLimitsIn(goos, i18n.Default)
}

func agentSandboxLimitsIn(goos string, lang i18n.Lang) string {
	limits := i18n.T(lang, "node-doctor.sandbox_limits")
	switch goos {
	case "darwin":
		return i18n.T(lang, "node-doctor.sandbox_limits_darwin", i18n.Args{"limits": limits})
	case "linux":
		return i18n.T(lang, "node-doctor.sandbox_limits_linux", i18n.Args{"limits": limits})
	default:
		return limits
	}
}

// agentSandboxWord is one status in the doctor's words. Unknown future states keep the old empty word.
func agentSandboxWord(lang i18n.Lang, state string) string {
	key := map[string]string{
		sandbox.AgentOff:         "node-doctor.sandbox_state_off",
		sandbox.AgentActive:      "node-doctor.sandbox_state_active",
		sandbox.AgentSkipped:     "node-doctor.sandbox_state_skipped",
		sandbox.AgentUnsupported: "node-doctor.sandbox_state_unsupported",
		sandbox.AgentUnavailable: "node-doctor.sandbox_state_unavailable",
	}[state]
	if key == "" {
		return ""
	}
	return i18n.T(lang, key)
}

// checkGlobalPermissionHooks looks at the user's own settings.json for synchronous PermissionRequest hooks (other
// tools put them there) and says how many; it never changes the file. One whose program is in a package of an
// extension installed on this computer (dataDir's node-plugins, extensionHookOwner) is that extension's, named and
// not warned about (#11973).
func checkGlobalPermissionHooks(configDir, dataDir string) DoctorCheck {
	return checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
}

func checkGlobalPermissionHooksIn(configDir, dataDir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "global-permission-hooks", Items: []string{}}
	b, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.global_hooks_no_file")
		return c
	}
	if err != nil {
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_unreadable", i18n.Args{"error": shorten(err.Error(), 60)})
		return c
	}
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Async   bool     `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &f) != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.global_hooks_invalid")
		c.Hint = i18n.T(lang, "node-doctor.global_hooks_invalid_hint")
		return c
	}
	var foreign, extensions []string
	fromExtensions := 0
	for _, m := range f.Hooks["PermissionRequest"] {
		for _, h := range m.Hooks {
			if h.Async || h.Type != "command" && h.Type != "" {
				continue
			}
			c.Items = append(c.Items, shorten(h.Command, 80))
			if name := extensionHookOwner(dataDir, hookProgram(h.Command, h.Args)); name != "" {
				fromExtensions++
				if !slices.Contains(extensions, name) {
					extensions = append(extensions, name)
				}
				continue
			}
			foreign = append(foreign, shorten(h.Command, 80))
		}
	}
	names := strings.Join(extensions, i18n.T(lang, "node-doctor.list_separator"))
	switch {
	case len(c.Items) == 0:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.global_hooks_none")
	case len(foreign) == 0: // all the user's own choice: installed extensions'
		c.Status = DoctorOK
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_extensions", i18n.Args{"count": fromExtensions, "extensions": names})
	default:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_found", i18n.Args{"count": len(foreign), "hook": foreign[0]})
		if fromExtensions > 0 {
			c.Detail = i18n.T(lang, "node-doctor.global_hooks_found_with_extensions", i18n.Args{
				"count": len(foreign), "hook": foreign[0], "extension_count": fromExtensions, "extensions": names,
			})
		}
		c.Hint = i18n.T(lang, "node-doctor.global_hooks_found_hint")
	}
	return c
}

// shorten cuts s to at most n runes, with … when it cut.
func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// DoctorCommand is `personad node doctor [--json] [--fix]`: it prints the report to out and says whether a check
// failed (the caller exits non-zero then).
func DoctorCommand(ctx context.Context, args []string, out io.Writer) (failed bool, err error) {
	return doctorCommand(ctx, args, out, DefaultDoctorOptions)
}

func doctorCommand(ctx context.Context, args []string, out io.Writer, defaults func() (DoctorOptions, error)) (failed bool, err error) {
	if len(args) > 0 && args[0] == preflightChildArg { // the preflight's reads, in a child of the doctor's (preflight.go)
		preflightChild(args[1:], out)
		return false, nil
	}
	lang := i18n.Terminal()
	flags := flag.NewFlagSet("node doctor", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, i18n.T(lang, "node-doctor.flag_json"))
	fix := flags.Bool("fix", false, i18n.T(lang, "node-doctor.flag_fix"))
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		return false, errors.New(i18n.T(lang, "node-doctor.positional_arguments"))
	}
	if *asJSON {
		// JSON is machine output: every human field keeps its legacy zh-Hans bytes, including a preflight answer
		// requested from the resident node. The terminal language is used only for flag help and argument errors.
		lang = i18n.Default
	}
	opts, err := defaults()
	if err != nil {
		return false, err
	}
	opts.Fix = *fix
	opts.Lang = lang
	rep := RunDoctor(ctx, opts)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return !rep.OK, enc.Encode(rep)
	}
	PrintDoctorIn(out, rep, lang)
	return !rep.OK, nil
}

// PrintDoctor writes the legacy zh-Hans text report. New display paths choose their language explicitly.
func PrintDoctor(out io.Writer, rep DoctorReport) {
	PrintDoctorIn(out, rep, i18n.Default)
}

// PrintDoctorIn writes rep, one line per check: label | name: what was found; what to do about it.
func PrintDoctorIn(out io.Writer, rep DoctorReport, lang i18n.Lang) {
	fmt.Fprintln(out, i18n.T(lang, "node-doctor.title", i18n.Args{"dir": rep.Dir}))
	counts := map[string]int{}
	for _, c := range rep.Checks {
		counts[c.Status]++
		args := i18n.Args{"label": doctorLabel(lang, c.Status), "name": c.Name, "detail": c.Detail}
		key := "node-doctor.check_line"
		if c.Hint != "" && c.Status != DoctorOK {
			key = "node-doctor.check_line_hint"
			args["hint"] = c.Hint
		}
		fmt.Fprintln(out, i18n.T(lang, key, args))
	}
	var regenerated []string
	for _, f := range rep.Fixed {
		switch f {
		case launchdCheck:
			fmt.Fprintln(out, i18n.T(lang, "node-doctor.fixed_launchd"))
		case launchdReloaded:
			fmt.Fprintln(out, i18n.T(lang, "node-doctor.fixed_reloaded"))
		default:
			regenerated = append(regenerated, f)
		}
	}
	if len(regenerated) > 0 {
		fmt.Fprintln(out, i18n.T(lang, "node-doctor.fixed_regenerated", i18n.Args{
			"items": strings.Join(regenerated, i18n.T(lang, "node-doctor.list_separator")),
		}))
	}
	fmt.Fprintln(out, i18n.T(lang, "node-doctor.summary", i18n.Args{
		"total": len(rep.Checks), "ok": counts[DoctorOK], "warn": counts[DoctorWarn], "fail": counts[DoctorFail],
	}))
}

func doctorLabel(lang i18n.Lang, status string) string {
	key := map[string]string{
		DoctorOK: "node-doctor.label_ok", DoctorWarn: "node-doctor.label_warn", DoctorFail: "node-doctor.label_fail",
	}[status]
	if key == "" {
		return ""
	}
	return i18n.T(lang, key)
}
