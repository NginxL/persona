package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestHubModelsIncludesLauncherRecovery(t *testing.T) {
	h := &Hub{machines: []*Machine{{cfg: MachineConfig{
		ID: "server",
		LauncherRecovery: &assistantwire.LauncherRecovery{
			WaitSeconds: 45,
			Fallbacks:   map[string][]string{"custom-a": {"custom-b", "claude"}},
		},
	}}}}
	got := h.hubModels().LauncherRecovery["server"]
	if got.WaitSeconds != 45 || len(got.Fallbacks["custom-a"]) != 2 || got.Fallbacks["custom-a"][0] != "custom-b" || got.Fallbacks["custom-a"][1] != "claude" {
		t.Fatalf("launcher recovery did not reach the manager hello: %+v", got)
	}
}
