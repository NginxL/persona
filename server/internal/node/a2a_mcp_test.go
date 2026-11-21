package node

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestA2ARuntimeMCPTicket(t *testing.T) {
	launchKeyHome(t)
	if _, err := ensureLaunchKey(nodeclient.LaunchKeyPath()); err != nil {
		t.Fatal(err)
	}
	n, hub := runtimeNode(t)
	const sessionID = "runtime-agent"
	var discovered, called string
	hub.mu.Lock()
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		var p struct {
			Session string `json:"session_id"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err, true
		}
		switch method {
		case "mcp.tools":
			discovered = p.Session
			return map[string]any{"tools": []proto.Tool{{Name: "phone_contacts"}, {Name: "a2a_get"}}, "revision": "1"}, nil, true
		case "mcp.call":
			called = p.Session + "/" + p.Name
			return proto.TextResult("ok", false), nil, true
		}
		return nil, nil, false
	}
	hub.mu.Unlock()
	provider, err := helloAt(t, n.runtimeSock, withInstance(dshHello(), "provider"))
	if err != nil {
		t.Fatal(err)
	}
	var ticket struct{ Token string }
	if err := provider.peer.Call(t.Context(), "runtime.a2a_mcp", map[string]string{"session_id": sessionID}, &ticket); err != nil || ticket.Token == "" {
		t.Fatalf("ticket request: %v", err)
	}
	list := sockRequest{Kind: "mcp.tools", SessionID: sessionID, A2AToken: ticket.Token}
	call := sockRequest{Kind: "mcp.call", SessionID: sessionID, A2AToken: ticket.Token, Name: "a2a_get", Args: json.RawMessage(`{}`)}
	checkList := func(wantSession string) {
		t.Helper()
		out := n.scopedA2AMCP(t.Context(), list).(map[string]any)
		tools := out["tools"].([]proto.Tool)
		if len(tools) != 1 || tools[0].Name != "a2a_get" || out["revision"] != "1" || discovered != wantSession {
			t.Fatalf("tools for %q: %+v", wantSession, out)
		}
	}
	checkList("")
	if got := n.scopedA2AMCP(t.Context(), call).(proto.ToolResult); !got.IsError {
		t.Fatal("unregistered session used its ticket")
	}
	publish := func(f *fakeRuntime, id string) {
		t.Helper()
		if err := f.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": id, "status": "idle"}}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "runtime session listed", func() bool { return listed(t, n)[id].SessionID == id })
	}
	publish(provider, sessionID)
	checkList(sessionID)
	if got := n.scopedA2AMCP(t.Context(), call).(proto.ToolResult); got.IsError || called != sessionID+"/a2a_get" {
		t.Fatalf("ticket call: %+v (%q)", got, called)
	}
	other := dshHello()
	other["runtime"] = "other"
	second, err := helloAt(t, n.runtimeSock, other)
	if err != nil {
		t.Fatal(err)
	}
	publish(second, "other-agent")
	wrongSession, wrongTool, wrongRuntime := call, call, call
	wrongSession.SessionID = "other-agent"
	wrongTool.Name = "phone_contacts"
	if err := second.peer.Call(t.Context(), "runtime.a2a_mcp", map[string]string{"session_id": sessionID}, &ticket); err != nil {
		t.Fatal(err)
	}
	wrongRuntime.A2AToken = ticket.Token
	for _, req := range []sockRequest{wrongSession, wrongTool, wrongRuntime} {
		if got := n.scopedA2AMCP(t.Context(), req).(proto.ToolResult); !got.IsError {
			t.Fatal("ticket granted a call outside its session and tool scope")
		}
	}
	reconnected, err := helloAt(t, n.runtimeSock, withInstance(dshHello(), "provider"))
	if err != nil {
		t.Fatal(err)
	}
	publish(reconnected, sessionID)
	checkList(sessionID)
	if _, err := n.runtimeHost.a2aMCPToken(&runtimeConn{daemon: "container"}, json.RawMessage(`{"session_id":"runtime-agent"}`)); err == nil {
		t.Fatal("daemon minted a local MCP ticket")
	}
}
