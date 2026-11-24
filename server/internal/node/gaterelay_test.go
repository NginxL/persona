package node

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A relayed gate (#12904) is one the proxy stands in for (an Auth of its own, no header); a gate with its own Auth and
// one the proxy does not stand in for stay as they were.
func TestRelayGates(t *testing.T) {
	own := &proto.GateAuth{Header: "X-Key", Value: "${K}"}
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{
		{Command: "relayed", BaseEnv: "B", SocketEnv: "S", Relay: true},
		{Command: "proxied", BaseEnv: "B", Auth: own},
		{Command: "plain"},
		{Command: "relayed-no-base", SocketEnv: "S", Relay: true},
	}}
	relayGates(&cfg)
	if a := cfg.Gates[0].Auth; a == nil || a.Header != "" || a.Value != "" {
		t.Errorf("relayed: %+v", a)
	}
	if cfg.Gates[1].Auth != own || cfg.Gates[2].Auth != nil || cfg.Gates[3].Auth != nil {
		t.Errorf("others changed: %+v", cfg.Gates)
	}
}
