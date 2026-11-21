package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeT3Code is a T3 Code server's MCP endpoint and its t3 command line, as the bridge uses them (synthetic data).
type fakeT3Code struct {
	t     *testing.T
	token string
	srv   *httptest.Server

	mu          sync.Mutex
	initialized bool
	pinged      bool
	interrupts  []string
	archived    []string
	sends       []string // "<thread> <message>"
	starts      []string // "<project> <message>"
	revoked     []string
}

func newFakeT3Code(t *testing.T) *fakeT3Code {
	f := &fakeT3Code{t: t, token: "synthetic-t3-token"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeT3Code) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden) // T3 Code refuses browser origins
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	switch {
	case msg.Method == "" && len(msg.Result) > 0: // the client's answer to the ping
		f.mu.Lock()
		f.pinged = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	case msg.Method == "initialize":
		w.Header().Set("Mcp-Session-Id", "mcp-session-1")
		writeT3Result(w, msg.ID, map[string]any{"protocolVersion": t3codeMCPProtocol, "capabilities": map[string]any{}})
		return
	case msg.Method == "notifications/initialized":
		f.mu.Lock()
		f.initialized = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	case r.Header.Get("Mcp-Session-Id") != "mcp-session-1":
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	args := msg.Params.Arguments
	switch msg.Params.Name {
	case "t3_project_list":
		writeT3Tool(w, msg.ID, map[string]any{"projects": []map[string]any{
			{"id": "proj-1", "title": "Demo App", "workspaceRoot": "/code/demo"},
			{"id": "proj-2", "title": "Demo App Docs", "workspaceRoot": "/code/demo/docs"},
		}, "nextCursor": nil})
	case "t3_thread_list":
		writeT3Tool(w, msg.ID, map[string]any{"threads": []map[string]any{
			{"threadId": "thr-1", "projectId": "proj-1", "title": "Tidy the build", "status": "running",
				"providerInstanceId": "claudeAgent", "model": "claude-sonnet", "runtimeMode": "full-access",
				"interactionMode": "default", "itemCount": 5, "updatedAt": "2026-11-20T10:00:00.000Z"},
			{"threadId": "thr-2", "projectId": "proj-2", "title": "", "status": "waiting",
				"providerInstanceId": "codex", "model": "gpt-5", "runtimeMode": "approval-required",
				"interactionMode": "plan", "itemCount": 0, "updatedAt": "2026-11-20T09:00:00.000Z"},
		}, "nextCursor": nil, "total": 2})
	case "t3_thread_read":
		f.read(w, msg.ID, args)
	case "t3_thread_interrupt":
		tid, _ := args["threadId"].(string)
		if tid == "thr-1" { // full access, above a CLI-issued session's ceiling
			writeT3Failure(w, msg.ID, "runtime_mode_escalation_denied", "This session's access is approval-required.")
			return
		}
		f.mu.Lock()
		f.interrupts = append(f.interrupts, tid)
		f.mu.Unlock()
		writeT3Tool(w, msg.ID, map[string]any{"threadId": tid, "runId": "run-2", "status": "interrupt_requested"})
	case "t3_thread_organize":
		f.mu.Lock()
		f.archived = append(f.archived, fmt.Sprint(args["threadId"], " ", args["action"]))
		f.mu.Unlock()
		writeT3Tool(w, msg.ID, map[string]any{"sequence": 1})
	default:
		writeT3Failure(w, msg.ID, "invalid_request", "unknown tool")
	}
}

