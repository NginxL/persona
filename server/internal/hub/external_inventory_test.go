package hub

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestExternalInventoryDiscoveryRequiresNodeAnnouncement(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol int
		caps     []string
		want     bool
	}{
		{"old_node", 0, nil, false},
		{"capability_without_protocol", 0, []string{nodeproto.ExternalLauncherInventory}, false},
		{"ordinary_node", nodeproto.Version, []string{nodeproto.Sessions}, false},
		{"enabled_local_adapter", nodeproto.Version, []string{nodeproto.ExternalLauncherInventory}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newTestHub(t)
			m := newMachine(h, MachineConfig{ID: "example-node", Name: "Example computer", Kind: "ssh"})
			m.info = proto.NodeInfo{Protocol: test.protocol, Capabilities: test.caps}
			h.machines, h.byID[m.cfg.ID] = append(h.machines, m), m
			raw, err := (assistantLink{h}).DeviceCall(t.Context(), assistantwire.DeviceCall{
				Device: assistantwire.DeviceAll, Method: assistantwire.DeviceDevicesList,
			})
			if err != nil {
				t.Fatal(err)
			}
			var listed struct {
				Devices []assistantwire.Device `json:"devices"`
			}
			if err := json.Unmarshal(raw, &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Devices) != 1 || listed.Devices[0].ExternalInventory != test.want {
				t.Fatalf("external source discovery was not derived from the node handshake: %s", raw)
			}
			var wire struct {
				Devices []map[string]json.RawMessage `json:"devices"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			_, present := wire.Devices[0]["external_inventory"]
			if present != test.want {
				t.Fatalf("disabled discovery changed an existing device's wire shape: %s", raw)
			}
		})
	}
}
