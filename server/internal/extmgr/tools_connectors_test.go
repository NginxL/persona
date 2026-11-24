package extmgr

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// connectorTool is what the manager answers the hub's mcp.call for one of its tools (hubConnectorTool):
// handled false when it is none of the manager's (the hub then answers unknown tool).
func connectorTool(t *testing.T, h *Manager, name, args string, assistant bool) (proto.ToolResult, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args), "assistant": assistant, "machine_id": "box"})
	out, err := testHubCall(context.Background(), h, hubConnectorTool, raw)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	res, _ := m["result"].(proto.ToolResult)
	return res, m["handled"] == true
}

// The coding agents get mail's tools and not Feishu's (nor can they call them); a mail_send that could not be sent
// is refused before the user is asked.
func TestMailToolsThroughItsConnector(t *testing.T) {
	h := newTestManager(t)
	h.mail = testMail(t)
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	var agent []string
	for _, tl := range toolsForAgents(t, h, false) {
		agent = append(agent, tl.Name)
	}
	if !slices.Contains(agent, "mail_send") || !slices.Contains(agent, "mail_read") || slices.Contains(agent, "lark_chats") {
		t.Fatalf("agent tools %v", agent)
	}
	if _, handled := connectorTool(t, h, "lark_chats", `{}`, false); handled {
		t.Fatal("an agent called lark_chats")
	}
	if r, handled := connectorTool(t, h, "mail_send", `{"to":"not a list"}`, false); !handled || !r.IsError || !strings.Contains(r.Content[0].Text, "invalid arguments") {
		t.Fatalf("invalid mail_send: %v %+v", handled, r)
	}
	if n := len(hubOf(h).asked()); n != 0 {
		t.Fatalf("the user was asked about a mail that could not be sent (%d cards)", n)
	}
}

// mail_send through the mail connector asks with the card it always had (tool, summary, arguments), and a declined
// mail is not sent.
func TestMailSendAsksWithTheSameCard(t *testing.T) {
	h := newTestManager(t)
	m := newReplyMail(t)
	h.mail = m
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	hubOf(h).answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: new(false)} }
	args := json.RawMessage(`{"account_id":"acc1","to":[{"email":"someone@example.com"}],"subject":"季度汇报","text":"附件已更新"}`)
	r := h.callConnector(context.Background(), asker{machine: "box"}, "mail_send", args)
	ix := hubOf(h).asked()
	if len(ix) != 1 || ix[0].ToolName != "mail_send" || ix[0].Summary != "Send mail to someone@example.com: 季度汇报" || string(ix[0].ToolInput) != string(args) || ix[0].MachineID != "box" {
		t.Fatalf("card %+v", ix)
	}
	if !r.IsError || len(m.sent) != 0 {
		t.Fatalf("declined but sent: %+v %v", r, m.sent)
	}
}

// A connector's write the assistant asks for is a card of its conversation, as a hook's is: the clients show a card in
// the chat of its session, and one with none only in "Needs you" (#2558). The user's own in a connector's thread has
// none. (The hub hands the agent's mcp.call here as extensions.hub_call connector_tool.)
func TestConnectorCardsBelongToTheCallingConversation(t *testing.T) {
	h := newTestManager(t)
	h.mail = newReplyMail(t)
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	deny := false
	hubOf(h).answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &deny} }
	args := json.RawMessage(`{"account_id":"acc1","to":[{"email":"someone@example.com"}],"subject":"s","text":"t"}`)
	params, _ := json.Marshal(map[string]any{"name": "mail_send", "arguments": args, "assistant": true, "machine_id": "box", "session_id": "conv1"})
	if _, err := testHubCall(context.Background(), h, hubConnectorTool, params); err != nil {
		t.Fatal(err)
	}
	ix := hubOf(h).asked()
	if len(ix) != 1 || ix[0].MachineID != "box" || ix[0].SessionID != "conv1" {
		t.Fatalf("card %+v, want one of box/conv1", ix)
	}
}

// The caller of a connector's write going away (the assistant's turn stopped: the node cancels the call, #2597)
// withdraws its card: allowing it afterwards must not run the write.
func TestAConnectorCardEndsWithItsCall(t *testing.T) {
	h := newTestManager(t)
	m := newReplyMail(t)
	h.mail = m
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	held := make(chan struct{})
	allow := true
	hubOf(h).answer = func(proto.Interaction) proto.Resolution { <-held; return proto.Resolution{Allow: &allow} }
	defer close(held)
	args := json.RawMessage(`{"account_id":"acc1","to":[{"email":"someone@example.com"}],"subject":"s","text":"t"}`)
	params, _ := json.Marshal(map[string]any{"name": "mail_send", "arguments": args, "assistant": true, "machine_id": "box", "session_id": "conv1"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan any, 1)
	go func() {
		out, _ := testHubCall(ctx, h, hubConnectorTool, params)
		done <- out
	}()
	for i := 0; i < 500 && len(hubOf(h).asked()) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if len(hubOf(h).asked()) != 1 {
		t.Fatalf("cards %+v", hubOf(h).asked())
	}
	cancel()
	select {
	case out := <-done:
		b, _ := json.Marshal(out)
		var r struct {
			Result proto.ToolResult `json:"result"`
		}
		_ = json.Unmarshal(b, &r)
		if !r.Result.IsError {
			t.Fatalf("a call nobody waits for went through: %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end with its caller")
	}
	hubOf(h).mu.Lock()
	withdrawn := len(hubOf(h).withdrawn)
	hubOf(h).mu.Unlock()
	if withdrawn != 1 {
		t.Fatal("the card stayed")
	}
	time.Sleep(50 * time.Millisecond)
	if len(m.sent) != 0 {
		t.Fatalf("sent after its caller went away: %v", m.sent)
	}
}
