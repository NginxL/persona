package node

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// A connector's command line (personad tool, internal/toolcli) reaches the hub through the node's socket like the MCP
// server, but the node, not the caller, says whether the assistant is asking. Only an assistant's own process or a
// verified runtime worker, or one of their descendants (its Bash tool), gets the assistant's tools. Any other caller
// (an agent, a program run by hand) is refused; so is a system where the other end of a socket cannot be known.
//
// The MCP server's requests (mcp.tools, mcp.call) have their own process checks (mcpAssistantOf): its PERSONA_ASSISTANT is
// repeated by every process started with the assistant's environment, an agent in an engine started from the
// assistant's Bash tool among them. Such an agent still counts as the assistant while its engine stays a descendant of
// the assistant's process (a server the Bash tool did not detach), as any program run there does.

// ancestorsMost bounds the walk up the process tree.
const ancestorsMost = 64

// errNoPeer: this system does not tell the process at the other end of a socket (peer_other.go).
var errNoPeer = errors.New("the process at the other end of a socket is not known on this system")

// assistantCaller is the assistant conversation whose process is c's peer or an ancestor of it.
func (n *Node) assistantCaller(c net.Conn) (string, bool) {
	pid, err := peerPID(c)
	if err != nil {
		return "", false
	}
	return n.assistantOf(pid, parentPID)
}

// assistantOf is the assistant conversation whose process is pid or an ancestor of it (parent gives a process's).
func (n *Node) assistantOf(pid int, parent func(int) (int, error)) (string, bool) {
	return assistantIn(lineage(pid, parent), n.assistantRoots())
}

// assistantIn is the assistant conversation whose process is one of pids, the first one found.
func assistantIn(pids []int, roots map[int]string) (string, bool) {
	for _, p := range pids {
		if id, ok := roots[p]; ok {
			return id, true
		}
	}
	return "", false
}

func strictDescendant(pid, ancestor int, parent func(int) (int, error)) bool {
	if pid <= 1 || ancestor <= 1 || pid == ancestor {
		return false
	}
	return slices.Contains(lineage(pid, parent)[1:], ancestor)
}

// lineage is pid and its ancestors, nearest first, as far as parent tells: up to ancestorsMost, init not included.
func lineage(pid int, parent func(int) (int, error)) []int {
	var out []int
	for range ancestorsMost {
		out = append(out, pid)
		next, err := parent(pid)
		if err != nil || next <= 1 || next == pid {
			break
		}
		pid = next
	}
	return out
}

// nodeLineage is this node's process and its ancestors (a variable for tests, whose runtimes run in the node's
// process).
var nodeLineage = func() []int { return lineage(os.Getpid(), parentPID) }

func (n *Node) assistantRoots() map[int]string {
	roots := n.localAssistantRoots()
	for _, worker := range n.runtimes.assistantWorkers() {
		if _, exists := roots[worker.pid]; !exists && strictDescendant(worker.pid, worker.runtimePID, parentPID) &&
			n.runtimeAssistantChat(worker.host, worker.session) {
			roots[worker.pid] = worker.session
		}
	}
	return roots
}

