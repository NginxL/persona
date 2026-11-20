package devices

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestSessionStartSpeedCapabilityAnnounced(t *testing.T) {
	if !slices.Contains(nodeproto.Announced, "session_start_speed") {
		t.Fatal("a new node does not announce session_start_speed")
	}
}

// Both start methods must refuse a billable faster-speed request before an old
// node can silently drop it. Omitted and standard starts keep their old path.
func TestMachineCallGatesSessionStartSpeed(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh", SSHHost: "box.invalid"})
	capable := current("new")
	capable.Capabilities = append(slices.Clone(capable.Capabilities), "session_start_speed")
	without := current("old")
	without.Capabilities = slices.DeleteFunc(slices.Clone(without.Capabilities), func(c string) bool { return c == "session_start_speed" })

	for _, info := range []proto.NodeInfo{capable, without, {Version: "pre-handshake", ModelConfiguration: true}} {
		hasCapability := slices.Contains(info.Capabilities, "session_start_speed")
		for _, method := range []string{"session.start", devicewire.MethodStartWorkSession} {
			for _, speed := range []string{"", "standard", "fast", "ultrafast"} {
				m.mu.Lock()
				m.info, m.online = info, true
				m.mu.Unlock()
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // an admitted call cannot reach a process in this fixture
				params := map[string]any{"cwd": "/tmp"}
				if speed != "" {
					params["speed"] = speed
				}
				var re *rpc.Error
				err := m.call(ctx, method, params, nil)
				refused := errors.As(err, &re) && re.Code == proto.CodeNodeOutdated
				want := (speed == "fast" || speed == "ultrafast") && !hasCapability
				if refused != want {
					t.Errorf("%s %s speed=%q: node_outdated=%v, want %v (err %v)", info.Version, method, speed, refused, want, err)
				}
			}
		}
	}

	// This is a start option, not a new gate on established sends.
	m.mu.Lock()
	m.info = without
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var re *rpc.Error
	if err := m.call(ctx, "session.send", map[string]any{"speed": "fast"}, nil); errors.As(err, &re) && re.Code == proto.CodeNodeOutdated {
		t.Fatal("an unrelated session.send gained the start-speed gate")
	}
}
