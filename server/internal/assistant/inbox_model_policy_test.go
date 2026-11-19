package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// policyExtensions is an extension registry whose read/write declarations and calls can be observed without any
// connector running. Embedding keeps this test double small while ToolAccess and CallTool are the policy boundary.
type policyExtensions struct {
	ExtensionTools
	read  map[string]bool
	calls []string
}

func (e *policyExtensions) ToolAccess(name string) (bool, bool) {
	read, known := e.read[name]
	return known, known && read
}

func (e *policyExtensions) CallTool(_ context.Context, _, _, name string, _ json.RawMessage, _ bool) (proto.ToolResult, bool) {
	if _, known := e.read[name]; !known {
		return proto.ToolResult{}, false
	}
	e.calls = append(e.calls, name)
	return proto.TextResult("read "+name, false), true
}

func inboxTool(t *testing.T, h *inboxHub, session, name, args string) proto.ToolResult {
	t.Helper()
	if args == "" {
		args = `{}`
	}
	return h.Tool(t.Context(), ToolCall{MachineID: "server", SessionID: session, Name: name, Arguments: json.RawMessage(args)})
}

func requireInboxRefusal(t *testing.T, res proto.ToolResult, name string) {
	t.Helper()
	text := toolText(res)
	if !res.IsError || !strings.Contains(text, "Use inbox_suggest") && !strings.Contains(text, "use inbox_suggest") {
		t.Fatalf("%s was not refused with an actionable suggestion path: %+v", name, res)
	}
}

// The model-facing entry points are default-deny for Inbox handling chats. Connector actions are classified by the
// extension's declared interface, not by a list of send-shaped names, so a future action cannot slip through.
func TestInboxModelOutboundToolsDenied(t *testing.T) {
	h, _, _ := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{
		"mail_send": false, "github_comment": false, "github_review": false,
		"lark_agent_reply": false, "example_future_write": false, "computer_run": false,
	}}
	h.SetExtensions(ext)
	h.originSet(t.Context(), "server", "s1", OriginInbox, "lark:f1:100", "")

	own := []string{
		"notify_user", "ask_user", "agent_start", "agent_send", "conversation_send",
		"conversation_new", "task_plan", "schedule_followup", "urgent_call",
	}
	for _, name := range own {
		t.Run(name, func(t *testing.T) {
			requireInboxRefusal(t, inboxTool(t, h, "s1", name, `{}`), name)
		})
	}
	extensionWrites := []string{
		"mail_send", "github_comment", "github_review", "lark_agent_reply",
		"example_future_write", "computer_run", "subscription_run", "future_unknown_tool",
	}
	for _, name := range extensionWrites {
		t.Run(name, func(t *testing.T) {
			res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
			requireInboxRefusal(t, res, name)
		})
	}
	for _, name := range []string{"phone_notify", "phone_messages_compose", "phone_clipboard_set", "phone_shortcut_run"} {
		t.Run(name, func(t *testing.T) {
			requireInboxRefusal(t, inboxTool(t, h, "s1", name, `{}`), name)
		})
	}
	if len(ext.calls) != 0 {
		t.Fatalf("refused extension calls executed: %v", ext.calls)
	}
	h.f.mu.Lock()
	pushes, starts, agentSends, conversations := len(h.f.pushes), len(h.f.starts), len(h.f.agentSent), len(h.f.sends)
	h.f.mu.Unlock()
	if pushes != 0 || starts != 0 || agentSends != 0 || conversations != 0 {
		t.Fatalf("a refused call had a side effect: pushes=%d starts=%d agent sends=%d conversations=%d", pushes, starts, agentSends, conversations)
	}
}

