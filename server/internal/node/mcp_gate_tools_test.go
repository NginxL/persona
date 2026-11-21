package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// hookSock serves n's hook socket and returns a call that sends one request and decodes the node's one-line answer.
func hookSock(t *testing.T, n *Node) func(req map[string]any) map[string]any {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcpg") // unix socket paths are short on macOS
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
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(req)
		_, _ = c.Write(append(b, '\n'))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(line, &out); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
}

func toolNames(res map[string]any) []string {
	list, _ := res["tools"].([]any)
	var out []string
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m["name"].(string))
		}
	}
	return out
}

func callText(res map[string]any) string {
	var out []string
	content, _ := res["content"].([]any)
	for _, x := range content {
		if m, ok := x.(map[string]any); ok {
			s, _ := m["text"].(string)
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// An Antigravity assistant chat's MCP server is `personad mcp` on the hook socket, not the runtime socket: its tool
// list has the command gates' tools, and a call of one goes through the gate and runs the skill's script
// here, as on the runtime socket (TestGatedToolGoesThroughTheGate). Claude Code's assistant (Bash and its hook), a
// guest, a narrower tool set and an agent get neither.
func TestMCPGateToolsOnTheHookSocketForARuntimesAssistant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	// This test is the runtime's process and its MCP server's (TestMCPAssistantFromARuntime).
	lineageWas := nodeLineage
	t.Cleanup(func() { nodeLineage = lineageWas })
	nodeLineage = func() []int { return nil }
	n, hub := runtimeNode(t)
	calls := assistantHub(hub)
	skills := filepath.Join(n.home, ".persona", "skills")
	t.Setenv("PERSONA_SKILLS_DIR", skills)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir = filepath.Join(n.home, ".persona")
	script := filepath.Join(skills, "example", "scripts", "ex.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(n.home, "ran")
	if err := os.WriteFile(script, []byte("#!/bin/bash\necho \"$*\" >> "+shellQuote(ran)+"\necho done \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	const gated = "chat_api"
	g := exampleGate("")
	g.Tool = &proto.GateTool{Name: gated, Description: "Run ex."}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	ask := hookSock(t, n)
	hubCalls := func() int {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		return len(calls.calls)
	}
	ranLines := func() string {
		b, _ := os.ReadFile(ran)
		return strings.TrimSpace(string(b))
	}
	tools := func(extra map[string]any) map[string]any {
		req := map[string]any{"kind": "mcp.tools", "assistant": true, "session_id": sid}
		for k, v := range extra {
			req[k] = v
		}
		return ask(req)
	}
	call := func(extra map[string]any, args ...string) map[string]any {
		req := map[string]any{"kind": "mcp.call", "name": gated, "args": map[string]any{"args": args}, "assistant": true, "session_id": sid}
		for k, v := range extra {
			req[k] = v
		}
		return ask(req)
	}

	// The runtime's assistant chat: the hub's tools, the gate's after them, and the session as before.
	res := tools(nil)
	if got := strings.Join(toolNames(res), ","); got != "phone_contacts,ask_user,"+gated {
		t.Fatalf("tools %q", got)
	}
	if res["session"] != sid {
		t.Fatalf("session %v, want %q", res["session"], sid)
	}
	var shown proto.Tool
	b, _ := json.Marshal(res["tools"].([]any)[2])
	if err := json.Unmarshal(b, &shown); err != nil || !strings.Contains(string(shown.InputSchema), `"required":["args"]`) || shown.Description != "Run ex." {
		t.Fatalf("the gate's tool %s (%v)", b, err)
	}
	// A chat the runtime does not list yet lists the tools as it starts (runtimeMayName), but runs nothing.
	if got := toolNames(ask(map[string]any{"kind": "mcp.tools", "assistant": true, "session_id": "session-new"})); !strings.Contains(strings.Join(got, ","), gated) {
		t.Fatalf("a starting chat's tools %v", got)
	}

	// A read goes through the gate (an allow rule) and runs the script; the hub is not asked to run it.
	before := hubCalls()
	if r := call(nil, "GET", "/me"); r["is_error"] == true || !strings.Contains(callText(r), "done GET /me") {
		t.Fatalf("a read: %v", r)
	}
	if hubCalls() != before {
		t.Fatal("the gated tool went to the hub")
	}
	// A write the configured rules leave to the phone is refused there: the script does not run.
	hub.mu.Lock()
	hub.resolve = func(ix proto.Interaction) proto.Resolution {
		no := false
		return proto.Resolution{Allow: &no, Message: "not now"}
	}
	hub.mu.Unlock()
	if r := call(nil, "POST", "/conversations/oc_test/messages", `{"text":"hi"}`); r["is_error"] != true || !strings.Contains(callText(r), "not now") {
		t.Fatalf("a refused write: %v", r)
	}
	hub.mu.Lock()
	asked := len(hub.asked)
	var card proto.Interaction
	if asked > 0 {
		card = hub.asked[0]
	}
	hub.mu.Unlock()
	if asked != 1 || card.SessionID != sid || card.ToolName == "" {
		t.Fatalf("%d cards, first %+v", asked, card)
	}
	// Missing args: the tool says how to call it, nothing runs.
	if r := ask(map[string]any{"kind": "mcp.call", "name": gated, "args": map[string]any{}, "assistant": true, "session_id": sid}); r["is_error"] != true {
		t.Fatalf("no args: %v", r)
	}

	// None of these get the gate's tool or run it: a guest, a narrower tool set, an agent, a chat no runtime lists.
	for _, c := range []struct {
		name  string
		extra map[string]any
	}{
		{"guest", map[string]any{"guest": true}},
		{"tool set", map[string]any{"tool_set": "supervise"}},
		{"agent", map[string]any{"assistant": false}},
		{"unlisted", map[string]any{"session_id": "session-new"}},
	} {
		if c.name != "unlisted" {
			if got := toolNames(tools(c.extra)); strings.Contains(strings.Join(got, ","), gated) {
				t.Errorf("%s: tools %v", c.name, got)
			}
		}
		before := hubCalls()
		call(c.extra, "GET", "/"+strings.ReplaceAll(c.name, " ", "-"))
		if hubCalls() != before+1 {
			t.Errorf("%s: the call did not go to the hub as any other tool's", c.name)
		}
	}
	if got := ranLines(); got != "GET /me" {
		t.Fatalf("the script ran %q", got)
	}

	// Once the runtime reports the worker that runs the conversation (runtime.assistant_pids), only that worker's
	// descendants run its gated tool: this test's process, the runtime's own, is not one of them.
	worker := exec.CommandContext(t.Context(), "sleep", "60")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	reportWorker := func(pids map[string]int) {
		t.Helper()
		if err := host.peer.Notify("runtime.assistant_pids", map[string]any{"pids": pids}); err != nil {
			t.Fatal(err)
		}
		want := len(pids)
		waitFor(t, "the worker report", func() bool { return len(n.runtimes.assistantWorkers()) == want })
	}
	reportWorker(map[string]int{sid: worker.Process.Pid})
	before = hubCalls()
	call(nil, "GET", "/not-the-worker")
	if hubCalls() != before+1 || ranLines() != "GET /me" {
		t.Fatalf("a process outside the conversation's worker ran its gated tool: %q", ranLines())
	}
	reportWorker(map[string]int{})
	if r := call(nil, "GET", "/again"); r["is_error"] == true || !strings.Contains(ranLines(), "GET /again") {
		t.Fatalf("without a worker report: %v", r)
	}

	// Claude Code's assistant, a process this node runs: it runs the gated command with Bash, not as a tool.
	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	res = tools(map[string]any{"session_id": "s1"})
	if res["session"] != "s1" || strings.Contains(strings.Join(toolNames(res), ","), gated) {
		t.Fatalf("Claude Code's assistant: %v", res)
	}
	before = hubCalls()
	call(map[string]any{"session_id": "s1"}, "GET", "/claude")
	if hubCalls() != before+1 || strings.Contains(ranLines(), "/claude") {
		t.Fatalf("Claude Code's assistant ran the gated tool here: %q", ranLines())
	}
}
