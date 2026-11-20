package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The hub's notice of how an app's message to an assistant conversation went names the message by its client_msg_id,
// delivered or not, so its manager settles that message only (#11392); a message sent without one is named by none.
func TestAssistantSentNamesTheMessage(t *testing.T) {
	var sends atomic.Int32
	h := newTestHub(t)
	h.store = newSentStore()
	pipeNode(t, h, "box", func(context.Context) error {
		if sends.Add(1) == 2 { // the second message: refused before anything was typed
			return &rpc.Error{Code: "busy", Message: "busy", Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		return nil
	})
	m := h.byID["box"]
	editSessions(m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "s1", Assistant: true, Status: proto.StatusIdle}}
	})
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
	if err != nil {
		t.Fatal(err)
	}
	g := connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, token, func(method string, raw json.RawMessage) (any, error) {
		if method == assistantwire.MethodSendParams {
			var p assistantwire.SendParams
			_ = json.Unmarshal(raw, &p)
			return map[string]any{"params": p.Params}, nil
		}
		return map[string]any{}, nil
	})
	notices := make(chan assistantwire.ConversationSent, 8)
	g.mu.Lock()
	g.onEvent = func(method string, params json.RawMessage) {
		if method == assistantwire.EventConversationSent {
			var r assistantwire.ConversationSent
			_ = json.Unmarshal(params, &r)
			notices <- r
		}
	}
	g.mu.Unlock()
	next := func() assistantwire.ConversationSent {
		t.Helper()
		select {
		case r := <-notices:
			return r
		case <-time.After(30 * time.Second):
			t.Fatal("no session.assistant_sent")
			return assistantwire.ConversationSent{}
		}
	}
	for _, c := range []struct{ body, id, outcome string }{
		{`{"text":"first","client_msg_id":"named-1"}`, "named-1", assistantwire.ConversationDelivered},
		{`{"text":"second","client_msg_id":"named-2"}`, "named-2", assistantwire.ConversationFailed},
		{`{"text":"third"}`, "", assistantwire.ConversationDelivered},
	} {
		sendAs(h, m, c.body)
		if r := next(); r.ClientMsgID != c.id || r.Outcome != c.outcome || r.MachineID != "box" || r.SessionID != "s1" {
			t.Fatalf("%s: notice %+v", c.body, r)
		}
	}
}
