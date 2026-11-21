package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A caller is the assistant's when its process, or one of its ancestors, is an assistant process the node started.
func TestAssistantOf(t *testing.T) {
	n, _ := testNode(t)
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: 100}}}
	n.procs["s2"] = &assistantProc{id: "s2"} // not started yet
	parents := map[int]int{300: 200, 200: 100, 100: 1, 400: 1, 500: 500}
	parent := func(pid int) (int, error) {
		if p, ok := parents[pid]; ok {
			return p, nil
		}
		return 0, os.ErrNotExist
	}
	for _, c := range []struct {
		pid  int
		want string
	}{{300, "s1"}, {100, "s1"}, {400, ""}, {500, ""}, {999, ""}} {
		if got, ok := n.assistantOf(c.pid, parent); got != c.want || ok != (c.want != "") {
			t.Errorf("pid %d: %q %v, want %q", c.pid, got, ok, c.want)
		}
	}
}

func TestAssistantCLICallerUsesOnlyVerifiedRuntimeWorkers(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no local peer process lookup")
	}
	for _, engine := range []string{"agy", "opencode"} {
		t.Run(engine, func(t *testing.T) { testAssistantCLICallerVerifiedRuntimeWorkers(t, engine) })
	}
}

func testAssistantCLICallerVerifiedRuntimeWorkers(t *testing.T, engine string) {
	n, hub := runtimeNode(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := ln.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"runtime", "unrelated"} {
		cmd := assistantPeerCommand(ln.Addr().String(), role)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cmd.Wait(); err != nil {
				t.Error(err)
			}
		})
	}
	peers := map[string]net.Conn{}
	pids := map[string]int{}
	for range 5 {
		conn, err := ln.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		pid, err := peerPID(conn)
		if err != nil || pid <= 1 {
			t.Fatalf("peer PID for %q: %d, %v", line, pid, err)
		}
		role := strings.TrimSpace(line)
		peers[role], pids[role] = conn, pid
	}
	assistantID, agentID := engine+"-assistant", engine+"-agent"
	n.state.put(assistantID, sessionState{Cwd: identity.AssistantDir(n.home)})
	host := n.runtimes.add(nil)
	host.ready, host.hello.Runtime, host.hello.Caps.Assistant, host.pid = true, engine, true, pids["runtime"]
	n.runtimes.claim(host, assistantID)
	n.runtimes.claim(host, agentID)
	report := func(pids map[string]int) {
		n.runtimes.setAssistantPIDs(host, pids, func(id string) bool { return n.runtimeAssistantChat(host, id) })
	}
	check := func(role, want string) {
		t.Helper()
		if got, ok := n.assistantCaller(peers[role]); got != want || ok != (want != "") {
			t.Errorf("%s: assistantCaller = (%q, %v), want %q", role, got, ok, want)
		}
	}
	for _, pid := range []int{pids["runtime"], os.Getpid(), pids["unrelated"], 1, 0, -1} {
		report(map[string]int{assistantID: pid})
		if roots := n.assistantRoots(); len(roots) != 0 {
			t.Fatalf("accepted non-worker PID %d: %v", pid, roots)
		}
	}
	for _, otherID := range []string{agentID, engine + "-assistant-two"} {
		if otherID != agentID {
			n.state.put(otherID, sessionState{Cwd: identity.AssistantDir(n.home)})
			n.runtimes.claim(host, otherID)
		}
		report(map[string]int{assistantID: pids["worker"], otherID: pids["worker"]})
		check("assistant", "")
	}
	report(map[string]int{assistantID: pids["worker"], agentID: pids["agent"]})
	check("assistant", assistantID)
	check("agent", "")
	check("runtime", "")
	check("unrelated", "")
	t.Run("cliCall", func(t *testing.T) {
		t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
		t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
		n.bin = "/opt/example/personad"
		stateCwd := prepareAssistantForTest(t, n, ".persona/assistant", []string{"example"}, nil)
		rowCwd := prepareAssistantForTest(t, n, ".persona/health", []string{"other"}, nil)
		n.runtimes.setSessions(host, []proto.Session{{SessionID: assistantID, Cwd: rowCwd}, {SessionID: agentID}})
		n.state.put(assistantID, sessionState{Cwd: stateCwd})
		calls := assistantHub(hub)
		call := func(role, name string) proto.ToolResult {
			return n.cliCall(t.Context(), peers[role], sockRequest{Name: name, SessionID: "forged", Assistant: true})
		}
		if res := call("assistant", "example_list"); res.IsError {
			t.Fatalf("prepared runtime assistant denied: %+v", res)
		}
		if res := call("assistant", "other_list"); !res.IsError {
			t.Fatalf("runtime row overrode prepared state: %+v", res)
		}
		n.state.put(assistantID, sessionState{})
		if res := call("assistant", "other_list"); res.IsError {
			t.Fatalf("prepared runtime row denied: %+v", res)
		}
		for _, role := range []string{"agent", "runtime", "unrelated"} {
			if res := call(role, "other_list"); !res.IsError || !strings.Contains(res.Content[0].Text, "只能在助手的对话里用") {
				t.Errorf("%s was not denied as an unauthorized caller: %+v", role, res)
			}
		}
		n.runtimes.setSessions(host, []proto.Session{{SessionID: assistantID}, {SessionID: agentID}})
		calls.mu.Lock()
		defer calls.mu.Unlock()
		if len(calls.calls) != 2 {
			t.Fatalf("hub received %d calls, want 2", len(calls.calls))
		}
		for index, name := range []string{"example_list", "other_list"} {
			var params map[string]any
			if err := json.Unmarshal(calls.calls[index], &params); err != nil {
				t.Fatal(err)
			}
			if params["session_id"] != assistantID || params["assistant"] != true || params["name"] != name {
				t.Errorf("unexpected hub request: %s", calls.calls[index])
			}
		}
	})
	n.state.put(assistantID, sessionState{Cwd: t.TempDir()})
	check("assistant", "")
	n.state.put(assistantID, sessionState{Cwd: identity.AssistantDir(n.home)})
	report(map[string]int{assistantID: 0})
	check("assistant", "")
	report(map[string]int{assistantID: pids["worker"]})
	check("assistant", assistantID)
	n.runtimes.remove(host)
	report(map[string]int{assistantID: pids["worker"]})
	check("assistant", "")
}

