package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An engine that runs only its own launchers (Paseo, #8140) ran one of the same kind in place of the launcher asked for,
// at a lower effort its model offers: the app's answer carries what was asked (instead) and says it in the request's
// language (launcher_note), naming the engine, the computer and both launchers.
func TestStartSessionSaysWhatTheEngineRanInstead(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(context.Background()) {
		t.Fatal("machine trust could not be initialized")
	}
	h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher.codex"}}
	starts := make(chan map[string]any, 1)
	modelsNode(t, h, MachineConfig{ID: "pbox", Name: "pbox"}, func(method string, p map[string]any) (any, error) {
		if method == "session.start" {
			starts <- p
			return map[string]any{
				"session": proto.Session{SessionID: "paseo-1", Cwd: "/code", Agent: "codex", Launcher: "my-codex", Effort: "high"},
				"instead": proto.StartInstead{Launcher: "codex", Effort: "xhigh"},
			}, nil
		}
		return map[string]any{}, nil
	})
	h.fleet.mu.Lock()
	m := h.byID["pbox"]
	h.fleet.mu.Unlock()
	m.mu.Lock()
	m.info.EngineHosts = &proto.EngineChoice{Hosts: []string{proto.EnginePaseo}, Default: proto.EnginePaseo}
	m.mu.Unlock()
	connectAssistant(t, h)
	h.applyDeviceStates(devicewire.States{})

	r := modelsRequest(h, "POST", "/api/v1/sessions", `{"machine_id":"pbox","cwd":"/code","launcher":"codex","agent_type":"codex","effort":"xhigh","prompt":"hi"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	if p := <-starts; p["launcher"] != "codex" || p["effort"] != "xhigh" {
		t.Fatalf("the node was asked for %v", p)
	}
	var res struct {
		Session      proto.Session       `json:"session"`
		LauncherNote string              `json:"launcher_note"`
		Instead      *proto.StartInstead `json:"instead"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Instead == nil || *res.Instead != (proto.StartInstead{Launcher: "codex", Effort: "xhigh"}) || res.Session.Launcher != "my-codex" {
		t.Fatalf("the answer: %s", r.Body)
	}
	for _, w := range []string{"Paseo", "pbox", "“codex”", "“my-codex”", "xhigh", "high"} {
		if !strings.Contains(res.LauncherNote, w) {
			t.Fatalf("launcher_note %q does not say %q", res.LauncherNote, w)
		}
	}
}

func TestStartSessionForwardsImageOnlyPromptAndClientMessageID(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(context.Background()) {
		t.Fatal("machine trust could not be initialized")
	}
	starts := make(chan map[string]any, 1)
	modelsNode(t, h, MachineConfig{ID: "test-node"}, func(method string, p map[string]any) (any, error) {
		if method == "session.start" {
			starts <- p
			return map[string]any{"session": proto.Session{SessionID: "agy-new", Cwd: "/work", Launcher: "agy"}}, nil
		}
		return map[string]any{}, nil
	})
	connectAssistant(t, h)
	r := modelsRequest(h, "POST", "/api/v1/sessions", `{"machine_id":"test-node","cwd":"/work","launcher":"agy","client_msg_id":"start-photo","images":[{"mime":"image/png","data":"aW1hZ2U="}]}`)
	if r.Code != http.StatusOK {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	params := <-starts
	images, _ := params["images"].([]any)
	if params["client_msg_id"] != "start-photo" || params["prompt"] != "" || len(images) != 1 {
		t.Fatalf("node start: %+v", params)
	}
}

// The host a computer's starts open in when they name none: its node's default engine host, else its single engine.
func TestStartEngineIsTheNodesDefaultHost(t *testing.T) {
	for _, c := range []struct {
		info proto.NodeInfo
		want string
	}{
		{proto.NodeInfo{Engine: proto.EngineHerdr, EngineHosts: &proto.EngineChoice{Hosts: []string{proto.EngineHerdr, proto.EnginePaseo}, Default: proto.EnginePaseo}}, proto.EnginePaseo},
		{proto.NodeInfo{Engine: proto.EngineOrca}, proto.EngineOrca},
		{proto.NodeInfo{}, ""},
	} {
		if got := startEngine(c.info); got != c.want {
			t.Errorf("%+v: %q, want %q", c.info, got, c.want)
		}
	}
}
