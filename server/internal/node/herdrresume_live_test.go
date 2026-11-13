//go:build herdrlive

package node

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// Against a real herdr and real Claude Code, in a herdr session of its own (never default or the node's own):
//
//	HERDR_SESSION=persona-resume-test PERSONA_HERDR_RESUME_LIVE=persona-resume-test \
//	  PERSONA_RESUME_SETTINGS=<a gateway's settings file> PERSONA_RESUME_CONFIG_DIR=<a Claude profile directory> \
//	  PERSONA_RESUME_CWD=<a folder that profile trusts> ./node.test -test.run TestLiveHerdrResumesThroughTheLauncher -test.v
//
// Two Claudes start the way the node starts them, one with a launcher's --settings, one in a CLAUDE_CONFIG_DIR profile;
// each gets one message so it has a transcript, and holdResume gives herdr its resume command. The session's server is
// stopped and started twice (a client attaches each time: herdr resumes only then). After the first restart the node
// sees a new server (herdr.Client.ServerID) and reports the command again; after each restart ps must show both Claudes
// resumed with the launcher's flags and profile, never as herdr's bare `claude --resume <id>`. (A shell function named
// claude, such as a gateway manager's, may add flags of its own in front: only Persona's own are checked.)
func TestLiveHerdrResumesThroughTheLauncher(t *testing.T) {
	session := os.Getenv("PERSONA_HERDR_RESUME_LIVE")
	if session == "" || session == "default" || !strings.HasSuffix(session, "-test") || herdr.Session() != session {
		t.Skip("PERSONA_HERDR_RESUME_LIVE=<a session named *-test>, with HERDR_SESSION set to it, not set")
	}
	settings, configDir := os.Getenv("PERSONA_RESUME_SETTINGS"), os.Getenv("PERSONA_RESUME_CONFIG_DIR")
	if settings == "" || configDir == "" {
		t.Skip("PERSONA_RESUME_SETTINGS and PERSONA_RESUME_CONFIG_DIR not set")
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
	}
	agents := []*agent{
		{name: "settings", pane: ws.RootPane.ID, spec: claudeLaunch{Launcher: "claude", Settings: settings}, profile: configDir},
		{name: "config-dir", spec: claudeLaunch{Launcher: "claude"}, profile: configDir},
	}
	tab, err := c.TabCreate(ctx, ws.Workspace.ID, cwd, "config-dir")
	if err != nil {
		t.Fatal(err)
	}
	agents[1].pane = tab.RootPane.ID
	n.claudeStartDir.Store(&configDir) // claude_start_dir: the node's, for every Claude it starts with the launcher "claude"
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
		waitWithin(t, 90*time.Second, "both agents resumed on the new server", func() bool {
			for _, a := range agents {
				g, err := c.AgentGet(ctx, a.pane)
				if err != nil || g.Agent != "claude" {
					return false
				}
				out, _ := exec.Command("sh", "-c", "ps -eo args | grep -F -- '--resume "+a.id+"' | grep -v grep").Output()
				if !strings.Contains(string(out), "--mcp-config "+filepath.Join(n.dir, "mcp.json")) {
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
		for _, a := range agents {
			ps, _ := exec.Command("sh", "-c", "ps -eo pid,args | grep -F -- '--resume "+a.id+"' | grep -v grep").Output()
			t.Logf("LIVE after restart %d, %s (%s):\n%s", round, a.name, a.id, ps)
			if !strings.Contains(string(ps), " --resume "+a.id+" ") || !strings.Contains(string(ps), "--mcp-config "+filepath.Join(n.dir, "mcp.json")) {
				t.Errorf("round %d: %s was not resumed with Persona's command", round, a.name)
			}
			if a.name == "settings" && !strings.Contains(string(ps), "--settings "+settings) {
				t.Errorf("round %d: %s lost the launcher's --settings", round, a.name)
			}
			for _, l := range strings.Split(strings.TrimSpace(string(ps)), "\n") {
				f := strings.Fields(l)
				if len(f) == 0 {
					continue
				}
				env, _ := os.ReadFile("/proc/" + f[0] + "/environ")
				dir := ""
				for _, kv := range strings.Split(string(env), "\x00") {
					if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
						dir = v
					}
				}
				t.Logf("LIVE   pid %s CLAUDE_CONFIG_DIR=%q", f[0], dir)
				if dir != a.profile {
					t.Errorf("round %d: %s resumed outside its profile (%q)", round, a.name, dir)
				}
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
		t.Logf("LIVE restart %d: both commands reported again to the new server", round)
	}
}