func assistantPeerCommand(path, role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistantWorkerPeerProcess$", "--", path, role)
	cmd.Stderr = os.Stderr
	return cmd
}

func TestAssistantWorkerPeerProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	path, role := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	var children []string
	switch role {
	case "runtime":
		children = []string{"worker", "agent"}
	case "worker":
		children = []string{"assistant"}
	}
	for _, child := range children {
		cmd := assistantPeerCommand(path, child)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cmd.Wait(); err != nil {
				t.Error(err)
			}
		})
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, role+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatal(err)
	}
}

// The other end of the node's socket is known on the systems the node runs on, and so are its parents.
func TestPeerProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here: every caller is refused")
	}
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", ln.Addr().String())
		if err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if pid, err := peerPID(c); err != nil || pid != os.Getpid() {
		t.Fatalf("peer %d %v, want %d", pid, err, os.Getpid())
	}
	if pid, err := parentPID(os.Getpid()); err != nil || pid != os.Getppid() {
		t.Fatalf("parent %d %v, want %d", pid, err, os.Getppid())
	}
}

// The command lines' calls reach the hub only from the assistant's processes, as the assistant's, and only for the
// connectors signed in.
func TestCLICallOnlyForTheAssistant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := testNode(t)
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var asked []map[string]any
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		return proto.TextResult(`{"chats":[]}`, false), nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })

	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	ask := func(name string) proto.ToolResult {
		t.Helper()
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{}, "assistant": true})
		_, _ = c.Write(append(b, '\n'))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var res proto.ToolResult
		_ = json.Unmarshal(line, &res)
		return res
	}

	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	first := prepareAssistantForTest(t, n, ".persona/assistant", []string{"example"}, nil)
	second := prepareAssistantForTest(t, n, ".persona/managers/second/assistant", []string{"other"}, nil)
	// This test is no assistant process, whatever it says.
	if res := ask("example_list"); !res.IsError || !strings.Contains(res.Content[0].Text, "只能在助手的对话里用") || len(asked) != 0 {
		t.Fatalf("not the assistant: %+v, the hub was asked %v", res, asked)
	}
	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cwd: first, cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	if res := ask("phone_contacts"); !res.IsError || len(asked) != 0 {
		t.Fatalf("not a signed-in connector's: %+v", res)
	}
	if res := ask("example_list"); res.IsError {
		t.Fatalf("the assistant: %+v", res)
	}
	if res := ask("other_list"); !res.IsError {
		t.Fatalf("first assistant called the second one's connector: %+v", res)
	}
	// An Agent's own tools are an Agent's, by its pane or Codex thread: the assistant's process, naming neither, does
	// not call them as the assistant (its task_plan is the whole planner), and the hub is not asked (#12096).
	for _, name := range []string{"task_plan", "todo_decisions", "task_review"} {
		if res := ask(name); !res.IsError || !strings.Contains(res.Content[0].Text, "is not available") {
			t.Fatalf("the assistant's %s from its shell: %+v", name, res)
		}
	}
	n.mu.Lock()
	delete(n.procs, "s1")
	n.procs["s2"] = &assistantProc{id: "s2", cwd: second, cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	if res := ask("example_list"); !res.IsError {
		t.Fatalf("second assistant called the first one's connector: %+v", res)
	}
	if res := ask("other_list"); res.IsError {
		t.Fatalf("second assistant: %+v", res)
	}
	for _, cwd := range []string{"", filepath.Join(n.dir, "unprepared")} {
		n.mu.Lock()
		n.procs["s2"].cwd = cwd
		n.mu.Unlock()
		if res := ask("other_list"); !res.IsError {
			t.Fatalf("unprepared directory called a connector: %+v", res)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0]["name"] != "example_list" || asked[0]["assistant"] != true || asked[0]["session_id"] != "s1" ||
		asked[1]["name"] != "other_list" || asked[1]["assistant"] != true || asked[1]["session_id"] != "s2" {
		t.Fatalf("the hub was asked %v", asked)
	}
}

func TestCLICallTaskSpawnUsesLivePaneSession(t *testing.T) {
	n, hub := runtimeNode(t)
	n.mu.Lock()
	n.panes = map[string]string{"w1:p1": "sess-stale"}
	n.agents = map[string]host.Agent{"sess-live": {PaneID: "w1:p1"}}
	n.mu.Unlock()
	taskCall := mcpRelay(t, n, hub)

	// An unknown pane is rejected before the peer/hub call, regardless of forged request identity fields.
	peer := n.peer
	n.peer = nil
	missing := n.cliCall(context.Background(), nil, sockRequest{Name: "task_spawn", Pane: "w9:p9", SessionID: "forged", Assistant: true})
	n.peer = peer
	if !missing.IsError || !strings.Contains(missing.Content[0].Text, "no Agent session is known") {
		t.Fatalf("unknown pane: %+v", missing)
	}
	empty := n.cliCall(context.Background(), nil, sockRequest{Name: "task_spawn", SessionID: "forged", Assistant: true})
	if !empty.IsError || !strings.Contains(empty.Content[0].Text, "no Agent session is known") {
		t.Fatalf("empty pane: %+v", empty)
	}

	got := taskCall(map[string]any{"kind": "cli.call", "name": "task_spawn", "args": map[string]any{"prompt": "work", "title": "task"},
		"pane": "w1:p1", "session_id": "forged-session", "assistant": true})
	if got["name"] != "task_spawn" || got["session_id"] != "sess-live" || got["assistant"] != false || got["guest"] != false {
		t.Fatalf("hub request %v: want live pane session, assistant false, guest false", got)
	}
	denied := n.cliCall(context.Background(), nil, sockRequest{Name: "example_list", Pane: "w1:p1"})
	if !denied.IsError || !strings.Contains(denied.Content[0].Text, "只能在助手的对话里用") {
		t.Fatalf("ordinary connector tool from Agent: %+v", denied)
	}
}

// An Agent's own tools (callerTools) from the terminal go to the hub as the Agent its pane runs, as an Agent's call,
// exactly as its MCP server's call from the same pane goes (#12096): a continuation's replacement Agent without the
// Persona MCP reads its to-do's decisions and, leading its task, plans workers; whether it may is the hub's decision
// for that session (a worker's task_plan is refused there). What the request says of itself is never read.
func TestCLICallAgentToolsGoAsThePaneAgent(t *testing.T) {
	n, hub := runtimeNode(t)
	n.mu.Lock()
	n.panes = map[string]string{"w1:p1": "sess-stale", "w2:p2": "sess-bound"}
	n.agents = map[string]host.Agent{"sess-live": {PaneID: "w1:p1"}}
	n.mu.Unlock()
	ask := mcpRelay(t, n, hub)
	for _, name := range callerTools {
		got := ask(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{"tasks": []any{}}, "pane": "w1:p1",
			"session_id": "forged", "assistant": true})
		if got["name"] != name || got["session_id"] != "sess-live" || got["assistant"] != false || got["guest"] != false {
			t.Fatalf("%s: hub request %v: want the pane's Agent (sess-live), assistant false, guest false", name, got)
		}
		if args, _ := got["args"].(map[string]any); args == nil || args["tasks"] == nil {
			t.Fatalf("%s: the arguments did not reach the hub: %v", name, got)
		}
	}
	// A guest conversation's session is said to be one, so the hub gives it only its own tools.
	n.state.put("sess-bound", sessionState{Launcher: "claude", Guest: []string{"lark_send"}})
	if got := ask(map[string]any{"kind": "cli.call", "name": "todo_decisions", "args": map[string]any{}, "pane": "w2:p2"}); got["guest"] != true {
		t.Fatalf("a guest's call: %v", got)
	}
	// The same caller as its MCP server's call from that pane: the live Agent there, else the hooks' binding.
	for pane, want := range map[string]string{"w1:p1": "sess-live", "w2:p2": "sess-bound"} {
		for _, name := range []string{"task_plan", "todo_decisions"} {
			viaMCP := ask(map[string]any{"kind": "mcp.call", "name": name, "args": map[string]any{}, "pane": pane})
			viaCLI := ask(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{}, "pane": pane})
			if viaMCP["session_id"] != want || viaCLI["session_id"] != want || viaCLI["assistant"] != viaMCP["assistant"] {
				t.Fatalf("%s in %s: terminal %v, MCP %v: want both as %s", name, pane, viaCLI, viaMCP, want)
			}
		}
	}
}

