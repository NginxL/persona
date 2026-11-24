package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	botSessionA     = "claude_registered_a"
	botSessionB     = "claude_registered_b"
	botSessionStale = "claude_history_without_live_binding"
)

type agentBotFake struct {
	f *orcarttest.Fake
	n *Node

	mu       sync.Mutex
	accepted map[string]string
	writes   int
	unknown  bool
}

func newAgentBotFake(t *testing.T) *agentBotFake {
	t.Helper()
	t.Setenv("ORCA_USER_DATA_PATH", t.TempDir())
	b := &agentBotFake{f: orcarttest.New(t), accepted: map[string]string{}, unknown: true}
	saved := orcaClient
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: b.f.MetaPath} }
	t.Cleanup(func() { orcaClient = saved })
	b.n = &Node{}
	b.f.SetWorktrees(orcarttest.Worktree("workspace", "/workspace", "main",
		orcarttest.Agent("structured-agent-session-"+botSessionA+":a", "claude", "done"),
		orcarttest.Agent("structured-agent-session-"+botSessionB+":b", "codex", "done")))
	b.f.Handle("agentSession.history", func(p map[string]any) any {
		session, _ := p["sessionId"].(string)
		if session != botSessionA && session != botSessionB && session != botSessionStale {
			return orcarttest.Fail{Code: "agent_session_identity_required"}
		}
		items := []any{
			map[string]any{"itemId": session + "-user-1", "revision": 1, "sequence": 10, "observedAt": 1000,
				"body": map[string]any{"kind": "message", "role": "user", "blocks": []any{map[string]any{"type": "text", "text": "First assignment"}}}},
			map[string]any{"itemId": session + "-assistant-1", "revision": 2, "sequence": 11, "sequenceIndex": 1, "observedAt": 2000,
				"body": map[string]any{"kind": "message", "role": "assistant", "blocks": []any{
					map[string]any{"type": "text", "text": "Reply from " + session},
					map[string]any{"type": "tool-call", "name": "ExampleTool", "input": map[string]any{"value": "safe"}},
				}}},
		}
		cursor := map[string]any{"epoch": "epoch-1", "sequence": 10}
		return map[string]any{"ok": true, "page": map[string]any{
			"sessionId": session, "epoch": "epoch-1", "fence": 7, "working": false, "direction": p["direction"],
			"items": items, "hasOlder": true, "hasNewer": false, "window": map[string]any{"nextCursor": cursor},
		}, "providerSession": map[string]any{"id": "provider-" + session}}
	})
	b.f.Handle("agentSession.send", func(p map[string]any) any {
		b.mu.Lock()
		defer b.mu.Unlock()
		envelope, _ := p["envelope"].(map[string]any)
		op, _ := envelope["clientOperationId"].(string)
		fingerprint, _ := envelope["payloadFingerprint"].(string)
		session, _ := envelope["sessionId"].(string)
		body, _ := p["body"].(map[string]any)
		fields := map[string]any{"body": body, "delivery": p["delivery"]}
		want, err := orcart.Fingerprint("agentSession.send", session, fields)
		if err != nil || fingerprint != want {
			return orcarttest.Fail{Code: "invalid_argument"}
		}
		if prior, ok := b.accepted[op]; ok {
			if prior != fingerprint {
				return map[string]any{"ok": false, "refusal": map[string]any{"code": "agent_session_payload_mismatch", "message": "raw payload"}}
			}
			return map[string]any{"ok": true, "replayed": true, "value": map[string]any{}}
		}
		b.accepted[op] = fingerprint
		b.writes++
		if b.unknown {
			b.unknown = false
			return orcarttest.Fail{Code: "agent_session_operation_unknown"}
		}
		return map[string]any{"ok": true, "replayed": false, "value": map[string]any{}}
	})
	return b
}

func agentBotCall(t *testing.T, n *Node, method string, params, out any) error {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := n.handleMethod(context.Background(), method, raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	body, _ := json.Marshal(res)
	return json.Unmarshal(body, out)
}

func agentBotErrorCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestAgentBotHistoryMapsExactSessionAndRealReplies(t *testing.T) {
	b := newAgentBotFake(t)
	var page agents.BotPage
	if err := agentBotCall(t, b.n, "agentbot.history", agents.BotHistoryRequest{SessionID: botSessionB, Limit: 40}, &page); err != nil {
		t.Fatal(err)
	}
	if !page.CanSend || page.RankTimeMs != 2000 || len(page.Messages) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page %+v", page)
	}
	reply := page.Messages[1]
	if reply.ExternalID != botSessionB+"-assistant-1" || reply.Role != "assistant" || reply.CreateTimeMs != 2000 || reply.Position != 11 ||
		len(reply.Blocks) != 2 || reply.Blocks[0].Text != "Reply from "+botSessionB || reply.Blocks[1].Name != "ExampleTool" {
		t.Fatalf("real history reply %+v", reply)
	}
	for _, params := range b.f.Params("agentSession.history") {
		if params["sessionId"] != botSessionB {
			t.Fatalf("history fell back to another session: %#v", params)
		}
	}
	if got := b.f.Invalid(); len(got) != 0 {
		t.Fatalf("schema rejected requests: %v", got)
	}
}

