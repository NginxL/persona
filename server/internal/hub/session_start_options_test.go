package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestWorkSessionHTTPUsesExactTarget(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(context.Background()) {
		t.Fatal("machine trust could not be initialized")
	}
	h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher.work", Model: "configured"}}
	opts, starts := make(chan map[string]any, 2), make(chan map[string]any, 3)
	cfg := MachineConfig{ID: "box", Launchers: []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: "work"}, AgentType: "codex"}}}
	modelsNode(t, h, cfg, func(method string, p map[string]any) (any, error) {
		switch method {
		case "session.start.options":
			opts <- p
			return map[string]any{"agent_type": "codex", "model_mode": "selectable", "models": []proto.AgentModel{{ID: "chosen", Name: "Chosen"}}}, nil
		case "session.start.work":
			starts <- p
			if p["model"] == "missing" {
				return nil, &rpc.Error{Code: "work_model_unavailable", Message: "model unavailable", Data: []byte(`{"delivered":"none"}`)}
			}
			return map[string]any{"session": proto.Session{SessionID: "work-id"}}, nil
		}
		return map[string]any{}, nil
	})
	connectAssistant(t, h)
	h.applyDeviceStates(devicewire.States{})
	w := modelsRequest(h, "GET", "/api/v1/machines/box/launchers/work/start-options", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"chosen"`) {
		t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	if p := <-opts; p["name"] != "work" || p["configured_agent"] != "codex" || p["configured"] != true {
		t.Fatalf("options target: %+v", p)
	}
	for _, tc := range []struct {
		more, model string
		status      int
	}{
		{`,"model":"chosen"`, "chosen", http.StatusOK},
		{"", "", http.StatusOK},
		{`,"model":"missing"`, "missing", http.StatusConflict},
	} {
		w := modelsRequest(h, "POST", "/api/v1/sessions", `{"work_session":true,"machine_id":"box","cwd":"/project","launcher":"work","agent_type":"codex","prompt":"first turn"`+tc.more+`}`)
		if w.Code != tc.status {
			t.Fatalf("start: %d %s", w.Code, w.Body)
		}
		if tc.status == http.StatusOK && !strings.Contains(w.Body.String(), `"session_id":"work-id"`) {
			t.Fatalf("missing ordinary session: %s", w.Body)
		}
		if p := <-starts; p["cwd"] != "/project" || p["launcher"] != "work" || p["agent_type"] != "codex" || p["prompt"] != "first turn" ||
			(tc.model == "" && p["model"] != nil) || (tc.model != "" && p["model"] != tc.model) {
			t.Fatalf("start target: %+v", p)
		}
	}
	w = modelsRequest(h, "POST", "/api/v1/sessions", `{"work_session":true,"machine_id":"box","cwd":"/project","launcher":"work"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing harness: %d %s", w.Code, w.Body)
	}
}

func TestWorkSessionHealthIgnoresValidationRefusals(t *testing.T) {
	h := newTestHub(t)
	registerMachine(h, newMachine(h, MachineConfig{ID: "box"}))
	var notices bytes.Buffer
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {peer: rpc.New("manager", &notices)}}
	params := map[string]any{"launcher": "work"}
	h.startEnded("box", "", params, fmt.Errorf("node: %w", &rpc.Error{Code: "work_model_unavailable", Message: "model unavailable"}))
	if notices.Len() != 0 {
		t.Fatalf("validation went to launcher health: %s", &notices)
	}
	h.startEnded("box", "", params, &rpc.Error{Code: "terminal_not_ready", Message: "terminal could not start"})
	var event struct {
		Event  string                   `json:"event"`
		Params assistantwire.StartEnded `json:"params"`
	}
	if err := json.Unmarshal(notices.Bytes(), &event); err != nil || event.Event != assistantwire.EventStartEnded ||
		event.Params.OK || event.Params.MachineID != "box" || event.Params.Launcher != "work" || event.Params.Code != "terminal_not_ready" {
		t.Fatalf("real failure was not reported: %s (%v)", &notices, err)
	}
}