// localAssistantRoots are the processes this node runs for the assistant, by pid, each with the session its calls go to
// the hub with: a conversation's, the warm spare's (its MCP server lists the tools as it starts, before a conversation
// adopts it), and a Codex process's key until its thread exists (the key its MCP server says, resolveAssistantKey).
func (n *Node) localAssistantRoots() map[int]string {
	roots := map[int]string{}
	add := func(id string, p *assistantProc) {
		if p == nil || p.cmd == nil || p.cmd.Process == nil {
			return
		}
		if id == "" && p.cx != nil {
			id = p.cx.key
		}
		if id != "" {
			roots[p.cmd.Process.Pid] = id
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, p := range n.procs {
		add(id, p)
	}
	for _, p := range n.akeys {
		add(p.id, p)
	}
	if n.spare != nil {
		add(n.spare.id, n.spare)
	}
	return roots
}

// mcpSession is the session and the assistant's word an MCP server's request (mcp.tools, mcp.call) goes to the hub
// with. The server's word that it is the assistant's is taken only when the node says so too (mcpAssistantOf, for the
// process at the other end of c); a request it refuses goes as an agent's, with the session it names. A request that
// does not say so is not made one. rt is the connected runtime whose assistant conversation it is (Antigravity's, whose
// MCP server is `personad mcp` too), nil for one this node runs itself.
func (n *Node) mcpSession(c net.Conn, req sockRequest) (string, bool, *runtimeConn) {
	if !req.Assistant {
		return req.SessionID, false, nil
	}
	pid, err := peerPID(c)
	if id, rt, ok := n.mcpAssistantOf(pid, err, parentPID, req.SessionID); ok {
		return id, true, rt
	}
	n.logf("%s: the caller (pid %d) says it is the assistant, but its process is not one Persona runs for the assistant: answered as an agent's", req.Kind, pid)
	return req.SessionID, false, nil
}

// mcpAssistantOf is the session of the assistant conversation whose MCP server is the process pid (err: why it is not
// known; parent gives a process's; session: the one the request names), or false:
//   - pid or an ancestor is a process this node runs for the assistant: that conversation's session, whatever the
//     request names;
//   - pid or an ancestor is a connected runtime that runs assistant conversations (caps.assistant): they run in the
//     runtime's processes, not the node's (assistant_runtime.go), and the runtime names their sessions, so the
//     request's, when it can be one of that runtime's assistant conversations (runtimeMayName);
//   - the system does not tell who is at the other end of a socket (errNoPeer): the request's session, if it is one
//     this node runs for the assistant, or an assistant conversation of a connected runtime that runs them.
//
// Any other caller is not the assistant. The runtime is the one that vouches (nil: a process of this node's).
func (n *Node) mcpAssistantOf(pid int, err error, parent func(int) (int, error), session string) (string, *runtimeConn, bool) {
	if errors.Is(err, errNoPeer) {
		if n.assistantSession(session) {
			return session, nil, true
		}
		if h, _, _ := n.runtimes.belongs(session); n.runtimeAssistantChat(h, session) {
			return session, h, true
		}
		return "", nil, false
	}
	if err != nil {
		return "", nil, false
	}
	line := lineage(pid, parent)
	if id, ok := assistantIn(line, n.localAssistantRoots()); ok {
		return id, nil, true
	}
	if h := n.runtimes.assistantIn(line); h != nil && n.runtimeMayName(h, session) {
		return session, h, true
	}
	return "", nil, false
}

// runtimeWorkerCaller: the process at the other end of c is session's own, when runtime rt reported the worker that
// runs it (runtime.assistant_pids, verified): one of the worker's descendants. Another process of rt's (an Agent's
// chat, another assistant conversation) does not get to name it. Without a report for session, or where the system
// does not tell who is at the other end (errNoPeer), the runtime's word stands, as in mcpAssistantOf.
func (n *Node) runtimeWorkerCaller(c net.Conn, rt *runtimeConn, session string) bool {
	for _, w := range n.runtimes.assistantWorkers() {
		if w.host != rt || w.session != session {
			continue
		}
		pid, err := peerPID(c)
		if errors.Is(err, errNoPeer) {
			return true
		}
		return err == nil && slices.Contains(lineage(pid, parentPID), w.pid)
	}
	return true
}

// assistantSession: session is the one a process this node runs for the assistant goes to the hub with.
func (n *Node) assistantSession(session string) bool {
	for _, id := range n.localAssistantRoots() {
		if id == session {
			return true
		}
	}
	return false
}

// runtimeMayName: session, named by a process of runtime h (one that runs assistant conversations), can be one of
// them: not one this node runs itself, and, once a runtime lists it, h's assistant conversation (runtimeAssistantChat).
// One no runtime lists yet passes if it is shaped like a runtime's: a runtime's chat lists the tools as it starts,
// before the runtime lists it (integrations/antigravity).
func (n *Node) runtimeMayName(h *runtimeConn, session string) bool {
	if n.assistantSession(session) {
		return false
	}
	if _, _, known := n.runtimes.belongs(session); known {
		return n.runtimeAssistantChat(h, session)
	}
	return runtimeSessionID(session)
}

// runtimeAssistantChat: session is an assistant conversation that runtime h, one that runs them (caps.assistant), has.
func (n *Node) runtimeAssistantChat(h *runtimeConn, session string) bool {
	return h != nil && h.assistant() && n.runtimes.owns(h, session) && n.runtimeAssistant(session)
}

type assistantPrepareState struct {
	connectors []string
	workGuide  bool
	clis       map[string]string // command name → connector id
}

// assistantPrepareDir gives prepare and running sessions the same key, including symlinked data directories.
func assistantPrepareDir(cwd string) string {
	if cwd == "" {
		return ""
	}
	return realPath(filepath.Clean(cwd))
}

// mayCall says whether this directory's command lines may call tool: one of a connector signed in as of its last
// assistant.prepare ("<id>_…", the connector's own tools and its <id>_cli).
func (n *Node) mayCall(cwd, tool string) bool {
	return slices.ContainsFunc(n.signedInConnectors(cwd), func(id string) bool { return strings.HasPrefix(tool, id+"_") })
}

// cliCall runs one of an Agent's own tools (callerTools) as the Agent its pane or Codex thread names, or a connector
// call as the assistant's. The Agent's tools go to the hub as an Agent's call (assistant false), as `personad mcp`
// sends them for the same pane (hooks.go mcpCaller): the hub decides each one for that session (a task leader's
// task_plan, the decisions of the Agent's own to-do, …). A call that names no known Agent session gets the assistant's
// check below, unchanged; no Agent-side exception grants connector access.
func (n *Node) cliCall(ctx context.Context, c net.Conn, req sockRequest) proto.ToolResult {
	if slices.Contains(callerTools, req.Name) {
		session, assistant := n.cliAgentCaller(req), false
		if assistantwire.IsA2ATool(req.Name) {
			session, assistant = n.a2aCaller(c, req, session)
			if session == "" {
				return proto.ErrorResult(proto.ToolErrorUnavailable, "A2A requires a verified local session", 0)
			}
		}
		if session != "" {
			if n.peer == nil {
				return proto.ErrorResult(proto.ToolErrorUnavailable, "Persona hub error: not connected", 0)
			}
			var res proto.ToolResult
			if err := n.peer.Call(ctx, "mcp.call", map[string]any{"name": req.Name, "args": req.Args, "session_id": session,
				"assistant": assistant, "guest": n.guestOf(session) != nil}, &res); err != nil {
				return proto.ErrorResult(proto.ToolErrorUnavailable, "Persona hub error: "+err.Error(), 0)
			}
			return res
		}
		if req.Name == "task_spawn" {
			return proto.ErrorResult(proto.ToolErrorUnavailable, "task_spawn is not available: no Agent session is known for this pane or Codex thread", 0)
		}
	}
	session, ok := n.assistantCaller(c)
	if !ok {
		return proto.ErrorResult(proto.ToolErrorUnavailable, "这个命令只能在助手的对话里用：这个进程不是 Persona 为助手起的", 0)
	}
	n.mu.Lock()
	cwd := ""
	p := n.procs[session]
	if p != nil {
		cwd = p.cwd
	}
	n.mu.Unlock()
	if p == nil {
		if st, ok := n.state.get(session); ok && st.Cwd != "" {
			cwd = st.Cwd
		} else if s, _, ok := n.runtimes.rowOf(session); ok && s.Cwd != "" {
			cwd = s.Cwd
		} else if n.runtimeAssistant(session) {
			cwd = identity.AssistantDir(n.home)
		}
	}
	if !n.mayCall(cwd, req.Name) {
		return proto.ErrorResult(proto.ToolErrorUnavailable, req.Name+" is not available: the command line calls only the tools of the hub's connectors that are signed in", 0)
	}
	if req.Name == toolMailSaveAttachment { // writes on this machine (mailattachment.go)
		return n.saveMailAttachment(ctx, req.Args, session, true, false)
	}
	if n.peer == nil {
		return proto.ErrorResult(proto.ToolErrorUnavailable, "Persona hub error: not connected", 0)
	}
	var res proto.ToolResult
	params := map[string]any{"name": req.Name, "args": req.Args, "session_id": session, "assistant": true}
	n.putSourcePermission(c, params, session, req.Name, req.Args)
	if origin := n.assistantOrigin(session); origin != "" {
		params["origin"] = origin
	}
	if err := n.peer.Call(ctx, "mcp.call", params, &res); err != nil {
		return proto.ErrorResult(proto.ToolErrorUnavailable, "Persona hub error: "+err.Error(), 0)
	}
	return res
}

// a2aCaller binds a shell or MCP process to a local execution session. The
// requested session alone is never sufficient to read another agent's mailbox.
func (n *Node) a2aCaller(c net.Conn, req sockRequest, claimed string) (string, bool) {
	if id, ok := n.assistantCaller(c); ok {
		n.mu.Lock()
		p := n.procs[id]
		round := p != nil && p.toolSet == roundToolSet
		n.mu.Unlock()
		if round {
			return "", false
		}
		return id, true
	}
	pid, err := peerPID(c)
	if err != nil {
		return "", false
	}
	if req.CodexThreadID != "" {
		if paneEnvOfPID(pid)["CODEX_THREAD_ID"] != req.CodexThreadID {
			return "", false
		}
		return n.taskCodexCaller(req.CodexThreadID), false
	}
	for _, ancestor := range lineage(pid, parentPID) {
		if req.Pane != "" && slices.Contains(n.panesOfPID(ancestor), req.Pane) {
			id := n.paneCaller(req.Pane)
			if id != "" && (claimed == "" || claimed == id) {
				return id, false
			}
		}
		if id := readClaudeSessionID(n.claudeSessionFile(ancestor)); id != "" && (claimed == "" || claimed == id) {
			return id, false
		}
	}
	return "", false
}

// cliAgentCaller is the Agent session a command line's call of an Agent's own tool comes from: the one its Codex
// thread names when it names one, else the one in its pane (paneCaller); "" when none is known. The request's own
// session and assistant fields are never read: the node, not the caller, says whose call it is.
func (n *Node) cliAgentCaller(req sockRequest) string {
	if req.CodexThreadID != "" {
		return n.taskCodexCaller(req.CodexThreadID)
	}
	return n.paneCaller(req.Pane)
}

// Codex's shared daemon can inherit a different pane's environment. A tool's per-turn thread id takes precedence;
// resolve it to exactly one public session, including non-default Codex homes, and never fall back to a stale pane.
func (n *Node) taskCodexCaller(thread string) string {
	if n.codices == nil || !validID(thread) {
		return ""
	}
	n.codices.mu.Lock()
	defer n.codices.mu.Unlock()
	found := ""
	for id, src := range n.codices.threads {
		if src.thread == thread {
			if found != "" {
				return "" // identical raw thread ids in different Codex homes are ambiguous
			}
			found = id
		}
	}
	return found
}

// signedInConnectors are this directory's signed-in connectors as of its last assistant.prepare: whose built-in
// skills the assistant gets (assistantskills.Requires). An unprepared directory gets none.
func (n *Node) signedInConnectors(cwd string) []string {
	return n.skillSelection(cwd).Connectors
}

func (n *Node) skillSelection(cwd string) assistantskills.Selection {
	key := assistantPrepareDir(cwd)
	n.mu.Lock()
	state, prepared := n.assistantPrepared[key]
	n.mu.Unlock()
	if !prepared {
		// A resumed process may precede the first prepare after a node restart.
		// Only Persona's own installation stamp can carry the guide across it.
		state.workGuide = assistantskills.WorkGuideInstalled(filepath.Join(cwd, ".claude", "skills"))
	}
	return assistantskills.Selection{Connectors: slices.Clone(state.connectors), WorkGuide: state.workGuide}
}

// syncRuntimeSkillDir refreshes one runtime's native skills when an Assistant starts.
func (n *Node) syncRuntimeSkillDir(cwd, dir string) {
	n.assistantSkillsMu.Lock()
	defer n.assistantSkillsMu.Unlock()
	if err := assistantskills.Sync(dir, assistantskills.UserDir(), n.skillSelection(cwd), n.logger.Printf); err != nil {
		n.logger.Printf("assistant: skills: %v", err)
	}
}

// syncAssistantSkills provisions native skills and connector commands during prepare.
// Before switching to the compact instructions, both runtimes must have the guide.
func (n *Node) syncAssistantSkills(cwd string) error {
	selection := n.skillSelection(cwd)
	dirs := []string{filepath.Join(cwd, ".claude", "skills")}
	agentDir := filepath.Join(cwd, ".agents", "skills")
	if st, err := os.Stat(agentDir); selection.WorkGuide || (err == nil && st.IsDir()) {
		dirs = append(dirs, agentDir)
	}
	var syncErr error
	for _, d := range dirs {
		if err := assistantskills.Sync(d, assistantskills.UserDir(), selection, n.logger.Printf); err != nil {
			syncErr = errors.Join(syncErr, err)
		}
	}
	n.writeCLIs(cwd, n.connectorCLIs(cwd))
	return syncErr
}

// connectorCLIs are this directory's signed-in connectors' command lines as of its last assistant.prepare.
func (n *Node) connectorCLIs(cwd string) map[string]string {
	key := assistantPrepareDir(cwd)
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.assistantPrepared[key].clis)
}

// assistantBin keeps the default assistant's existing PATH; every other directory gets its own wrappers so preparing
// one manager cannot remove another's commands or change which connector a shared command name calls.
func (n *Node) assistantBin(cwd string) string {
	key := assistantPrepareDir(cwd)
	if key == assistantPrepareDir(n.assistantDirOf(".persona/assistant")) {
		return filepath.Join(n.dir, "assistant-bin")
	}
	return filepath.Join(n.dir, "assistant-bins", fmt.Sprintf("%x", sha256.Sum256([]byte(key))))
}

// cliMark is in every command line the node writes on the assistant's PATH: one it wrote and no longer has a
// connector for goes, other files there (a plugin's gate commands) stay.
const cliMark = "# a connector's command line, on the assistant's PATH while it is signed in (node/clicall.go)"

// writeCLIs puts the signed-in connectors' command lines on the assistant's PATH (personad tool --cli <id>) and takes
// away those whose connector is not, and the built-in Telegram's tg (personad tg) it wrote before Telegram became an
// extension (#1704).
func (n *Node) writeCLIs(cwd string, clis map[string]string) {
	bin := n.assistantBin(cwd)
	want := map[string]string{}
	for name, id := range clis {
		want[name] = "#!/bin/sh\n" + cliMark + "\nexec " + shellQuote(n.bin) + " tool --cli " + shellQuote(id) + " \"$@\"\n"
	}
	entries, _ := os.ReadDir(bin)
	for _, e := range entries {
		if _, keep := want[e.Name()]; keep {
			continue
		}
		p := filepath.Join(bin, e.Name())
		if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), cliMark) || strings.Contains(string(b), "Telegram through the hub")) {
			_ = os.Remove(p)
		}
	}
	if len(want) == 0 {
		return
	}
	_ = os.MkdirAll(bin, 0o755)
	for name, wrapper := range want {
		p := filepath.Join(bin, name)
		if cur, err := os.ReadFile(p); err == nil && string(cur) == wrapper {
			continue
		}
		_ = os.WriteFile(p, []byte(wrapper), 0o755)
	}
}
