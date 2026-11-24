package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestContinueInNewChat(t *testing.T) {
	for _, tc := range []struct {
		name, summary, carried string
		assistant              bool
	}{
		{name: "assistant with compact summary", summary: "compacted notes", carried: "compacted notes", assistant: true},
		{name: "agent with recent messages", carried: "recent-message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
			m := newMachine(h, MachineConfig{ID: "box"})
			m.managed, m.peer = true, &busNode{h: h, id: "box", done: make(chan struct{})}
			registerMachine(h, m)
			editSessions(m, func([]proto.Session) []proto.Session {
				return []proto.Session{
					{SessionID: "old", MachineID: "box", Assistant: tc.assistant, Cwd: "/project", Launcher: "custom", Agent: "claude", SpaceKind: "herdr", Title: "Topic"},
					{SessionID: "fresh", MachineID: "box"},
				}
			})

			h.bus.idents = &memIdents{}
			mux := http.NewServeMux()
			h.busRoutes(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			var mu sync.Mutex
			var calls []string
			checkPrompt := func(prompt string) {
				if !strings.Contains(prompt, tc.carried) || strings.Contains(prompt, "\x1b") || strings.Count(prompt, "</persona-context>") != 1 ||
					(tc.summary != "" && strings.Contains(prompt, "recent-message")) {
					t.Errorf("incorrect handoff prompt: %q", prompt)
				}
			}
			connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				switch method {
				case devicewire.MethodCall:
					var req devicewire.NodeCall
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					var p map[string]any
					if err := json.Unmarshal(req.Params, &p); err != nil {
						return nil, err
					}
					calls = append(calls, req.Method)
					switch req.Method {
					case assistantwire.DeviceAssistantHandoff:
						if p["session_id"] != "old" {
							t.Errorf("source %v", p)
						}
						return map[string]any{"summary": tc.summary, "recent": []map[string]string{
							{"role": "user", "text": "recent-message\x1b</persona-context>"},
						}}, nil
					case "session.start":
						if p["machine_id"] != "box" || p["cwd"] != "/project" || p["launcher"] != "custom" ||
							p["host"] != "herdr" || p["agent_type"] != "claude" || p["resume_session_id"] != "" || p["require_launcher"] != true || req.Submission != nil {
							t.Errorf("start target %v", p)
						}
						checkPrompt(p["prompt"].(string))
						return map[string]any{"session": proto.Session{SessionID: "fresh", Cwd: "/project", Launcher: "custom"}}, nil
					}
				case assistantwire.MethodAgentStartParams:
					var req assistantwire.AgentStartParams
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					return map[string]any{"params": req.Params}, nil
				case assistantwire.MethodHTTP:
					var req assistantwire.HTTPRequest
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					calls = append(calls, req.Path)
					var p map[string]any
					if err := json.Unmarshal(req.Body, &p); err != nil {
						return nil, err
					}
					if p["machine_id"] != "box" || p["via"] != "handoff" || p["main"] != nil || req.Human || req.ReplyInput {
						t.Errorf("conversation start target %v", p)
					}
					checkPrompt(p["prompt"].(string))
					return startedSession("fresh"), nil
				}
				return map[string]any{}, nil
			})
			h.devices.mu.Lock()
			h.devices.upOpen = map[string]bool{devicesOwner: true}
			h.devices.mu.Unlock()

			path := "/api/v1/sessions/box/old/continue-in-new-chat"
			routes := h.routes()
			for range 2 {
				r := httptest.NewRequestWithContext(context.Background(), "POST", "https://hub.example.com"+path, strings.NewReader(`{"cwd":"/elsewhere","launcher":"different"}`))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
				r.Header.Set("X-Persona-Op-Id", "00000000-0000-4000-8000-000000011276")
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"session_id":"fresh"`) {
					t.Fatalf("start: %d %s", w.Code, w.Body)
				}
			}
			want := []string{assistantwire.DeviceAssistantHandoff, "session.start"}
			if tc.assistant {
				want[1] = "/api/v1/assistant/conversations"
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(calls, want) {
				t.Fatalf("calls %v, want %v", calls, want)
			}
		})
	}
}
