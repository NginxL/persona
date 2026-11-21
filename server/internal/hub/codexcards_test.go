package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexPushTitles(t *testing.T) {
	q := interactionPayload(proto.Interaction{Kind: proto.IxQuestion, Agent: "codex", Questions: []proto.Question{{Question: "Which color?"}}}, "server")
	alert := func(m map[string]any) map[string]any { return m["aps"].(map[string]any)["alert"].(map[string]any) }
	if alert(q)["title"] != "Codex 在问你" || alert(q)["body"] != "Which color?" {
		t.Errorf("%v", alert(q))
	}
	for _, agent := range []string{"traex", "traecli", "trae-cli"} {
		p := interactionPayload(proto.Interaction{Kind: proto.IxPermission, Agent: agent, Summary: "rm -rf build"}, "server")
		if alert(p)["title"] != "TRAE CLI 需要你授权" {
			t.Errorf("%s: %v", agent, alert(p))
		}
	}
}

func TestPaseoTraeCLIPushTitles(t *testing.T) {
	for _, tc := range []struct{ label, title string }{
		{" TRAEX ", "TRAE CLI 需要你授权"},
		{"TraeCLI", "TRAE CLI 需要你授权"},
		{"trae-cli", "TRAE CLI 需要你授权"},
		{"trae cli", "TRAE CLI 需要你授权"},
		{"Team relay", "Team relay 需要你授权"},
		{"", "需要你授权"},
	} {
		p := interactionPayload(proto.Interaction{Kind: proto.IxPermission, Agent: "paseo", AgentLabel: tc.label}, "server")
		alert := p["aps"].(map[string]any)["alert"].(map[string]any)
		if got := alert["title"]; got != tc.title {
			t.Errorf("label %q: got title %q, want %q", tc.label, got, tc.title)
		}
	}
}