// A call that names no known Agent session, by pane or Codex thread, is refused as before, before the hub is asked;
// and only an Agent's own tools go as the Agent's: anything else from an Agent's pane still needs the assistant's
// process. n.peer is nil while they are asked, so a refusal that reached for the hub would say "not connected".
func TestCLICallAgentToolsRefuseUnknownCallers(t *testing.T) {
	n, _ := runtimeNode(t)
	thread := "10000000-0000-0000-0000-000000000001"
	n.codices = newCodexRegistry()
	n.codices.threads["c0de-10000000000000000000000000000001"] = codexSource{thread: thread, home: "/profiles/codex"}
	n.mu.Lock()
	n.agents = map[string]host.Agent{"sess-live": {PaneID: "w1:p1"}}
	n.mu.Unlock()
	peer := n.peer
	n.peer = nil
	t.Cleanup(func() { n.peer = peer })
	const notAssistant = "这个命令只能在助手的对话里用：这个进程不是 Persona 为助手起的" // i18n-ignore: the node's refusal, matched as it is
	refused := func(what string, req sockRequest, want string) {
		t.Helper()
		res := n.cliCall(context.Background(), nil, req)
		if !res.IsError || res.ErrorKind != proto.ToolErrorUnavailable || len(res.Content) == 0 || res.Content[0].Text != want {
			t.Fatalf("%s: %+v, want the refusal %q", what, res, want)
		}
	}
	for _, name := range []string{"task_plan", "todo_decisions", "task_review", "report_to_supervisor", "dispatch_start"} {
		refused(name+" from an unknown pane", sockRequest{Name: name, Pane: "w9:p9", SessionID: "forged", Assistant: true}, notAssistant)
		refused(name+" from no pane", sockRequest{Name: name, SessionID: "forged", Assistant: true}, notAssistant)
		// An unknown thread never falls back to the pane a shared Codex daemon may have inherited.
		refused(name+" from an unknown Codex thread", sockRequest{Name: name, Pane: "w1:p1", CodexThreadID: "20000000-0000-0000-0000-000000000002"}, notAssistant)
	}
	refused("task_spawn from an unknown pane", sockRequest{Name: "task_spawn", Pane: "w9:p9"},
		"task_spawn is not available: no Agent session is known for this pane or Codex thread")
	for _, name := range []string{"task_status", "agent_send", "agent_start", "kb_search", "example_list", "todo_decisions_cli"} {
		refused(name+" from an Agent's pane", sockRequest{Name: name, Pane: "w1:p1"}, notAssistant)
		refused(name+" from a known Codex thread", sockRequest{Name: name, CodexThreadID: thread}, notAssistant)
	}
}

