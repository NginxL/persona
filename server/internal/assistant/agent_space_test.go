package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentToolsRespectConversationSpace(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("server",
		proto.Session{SessionID: "conv1", Assistant: true},
		proto.Session{SessionID: "health-chat", Cwd: "/home/me/.persona/health", Assistant: true, Space: SpaceHealth},
	)
	healthChild := proto.Session{SessionID: "folded-private", Space: SpaceHealth}
	healthChild.SetFold("agent-project", "", "", nil)
	f.setSessions("box",
		proto.Session{SessionID: "agent-project", Cwd: "/work/project", Title: "Project", Status: proto.StatusIdle},
		proto.Session{SessionID: "agent-private", Cwd: "/work/other", Space: SpaceHealth, Title: "Private", Status: proto.StatusIdle},
		proto.Session{SessionID: "agent-notes", Cwd: "/home/me/.persona/health-m-0b0b0b/notes", Title: "Notes"},
		healthChild,
	)
	f.hubCards = []proto.Interaction{{ID: "folded-card", MachineID: "box", SessionID: healthChild.SessionID,
		FoldedUnder: "agent-project", Kind: proto.IxPermission, Summary: "Private card"}}

	for _, tc := range []struct {
		caller, allowed, denied, title string
	}{
		{"conv1", "agent-project", "agent-private", "Project"},
		{"health-chat", "agent-private", "agent-project", "Private"},
	} {
		t.Run(tc.caller, func(t *testing.T) {
			tool := func(name string, args map[string]string) (string, bool) {
				t.Helper()
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
				defer cancel()
				res := s.Tool(ctx, ToolCall{MachineID: "server", SessionID: tc.caller, Name: name, Arguments: raw})
				if len(res.Content) != 1 {
					t.Fatalf("%s: unexpected result %+v", name, res)
				}
				return res.Content[0].Text, res.IsError
			}
			list, failed := tool("agents_list", map[string]string{"state": "all"})
			if failed || !strings.Contains(list, tc.allowed) || strings.Contains(list, tc.denied) ||
				strings.Contains(list, "folded-card") || strings.Contains(list, "agent-notes") != (tc.caller == "health-chat") {
				t.Fatalf("unexpected agent list: %s", list)
			}

			reads, sends := itemReads(f), len(sentToAgent(f))
			for _, name := range []string{"agent_read", "agent_send"} {
				args := map[string]string{"machine": "box", "session_id": tc.denied}
				if name == "agent_send" {
					args["text"] = "Check the fixtures"
				}
				if text, failed := tool(name, args); !failed || !strings.HasPrefix(text, "no agent ") {
					t.Errorf("%s cross-space result: %q (error=%v)", name, text, failed)
				}
			}
			if itemReads(f) != reads || len(sentToAgent(f)) != sends {
				t.Fatal("a cross-space call reached the fake node")
			}

			// Both spaces have a matching Agent. Only the in-space match can resolve the prefix.
			if text, failed := tool("agent_read", map[string]string{"machine": "box", "session_id": "agent-pr"}); failed || !strings.Contains(text, tc.title) || itemReads(f) != reads+1 {
				t.Fatalf("same-space read: %q (error=%v)", text, failed)
			}
			const message = "Check the fixtures"
			if tc.caller == "health-chat" {
				answerOutbound(t, s, tc.caller, "agent_send", message, true)
			}
			if text, failed := tool("agent_send", map[string]string{"machine": "box", "session_id": tc.allowed, "text": message}); failed || !strings.HasPrefix(text, "Sent to "+tc.title) {
				t.Fatalf("same-space send: %q (error=%v)", text, failed)
			}
			if got := sentToAgent(f); len(got) != sends+1 || got[sends] != message {
				t.Fatalf("same-space send was not delivered once: %v", got)
			}
		})
	}
}

func TestExtensionConversationStartSpace(t *testing.T) {
	s, _ := newQuietService(t)
	for _, tc := range []struct {
		space string
		code  int
	}{
		{"", http.StatusOK},
		{SpaceHealth, http.StatusForbidden},
	} {
		body, err := json.Marshal(map[string]string{"space": tc.space, "prompt": "Check the fixtures"})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(string(body)))
		r.Header.Set(RoleHeader, roleExtension)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("start in space %q: status %d, want %d: %s", tc.space, w.Code, tc.code, w.Body.String())
		}
	}
}
