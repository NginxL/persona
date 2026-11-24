package devices

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Isolation must work for all six connections with usable packages present,
// independently of package-directory containment or previous failed attempts.
func TestHubOnlyPolicyDoesNotDeployNodes(t *testing.T) {
	calls := fakeSSH(t)
	hubNodeBinary(t, "linux", "amd64", "new-build")
	d := newTestDevices(t)
	d.cfg.HubVersion = "new-build"
	for _, id := range []string{"one", "two", "three", "four", "five", "six"} {
		m := newMachine(d, MachineConfig{ID: id, Kind: "ssh", SSHHost: "example.invalid"})
		m.online = true
		m.info = proto.NodeInfo{OS: "linux", Arch: "amd64", Version: "old-build", NodeUpdate: true}
		d.addEntry(m)
		m.updateOnConnect(context.Background())
		m.updateOnConnect(context.Background())
		if err := m.startAutoDeploy(); !errors.Is(err, errAutoNodeUpdatesDisabled) {
			t.Fatalf("automatic deployment gate: %v", err)
		}
	}
	offline := newMachine(d, MachineConfig{ID: "offline", Kind: kindLink})
	d.addEntry(offline)
	d.updateNodes(context.Background())
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("unexpected SSH deployment: %v", err)
	}
	for _, m := range d.machinesNow() {
		var record nodeDeployRecord
		found, err := d.opts.Store.Setting(context.Background(), m.deployRecordKey(), &record)
		if err != nil || found || m.snapshot().Deploying {
			t.Fatalf("%s: found=%v err=%v record=%+v", m.cfg.ID, found, err, record)
		}
	}
}

func TestHubOnlyPolicyDoesNotResumeQueuedNodeTransfer(t *testing.T) {
	d, m, _, _ := linkedDeployFixture(t, kindLink)
	d.cfg.AutoUpdateNodes = false
	if err := m.queueOfflineNodeUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(context.Background(), m.peer, m.info)
	if m.snapshot().Deploying {
		t.Fatal("resumed a queued node upgrade under Hub-only policy")
	}
}

// With the package resolver configured, automatic entry points must stop before
// even asking the publisher for a manifest. Explicit Redeploy remains separate.
func TestHubOnlyPolicyDoesNotResolveRemoteNodePackages(t *testing.T) {
	site := newFakeRelease(t)
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	d.opts.Store = newMemStore()
	m := newMachine(d, MachineConfig{ID: "box", Kind: kindLink})
	m.online = true
	m.info = proto.NodeInfo{OS: otherOS, Arch: otherArch, Version: "old", NodeUpdate: true}
	d.addEntry(m)
	m.updateOnConnect(context.Background())
	d.updateNodes(context.Background())
	if err := m.startAutoDeploy(); !errors.Is(err, errAutoNodeUpdatesDisabled) {
		t.Fatal(err)
	}
	if requests := site.requests(); len(requests) != 0 {
		t.Fatalf("resolved packages under Hub-only policy: %v", requests)
	}
}