// A Codex thread names the caller of every Agent tool, as it does task_spawn's: not the pane its shared daemon may
// have inherited from another Agent.
func TestCLICallAgentToolsFollowTheCodexThread(t *testing.T) {
	n, hub := runtimeNode(t)
	thread := "10000000-0000-0000-0000-000000000001"
	public := "c0de-10000000000000000000000000000001"
	n.codices = newCodexRegistry()
	n.codices.threads[public] = codexSource{thread: thread, home: "/profiles/codex"}
	n.panes = map[string]string{"w1:p1": "another-agent"}
	ask := mcpRelay(t, n, hub)
	for _, name := range []string{"todo_decisions", "task_plan", "task_review"} {
		got := ask(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{}, "pane": "w1:p1", "codex_thread_id": thread,
			"session_id": "forged", "assistant": true})
		if got["session_id"] != public || got["assistant"] != false {
			t.Fatalf("%s: %v: want the Codex thread's session %s, assistant false", name, got, public)
		}
	}
}

func TestTaskSpawnCodexCallerDoesNotUseAStalePane(t *testing.T) {
	n, hub := runtimeNode(t)
	thread := "10000000-0000-0000-0000-000000000001"
	public := "c0de-10000000000000000000000000000001"
	n.codices = newCodexRegistry()
	n.codices.threads[public] = codexSource{thread: thread, home: "/profiles/codex"}
	n.panes = map[string]string{"w1:p1": "another-agent"}
	ask := mcpRelay(t, n, hub)
	call := map[string]any{"kind": "cli.call", "name": "task_spawn", "args": map[string]any{"title": "Check", "prompt": "Check"},
		"pane": "w1:p1", "codex_thread_id": thread, "session_id": "forged", "assistant": true}
	got := ask(call)
	if got["session_id"] != public || got["assistant"] != false {
		t.Fatalf("Codex's thread did not override the daemon's stale pane: %+v", got)
	}
	// A known thread without a pane is the shared app-server case.
	delete(call, "pane")
	if got := ask(call); got["session_id"] != public {
		t.Fatalf("pane-less Codex caller: %+v", got)
	}
	for _, raw := range []string{"unknown", "20000000-0000-0000-0000-000000000002"} {
		res := n.cliCall(context.Background(), nil, sockRequest{Name: "task_spawn", Pane: "w1:p1", CodexThreadID: raw})
		if !res.IsError {
			t.Fatalf("unknown Codex thread fell back to another pane: %+v", res)
		}
	}
	n.codices.threads["another-home"] = codexSource{thread: thread, home: "/other/codex"}
	if got := n.taskCodexCaller(thread); got != "" {
		t.Fatalf("ambiguous raw thread chose %q", got)
	}
}

