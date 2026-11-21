package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime's card is pushed under its own name: DSH's, and omp's, whose extension names its cards omp
// (integrations/omp, docs/integrations/omp.md).
func TestRuntimePushTitle(t *testing.T) {
	for _, c := range []struct{ agent, label, title string }{
		{"dsh", "DSH", "DSH 需要你授权"}, // i18n-ignore: the hub's zh-Hans push text
		{"omp", "omp", "omp 需要你授权"}, // i18n-ignore: the hub's zh-Hans push text
	} {
		p := interactionPayload(proto.Interaction{Kind: proto.IxPermission, Agent: c.agent, AgentLabel: c.label, Summary: "echo hi"}, "server")
		alert := p["aps"].(map[string]any)["alert"].(map[string]any)
		if alert["title"] != c.title {
			t.Errorf("%s: %v", c.agent, alert)
		}
	}
}
