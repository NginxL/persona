package node

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestModelRetryUsesDurableScheduler(t *testing.T) {
	n := &Node{}
	p := &assistantProc{id: "retry-session", launcher: "claude"}
	for _, text := range []string{"503 unavailable", "429 throttled", "connection lost", "408 timeout", "401 upstream 503", "403 timeout", "model_not_found"} {
		if n.recoverAssistantResult(p, true, text) {
			t.Fatalf("node started an immediate recovery for %q", text)
		}
	}
}

func TestModelRetryRestoredReceipt(t *testing.T) {
	n := &Node{}
	id, msg := "session", "model-retry:session:1:1"
	n.restoreEchoes([]echoRecord{{SessionID: id, ClientMsgID: msg, Line: "user-1", SentAt: time.Now(), In: echoInTranscript}})
	sent := 0
	for i := 0; i < 3; i++ {
		if err := n.sendEchoed(id, msg, "Continue", 0, echoAfter{}, func() error { sent++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if sent != 0 {
		t.Fatal("a completed continuation was replayed after receipt restoration")
	}
}

func TestModelRetryRefusesUserStop(t *testing.T) {
	for _, cause := range []proto.StopCause{proto.StopUser, proto.StopExit} {
		t.Run(cause.String(), func(t *testing.T) {
			n := assistantNode(t)
			n.modes = map[string]sessionModes{}
			n.state.put("11111111-2222-3333-4444-555555555555", sessionState{StopCause: cause})
			_, err := n.assistantSend(withClientMsgID(t.Context(), "model-retry:11111111-2222-3333-4444-555555555555:1:1"), "11111111-2222-3333-4444-555555555555", "", "claude", "Continue", nil, "", "", modelExecution{})
			if code, data := refusalOf(t, err); code != agentStopped || data["delivered"] != "none" {
				t.Fatalf("automatic continuation bypassed %s: code=%q data=%v err=%v", cause, code, data, err)
			}
		})
	}
}

func TestModelRetryRPCRefusesUserStop(t *testing.T) {
	for _, cause := range []proto.StopCause{proto.StopUser, proto.StopExit} {
		t.Run(cause.String(), func(t *testing.T) {
			n := assistantNode(t)
			n.modes = map[string]sessionModes{}
			n.state.put("11111111-2222-3333-4444-555555555555", sessionState{StopCause: cause})
			params, err := json.Marshal(map[string]any{"session_id": "11111111-2222-3333-4444-555555555555", "text": "Continue", "client_msg_id": "model-retry:11111111-2222-3333-4444-555555555555:1:1"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = n.handle(t.Context(), "session.send", params)
			if code, data := refusalOf(t, err); code != agentStopped || data["delivered"] != "none" {
				t.Fatalf("RPC continuation bypassed %s: code=%q data=%v err=%v", cause, code, data, err)
			}
		})
	}
}