// The tg an older node wrote for the built-in Telegram (personad tg) goes at the next message, Telegram signed in or
// not: Telegram's command line is its extension's now (#1704), which the hub names in clis like any connector's.
func TestPrepareTakesAwayTheBuiltInTg(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	prepare := func(conns []string, clis map[string]string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"dir": ".persona/assistant", "claude_md": "# 示例", "connectors": conns, "clis": clis})
		if _, err := n.handle(context.Background(), "assistant.prepare", params); err != nil {
			t.Fatal(err)
		}
	}
	tg := filepath.Join(n.dir, "assistant-bin", "tg")
	_ = os.MkdirAll(filepath.Dir(tg), 0o755)
	old := "#!/bin/sh\n# Telegram through the hub (the telegram skill), on the assistant's PATH while it is signed in (node/clicall.go).\n" +
		"exec /opt/example/personad tg \"$@\"\n"
	_ = os.WriteFile(tg, []byte(old), 0o755)
	prepare([]string{"telegram"}, nil)
	if _, err := os.Stat(tg); !os.IsNotExist(err) {
		t.Fatalf("the built-in's tg stayed: %v", err)
	}
	prepare([]string{"telegram"}, map[string]string{"tg": "telegram"})
	if b, err := os.ReadFile(tg); err != nil || !strings.Contains(string(b), "exec /opt/example/personad tool --cli telegram \"$@\"") {
		t.Fatalf("the extension's tg: %q %v", b, err)
	}
	prepare(nil, nil)
	if n.mayCall(n.assistantDirOf(".persona/assistant"), "telegram_list_chats") {
		t.Fatal("Telegram signed out, its tools are still called")
	}
}

