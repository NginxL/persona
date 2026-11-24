package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
)

type botSourcePeer struct {
	nodeCaller
	calls []string
	page  agents.BotPage
}

func (p *botSourcePeer) Done() <-chan struct{} { return nil }

func (p *botSourcePeer) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	p.calls = append(p.calls, method+" "+string(raw))
	body, _ := json.Marshal(p.page)
	return json.Unmarshal(body, out)
}

func TestManagerRegistersOnlyConfiguredBotEndpoints(t *testing.T) {
	needsLarkModule(t)
	h := newTestManager(t)
	h.cfg.Bots.Endpoints = []agents.BotRegistration{
		{ID: "research", Provider: "orca", Title: "Research bot", MachineID: "machine-b", SessionID: "claude_exact_registered"},
	}
	peer := &botSourcePeer{page: agents.BotPage{CanSend: true, RankTimeMs: 100}}
	m := &Machine{cfg: MachineConfig{ID: "machine-b", Name: "Machine B"}, peer: peer, online: true}
	h.machines, h.byID = []*Machine{m}, map[string]*Machine{"machine-b": m}
	h.initAgents()
	if h.agents.Get("bot_research") == nil || h.agents.Get("bot_machine-b") != nil || h.agents.Get("orca") != nil {
		t.Fatal("directory contains something other than the stable explicit registration")
	}
	list, err := h.agents.Sessions(context.Background(), "bot_research", "")
	if err != nil || len(list) != 1 || list[0].ID != "bot_research:endpoint" {
		t.Fatalf("registered directory %+v %v", list, err)
	}
	if len(peer.calls) == 0 {
		t.Fatal("manager did not call the bound machine")
	}
	for _, call := range peer.calls {
		if call == "" || strings.Contains(call, "machine-a") || !strings.Contains(call, "claude_exact_registered") {
			t.Fatalf("wrong machine or fallback session: %s", call)
		}
	}
}

func TestInvalidBotRegistrationDoesNotDisableValidRegistration(t *testing.T) {
	needsLarkModule(t)
	h := newTestManager(t)
	h.cfg.Bots.Endpoints = []agents.BotRegistration{
		{ID: "bad:id", Provider: "orca", Title: "Bad", MachineID: "machine-b", SessionID: "claude_bad"},
		{ID: "research", Provider: "orca", Title: "Research bot", MachineID: "machine-b", SessionID: "claude_exact_registered"},
	}
	h.initAgents()
	if h.agents.Get("bot_research") == nil || h.agents.Get("bot_bad:id") != nil {
		t.Fatal("invalid registration prevented the valid source")
	}
}
