package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// imessageNode is a Mac's node over the real RPC pipe, answering connector.* from answer (method → result or error).
func imessageNode(t *testing.T, h *Manager, answer func(method string, params json.RawMessage) (any, error)) (*Machine, *[]string) {
	t.Helper()
	builtin := fakeNodesBuiltin
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	var mu sync.Mutex
	calls := &[]string{}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		method, params = fakeBuiltinRoute(t, builtin, method, params) // either way the hub asks, the same answers (#1830 step 6)
		mu.Lock()
		*calls = append(*calls, method)
		mu.Unlock()
		return answer(method, params)
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: "mac", Name: "示例 Mac", Kind: "ssh"})
	m.peer = hubSide
	m.info = proto.NodeInfo{OS: "darwin", BuiltinPrograms: fakeBuiltinPrograms(builtin)}
	h.byID["mac"] = m
	h.machines = append(h.machines, m)
	return m, calls
}

const imessageChatsPage = `{"chats":[{"id":"imessage-0123456789ab","name":"张三","members":[{"handle":"+15550100","name":"张三"}],
"services":["SMS","iMessage"],"last":"2026-10-30T08:00:00Z","preview":"示例消息","unread":1}],"more":true,"before":"1.imessage-0123456789ab"}`

// The app reads a Mac's Messages through the hub: status with its steps, conversations, messages; the hub passes
// the pages along, refuses what is not a conversation id, and turns the node's failures into codes the app acts on.
func TestIMessageThroughTheHub(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	var logs bytes.Buffer
	var logMu sync.Mutex
	h.logger = log.New(&logs, "", 0)
	h.opts.Logf = func(format string, args ...any) { // the manager's log (the hub's request log has no queries either)
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	}
	mux := h.routes()

	// Not set up: 404, and no connector in the list.
	if code, out := call(t, mux, "GET", "/api/v1/imessage", h.cfg.Token, ""); code != 404 || out["code"] != "not_configured" {
		t.Fatalf("not configured: %d %v", code, out)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/imessage/chats", h.cfg.Token, ""); code != 404 {
		t.Fatalf("chats not configured: %d", code)
	}

	permission := false
	_, calls := imessageNode(t, h, func(method string, params json.RawMessage) (any, error) {
		switch {
		case method == "connector.list" && permission:
			return json.RawMessage(`{"connectors":[{"id":"imessage","status":"ok","via":"ssh"}]}`), nil
		case method == "connector.list":
			return json.RawMessage(`{"connectors":[{"id":"imessage","status":"no_permission","error":"macOS 不让 Persona 读","guide":["一","二","三"],"via":"ssh"}]}`), nil
		case !permission:
			return nil, &rpc.Error{Code: proto.CodeNoPermission, Message: "denied"}
		case method == "connector.chats":
			var p struct {
				Limit  int    `json:"limit"`
				Before string `json:"before"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Limit != 20 || p.Before != "9.imessage-0123456789ab" {
				return nil, fmt.Errorf("params %s", params)
			}
			return json.RawMessage(imessageChatsPage), nil
		case method == "connector.older":
			return nil, &rpc.Error{Code: proto.CodeNoChat, Message: "no such conversation"}
		}
		return nil, fmt.Errorf("unexpected %s", method)
	})
	h.cfg.IMessage = &imessagewire.Config{Machine: "mac"}
	if err := h.addIMessage(*h.cfg.IMessage); err != nil {
		t.Fatal(err)
	}
	h.imessage().Check(context.Background())

	// No Full Disk Access yet: the status has the steps, a read is 403.
	code, out := call(t, mux, "GET", "/api/v1/imessage", h.cfg.Token, "")
	if code != 200 || out["status"] != "no_permission" || len(out["guide"].([]any)) != 3 || out["machine"] != "示例 Mac" || out["via"] != "ssh" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/imessage/chats?limit=20", h.cfg.Token, ""); code != 403 || out["code"] != proto.CodeNoPermission ||
		!strings.Contains(out["error"].(string), "完全磁盘访问权限") {
		t.Fatalf("chats without permission: %d %v", code, out)
	}

	permission = true
	h.imessage().Check(context.Background())
	_, out = call(t, mux, "GET", "/api/v1/connectors", h.cfg.Token, "")
	list := out["connectors"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "imessage" || list[0].(map[string]any)["status"] != "ok" ||
		list[0].(map[string]any)["account"] != "示例 Mac" || list[0].(map[string]any)["name"] != "信息" {
		t.Fatalf("connectors: %v", out)
	}
	if caps := list[0].(map[string]any)["capabilities"].([]any); fmt.Sprint(caps) != "[status tools catalog events inbox archive]" {
		t.Errorf("capabilities %v", caps)
	}
	if n := len(h.connectors.Tools(context.Background())); n != 3 {
		t.Errorf("%d tools with 交给 Persona on", n)
	}
	h.imessage().SetAI(false)
	if n := len(h.connectors.Tools(context.Background())); n != 0 {
		t.Errorf("%d tools with 交给 Persona off", n)
	}
	h.imessage().SetAI(true)

	code, out = call(t, mux, "GET", "/api/v1/imessage/chats?limit=20&before=9.imessage-0123456789ab", h.cfg.Token, "")
	chats, _ := out["chats"].([]any)
	if code != 200 || len(chats) != 1 || chats[0].(map[string]any)["name"] != "张三" || out["before"] != "1.imessage-0123456789ab" {
		t.Fatalf("chats: %d %v", code, out)
	}
	n := len(*calls)
	for path, want := range map[string]int{
		"/api/v1/imessage/chats/imessage-0123456789ab/messages": 404, // the Mac has no such conversation
		"/api/v1/imessage/chats/%2B15550100/messages":           404, // not an id: never sent to the Mac
	} {
		if code, out := call(t, mux, "GET", path, h.cfg.Token, ""); code != want || out["code"] != proto.CodeNoChat {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
	if len(*calls) != n+1 {
		t.Errorf("%d calls for two reads, one of them not an id", len(*calls)-n)
	}

	// Nothing about the messages in the log: the request log has paths (hashed ids) but no queries, and the only
	// number in it is the one this test sent in place of an id.
	logMu.Lock()
	logged := logs.String()
	logMu.Unlock()
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "张三") || strings.Contains(line, "示例消息") || strings.Contains(line, "5550100") && !strings.Contains(line, "/chats/+15550100/") {
			t.Errorf("logged: %s", line)
		}
	}
}

// An old node, and a Mac that is not connected.
func TestIMessageOldOrOfflineMac(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	mux := h.routes()
	m, _ := imessageNode(t, h, func(method string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Message: "unknown method " + method}
	})
	if err := h.addIMessage(imessagewire.Config{Machine: "mac"}); err != nil {
		t.Fatal(err)
	}
	h.imessage().Check(context.Background())
	if _, out := call(t, mux, "GET", "/api/v1/imessage", h.cfg.Token, ""); out["status"] != "error" || !strings.Contains(out["error"].(string), "太旧") {
		t.Fatalf("old node: %v", out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/imessage/chats", h.cfg.Token, ""); code != 409 || out["code"] != proto.CodeNodeOutdated {
		t.Fatalf("old node chats: %d %v", code, out)
	}
	m.mu.Lock()
	m.peer = nil
	m.mu.Unlock()
	if code, out := call(t, mux, "GET", "/api/v1/imessage/chats", h.cfg.Token, ""); code != 503 || !strings.Contains(out["error"].(string), "不在线") {
		t.Fatalf("offline: %d %v", code, out)
	}
}

// "Hand to Persona": on unless the user turned it off; the choice is kept and applied at once.
func TestIMessageAISwitch(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.store = newMemStore() // the choice is kept
	h.connectors = connector.NewRegistry(nil)
	mux := h.routes()
	imessageNode(t, h, func(method string, _ json.RawMessage) (any, error) {
		return json.RawMessage(`{"connectors":[{"id":"imessage","status":"ok"}]}`), nil
	})
	if err := h.addIMessage(imessagewire.Config{Machine: "mac"}); err != nil {
		t.Fatal(err)
	}
	if _, out := call(t, mux, "GET", "/api/v1/imessage", h.cfg.Token, ""); out["ai"] != true {
		t.Fatalf("default: %v", out)
	}
	if code, out := call(t, mux, "PUT", "/api/v1/imessage/settings", h.cfg.Token, `{"ai":false}`); code != 200 || out["ai"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	if h.imessage().AI() || len(h.connectors.Tools(context.Background())) != 0 {
		t.Error("still on")
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/imessage/settings", h.cfg.Token, `{}`); code != 400 {
		t.Errorf("no ai: %d", code)
	}
	// A restart reads the choice back.
	h.connectors = connector.NewRegistry(nil)
	if err := h.addIMessage(imessagewire.Config{Machine: "mac"}); err != nil {
		t.Fatal(err)
	}
	if h.imessage().AI() {
		t.Error("on again after a restart")
	}
	if code, out := call(t, mux, "PUT", "/api/v1/imessage/settings", h.cfg.Token, `{"ai":true}`); code != 200 || out["ai"] != true || !h.imessage().AI() {
		t.Errorf("on: %d %v", code, out)
	}
}

// The Mac's new messages go to the knowledge base (the assistant's, in this process: it reads them with app.call
// connectors.archives / connectors.changes) as chat imessage-<the conversation's hash>, senders as the hash of their
// number: no number in an id; the cursor moves on.
func TestIMessageArchive(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	imessageNode(t, h, func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "connector.list":
			return json.RawMessage(`{"connectors":[{"id":"imessage","status":"ok"}]}`), nil
		case "connector.changes":
			return json.RawMessage(`{"next":"9","batches":[{"chat":{"id":"imessage-0123456789ab","name":"张三","members":[{"handle":"+15550100","name":"张三"}],
"services":["SMS"],"last":"` + time.Now().UTC().Format(time.RFC3339) + `"},"messages":[
{"id":"8","at":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `","kind":"message","sender":{"handle":"+15550100","name":"张三"},"text":"示例：周五见"},
{"id":"9","at":"` + time.Now().UTC().Format(time.RFC3339) + `","kind":"message","from_me":true,"text":"好"}]}]}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	})
	if err := h.addIMessage(imessagewire.Config{Machine: "mac"}); err != nil {
		t.Fatal(err)
	}
	h.imessage().Check(context.Background())
	ask := func(method string, params any) json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(params)
		out, ok, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method, Params: raw})
		if !ok || err != nil {
			t.Fatalf("%s: handled %v, %v", method, ok, err)
		}
		return out
	}
	var archives assistantwire.Archives
	_ = json.Unmarshal(ask(assistantwire.ConnectorsArchives, map[string]any{}), &archives)
	if !slices.ContainsFunc(archives.Archives, func(a assistantwire.ArchiveSource) bool { return a.ID == imessagewire.ID }) {
		t.Fatalf("archives %+v", archives)
	}
	var res assistantwire.ChangesResult
	_ = json.Unmarshal(ask(assistantwire.ConnectorsChanges, map[string]any{"id": imessagewire.ID, "cursor": "", "fence_version": assistantwire.ArchiveFenceV1}), &res)
	if res.Next != "9" || len(res.Batches) != 1 {
		t.Fatalf("changes %+v", res)
	}
	b := res.Batches[0]
	if b.Chat.ID != "0123456789ab" || b.Chat.Name != "张三" || b.Chat.Kind != "dm" || len(b.Messages) != 2 {
		t.Fatalf("batch %+v", b)
	}
	if m := b.Messages[0]; m.SenderID != imessagePersonKey("+15550100") || m.SenderName != "张三" || m.Mine {
		t.Fatalf("their message %+v", m)
	}
	if !b.Messages[1].Mine {
		t.Fatalf("mine %+v", b.Messages[1])
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(strings.ReplaceAll(string(raw), `"value":"+15550100"`, ""), "5550100") {
		t.Errorf("a number outside the identity hint (hashed by the knowledge base): %s", raw)
	}
}

// Deleting what was kept: only once "Hand to Persona" is off; the knowledge base's iMessage chats and people
// (kb.forget) and the inbox's iMessage items (inbox.forget), both the assistant's in this process, go; the counts are
// theirs.
func TestIMessageDelete(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	a := withAssistant(h)
	a.on(assistantwire.MethodKBForget, func(json.RawMessage) (any, error) {
		return assistantwire.KBForgotten{Chats: 1, People: 1}, nil
	})
	a.on(assistantwire.MethodInboxForget, func(json.RawMessage) (any, error) { return map[string]any{"deleted": 1}, nil })
	mux := h.routes()
	imessageNode(t, h, func(method string, _ json.RawMessage) (any, error) {
		return json.RawMessage(`{"connectors":[{"id":"imessage","status":"ok"}]}`), nil
	})
	if err := h.addIMessage(imessagewire.Config{Machine: "mac"}); err != nil {
		t.Fatal(err)
	}

	if code, out := call(t, mux, "DELETE", "/api/v1/imessage/data", h.cfg.Token, ""); code != 409 || out["code"] != "ai_on" {
		t.Fatalf("while on: %d %v", code, out)
	}
	if n := len(a.got(assistantwire.MethodKBForget)) + len(a.got(assistantwire.MethodInboxForget)); n != 0 {
		t.Fatal("deleted while on")
	}
	call(t, mux, "PUT", "/api/v1/imessage/settings", h.cfg.Token, `{"ai":false}`)
	code, out := call(t, mux, "DELETE", "/api/v1/imessage/data", h.cfg.Token, "")
	if code != 200 || out["chats"] != 1.0 || out["people"] != 1.0 || out["inbox"] != 1.0 {
		t.Fatalf("delete: %d %v", code, out)
	}
	for _, m := range []string{assistantwire.MethodKBForget, assistantwire.MethodInboxForget} {
		if got := a.got(m); len(got) != 1 || !strings.Contains(string(got[0]), `"source":"imessage"`) {
			t.Errorf("%s: %s", m, got)
		}
	}
	// The assistant away: its part is not deleted, the app is told to try again.
	a.on(assistantwire.MethodKBForget, func(json.RawMessage) (any, error) { return nil, errAssistantOffline })
	if code, out := call(t, mux, "DELETE", "/api/v1/imessage/data", h.cfg.Token, ""); code != 503 {
		t.Fatalf("assistant away: %d %v", code, out)
	}
}