// A connector with a command line of its own (connector.Manifest.CLI, #1704): the hub says it with the connectors, the
// node puts it on the assistant's PATH as personad tool --cli <id>, lets the
// command lines call that connector's tools, and takes away only the command lines it wrote once the connector is
// signed out (a plugin's gate commands live there too).
func TestConnectorCommandLines(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	prepare := func(conns []string, clis map[string]string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"dir": ".persona/assistant", "claude_md": "# 示例", "connectors": conns, "clis": clis})
		if _, err := n.handle(context.Background(), "assistant.prepare", params); err != nil {
			t.Fatal(err)
		}
	}
	cwd := n.assistantDirOf(".persona/assistant")
	bin := filepath.Join(n.dir, "assistant-bin")
	_ = os.MkdirAll(bin, 0o755)
	gate := filepath.Join(bin, "ol") // a plugin's gate command: not the node's to take away
	_ = os.WriteFile(gate, []byte("#!/bin/sh\nexec sh /x/ol.sh \"$@\"\n"), 0o755)

	prepare([]string{"telegram", "example"}, map[string]string{"tg": "telegram", "ex": "example"})
	for name, id := range map[string]string{"tg": "telegram", "ex": "example"} {
		if b, err := os.ReadFile(filepath.Join(bin, name)); err != nil || !strings.Contains(string(b), "exec /opt/example/personad tool --cli "+id+" \"$@\"") {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	if !n.mayCall(cwd, "example_list") || !n.mayCall(cwd, "telegram_cli") || n.mayCall(cwd, "phone_contacts") || n.mayCall(cwd, "examplex_list") {
		t.Fatal("the command lines call the signed-in connectors' tools, and only those")
	}

	prepare([]string{"telegram"}, nil) // signed in, and no command line said: none on the PATH
	if _, err := os.Stat(filepath.Join(bin, "tg")); !os.IsNotExist(err) {
		t.Fatalf("no command line said, tg stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "ex")); !os.IsNotExist(err) {
		t.Fatalf("example signed out, ex stayed: %v", err)
	}
	if n.mayCall(cwd, "example_list") {
		t.Fatal("example signed out, its tools are still called")
	}
	prepare(nil, nil)
	if _, err := os.Stat(filepath.Join(bin, "tg")); !os.IsNotExist(err) {
		t.Fatalf("signed out, tg stayed: %v", err)
	}
	if _, err := os.Stat(gate); err != nil {
		t.Fatal("a command the node did not write was taken away")
	}
}

// mcpRelay is a node socket served by n and the hub end of n's connection: ask sends one of the MCP server's requests
// and gives what the node relayed to the hub for it (assistant, session_id…).
func mcpRelay(t *testing.T, n *Node, hub *runtimeHub) (ask func(req map[string]any) map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var relayed []map[string]any
	hub.mu.Lock()
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "mcp.tools":
			relayed = append(relayed, p)
			return map[string]any{"tools": []proto.Tool{}}, nil, true
		case "mcp.call":
			relayed = append(relayed, p)
			return proto.TextResult("ok", false), nil, true
		}
		return nil, nil, false
	}
	hub.mu.Unlock()
	dir, err := os.MkdirTemp("/tmp", "mcp") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	return func(req map[string]any) map[string]any {
		t.Helper()
		mu.Lock()
		before := len(relayed)
		mu.Unlock()
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(req)
		_, _ = c.Write(append(b, '\n'))
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(relayed) != before+1 {
			t.Fatalf("%v: the hub was asked %d times", req, len(relayed)-before)
		}
		return relayed[before]
	}
}