// read answers t3_thread_read as an event stream, with a ping before the answer.
func (f *fakeT3Code) read(w http.ResponseWriter, id json.RawMessage, args map[string]any) {
	text := func(s string) *string { return &s }
	all := []map[string]any{
		{"position": 0, "itemId": "i0", "type": "user_message", "status": "completed", "text": text("Tidy the build please"), "updatedAt": "2026-11-20T09:59:00.000Z"},
		{"position": 1, "itemId": "i1", "type": "reasoning", "status": "completed", "text": text("Looking at the Makefile"), "updatedAt": "2026-11-20T09:59:01.000Z"},
		{"position": 2, "itemId": "i2", "type": "command_execution", "status": "completed", "title": text("make lint"), "text": text("ok"), "updatedAt": "2026-11-20T09:59:02.000Z"},
		{"position": 3, "itemId": "i3", "type": "checkpoint", "status": "completed", "updatedAt": "2026-11-20T09:59:03.000Z"},
		{"position": 4, "itemId": "i4", "type": "assistant_message", "status": "completed", "text": text("The build is tidy."), "updatedAt": "2026-11-20T09:59:04.000Z"},
	}
	after := -1
	if v, ok := args["afterPosition"].(float64); ok {
		after = int(v)
	}
	limit := 100
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	var page []map[string]any
	for _, it := range all {
		if it["position"].(int) > after && len(page) < limit {
			page = append(page, it)
		}
	}
	var next any
	if len(page) > 0 {
		next = page[len(page)-1]["position"]
	}
	res := map[string]any{"thread": map[string]any{"threadId": args["threadId"], "itemCount": len(all)},
		"recentRuns": []any{}, "items": page, "nextPosition": next,
		"hasMore": len(page) > 0 && page[len(page)-1]["position"].(int) < len(all)-1}
	raw, _ := json.Marshal(res)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw)}})
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
}

func writeT3Result(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeT3Tool(w http.ResponseWriter, id json.RawMessage, structured any) {
	raw, _ := json.Marshal(structured)
	writeT3Result(w, id, map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}, "structuredContent": structured})
}

func writeT3Failure(w http.ResponseWriter, id json.RawMessage, code, message string) {
	raw, _ := json.Marshal(map[string]any{"_tag": "OrchestratorMcpFailure", "code": code, "message": message})
	writeT3Result(w, id, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": string(raw)}}})
}

// cli stands in for the t3 command line.
func (f *fakeT3Code) cli(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "auth session issue "):
		if !strings.Contains(joined, "--subject mcp-client") || !strings.Contains(joined, "--json") {
			return nil, errors.New("t3 auth: unexpected flags " + joined)
		}
		return json.Marshal(map[string]any{"sessionId": "sess-1", "token": f.token, "expiresAt": "2026-11-28T00:00:00.000Z"})
	case strings.HasPrefix(joined, "auth session revoke "):
		f.revoked = append(f.revoked, args[len(args)-1])
		return []byte("{}"), nil
	case strings.HasPrefix(joined, "thread send --json "):
		f.sends = append(f.sends, args[3]+" "+stdin)
		return json.Marshal(map[string]any{"threadId": args[3], "delivery": "queued"})
	case strings.HasPrefix(joined, "thread start --json "):
		f.starts = append(f.starts, args[3]+" "+stdin)
		return json.Marshal(map[string]any{"threadId": "thr-new", "projectId": args[3], "runId": "run-9"})
	}
	return nil, errors.New("t3: unexpected command " + joined)
}

// useFakeT3Code points the bridge's discovery and command line at f: a T3 home whose state names f's server.
func useFakeT3Code(t *testing.T, f *fakeT3Code) {
	home := t.TempDir()
	t.Setenv("T3CODE_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "userdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 1, "origin": f.srv.URL, "serverVersion": "0.1.11"})
	if err := os.WriteFile(filepath.Join(home, "userdata", "server-runtime.json"), st, 0o600); err != nil {
		t.Fatal(err)
	}
	oldBin, oldRun, oldAlive := t3codeBin, t3codeRun, t3codeAlive
	oldPoll, oldRestart := t3codeBridgePoll, t3codeBridgeRestart
	t3codeBin = func() string { return "/fake/bin/t3" }
	t3codeRun = f.cli
	t3codeAlive = func(int) bool { return true }
	t3codeBridgePoll, t3codeBridgeRestart = 30*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() {
		t3codeBin, t3codeRun, t3codeAlive = oldBin, oldRun, oldAlive
		t3codeBridgePoll, t3codeBridgeRestart = oldPoll, oldRestart
	})
}