func TestWorkSessionOptionsRespectsMachineAndLauncherScope(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(context.Background()) {
		t.Fatal("machine trust could not be initialized")
	}
	cfg := MachineConfig{ID: "other", Launchers: []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: "private"}, AgentType: "codex"}}}
	registerMachine(h, newMachine(h, cfg))
	var probe map[string]any
	modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, p map[string]any) (any, error) {
		if method != "session.start.options" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		probe = p
		return nil, &rpc.Error{Code: "work_launcher_unavailable", Message: "unknown launcher"}
	})
	const target = "/api/v1/machines/box/launchers/private/start-options"
	connectAssistant(t, h)
	h.applyDeviceStates(devicewire.States{})
	h.setLeaving("box", true)
	if w := modelsRequest(h, "GET", target, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "machine_pending") || probe != nil {
		t.Fatalf("an unconfirmed device was probed: %d %s", w.Code, w.Body)
	}
	h.setLeaving("box", false)
	if w := modelsRequest(h, "GET", target+"?configured=1&configured_agent=codex&machine_id=other", ""); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "work_launcher_unavailable") || probe["name"] != "private" || probe["configured"] != nil || probe["configured_agent"] != nil {
		t.Fatalf("client or other device vouched for launcher: %d %s (%v)", w.Code, w.Body, probe)
	}
}

// #7425: New work session's start options and the assistant's agent_start agree on whether a computer takes a
// session, as the hub decides it (machine_trust.go, ownership.go): a confirmed computer of the assistant's takes both;
// one waiting to be confirmed, and one moved to another manager, take neither and neither reaches its node. Only the
// one waiting to be confirmed is trust "pending" in the apps' list, the card's prompt to confirm it. The options are
// asked of the assistant itself, as agent_start is (the hub relays another manager's computer to that manager).
func TestWorkStartOptionsAndAgentStartAgreeOnTheComputer(t *testing.T) {
	for _, c := range []struct {
		name           string
		set            func(h *Hub)
		takes, confirm bool
	}{
		{"confirmed, the assistant's", func(*Hub) {}, true, false},
		{"waiting to be confirmed", func(h *Hub) { h.setPendingLocal("box", time.Now(), true) }, false, true},
		{"another manager's", func(h *Hub) {
			h.devices.mu.Lock()
			h.devices.assign = map[string]string{"box": "m-0c0c0c"}
			h.devices.mu.Unlock()
		}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHub(t)
			ctx := context.Background()
			if !h.loadMachineTrust(ctx) {
				t.Fatal("machine trust could not be initialized")
			}
			cfg := MachineConfig{ID: "box"}
			h.cfg.Machines = append(h.cfg.Machines, cfg)
			var mu sync.Mutex
			asked := map[string]int{}
			modelsNode(t, h, cfg, func(method string, _ map[string]any) (any, error) {
				mu.Lock()
				asked[method]++
				mu.Unlock()
				switch method {
				case "session.start.options":
					return map[string]any{"agent_type": "claude", "model_mode": "default"}, nil
				case "session.start":
					return map[string]any{"session": proto.Session{SessionID: "started", MachineID: "box"}}, nil
				}
				return map[string]any{}, nil
			})
			connectAssistant(t, h)
			h.applyDeviceStates(devicewire.States{})
			c.set(h)

			w := httptest.NewRecorder()
			h.forwardHTTP(w, httptest.NewRequest("GET", "/api/v1/machines/box/launchers/claude/start-options", nil), assistantwire.AssistantID)
			res := h.callAssistantTool(ctx, h.localMachine("box"), "", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/project","prompt":"go"}`))
			mu.Lock()
			defer mu.Unlock()
			options := w.Code == http.StatusOK && asked["session.start.options"] == 1
			started := !res.IsError && asked["session.start"] == 1
			if options != c.takes || started != c.takes {
				t.Fatalf("options %d %s (node asked %d), agent_start %+v (node asked %d): want both %v",
					w.Code, w.Body, asked["session.start.options"], res, asked["session.start"], c.takes)
			}
			if !c.takes && (w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "machine_pending") || asked["session.start.options"]+asked["session.start"] != 0) {
				t.Fatalf("refused unlike before: options %d %s, node asked %v", w.Code, w.Body, asked)
			}
			pending := slices.ContainsFunc(h.machineList(), func(m proto.Machine) bool { return m.ID == "box" && m.Trust == trustPending })
			if pending != c.confirm {
				t.Fatalf("the apps' list says pending %v, want %v: %+v", pending, c.confirm, h.machineList())
			}
		})
	}
}