func mcpTools(assistant bool, session string) map[string]any {
	return map[string]any{"kind": "mcp.tools", "assistant": assistant, "session_id": session}
}

func mcpCall(assistant bool, session string) map[string]any {
	return map[string]any{"kind": "mcp.call", "name": "kb_search", "args": map[string]any{}, "assistant": assistant, "session_id": session}
}

// An MCP server's word that it is the assistant's (PERSONA_ASSISTANT, which every process started with the
// assistant's environment repeats) reaches the hub only from a process the node runs for the assistant, with that
// conversation's session. Any other caller's request goes as an agent's; one that does not say so is not made one.
func TestMCPAssistantOnlyFromTheAssistantsProcesses(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here: TestMCPAssistantWithoutPeer")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	check := func(req map[string]any, assistant bool, session string) {
		t.Helper()
		if got := ask(req); got["assistant"] != assistant || got["session_id"] != session {
			t.Errorf("%v: relayed %v, want assistant %v and session %q", req, got, assistant, session)
		}
	}
	self := &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}

	// This test is no assistant process, whatever its server says: an agent with the assistant's environment.
	check(mcpTools(true, ""), false, "")
	check(mcpTools(true, "s1"), false, "s1")
	check(mcpCall(true, "s1"), false, "s1")

	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cmd: self}
	n.mu.Unlock()
	check(mcpTools(true, "s1"), true, "s1")
	check(mcpTools(true, ""), true, "s1") // the conversation's session, whatever the server names
	check(mcpCall(true, "other"), true, "s1")
	check(mcpTools(false, "x"), false, "x")

	// The warm spare's server lists the tools as it starts, before a conversation adopts it.
	n.mu.Lock()
	delete(n.procs, "s1")
	n.spare = &assistantProc{id: "spare-1", cmd: self}
	n.mu.Unlock()
	check(mcpTools(true, "spare-1"), true, "spare-1")

	// A Codex process's server says the process's key until its thread exists.
	n.mu.Lock()
	n.spare = nil
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: self, cx: &codexConn{key: "proc-k"}}}
	n.mu.Unlock()
	check(mcpTools(true, "proc-k"), true, "proc-k")

	n.mu.Lock()
	n.akeys = map[string]*assistantProc{}
	n.mu.Unlock()
	check(mcpCall(true, "proc-k"), false, "proc-k")
}

// A runtime's assistant conversations run in its own processes (assistant_runtime.go): a connected runtime that runs
// them (caps.assistant) vouches for its descendants' word, with the session they name; one that does not, for nothing.
func TestMCPAssistantFromARuntime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	// This test is the process at the other end of the runtime socket, and the MCP server's. It is the node's too,
	// which a runtime is not (TestMCPAssistantNotFromARelay). Swapped before the node listens: its connections read it.
	lineageWas := nodeLineage
	t.Cleanup(func() { nodeLineage = lineageWas })
	nodeLineage = func() []int { return nil }
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	check := func(req map[string]any, assistant bool) {
		t.Helper()
		if got := ask(req); got["assistant"] != assistant || got["session_id"] != "session-a" {
			t.Errorf("%v: relayed %v, want assistant %v", req, got, assistant)
		}
	}
	dialRuntime(t, n, map[string]any{"runtime": "plain", "caps": map[string]any{"start": true, "send": true}})
	check(mcpTools(true, "session-a"), false)
	check(mcpCall(true, "session-a"), false)
	dialRuntime(t, n, assistantRuntimeHello())
	check(mcpTools(true, "session-a"), true) // no runtime lists it yet: a chat lists the tools as it starts
	check(mcpCall(true, "session-a"), true)

	// Only a session that can be one of this runtime's assistant chats.
	conn := func(rt string) *runtimeConn {
		n.runtimes.mu.Lock()
		defer n.runtimes.mu.Unlock()
		return n.runtimes.byRuntime(rt)
	}
	n.runtimes.claim(conn("plain"), "session-p")
	n.runtimes.claim(conn("dsh"), "session-w") // the runtime's, an agent's chat
	n.runtimes.claim(conn("dsh"), "session-r")
	n.state.put("session-r", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.mu.Lock()
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: &exec.Cmd{Process: &os.Process{Pid: 1 << 22}}, cx: &codexConn{key: "proc-k"}}}
	n.mu.Unlock()
	for _, c := range []struct {
		session string
		ok      bool
	}{{"session-r", true}, {"", false}, {"0d000000-0000-4000-8000-00000000000a", false}, {"session-p", false}, {"session-w", false}, {"proc-k", false}} {
		if got := ask(mcpTools(true, c.session)); got["assistant"] != c.ok || got["session_id"] != c.session {
			t.Errorf("%q: relayed %v, want assistant %v", c.session, got, c.ok)
		}
	}
}