// Read-only connector investigation, knowledge-base reads and suggestion submission remain available at the model
// entry point. The suggestion itself is stored; no reply is sent before the owner's action.
func TestInboxModelReadOnlyInvestigationAndSuggestion(t *testing.T) {
	h, lark, it := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"lark_chats": true, "mail_search": true, "subscription_list": true}}
	h.SetExtensions(ext)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")

	for _, name := range []string{"lark_chats", "mail_search", "subscription_list"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
		if res.IsError || toolText(res) != "read "+name {
			t.Fatalf("read-only %s: %+v", name, res)
		}
	}
	h.kb.dir = t.TempDir()
	t.Cleanup(h.kbEnd)
	h.f.hello.KB = &KBSettings{ReadOnly: true}
	if res := inboxTool(t, h, "s1", "kb_search", `{"query":"project context"}`); res.IsError {
		t.Fatalf("KB read through the model entry point: %+v", res)
	}
	h.f.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
		return proto.TextResult(`{"events":[]}`, false), nil
	})
	if res := h.mcpCallWithOrigin(t.Context(), "server", "s1", "phone_calendar_events", json.RawMessage(`{}`), true, OriginInbox); res.IsError {
		t.Fatalf("phone read through the model entry point: %+v", res)
	}
	if calls := h.f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall); len(calls) != 1 {
		t.Fatalf("phone read executed %d times, want 1", len(calls))
	}
	if _, refused := h.inboxModelRefusal(t.Context(), "server", "s1", OriginInbox, "agents_list"); refused {
		t.Fatal("read-only Agent investigation was refused")
	}
	turn := inboxTestProcessingTurn(h, "s1")
	args := fmt.Sprintf(`{"processing_turn":%q,"kind":"reply","text":"Suggested reply","recap":"Waiting for owner confirmation"}`, turn)
	if res := inboxTool(t, h, "s1", "inbox_suggest", args); res.IsError {
		t.Fatalf("inbox_suggest: %+v", res)
	}
	h.inbox.mu.Lock()
	suggestion := it.Suggestion
	h.inbox.mu.Unlock()
	if suggestion == nil || suggestion.Text != "Suggested reply" || len(lark.got) != 0 {
		t.Fatalf("suggestion=%+v, outbound replies=%v", suggestion, lark.got)
	}
}

// A native child Agent shares its parent's assistant process and MCP identity. The manager therefore sees the same
// Inbox origin for the child's calls too; this is deliberately a separate acceptance case from the parent table.
func TestInboxNativeChildAgentCannotSend(t *testing.T) {
	h, _, _ := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"mail_send": false}}
	h.SetExtensions(ext)
	for _, name := range []string{"agent_send", "conversation_send", "mail_send"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
		requireInboxRefusal(t, res, name)
	}
	if len(ext.calls) != 0 {
		t.Fatalf("a child Agent executed connector writes: %v", ext.calls)
	}
}

// The initial node request attests origin before startOn can add the session to the manager's origin book. No prior
// originSet is performed here: an early tool call must still be denied.
func TestInboxOriginHintDeniesBeforeOriginBook(t *testing.T) {
	h := agentHub(t)
	h.f.setSessions("server", proto.Session{SessionID: "early-session", Status: proto.StatusWorking})
	for _, name := range []string{"notify_user", "agent_start", "conversation_send"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "early-session", name, json.RawMessage(`{}`), true, OriginInbox)
		requireInboxRefusal(t, res, name)
	}
}

