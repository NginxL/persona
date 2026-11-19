package node

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCindyEngineDetected(t *testing.T) {
	n, _ := runtimeNode(t)
	home := t.TempDir()
	t.Setenv("CINDY_HOME", home)
	if e := n.cindyEngine(context.Background()); e.ID != proto.EngineCindy || e.Name != "Cindy" || e.Available {
		t.Fatalf("without Cindy: %+v", e)
	}

	// 1. Installed Linux release layout with resources/linux-build-info
	relDir := filepath.Join(t.TempDir(), "releases", "0.1.128-abc")
	if err := os.MkdirAll(filepath.Join(relDir, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "Cindy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "resources", "linux-build-info"), []byte("cindy-linux-v1\n0.1.128\nx64\nglobal\nCindy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CINDY_BIN", filepath.Join(relDir, "Cindy"))
	if e := n.cindyEngine(context.Background()); !e.Available || e.Version != "0.1.128" {
		t.Fatalf("installed Cindy: %+v", e)
	}
	t.Setenv("CINDY_BIN", "")

	// 2. Running cindy-host endpoint in CINDY_HOME
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "cindy-host", "version": "0.1.130"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	epBytes, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "baseUrl": srv.URL})
	if err := os.WriteFile(filepath.Join(home, "host-endpoint.json"), epBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if e := n.cindyEngine(context.Background()); !e.Available || e.Version != "0.1.130" {
		t.Fatalf("cindy-host listening: %+v", e)
	}

	// 3. Connected Cindy runtime wins
	hello := map[string]any{
		"runtime": "cindy",
		"name":    "Cindy",
		"agent":   "claude",
		"version": "0.1.135",
		"caps":    map[string]any{"send": true, "interrupt": true, "start": true, "resume": true, "modes": true},
	}
	dialRuntime(t, n, hello)
	if e := n.cindyEngine(context.Background()); !e.Available || e.Version != "0.1.135" {
		t.Fatalf("with the runtime connected: %+v", e)
	}
	var found bool
	for _, x := range n.engines(context.Background(), "") {
		found = found || x.ID == proto.EngineCindy
	}
	if !found {
		t.Fatal("node.info's engines leave Cindy out")
	}
}

func TestCindyBridgeSessionsAndOperations(t *testing.T) {
	oldPoll, oldRestart := cindyBridgePoll, cindyBridgeRestart
	cindyBridgePoll, cindyBridgeRestart = 30*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { cindyBridgePoll, cindyBridgeRestart = oldPoll, oldRestart })

	n, _ := runtimeNode(t)
	home := t.TempDir()
	t.Setenv("CINDY_HOME", home)
	token := "test-cindy-token-0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(home, "host-api-token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	sessions := []map[string]any{
		{
			"id":             "s1",
			"title":          "Refactor engine picker",
			"workingDir":     "/code/app/wt-1",
			"projectDir":     "/code/app",
			"workspaceKind":  "project",
			"worktreePath":   "/code/app/wt-1",
			"model":          "claude-sonnet-4-6",
			"effort":         "high",
			"permissionMode": "acceptEdits",
			"status":         "active",
			"sdkSessionId":   sdkSession,
			"agentKind":      "cc",
			"lastActivityAt": time.Now().UnixMilli(),
			"listPreview":    "Updated the engine picker options.",
		},
		{
			"id":             "s2",
			"title":          "Fix Pi adapter",
			"workingDir":     "/code/pi-proj",
			"projectDir":     "/code/pi-proj",
			"workspaceKind":  "project",
			"model":          "claude-opus-4-6",
			"effort":         "medium",
			"permissionMode": "ask",
			"status":         "active",
			"agentKind":      "pi",
			"lastActivityAt": time.Now().UnixMilli(),
		},
	}
	var enqueued []map[string]any
	var stopped []string
	var patches []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "cindy-host", "version": "0.1.128"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(": connected\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"providers": []map[string]any{
					{
						"id":        "xd",
						"name":      "Cindy Gateway",
						"connected": true,
						"agents":    []string{"claude-code", "codex"},
						"models": map[string]any{
							"claude-code": []map[string]any{
								{"id": "claude-sonnet-4-6", "name": "Sonnet 4.6", "efforts": []string{"low", "medium", "high"}, "defaultEffort": "high"},
							},
						},
					},
				},
			})
		case "/api/sessions/list":
			_ = json.NewEncoder(w).Encode(sessions)
		case "/api/runtime/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"sessionId": "s2", "isTurnRunning": true},
			})
		case "/api/project-aliases/list":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"projectKey": "/code/app", "alias": "App Monorepo"},
			})
		case "/api/interactions/pending":
			_ = json.NewEncoder(w).Encode([]any{})
		case "/api/sessions/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]any{
					{"id": "m1", "role": "user", "content": `{"text":"hello cindy"}`, "createdAt": time.Now().UnixMilli()},
					{"id": "m2", "role": "assistant", "content": "hi from cindy", "createdAt": time.Now().UnixMilli()},
				},
			})
		case "/api/input/steer":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
		case "/api/input/enqueue":
			enqueued = append(enqueued, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"turnInProgress": true})
		case "/api/input/stop":
			stopped = append(stopped, body["sessionId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"turnInProgress": false})
		case "/api/sessions/update":
			patches = append(patches, body)
			_ = json.NewEncoder(w).Encode(sessions[0])
		case "/api/sessions/create":
			created := map[string]any{
				"id":         "s3",
				"title":      "New task",
				"workingDir": body["workingDir"],
				"projectDir": body["workingDir"],
				"status":     "active",
				"agentKind":  body["agentKind"],
				"model":      "claude-sonnet-4-6",
			}
			sessions = append(sessions, created)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(created)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	epBytes, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "baseUrl": srv.URL})
	if err := os.WriteFile(filepath.Join(home, "host-endpoint.json"), epBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := nodeCall(t, n, "engine.use", map[string]any{"engine": proto.EngineCindy}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.runtimeHost.setCindyBridge(false) })

	waitFor(t, "Cindy sessions listed", func() bool {
		m := listed(t, n)
		return m[sdkSession].SessionID == sdkSession && m["cindy-s2"].SessionID == "cindy-s2"
	})

	got := listed(t, n)
	s1 := got[sdkSession]
	if s1.Host != proto.EngineCindy || s1.SpaceKind != proto.EngineCindy || s1.SpaceName != "App Monorepo" ||
		s1.RepoRoot != "/code/app" || s1.PermissionMode != "acceptEdits" || s1.Effort != "high" || s1.Status != proto.StatusDone {
		t.Fatalf("s1 = %+v", s1)
	}
	s2 := got["cindy-s2"]
	if s2.Host != proto.EngineCindy || s2.Agent != "pi" || s2.Status != proto.StatusWorking {
		t.Fatalf("s2 = %+v", s2)
	}

	// Read items from Cindy messages API
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "cindy-s2", "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	_ = json.Unmarshal(raw, &page)
	if len(page.Items) != 2 || page.Items[0].Text != "hello cindy" || page.Items[1].Text != "hi from cindy" {
		t.Fatalf("items = %+v", page.Items)
	}

	// Send message, interrupt, modes, close, and start
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "cindy-s2", "text": "ship it", "client_msg_id": "msg-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": "cindy-s2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": sdkSession, "permission_mode": "plan", "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.close", map[string]any{"session_id": "cindy-s2"}); err != nil {
		t.Fatal(err)
	}
	startDir := t.TempDir()
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": startDir, "prompt": "build feature", "launcher": "cindy-claude"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(enqueued) < 2 || len(stopped) != 1 || stopped[0] != "s2" || len(patches) < 2 {
		t.Fatalf("enqueued=%d stopped=%v patches=%d", len(enqueued), stopped, len(patches))
	}
}

// Cindy has no image-input contract. Sending or starting with pictures must refuse explicitly, never answer ok after
// dropping them (#11084).
func TestCindyImageInputIsExplicitlyUnsupported(t *testing.T) {
	b := &cindyBridge{}
	for _, method := range []string{"session.send", "session.start"} {
		params, _ := json.Marshal(map[string]any{"session_id": "s1", "text": "read this", "prompt": "read this",
			"images": []map[string]any{{"mime": "image/png", "data": "aW1hZ2U="}}})
		_, err := b.request(context.Background(), method, params)
		var rpcErr *rpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != "image_input_unsupported" || !strings.Contains(rpcErr.Message, "does not support image input") {
			t.Fatalf("%s: %v, want image_input_unsupported", method, err)
		}
	}
}