// A connection into the runtime socket from the node's own process or an ancestor of it is a relay (the manager's
// egress proxy leading an extension's sandbox there; the manager runs the node), not the runtime's process: it
// vouches for nothing, or everything the node runs would be the assistant.
func TestMCPAssistantNotFromARelay(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	dialRuntime(t, n, assistantRuntimeHello()) // from this test: the node's process
	if got := ask(mcpTools(true, "session-a")); got["assistant"] != false {
		t.Fatalf("relayed %v", got)
	}
}

// Where the system does not tell who is at the other end of the socket (Windows), the request must name a session
// the node runs for the assistant, or an assistant conversation of a connected runtime that runs them.
func TestMCPAssistantWithoutPeer(t *testing.T) {
	n, _ := testNode(t)
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: 100}}}
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: &exec.Cmd{Process: &os.Process{Pid: 200}}, cx: &codexConn{key: "proc-k"}}}
	n.state.put("session-r", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.state.put("session-w", sessionState{Cwd: filepath.Join(n.home, "work")})
	check := func(cases map[string]bool) {
		t.Helper()
		for session, want := range cases {
			got, rt, ok := n.mcpAssistantOf(0, errNoPeer, nil, session)
			if ok != want || ok && got != session {
				t.Errorf("%q: %q %v, want %v", session, got, ok, want)
			}
			// The runtime that vouches for one of its own conversations, none for one of this node's processes.
			if onRuntime := strings.HasPrefix(session, "session-"); ok && (rt != nil) != onRuntime {
				t.Errorf("%q: runtime %v, want one %v", session, rt, onRuntime)
			}
		}
	}
	// session-r is an assistant conversation by its directory, but no connected runtime has it.
	check(map[string]bool{"s1": true, "proc-k": true, "session-r": false, "session-w": false, "other": false, "": false})

	n.runtimes = newRuntimeRegistry()
	connect := func(rt string, assistant bool) *runtimeConn {
		h := n.runtimes.add(nil)
		h.mu.Lock()
		h.ready, h.hello.Runtime, h.hello.Caps.Assistant = true, rt, assistant
		h.mu.Unlock()
		return h
	}
	n.state.put("session-p", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.runtimes.claim(connect("plain", false), "session-p") // a runtime that runs no assistant
	dsh := connect("dsh", true)
	n.runtimes.claim(dsh, "session-r")
	n.runtimes.claim(connect("dsh2", true), "session-w") // not an assistant conversation
	check(map[string]bool{"session-r": true, "session-p": false, "session-w": false})
	if _, rt, _ := n.mcpAssistantOf(0, errNoPeer, nil, "session-r"); rt != dsh {
		t.Errorf("session-r: runtime %v, want the one that has it", rt)
	}
	// Any other reason the peer is not known (not a unix socket) is no assistant's.
	if got, _, ok := n.mcpAssistantOf(0, errors.New("not a unix socket"), nil, "s1"); ok {
		t.Errorf("a peer not known for another reason: %q", got)
	}
}