func TestAgentBotSendExactRoutingReplayAndPayloadConflict(t *testing.T) {
	b := newAgentBotFake(t)
	req := agents.BotSendRequest{SessionID: botSessionB, OperationID: "1793850000000-0123456789abcdef0123456789abcdef", Text: "Use the second bot"}
	if err := agentBotCall(t, b.n, "agentbot.send", req, nil); err == nil || agentBotErrorCode(err) != "unknown" {
		t.Fatalf("first unknown outcome %v", err)
	}
	if b.writes != 1 || b.f.Count("agentSession.send") != 1 {
		t.Fatalf("unknown outcome auto-reposted: writes=%d calls=%d", b.writes, b.f.Count("agentSession.send"))
	}
	// The explicit retry is the same operation and replays without another accepted write.
	if err := agentBotCall(t, b.n, "agentbot.send", req, nil); err != nil {
		t.Fatal(err)
	}
	if b.writes != 1 || b.f.Count("agentSession.send") != 2 {
		t.Fatalf("same operation did not replay: writes=%d calls=%d", b.writes, b.f.Count("agentSession.send"))
	}
	params := b.f.Params("agentSession.send")
	for _, p := range params {
		envelope, _ := p["envelope"].(map[string]any)
		if envelope["sessionId"] != botSessionB || envelope["clientOperationId"] != req.OperationID {
			t.Fatalf("write escaped exact binding: %#v", p)
		}
	}
	changed := req
	changed.Text = "Same operation, different payload"
	if err := agentBotCall(t, b.n, "agentbot.send", changed, nil); err == nil || agentBotErrorCode(err) != "conflict" {
		t.Fatalf("payload mismatch %v", err)
	}
	if b.writes != 1 || b.f.Count("agentSession.send") != 3 {
		t.Fatalf("payload conflict became another write: writes=%d calls=%d", b.writes, b.f.Count("agentSession.send"))
	}
}

func TestAgentBotUnknownAndStaleBindingsNeverFallBack(t *testing.T) {
	b := newAgentBotFake(t)
	for _, session := range []string{"claude_unknown", botSessionStale} {
		before := b.f.Count("agentSession.send")
		err := agentBotCall(t, b.n, "agentbot.send", agents.BotSendRequest{
			SessionID: session, OperationID: "1793850000000-0123456789abcdef0123456789abcdef", Text: "must not send",
		}, nil)
		if err == nil || agentBotErrorCode(err) != "not_found" {
			t.Fatalf("%s: %v", session, err)
		}
		if b.f.Count("agentSession.send") != before {
			t.Fatalf("%s reached a fallback write", session)
		}
	}
}

func TestAgentBotDuplicateBindingFailsWithoutWrite(t *testing.T) {
	b := newAgentBotFake(t)
	b.f.SetWorktrees(orcarttest.Worktree("workspace", "/workspace", "main",
		orcarttest.Agent("structured-agent-session-"+botSessionB+":one", "claude", "done"),
		orcarttest.Agent("structured-agent-session-"+botSessionB+":two", "codex", "done")))
	before := b.f.Count("agentSession.send")
	err := agentBotCall(t, b.n, "agentbot.send", agents.BotSendRequest{
		SessionID: botSessionB, OperationID: "1793850000000-0123456789abcdef0123456789abcdef", Text: "must not send",
	}, nil)
	if err == nil || agentBotErrorCode(err) != "conflict" {
		t.Fatalf("ambiguous binding: %v", err)
	}
	if b.f.Count("agentSession.send") != before {
		t.Fatal("ambiguous binding reached a write")
	}
}

func TestAgentBotErrorsNeverExposeRuntimeSecretsOrPayloads(t *testing.T) {
	b := newAgentBotFake(t)
	err := agentBotCall(t, b.n, "agentbot.send", agents.BotSendRequest{
		SessionID: botSessionB, OperationID: "1793850000000-0123456789abcdef0123456789abcdef", Text: "sensitive assignment",
	}, nil)
	if err == nil {
		t.Fatal("expected unknown outcome")
	}
	for _, forbidden := range []string{b.f.Socket, b.f.Token(), "sensitive assignment", "payloadFingerprint", "authToken"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}
