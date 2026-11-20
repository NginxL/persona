package node

import (
	"path/filepath"
	"strings"
)

// Persona starts every Claude Code Agent with its own command (claudeCommandLine): the launcher, with its --settings
// and --model, and always --mcp-config <node dir>/mcp.json. A restart can bring the conversation back without it:
// herdr older than 0.9.2 resumes it with its bare `claude --resume <id>` (#11792), and anyone can type a resume by
// hand. The record still names the launcher it was started on, so the Agent was listed there, and moving it back to
// that launcher was taken as a move to the launcher it runs on already: the assistant refused it, the node dropped it.
//
// So the record is checked against the process. The scan of running claudes (claudedirs.go) keeps each one's
// --mcp-config values; a Persona-started Claude whose claude was started without Persona's is off its launcher
// (Session.OffLauncher). A launcher switch to the launcher it was started on is then a switch like any other
// (launcherModes, applySwitch): carried out with the next message, it quits the agent and resumes it there with
// Persona's command, after which it is on its launcher again.

// claudeRun is a live claude as the scan saw it: its process and the --mcp-config values it was started with.
type claudeRun struct {
	PID       int
	Start     string // procStartMark: a pid the system gave to another process since is not this one
	MCPConfig string // NUL-separated, "" for none (procEnv.MCPConfig)
}

// offLauncher: session id was started by Persona on a launcher (its record names one), and the live claude running it,
// as the scan of running claudes last saw it, was not started with Persona's --mcp-config. False when the scan saw no
// live claude of it (stopped, gone since, two claudes on it, a system whose command lines the scan does not read), and
// for an assistant conversation, which runs with an MCP config of its own.
func (n *Node) offLauncher(id string) bool {
	if st, ok := n.state.get(id); !ok || st.Launcher == "" {
		return false
	}
	r, ok := n.sessionRun(id)
	if !ok || !processAlive(r.PID, r.Start) || n.isAssistantSession(id) {
		return false
	}
	return !mcpConfigsHold(r.MCPConfig, n.agentMCPConfig())
}

// agentMCPConfig is the --mcp-config every Claude Code Agent Persona starts carries (claudeCommandLine): what tells
// its claude from one started otherwise.
func (n *Node) agentMCPConfig() string { return filepath.Join(n.dir, "mcp.json") }

// mcpConfigsHold: path is one of configs, the NUL-separated --mcp-config values of a claude (claudeMCPConfigs).
func mcpConfigsHold(configs, path string) bool {
	want := filepath.Clean(path)
	for _, c := range strings.Split(configs, "\x00") {
		if c != "" && filepath.Clean(c) == want {
			return true
		}
	}
	return false
}

// claudeMCPConfigs are the --mcp-config values of a claude's command line, NUL-separated: Claude Code takes several
// after the flag (up to the next option) and the flag more than once; nothing after "--", the prompt.
func claudeMCPConfigs(argv []string) string {
	var out []string
	in := false
	for _, a := range argv {
		switch {
		case a == "--":
			return strings.Join(out, "\x00")
		case a == "--mcp-config":
			in = true
		case strings.HasPrefix(a, "--mcp-config="):
			out, in = append(out, strings.TrimPrefix(a, "--mcp-config=")), false
		case strings.HasPrefix(a, "-"):
			in = false
		case in:
			out = append(out, a)
		}
	}
	return strings.Join(out, "\x00")
}

// psMCPConfigs is claudeMCPConfigs for a line of `ps -E -o pid=,command=`, whose arguments are joined by spaces and
// followed by the environment: the value after " --mcp-config " runs up to the next option, so a path with a space in
// it stays whole (Persona's command always has another option after it).
func psMCPConfigs(line string) string {
	var out []string
	for rest := line; ; {
		i := strings.Index(rest, " --mcp-config ")
		if i < 0 {
			return strings.Join(out, "\x00")
		}
		rest = rest[i+len(" --mcp-config "):]
		end := strings.Index(rest, " -")
		if end < 0 {
			end = len(rest)
		}
		if v := strings.TrimSpace(rest[:end]); v != "" {
			out = append(out, v)
		}
		rest = rest[end:]
	}
}
