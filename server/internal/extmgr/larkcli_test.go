package extmgr

import (
	"context"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lark"
	"github.com/nwverse/persona/server/internal/proto"
)

// With only lark-cli in hub.json (docs/plans/plan-lark-cli.md), the assistant's machine learns how to run it, and there is
// no gateway token to hand out (asking the Lark gateway would fail: it is not configured). A node's assistant.lark is the
// assistant's (its fleet_tools.go gives it to its own machine only); what it hands over is the extensions' answer.
func TestAssistantLarkWithLarkCLIOnly(t *testing.T) {
	needsLarkCLI(t)
	h := newTestManager(t)
	h.cfg.Lark = lark.Config{CLI: &lark.CLIConfig{Bin: "/opt/lark/bin/lark-cli", Profile: "work"}, Prefer: "cli"}
	h.cfg.ConnectorGate = []connector.Rule{{Connector: "lark", Action: "CLI /im/+messages-send", Decision: "allow"}}
	h.cfg.LarkGate = []proto.LarkRule{{Method: "CLI", Path: "/docs/**", Action: "deny"}}
	got, err := h.AssistantLark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CLI == nil || got.CLI.Bin != "/opt/lark/bin/lark-cli" || got.CLI.Profile != "work" || got.Prefer != "cli" {
		t.Fatalf("lark-cli config not passed on: %+v", got)
	}
	// connector_gate's lark rules first, then the older lark_gate ones.
	if want := []proto.LarkRule{{Method: "CLI", Path: "/im/+messages-send", Action: "allow"}, h.cfg.LarkGate[0]}; !reflect.DeepEqual(got.Rules, want) {
		t.Fatalf("rules: %+v, want %+v", got.Rules, want)
	}
	if len(got.Gates) != 0 {
		t.Fatalf("no plugin, no gated command: %+v", got.Gates)
	}
}