// Main/user, scheduled, spawned and task-plan conversations retain the tools the Inbox policy removes. Each tool is
// actually executed against the fake hub; this is not merely a negative assertion on the Inbox refusal helper.
func TestOrdinaryOriginsKeepNotifyDelegationAndConversationSend(t *testing.T) {
	origins := []struct{ name, origin string }{
		{"main or user", OriginUser},
		{"schedule or channel", OriginChannel},
		{"spawned conversation", OriginSpawned},
		{"task plan", OriginTask},
	}
	for _, origin := range origins {
		t.Run(origin.name, func(t *testing.T) {
			h := agentHub(t)
			const caller, target = "caller", "target"
			h.f.setSessions("server",
				proto.Session{SessionID: caller, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"},
				proto.Session{SessionID: target, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
			h.originSet(t.Context(), "server", caller, origin.origin, "", "")
			if res := inboxTool(t, h, caller, "notify_user", `{"title":"Update","body":"Read-only work is done"}`); res.IsError {
				t.Fatalf("notify_user: %+v", res)
			}
			if res := inboxTool(t, h, caller, "agent_start", `{"machine":"server","cwd":"/work","prompt":"Investigate"}`); res.IsError {
				t.Fatalf("agent_start: %+v", res)
			}
			if res := inboxTool(t, h, caller, "conversation_send", `{"id":"target","text":"Investigation result"}`); res.IsError {
				t.Fatalf("conversation_send: %+v", res)
			}
			h.f.mu.Lock()
			pushes, starts := len(h.f.pushes), len(h.f.starts)
			h.f.mu.Unlock()
			if pushes != 1 || starts != 1 {
				t.Fatalf("ordinary origin %q: pushes=%d starts=%d", origin.origin, pushes, starts)
			}
		})
	}
}

// Inbox starts use non-bypass default even if the caller asks for bypass, and the initial node request carries origin.
// Unlike Plan, default lets the node's explicit Persona MCP allowlist run investigation and inbox_suggest unasked.
// Ordinary starts retain their selected mode.
func TestInboxStartForcesDefaultAndAttestsOrigin(t *testing.T) {
	h := agentHub(t)
	sess, err := h.Start(t.Context(), StartParams{Text: "Investigate", Origin: OriginInbox, PermissionMode: proto.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	inboxStart := h.f.sends[len(h.f.sends)-1]
	h.f.mu.Unlock()
	if inboxStart["permission_mode"] != "default" || inboxStart["origin"] != OriginInbox || sess.PermissionMode != "default" {
		t.Fatalf("Inbox start params=%v session mode=%q", inboxStart, sess.PermissionMode)
	}
	sess, err = h.Start(t.Context(), StartParams{Text: "Ordinary", Origin: OriginUser, PermissionMode: proto.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	ordinaryStart := h.f.sends[len(h.f.sends)-1]
	h.f.mu.Unlock()
	if ordinaryStart["permission_mode"] != proto.ModeBypass || ordinaryStart["origin"] != OriginUser || sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("ordinary start params=%v session mode=%q", ordinaryStart, sess.PermissionMode)
	}
}

// Later sends re-attest the manager's persisted origin. This closes the node-restart window: its in-memory Origin
// field is not a database column, so the first resumed process must receive Inbox from the manager again. Caller input
// cannot substitute another origin.
func TestInboxLaterSendReattestsManagerOrigin(t *testing.T) {
	h := agentHub(t)
	const session = "inbox-after-node-restart"
	h.f.setSessions("server", proto.Session{SessionID: session, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
	h.originSet(t.Context(), "server", session, OriginInbox, "lark:real-item:1", "")
	params, err := h.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: session,
		Params: map[string]any{"text": "Continue", "origin": OriginUser, "permission_mode": proto.ModeBypass}})
	if err != nil {
		t.Fatal(err)
	}
	if params["origin"] != OriginInbox {
		t.Fatalf("later send origin=%v, want the manager's Inbox attestation", params["origin"])
	}
}

// The real continuation path is deliverNotes -> sendToSession -> session.send, not assistant.send's SendParams.
// Re-attest there too: the manager's origin book, not a caller-supplied field or the node's pre-reload memory, is authoritative.
func TestInboxDeliverNotesReattestsSessionSendOrigin(t *testing.T) {
	h, _, it := handledHub(t)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteNewMessages, Text: "A new message"}, true, time.Now())
	h.inbox.mu.Unlock()
	h.deliverNotes(t.Context(), it)
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if len(h.f.agentSent) != 1 {
		t.Fatalf("deliverNotes made %d session.send calls, want 1", len(h.f.agentSent))
	}
	if got := h.f.agentSent[0]["origin"]; got != OriginInbox {
		t.Fatalf("session.send origin=%v, want the manager's Inbox attestation", got)
	}
}

// Owner-confirmed reply execution is separate from the model-call guard even while the handling conversation is Inbox.
func TestInboxOwnerConfirmedReplyRuns(t *testing.T) {
	h, lark, it := handledHub(t)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Owner-approved reply"}`); err != nil {
		t.Fatal(err)
	}
	code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code != 200 || len(lark.got) != 1 || lark.got[0].Text != "Owner-approved reply" {
		t.Fatalf("confirmReply code=%d sent=%v", code, lark.got)
	}
}

// confirmAgent reaches runTool(agent_start), not Tool, so the model-call policy cannot intercept this owner action.
func TestInboxOwnerConfirmedAgentRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := agentHub(t)
		h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
		it := disabledTwinItem(h, "item", HandlingDone, AgentSkipped, "parent-session")
		h.originSet(t.Context(), "server", "parent-session", OriginInbox, it.ID, "")
		it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestAgent, Text: "Inspect the request", At: time.Now(), MachineID: "server", Cwd: "/work"}
		if _, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		h.f.mu.Lock()
		starts := len(h.f.starts)
		h.f.mu.Unlock()
		if starts != 1 {
			t.Fatalf("confirmAgent started %d Agents, want 1", starts)
		}
	})
}

// confirmTodo is likewise an internal owner-confirmed execution path, not a model call on the handling conversation.
func TestInboxOwnerConfirmedTodoRuns(t *testing.T) {
	h := muteHub(t)
	mergeItems(h)
	it := h.inbox.items["b"]
	it.Agent = &InboxAgent{State: AgentDrafted, MachineID: "server", SessionID: "s1"}
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestTodo, Text: "Send Ana the numbers", At: time.Now()}
	out, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true)
	if err != nil || out.DoneBy != doneByTodo {
		t.Fatalf("confirmTodo: %v %+v", err, out)
	}
	last := out.Chain[len(out.Chain)-1]
	if last.Link == nil {
		t.Fatal("confirmTodo did not link the new to-do")
	}
	if todo, ok := h.todoNow(last.Link.ID); !ok || todo.Title != "Send Ana the numbers" {
		t.Fatalf("confirmTodo todo=%+v present=%v", todo, ok)
	}
}
