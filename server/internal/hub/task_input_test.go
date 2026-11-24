package hub

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestTaskInputFollowsManagerCapability(t *testing.T) {
	h, _, manager := handoffBusHubWithCapabilities(t, nil)
	if slices.Contains(h.features(), assistantwire.CapabilityTaskInput) {
		t.Fatal("older manager advertised the rich editor")
	}
	if err := manager.call("manager.hello", map[string]any{"id": assistantwire.AssistantID, "capabilities": []string{assistantwire.CapabilityTaskInput}}, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.features(), assistantwire.CapabilityTaskInput) {
		t.Fatal("capable manager lost its editor")
	}
}
