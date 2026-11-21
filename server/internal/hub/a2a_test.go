package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestA2ATwoNodesThroughManagerAndHub(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "parent", Name: "Parent", Kind: "local"}, {ID: "worker", Name: "Worker", Kind: "local"}}
	parent := newMeshNode("a2a-parent-machine", "parent", "parent-session")
	worker := newMeshNode("a2a-worker-machine", "worker", "worker-session")
	parent.sessions[0].Agent = "codex"
	worker.sessions[0].Agent = "opencode"
	worker.sessions[0].SourceKind = proto.SourceRuntime
	worker.sessions[0].Capabilities = &proto.SessionCapabilities{Read: true, Send: true}
	mgr := startTestManager(t, h, srv, map[string]func(context.Context) (net.Conn, error){
		"parent": parent.dialer(h), "worker": worker.dialer(h),
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); mgr.a.svc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	mirror(t, h, "parent")
	mirror(t, h, "worker")
	eventually(t, 10*time.Second, "both nodes listing sessions", func() bool {
		_, a := mgr.d.Session("parent", "parent-session")
		_, b := mgr.d.Session("worker", "worker-session")
		return a && b
	})
	mcp := func(node *meshNode, session, tool string, args map[string]string, out any) {
		t.Helper()
		var res proto.ToolResult
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := node.current().p.Call(ctx, "mcp.call", map[string]any{"session_id": session, "name": tool, "args": args}, &res); err != nil || res.IsError {
			t.Fatalf("%s: %v %s", tool, err, textOf(res))
		}
		if out != nil {
			if err := json.Unmarshal([]byte(textOf(res)), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var task struct {
		ID string `json:"task_id"`
	}
	mcp(parent, "parent-session", "a2a_send", map[string]string{
		"machine_id": "worker", "session_id": "worker-session", "message_id": "start", "text": "Inspect the integration test.",
	}, &task)
	if task.ID == "" {
		t.Fatal("no task created")
	}
	eventually(t, 10*time.Second, "the worker received the task", func() bool {
		return len(worker.sentTexts()) == 1 && strings.Contains(worker.sentTexts()[0], task.ID)
	})
	mcp(worker, "worker-session", "a2a_report", map[string]string{
		"task_id": task.ID, "message_id": "question", "state": "input_required", "text": "Which test should I inspect?",
	}, nil)
	eventually(t, 10*time.Second, "the parent's chat shows the question", func() bool {
		status, out := call(t, srv.Config.Handler, "GET", "/api/v1/sessions/parent/parent-session/items", h.cfg.Token, "")
		b, _ := json.Marshal(out)
		return status == http.StatusOK && strings.Contains(string(b), "Which test should I inspect?")
	})
	mcp(parent, "parent-session", "a2a_send", map[string]string{
		"task_id": task.ID, "message_id": "answer", "text": "Inspect the socket test.",
	}, nil)
	eventually(t, 10*time.Second, "the worker received the answer", func() bool { return len(worker.sentTexts()) == 2 })
	mcp(worker, "worker-session", "a2a_report", map[string]string{
		"task_id": task.ID, "message_id": "result", "state": "completed", "text": "The socket test passed.",
	}, nil)
	eventually(t, 10*time.Second, "the parent received the result", func() bool {
		return len(parent.sentTexts()) == 2 && strings.Contains(parent.sentTexts()[1], "The socket test passed.")
	})
}

func TestA2AScopedCredentialStreamsOnlyA2A(t *testing.T) {
	entered := make(chan http.Header, 1)
	h, _, srv := relayTestHubServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {}\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		<-r.Context().Done()
	}))
	code, out := call(t, srv.Config.Handler, "POST", "/api/v1/auth/tokens", h.cfg.Token,
		`{"name":"A2A integration","kind":"other","scopes":["agents.a2a"]}`)
	if code != http.StatusOK || out["role"] != roleScoped {
		t.Fatalf("issue scoped credential: %d %v", code, out)
	}
	token := out["token"].(string)
	for _, path := range []string{"/api/v1/sessions", "/api/v1/recordings/recording/audio"} {
		if status, _ := call(t, srv.Config.Handler, "GET", path, token, ""); status != http.StatusForbidden {
			t.Fatalf("an A2A token opened %s: %d", path, status)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v1/a2a/computer/session/rpc", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set(extwire.HeaderRole, roleFull)
	req.Header.Set(extwire.HeaderDevice, "forged")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		var body any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("stream response %d: %v", resp.StatusCode, body)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: {}\n" {
		t.Fatalf("first frame: %q %v", line, err)
	}
	headers := <-entered
	if headers.Get(extwire.HeaderRole) != roleScoped || headers.Get(extwire.HeaderDevice) != out["id"] ||
		headers.Get(extwire.HeaderScopes) != assistantwire.ScopeA2A || headers.Get("Authorization") != "" ||
		headers.Get("A2A-Version") != "1.0" {
		t.Fatalf("relayed headers: %v", headers)
	}
}