func TestT3CodeEngineDetected(t *testing.T) {
	n, _ := runtimeNode(t)
	t.Setenv("T3CODE_HOME", t.TempDir())
	oldBin := t3codeBin
	t.Cleanup(func() { t3codeBin = oldBin })
	t3codeBin = func() string { return "" }
	if e := n.t3codeEngine(context.Background()); e.ID != proto.EngineT3Code || e.Name != "T3 Code" || e.Available {
		t.Fatalf("without T3 Code: %+v", e)
	}
	t3codeBin = func() string { return "/fake/bin/t3" }
	if e := n.t3codeEngine(context.Background()); !e.Available || e.Version != "" {
		t.Fatalf("t3 installed, server not running: %+v", e)
	}

	f := newFakeT3Code(t)
	useFakeT3Code(t, f)
	t3codeBin = func() string { return "" }
	if e := n.t3codeEngine(context.Background()); !e.Available || e.Version != "0.1.11" || !strings.Contains(e.Note, "t3") {
		t.Fatalf("server running without the t3 command line: %+v", e)
	}

	var found bool
	for _, x := range n.engines(context.Background(), "") {
		found = found || x.ID == proto.EngineT3Code
	}
	if !found {
		t.Fatal("node.info's engines leave T3 Code out")
	}
}

func TestT3CodeServerStateNeedsLoopbackAndALiveProcess(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "userdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(origin string) {
		st, _ := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 3773, "origin": origin, "serverVersion": "0.1.11"})
		if err := os.WriteFile(filepath.Join(home, "userdata", "server-runtime.json"), st, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldAlive := t3codeAlive
	t.Cleanup(func() { t3codeAlive = oldAlive })
	t3codeAlive = func(int) bool { return true }
	for origin, want := range map[string]bool{
		"http://127.0.0.1:3773": true,
		"http://localhost:3773": true,
		"http://[::1]:3773":     true,
		"http://192.0.2.7:3773": false, // the bridge sends its session only to this computer
		"https://127.0.0.1:1":   false,
		"":                      false,
	} {
		write(origin)
		if _, ok := t3codeServerOf(home); ok != want {
			t.Errorf("origin %q: found %v, want %v", origin, ok, want)
		}
	}
	write("http://127.0.0.1:3773")
	t3codeAlive = func(int) bool { return false }
	if _, ok := t3codeServerOf(home); ok {
		t.Error("a server whose process is gone is still found")
	}
}

