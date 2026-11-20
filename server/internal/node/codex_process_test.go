package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercises the built CLI, not just n.handle. Its HOME, registry and servers are independent of the user's runtime.
func TestCodexRemoteNodeProcess(t *testing.T) {
	bin := os.Getenv("PERSONA_CODEX_NODE_BINARY")
	if bin == "" {
		t.Skip("set PERSONA_CODEX_NODE_BINARY to the built personad")
	}
	home := t.TempDir()
	profile := t.TempDir()
	f := &remoteFixture{home: profile, threads: []string{remoteRaw}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "node", "--stdio")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "ORCA_USER_DATA_PATH=" + filepath.Join(home, ".orca")}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); stdin.Close(); _ = cmd.Wait() }()
	peer := rpc.New("test", stdin)
	peer.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"sessions": map[string]any{}}, nil
	}
	go peer.Serve(ctx, stdout)
	no := false
	cfg := proto.CodexConfig{Discovery: &no, Homes: []string{profile}, Servers: []proto.CodexServer{{ID: "smoke", Home: profile, Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")}}}
	if err = peer.Call(ctx, "node.configure", map[string]any{"machine_id": "test", "codex": cfg}, nil); err != nil {
		t.Fatal(err)
	}
	if err = peer.Call(ctx, "engine.use", map[string]any{"engine": "herdr-core"}, nil); err != nil {
		t.Fatal(err)
	}
	var session proto.Session
	waitFor(t, "real node publishes external thread", func() bool {
		var reply struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if peer.Call(ctx, "sessions.list", map[string]any{}, &reply) != nil {
			return false
		}
		for _, s := range reply.Sessions {
			if s.SourceThreadID == remoteRaw && s.Capabilities != nil && s.Capabilities.Send {
				session = s
				return true
			}
		}
		return false
	})
	if f.count("thread/resume") != 0 {
		t.Fatal("listing resumed the real node's thread")
	}
	f.mu.Lock()
	f.reject = "turn/start"
	f.rejectMessage = "thread " + remoteRaw + " already has an active writer"
	f.mu.Unlock()
	err = peer.Call(ctx, "session.send", map[string]any{"session_id": session.SessionID, "text": "合成协议请求", "client_msg_id": "smoke-one"}, nil)
	var refusal *rpc.Error
	if !errors.As(err, &refusal) || refusal.Code != "codex_writer_busy" {
		t.Fatalf("active writer through the real node = %v", err)
	}
	var outcome struct {
		Delivered string `json:"delivered"`
		Detail    string `json:"detail"`
	}
	if json.Unmarshal(refusal.Data, &outcome) != nil || outcome.Delivered != "none" || !strings.Contains(outcome.Detail, "active writer") {
		t.Fatalf("active writer outcome = %+v (%s)", outcome, refusal.Data)
	}
	f.mu.Lock()
	f.reject = ""
	f.mu.Unlock()
	// An undelivered conflict keeps the draft: the same intentional retry has the same delivery ID and runs once now.
	if err = peer.Call(ctx, "session.send", map[string]any{"session_id": session.SessionID, "text": "合成协议请求", "client_msg_id": "smoke-one"}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count("turn/start") != 2 || f.count("thread/resume") != 1 {
		t.Fatal("conflict retry did not deliver exactly one follow-up through the same owner")
	}
	if err = peer.Call(ctx, "session.interrupt", map[string]any{"session_id": session.SessionID}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count("turn/interrupt") != 1 || f.count("thread/backgroundTerminals/clean") != 0 {
		t.Fatal("real process interrupt scope")
	}
}
