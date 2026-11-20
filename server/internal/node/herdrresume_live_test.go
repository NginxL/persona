//go:build herdrlive && !windows

package node

import (
	"cmp"
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// Against a real herdr and real Claude Code, in a herdr session of its own (never default or the node's own), on Linux
// (it reads /proc):
//
//	HERDR_SESSION=persona-resume-test PERSONA_HERDR_RESUME_LIVE=persona-resume-test \
//	  PERSONA_RESUME_SETTINGS=<a gateway's settings file> PERSONA_RESUME_CONFIG_DIR=<a Claude profile directory> \
//	  PERSONA_RESUME_CWD=<a folder that profile trusts> ./node.test -test.run TestLiveHerdrResumesThroughTheLauncher -test.v
//
// or, for launchers on PATH that wrap claude with flags of their own (a gateway's --settings, --model):
//
//	… PERSONA_RESUME_LAUNCHERS=<launcher>,<launcher> PERSONA_RESUME_CWD=<a folder their Claude trusts> ./node.test …
//
// Claudes start the way the node starts them: one with a launcher's --settings and one in a CLAUDE_CONFIG_DIR profile,
// or one per launcher. Each gets one message so it has a transcript, and holdResume gives herdr its resume command. The
// session's server is stopped and started twice (a client attaches each time: herdr resumes only then). After the
// first restart the node sees a new server (herdr.Client.ServerID) and reports the command again; after each restart
// the resumed claude must carry Persona's --mcp-config and the --settings, --model and CLAUDE_CONFIG_DIR its first
// claude was started with, never herdr's bare `claude --resume <id>`, and the node must not list it off its launcher.
// (A shell function named claude, such as a gateway manager's, may add flags of its own in front: they are compared
// like the launcher's.) On a herdr older than 0.9.2, which drops the resume command (#11792), the resume is bare
// instead, and the node must list the Agent off its launcher.
func TestLiveHerdrResumesThroughTheLauncher(t *testing.T) {
	session := os.Getenv("PERSONA_HERDR_RESUME_LIVE")
	if session == "" || session == "default" || !strings.HasSuffix(session, "-test") || herdr.Session() != session {
		t.Skip("PERSONA_HERDR_RESUME_LIVE=<a session named *-test>, with HERDR_SESSION set to it, not set")
	}
	settings, configDir := os.Getenv("PERSONA_RESUME_SETTINGS"), os.Getenv("PERSONA_RESUME_CONFIG_DIR")
	launchers := strings.FieldsFunc(os.Getenv("PERSONA_RESUME_LAUNCHERS"), func(r rune) bool { return r == ',' })
	if len(launchers) == 0 && (settings == "" || configDir == "") {
		t.Skip("PERSONA_RESUME_LAUNCHERS, or PERSONA_RESUME_SETTINGS and PERSONA_RESUME_CONFIG_DIR, not set")
	}
	home, _ := os.UserHomeDir()
	cwd := os.Getenv("PERSONA_RESUME_CWD") // a folder the profile's Claude already trusts (no trust dialog)
	if cwd == "" {
		cwd = filepath.Join(home, "workspace")
	}
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("no herdr")
	}
	cli := func(args ...string) string {
		out, _ := exec.Command(herdrBin, append([]string{"--session", session}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	launchHerdr, findHerdr = launchHerdrServer, herdrBinary // the package's tests stub both out
	herdrPaneShell = userShell                              // TestMain pins it to zsh: the panes run the user's own shell
	herdr.ReportSeq = func() int64 { return time.Now().UnixNano() }
	ctx := context.Background()
	c := herdr.New()
	n, _ := testNode(t)
	n.dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(n.dir, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	n.hosts = herdrHosts(c)
	n.logger = log.New(os.Stderr, "node: ", log.Ltime)
	h := n.hosts.Get(host.Herdr).(*herdrHost)
	if err := n.ensureHerdr(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	defer cli("session", "stop", session)
	ws, err := c.WorkspaceCreate(ctx, cwd, "resume-live")
	if err != nil {
		t.Fatal(err)
	}

	type agent struct {
		name, pane, id, profile string
		spec                    claudeLaunch
		start                   liveClaude // its first claude, as started
	}
	var agents []*agent
	if len(launchers) > 0 {
		profile := cmp.Or(configDir, filepath.Join(home, ".claude"))
		for _, l := range launchers {
			agents = append(agents, &agent{name: l, spec: claudeLaunch{Launcher: l}, profile: profile})
		}
	} else {
		agents = []*agent{
			{name: "settings", spec: claudeLaunch{Launcher: "claude", Settings: settings}, profile: configDir},
			{name: "config-dir", spec: claudeLaunch{Launcher: "claude"}, profile: configDir},
		}
		n.claudeStartDir.Store(&configDir) // claude_start_dir: the node's, for every Claude it starts with the launcher "claude"
	}
	agents[0].pane = ws.RootPane.ID
	for _, a := range agents[1:] {
		tab, err := c.TabCreate(ctx, ws.Workspace.ID, cwd, a.name)
		if err != nil {
			t.Fatal(err)
		}
		a.pane = tab.RootPane.ID
	}
	own := n.agentMCPConfig()
	n.panes = map[string]string{}
	for _, a := range agents {
		a.id = newUUID()
		line := n.agentCommand(withHerdrAgent("claude", n.claudeCommandLine(a.spec, "--session-id "+a.id, shellQuote)), shellQuote, herdrPaneShell())
		t.Logf("LIVE start %s in %s: %s", a.name, a.pane, line)
		if err := c.PaneRun(ctx, a.pane, line); err != nil {
			t.Fatal(err)
		}
		waitWithin(t, 60*time.Second, a.name+" detected as claude", func() bool {
			g, err := c.AgentGet(ctx, a.pane)
			return err == nil && g.Agent == "claude" && g.AgentStatus == "idle"
		})
		_ = c.PaneRun(ctx, a.pane, "Reply with the single word ok.")
		waitWithin(t, 90*time.Second, a.name+"'s transcript", func() bool {
			m, _ := filepath.Glob(filepath.Join(a.profile, "projects", "*", a.id+".jsonl"))
			return len(m) > 0
		})
		a.start = liveClaudeRunning(a.id)
		if a.start.pid == 0 {
			t.Fatalf("%s: no claude runs %s", a.name, a.id)
		}
		t.Logf("LIVE started %s: pid %d, --settings %q --model %q CLAUDE_CONFIG_DIR=%q", a.name, a.start.pid, a.start.settings, a.start.model, a.start.configDir)
		st, _ := n.state.get(a.id) // the record control.go keeps for the start
		st.Launcher = a.spec.Launcher
		n.state.put(a.id, st)
		n.holdResume(ctx, h, a.pane, a.id, a.spec) // as control.go does after a start
		n.mu.Lock()
		got := n.launches[a.pane]
		n.mu.Unlock()
		if got.Server == "" || got.Server != c.ServerID() {
			t.Fatalf("%s: resume command not recorded against this server (%q, server %q)", a.name, got.Server, c.ServerID())
		}
	}
	for round := 1; round <= 2; round++ {
		before := c.ServerID()
		t.Logf("LIVE restart %d: %s", round, cli("session", "stop", session))
		if err := n.ensureHerdr(ctx, "live restart"); err != nil {
			t.Fatal(err)
		}
		attach := exec.Command("script", "-q", "/dev/null", "-c", herdrBin+" --session "+session)
		attach.Env = append(os.Environ(), "TERM=xterm-256color")
		if err := attach.Start(); err != nil {
			t.Fatal(err)
		}
		dropped := herdrDropsResume(c.ServerVersion(ctx)) // herdr resumes bare: what to expect, not what to accept
		waitWithin(t, 90*time.Second, "every agent resumed on the new server", func() bool {
			for _, a := range agents {
				g, err := c.AgentGet(ctx, a.pane)
				if err != nil || g.Agent != "claude" {
					return false
				}
				r := liveClaudeRunning(a.id)
				if r.pid == 0 || r.pid == a.start.pid || !slices.Contains(r.argv, "--resume") || !dropped && !r.mcp(own) {
					return false
				}
			}
			return true
		})
		_ = attach.Process.Kill()
		_ = attach.Wait()
		if c.ServerID() == before {
			t.Fatalf("round %d: the server's socket did not change", round)
		}
		t.Logf("LIVE herdr server %s (drops resume commands: %v)", c.ServerVersion(ctx), dropped)
		// The node's scan of running claudes, over the real profiles, fresh each time: the node's own comes every
		// claudeScanEvery.
		n.home = home
		offNow := func(id string) bool {
			n.cdirs = newClaudeDirs()
			return n.offLauncher(id)
		}
		for _, a := range agents {
			r := liveClaudeRunning(a.id)
			off := offNow(a.id)
			seen, ok := n.sessionRun(a.id)
			t.Logf("LIVE after restart %d, %s (%s): pid %d\n  argv: %s\n  --settings %q --model %q CLAUDE_CONFIG_DIR=%q\n  scan: %+v (%v), off_launcher=%v",
				round, a.name, a.id, r.pid, strings.Join(r.argv, " "), r.settings, r.model, r.configDir, seen, ok, off)
			if dropped {
				if r.mcp(own) {
					t.Errorf("round %d: %s on a herdr that drops resume commands resumed with Persona's command", round, a.name)
				}
				waitWithin(t, 60*time.Second, a.name+" listed off its launcher", func() bool { return offNow(a.id) })
				t.Logf("LIVE   %s: off_launcher=true", a.name)
				continue
			}
			if !r.mcp(own) {
				t.Errorf("round %d: %s was not resumed with Persona's command", round, a.name)
			}
			if !slices.Equal(r.settings, a.start.settings) || !slices.Equal(r.model, a.start.model) || r.configDir != a.start.configDir {
				t.Errorf("round %d: %s resumed off its launcher: --settings %q --model %q CLAUDE_CONFIG_DIR=%q, started with %q %q %q",
					round, a.name, r.settings, r.model, r.configDir, a.start.settings, a.start.model, a.start.configDir)
			}
			waitWithin(t, 60*time.Second, a.name+" seen by the scan", func() bool {
				off = offNow(a.id)
				_, ok := n.sessionRun(a.id)
				return ok
			})
			t.Logf("LIVE   %s: seen by the scan, off_launcher=%v", a.name, off)
			if off {
				t.Errorf("round %d: %s listed off its launcher", round, a.name)
			}
		}
		// The node's refresh: a new server, so each pane's command again.
		list := make([]host.Agent, len(agents))
		n.mu.Lock()
		for i, a := range agents {
			n.panes[a.pane] = a.id
			list[i] = host.Agent{Agent: "claude", PaneID: a.pane}
		}
		n.mu.Unlock()
		n.backfillResume(list)
		for _, a := range agents {
			waitWithin(t, 20*time.Second, a.name+" reported to the new server", func() bool {
				n.mu.Lock()
				defer n.mu.Unlock()
				return n.launches[a.pane].Server == c.ServerID()
			})
		}
		t.Logf("LIVE restart %d: every command settled on the new server", round)
	}
}

// liveClaude is a running claude as /proc shows it, with the flags a launcher sets.
type liveClaude struct {
	pid             int
	argv            []string
	settings, model []string // each --settings / --model value, in order
	configDir       string   // CLAUDE_CONFIG_DIR
}

func (r liveClaude) mcp(path string) bool { return mcpConfigsHold(claudeMCPConfigs(r.argv), path) }

// liveClaudeRunning is the one live claude whose command line names session id (zero when none or several do).
func liveClaudeRunning(id string) liveClaude {
	var out liveClaude
	for pid, argv := range readProcArgv("/proc", uint32(os.Getuid())) {
		if !isClaudeCmd(argv) || !slices.Contains(argv, id) {
			continue
		}
		if out.pid != 0 {
			return liveClaude{}
		}
		out = liveClaude{pid: pid, argv: argv}
	}
	for i := 0; i+1 < len(out.argv); i++ {
		switch out.argv[i] {
		case "--settings":
			out.settings = append(out.settings, out.argv[i+1])
		case "--model":
			out.model = append(out.model, out.argv[i+1])
		}
	}
	if out.pid != 0 {
		env, _ := os.ReadFile("/proc/" + strconv.Itoa(out.pid) + "/environ")
		out.configDir = parseProcEnv(string(env)).ConfigDir
	}
	return out
}
