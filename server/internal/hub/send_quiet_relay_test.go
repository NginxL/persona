package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type quietRelayHubStore struct {
	*settingsStore
	sends *sentStore
}

func (s *quietRelayHubStore) ClaimSend(ctx context.Context, id, machine, session string) (string, error) {
	return s.sends.ClaimSend(ctx, id, machine, session)
}

func (s *quietRelayHubStore) SettleSend(ctx context.Context, id, state string) error {
	return s.sends.SettleSend(ctx, id, state)
}

func quietRelayHub(t *testing.T) (*Hub, *assistantNode) {
	t.Helper()
	h := newTestHub(t)
	h.store = &quietRelayHubStore{
		settingsStore: &settingsStore{vals: map[string][]byte{}},
		sends:         newSentStore(),
	}
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	if err := h.store.PutSetting(context.Background(), "assistant", assistant.Settings{
		MachineID: "server",
		Main:      "main-0001",
		Timezone:  "Asia/Shanghai",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	list := []proto.Session{
		{SessionID: "main-0001", Cwd: convDir, Title: "Persona", UpdatedAt: now.Add(-time.Minute)},
		{SessionID: "lead-0002", Cwd: convDir, Title: "Release Lead", UpdatedAt: now.Add(-2 * time.Minute)},
		{SessionID: "agent-0005", Cwd: "/code/app", Title: "Worker Agent", PaneID: "%1", UpdatedAt: now.Add(-3 * time.Minute)},
	}
	editSessions(n.m, func([]proto.Session) []proto.Session { return list })
	for _, s := range list {
		n.known[s.SessionID] = true
	}
	connectAssistant(t, h)
	eventually(t, 5*time.Second, "main conversation reported", func() bool { return h.isMainOn("server", "main-0001") })
	return h, n
}

func postUserMessage(h *Hub, m *Machine, sid, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/server/"+sid+"/messages", strings.NewReader(body))
	r.SetPathValue("sid", sid)
	w := httptest.NewRecorder()
	h.sendMessage(w, r, m)
	return w
}

// End-to-end POST /api/v1/sessions/{mid}/{sid}/messages flow:
//   - User message to a managed assistant conversation carries queued wake:false relays, drains them on node acceptance,
//     and clears queuedRelays (Queued / QueuedRelayed -> 0) on the session view.
//   - A failed user send preserves the quiet relay; a retry under the same client_msg_id reuses the original reservation
//     without swallowing a quiet relay that arrived in between.
//   - A user message to an ordinary Agent session is completely unaffected.
func TestUserSendDrainsQuietRelayEndToEnd(t *testing.T) {
	h, n := quietRelayHub(t)

	// Another conversation sends a wake:false message to the main conversation.
	out, isErr := tool(t, h, n, "lead-0002", "conversation_send", `{"id":"main-0001","text":"Quiet sync #1.","wake":false}`)
	if isErr || !strings.Contains(out, "without waking it") {
		t.Fatalf("conversation_send wake:false = %q (err=%v)", out, isErr)
	}
	eventually(t, 5*time.Second, "quiet relay shown queued above composer", func() bool {
		s := shown(h, "main-0001")
		return len(s.Queued) == 1 && strings.Contains(s.Queued[0], "Quiet sync #1.")
	})

	// 1. Ordinary Agent session send does not carry or drain main-0001's quiet relays.
	if w := postUserMessage(h, n.m, "agent-0005", `{"text":"Run go test","client_msg_id":"agent-msg-1"}`); w.Code != http.StatusOK {
		t.Fatalf("agent send: %d %s", w.Code, w.Body)
	}
	n.mu.Lock()
	agentSend := n.sends[len(n.sends)-1]
	n.mu.Unlock()
	if agentSend["session_id"] != "agent-0005" || agentSend["text"] != "Run go test" {
		t.Fatalf("agent send altered: %+v", agentSend)
	}
	if s := shown(h, "main-0001"); len(s.Queued) != 1 {
		t.Fatalf("agent send disturbed main quiet relay: queued=%v", s.Queued)
	}

	// 2. Failed send to main-0001 (refused before typing) preserves Quiet sync #1.
	n.mu.Lock()
	n.sendErr = func(map[string]any) error {
		return &rpc.Error{Code: "rejected", Message: "refused", Data: json.RawMessage(`{"delivered":"none"}`)}
	}
	n.mu.Unlock()
	if w := postUserMessage(h, n.m, "main-0001", `{"text":"What is new?","client_msg_id":"user-msg-1"}`); w.Code == http.StatusOK {
		t.Fatalf("expected failure, got %d %s", w.Code, w.Body)
	}
	n.mu.Lock()
	failedSend := n.sends[len(n.sends)-1]
	n.sendErr = nil
	n.mu.Unlock()
	failedText, _ := failedSend["text"].(string)
	if !strings.Contains(failedText, `<persona-context kind="context" title="没有叫醒你的消息" url="">`) ||
		!strings.Contains(failedText, "Quiet sync #1.") ||
		!strings.HasSuffix(failedText, "\nWhat is new?") {
		t.Fatalf("first attempt payload = %q", failedText)
	}
	// Quiet sync #2 arrives while user-msg-1 is about to retry.
	out, isErr = tool(t, h, n, "lead-0002", "conversation_send", `{"id":"main-0001","text":"Quiet sync #2.","wake":false}`)
	if isErr || !strings.Contains(out, "without waking it") {
		t.Fatalf("second conversation_send wake:false = %q (err=%v)", out, isErr)
	}
	eventually(t, 5*time.Second, "both quiet relays waiting after failed send", func() bool {
		s := shown(h, "main-0001")
		return len(s.Queued) == 2
	})

	// 3. Retry with the same client_msg_id reuses Quiet sync #1 only and leaves Quiet sync #2 queued.
	if w := postUserMessage(h, n.m, "main-0001", `{"text":"What is new?","client_msg_id":"user-msg-1"}`); w.Code != http.StatusOK {
		t.Fatalf("retry send: %d %s", w.Code, w.Body)
	}
	n.mu.Lock()
	retrySend := n.sends[len(n.sends)-1]
	n.mu.Unlock()
	retryText, _ := retrySend["text"].(string)
	if retryText != failedText || strings.Contains(retryText, "Quiet sync #2.") {
		t.Fatalf("retry changed payload or swallowed new quiet relay:\nfirst=%q\nretry=%q", failedText, retryText)
	}
	eventually(t, 5*time.Second, "retry drains only Quiet sync #1", func() bool {
		s := shown(h, "main-0001")
		return len(s.Queued) == 1 && strings.Contains(s.Queued[0], "Quiet sync #2.")
	})

	// 4. Next user message with a new client_msg_id carries Quiet sync #2 and clears the queue to zero.
	if w := postUserMessage(h, n.m, "main-0001", `{"text":"And next?","client_msg_id":"user-msg-2"}`); w.Code != http.StatusOK {
		t.Fatalf("second user send: %d %s", w.Code, w.Body)
	}
	n.mu.Lock()
	secondSend := n.sends[len(n.sends)-1]
	n.mu.Unlock()
	secondText, _ := secondSend["text"].(string)
	if !strings.Contains(secondText, "Quiet sync #2.") || strings.Contains(secondText, "Quiet sync #1.") || !strings.HasSuffix(secondText, "\nAnd next?") {
		t.Fatalf("second user send payload = %q", secondText)
	}
	eventually(t, 5*time.Second, "queue strip clears to zero", func() bool {
		s := shown(h, "main-0001")
		return len(s.Queued) == 0
	})
}