func TestT3CodeMappings(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"approval-required", "default"}: "default",
		{"auto-accept-edits", "default"}: "acceptEdits",
		{"auto", "default"}:              "auto",
		{"full-access", "default"}:       proto.ModeBypass,
		{"full-access", "plan"}:          "plan",
		{"", ""}:                         "",
	} {
		if got := t3codeMode(in[0], in[1]); got != want {
			t.Errorf("t3codeMode(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	for in, want := range map[string]string{"claudeAgent": "claude", "codex": "codex", "opencode": "opencode", "pi": "pi", "": "codex"} {
		if got := t3codeAgent(in); got != want {
			t.Errorf("t3codeAgent(%q) = %q, want %q", in, got, want)
		}
	}
	if t3codeStatus("idle", 0) != proto.StatusIdle || t3codeStatus("idle", 3) != proto.StatusDone ||
		t3codeStatus("waiting", 3) != proto.StatusBlocked || t3codeStatus("starting", 0) != proto.StatusWorking {
		t.Error("t3codeStatus maps a status wrongly")
	}
	if t3codeSessionID("") != "" || t3codeSessionID("bad id") != "" || t3codeSessionID("thr_1") != "t3code-thr_1" {
		t.Error("t3codeSessionID")
	}
}

func TestT3CodeBridgeSessionsAndOperations(t *testing.T) {
	f := newFakeT3Code(t)
	useFakeT3Code(t, f)
	n, _ := runtimeNode(t)
	if _, err := nodeCall(t, n, "engine.use", map[string]any{"engine": proto.EngineT3Code}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.runtimeHost.setT3CodeBridge(false) })

	waitFor(t, "T3 Code threads listed", func() bool {
		m := listed(t, n)
		return m["t3code-thr-1"].SessionID != "" && m["t3code-thr-2"].SessionID != ""
	})
	got := listed(t, n)
	s1, s2 := got["t3code-thr-1"], got["t3code-thr-2"]
	if s1.Host != proto.EngineT3Code || s1.Title != "Tidy the build" || s1.Cwd != "/code/demo" || s1.SpaceName != "Demo App" ||
		s1.Agent != "claude" || s1.Status != proto.StatusWorking || s1.PermissionMode != proto.ModeBypass || s1.Model != "claude-sonnet" {
		t.Fatalf("thr-1 = %+v", s1)
	}
	if s2.Title != "Demo App Docs" || s2.Status != proto.StatusBlocked || s2.PermissionMode != "plan" || s2.Agent != "codex" {
		t.Fatalf("thr-2 = %+v", s2)
	}

	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "t3code-thr-1", "limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items   []proto.Item `json:"items"`
		ToSeq   int          `json:"to_seq"`
		HasMore bool         `json:"has_more"`
	}
	_ = json.Unmarshal(raw, &page)
	if page.ToSeq != 5 || page.HasMore || len(page.Items) != 4 {
		t.Fatalf("page = %+v", page)
	}
	if it := page.Items[0]; it.Role != "user" || it.Kind != proto.KindText || it.Text != "Tidy the build please" || it.Seq != 1 {
		t.Fatalf("first item = %+v", it)
	}
	if it := page.Items[1]; it.Kind != proto.KindThinking {
		t.Fatalf("reasoning = %+v", it)
	}
	if it := page.Items[2]; it.Kind != proto.KindToolUse || it.ToolName != "make lint" {
		t.Fatalf("command = %+v", it)
	}
	if it := page.Items[3]; it.Role != "assistant" || it.Text != "The build is tidy." || it.Seq != 5 {
		t.Fatalf("last item = %+v", it)
	}

	raw, err = nodeCall(t, n, "session.items", map[string]any{"session_id": "t3code-thr-1", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	page.Items = nil
	_ = json.Unmarshal(raw, &page)
	if !page.HasMore || len(page.Items) != 1 || page.Items[0].Seq != 5 {
		t.Fatalf("last two positions = %+v", page)
	}

	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "t3code-thr-1", "text": "also the tests", "client_msg_id": "msg-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = nodeCall(t, n, "session.interrupt", map[string]any{"session_id": "t3code-thr-1"})
	if err == nil || !strings.Contains(err.Error(), "T3 Code") {
		t.Fatalf("interrupting a full-access thread: %v, want the refusal that points to T3 Code", err)
	}
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": "t3code-thr-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.close", map[string]any{"session_id": "t3code-thr-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/code/demo/docs/guide", "prompt": "Write the guide"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/elsewhere", "prompt": "Nothing"}); err == nil {
		t.Fatal("a start outside every T3 project was taken")
	}

	f.mu.Lock()
	if !f.initialized || !f.pinged {
		t.Errorf("initialized=%v pinged=%v", f.initialized, f.pinged)
	}
	if len(f.sends) != 1 || f.sends[0] != "thr-1 also the tests" {
		t.Errorf("sends = %q", f.sends)
	}
	if len(f.interrupts) != 1 || f.interrupts[0] != "thr-2" || len(f.archived) != 1 || f.archived[0] != "thr-2 archive" {
		t.Errorf("interrupts = %q archived = %q", f.interrupts, f.archived)
	}
	if len(f.starts) != 1 || f.starts[0] != "proj-2 Write the guide" { // the deepest project holding the folder
		t.Errorf("starts = %q", f.starts)
	}
	f.mu.Unlock()

	n.runtimeHost.setT3CodeBridge(false)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revoked) == 0 || f.revoked[0] != "sess-1" {
		t.Errorf("the bridge's session was not revoked when it stopped: %q", f.revoked)
	}
}

// T3 Code takes no pictures through the bridge: a send or start with them is refused, never answered ok after dropping
// them (#11084).
func TestT3CodeImageInputIsExplicitlyUnsupported(t *testing.T) {
	b := newT3CodeBridge(nil, nil, t.Logf)
	for _, method := range []string{"session.send", "session.start"} {
		params, _ := json.Marshal(map[string]any{"session_id": "t3code-thr-1", "text": "read this", "prompt": "read this",
			"images": []map[string]any{{"mime": "image/png", "data": "aW1hZ2U="}}})
		_, err := b.request(context.Background(), method, params)
		var rpcErr *rpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != "image_input_unsupported" {
			t.Fatalf("%s: %v, want image_input_unsupported", method, err)
		}
	}
}
