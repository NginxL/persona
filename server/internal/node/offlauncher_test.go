package node

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// scannedClaude makes the scan of running claudes (claudedirs.go) see exactly one, pid, running session id with e, and
// that pid alive; a later call replaces it (the old process gone).
func scannedClaude(t *testing.T, n *Node, home string, pid int, id string, e procEnv) {
	t.Helper()
	dir := mkdir(t, filepath.Join(home, ".claude", "sessions"))
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(`{"pid":`+strconv.Itoa(pid)+`,"sessionId":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	n.cdirs = newClaudeDirs()
	n.cdirs.scan = func() map[int]procEnv { return map[int]procEnv{pid: e} }
	was := processAlive
	processAlive = func(p int, _ string) bool { return p == pid }
	t.Cleanup(func() { processAlive = was })
}

// A herdr restart brought a Persona Agent back with herdr's bare `claude --resume <id>` (#11792): its row says it runs
// off the launcher it was started on, and moving it to that launcher is a switch, carried out with the user's next
// message, not taken as one it runs on already (which used to drop it), and with the model it was started with there.
// Started again by Persona's command, it is on its launcher.
func TestPersonaAgentResumedBareRunsOffItsLauncher(t *testing.T) {
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	st, _ := n.state.get(sidA)
	st.Launcher, st.Model = "work", "model-w" // started on the gateway launcher, with one of its models
	n.state.put(sidA, st)
	scannedClaude(t, n, home, 4242, sidA, procEnv{Cmd: true}) // herdr's bare `claude --resume <id>`
	if row := rowOf(t, n, sidA); row.Launcher != "work" || !row.OffLauncher {
		t.Fatalf("row: launcher %q, off_launcher %v", row.Launcher, row.OffLauncher)
	}
	if err := pick(t, n, map[string]any{"launcher": "work", "from": "work"}); err != nil {
		t.Fatalf("moving it back to its launcher: %v", err)
	}
	if ps := n.pendingSwitch(sidA); ps == nil || ps.Launcher != "work" {
		t.Fatalf("no switch back to its launcher waits: %+v", ps)
	}
	f.reset()
	quitsToShell(f, cwd)
	comesUpWhen(f, "w1:p1", func(string) bool { return true })
	if err := tell(t, n, "go on", true); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(f.trace(), "\n"))
	}
	if starts := typedStarts(f, "w1:p1"); len(starts) != 1 || !strings.Contains(starts[0], " launch work ") ||
		!strings.Contains(starts[0], "--resume "+sidA) || !strings.Contains(starts[0], "--mcp-config "+filepath.Join(n.dir, "mcp.json")) ||
		!strings.Contains(starts[0], "--model model-w") {
		t.Fatalf("typed %q", starts)
	}
	if st, _ := n.state.get(sidA); st.Launcher != "work" || st.PendingSwitch != nil {
		t.Errorf("record after the switch = %+v", st)
	}

	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	scannedClaude(t, n, home, 4343, sidA, procEnv{Cmd: true, MCPConfig: filepath.Join(n.dir, "mcp.json")})
	if row := rowOf(t, n, sidA); row.Launcher != "work" || row.OffLauncher {
		t.Fatalf("started by Persona's command: launcher %q, off_launcher %v", row.Launcher, row.OffLauncher)
	}
}

// Off its launcher, an Agent still has a way to cancel a switch to another launcher: picking its own one then cancels
// that switch, as it does for an Agent on it (agent_switch drops a pick whose message never got there that way); with
// no other switch waiting, the same pick is the switch back.
func TestOwnLauncherPickCancelsAnotherSwitchOffTheLauncher(t *testing.T) {
	n, _, home, _, _ := switchNode(t, proto.StatusIdle, nil)
	st, _ := n.state.get(sidA)
	st.Launcher = "work"
	n.state.put(sidA, st)
	scannedClaude(t, n, home, 4242, sidA, procEnv{Cmd: true})
	if err := pick(t, n, map[string]any{"launcher": "claude", "from": "work"}); err != nil {
		t.Fatalf("a switch to another launcher: %v", err)
	}
	if ps := n.pendingSwitch(sidA); ps == nil || ps.Launcher != "claude" {
		t.Fatalf("no switch to claude waits: %+v", ps)
	}
	if err := pick(t, n, map[string]any{"launcher": "work", "from": "work"}); err != nil {
		t.Fatalf("cancelling it: %v", err)
	}
	if ps := n.pendingSwitch(sidA); ps != nil {
		t.Fatalf("its own launcher picked while a switch to claude waited: %+v waits, want it cancelled", ps)
	}
	if err := pick(t, n, map[string]any{"launcher": "work", "from": "work"}); err != nil {
		t.Fatalf("the switch back: %v", err)
	}
	if ps := n.pendingSwitch(sidA); ps == nil || ps.Launcher != "work" {
		t.Fatalf("no switch back to its launcher waits: %+v", ps)
	}
}

// Only a claude seen running a session Persona started, without Persona's --mcp-config, is off its launcher: not a
// session Persona keeps no record of (the user's own), not a claude that has gone, not one whose command line the
// scan does not keep (Windows), not an assistant conversation (its own MCP config).
func TestOffLauncherNeedsALiveClaudeWithoutPersonasCommand(t *testing.T) {
	n, _, home, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	n.state.put(sidA, sessionState{Launcher: "work", Cwd: cwd})
	for _, tc := range []struct {
		name string
		id   string
		e    procEnv
		dead bool
		want bool
	}{
		{"bare resume", sidA, procEnv{Cmd: true}, false, true},
		{"another --mcp-config", sidA, procEnv{Cmd: true, MCPConfig: "/elsewhere/mcp.json"}, false, true},
		{"Persona's command", sidA, procEnv{Cmd: true, MCPConfig: filepath.Join(n.dir, "mcp.json")}, false, false},
		{"Persona's among others", sidA, procEnv{Cmd: true, MCPConfig: "/elsewhere/mcp.json\x00" + filepath.Join(n.dir, "mcp.json")}, false, false},
		{"command line not read", sidA, procEnv{}, false, false},
		{"gone", sidA, procEnv{Cmd: true}, true, false},
		{"not Persona's", sidB, procEnv{Cmd: true}, false, false},
	} {
		scannedClaude(t, n, home, 4242, tc.id, tc.e)
		if tc.dead {
			processAlive = func(int, string) bool { return false }
		}
		if got := n.offLauncher(tc.id); got != tc.want {
			t.Errorf("%s: off %v, want %v", tc.name, got, tc.want)
		}
	}
}

// What the scan keeps of a claude's command line: every --mcp-config value (Claude Code takes several), up to the
// prompt after "--".
func TestClaudeMCPConfigs(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--resume", sidA}, ""},
		{[]string{"claude", "--settings", "/s.json", "--mcp-config", "/h/.persona/mcp.json", "--allowedTools", "x"}, "/h/.persona/mcp.json"},
		{[]string{"claude", "--mcp-config=/a.json", "--mcp-config", "/b.json", "/c.json", "--model", "m"}, "/a.json\x00/b.json\x00/c.json"},
		{[]string{"claude", "--", "--mcp-config", "/p.json"}, ""},
	} {
		if got := claudeMCPConfigs(tc.argv); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.argv, got, tc.want)
		}
	}
	// ps -E prints the arguments joined by spaces, then the environment: a value with a space in it stays whole up to
	// the next flag.
	line := " 4242 claude --mcp-config /Users/a b/.persona/mcp.json --allowedTools x HOME=/Users/a b"
	if got := psMCPConfigs(line); got != "/Users/a b/.persona/mcp.json" {
		t.Errorf("ps line: %q", got)
	}
	if got := psMCPConfigs(" 4242 claude --resume " + sidA + " HOME=/h"); got != "" {
		t.Errorf("bare ps line: %q", got)
	}
}
