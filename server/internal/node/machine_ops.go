package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// MachineOps exposes strictly read-only local machine diagnostics for the five
// machine-ops domains (#12349 storage/hotspots, #12350 toolchain, #12351
// redacted network/proxy probes, #12352 listening ports and processes, #12353
// user services/containers and bounded logs).
//
// Mutating operations (storage cleanup, process kill, service start/stop/restart)
// and human-only file browser/write endpoints (#12354/#12403) are deliberately
// excluded from this surface.
type MachineOps struct {
	node *Node
}

// NewMachineOps creates a standalone read-only diagnostics runner for the local
// machine without starting any node sockets, hub connections, or background loops.
func NewMachineOps() *MachineOps {
	home, _ := os.UserHomeDir()
	dir := identity.HomeDir()
	if dir == "" && home != "" {
		dir = identity.DataDir(home)
	}
	return &MachineOps{
		node: &Node{
			home: home,
			dir:  dir,
		},
	}
}

// Storage collects fast read-only per-mount disk and inode utilization (statfs only).
func (m *MachineOps) Storage(ctx context.Context) (proto.MachineStorageReport, error) {
	out, err := m.node.machineStorage(ctx, nil)
	if err != nil {
		return proto.MachineStorageReport{}, err
	}
	rep, ok := out.(proto.MachineStorageReport)
	if !ok {
		return proto.MachineStorageReport{}, fmt.Errorf("unexpected storage report type %T", out)
	}
	return rep, nil
}

// StorageHotspots runs an on-demand bounded scan across allowlisted storage hotspot categories.
func (m *MachineOps) StorageHotspots(ctx context.Context, params proto.StorageHotspotsParams) (proto.MachineStorageHotspotsReport, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return proto.MachineStorageHotspotsReport{}, err
	}
	out, err := m.node.machineStorageHotspots(ctx, raw)
	if err != nil {
		return proto.MachineStorageHotspotsReport{}, err
	}
	rep, ok := out.(proto.MachineStorageHotspotsReport)
	if !ok {
		return proto.MachineStorageHotspotsReport{}, fmt.Errorf("unexpected storage hotspots report type %T", out)
	}
	return rep, nil
}

// Toolchain collects the redacted local development toolchain, agent CLI, Docker/gh, and hardware profile.
func (m *MachineOps) Toolchain(ctx context.Context, params proto.MachineToolchainParams) (proto.MachineToolchainReport, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return proto.MachineToolchainReport{}, err
	}
	out, err := m.node.machineToolchain(ctx, raw)
	if err != nil {
		return proto.MachineToolchainReport{}, err
	}
	rep, ok := out.(proto.MachineToolchainReport)
	if !ok {
		return proto.MachineToolchainReport{}, fmt.Errorf("unexpected toolchain report type %T", out)
	}
	return rep, nil
}

// Network runs redacted proxy inspection and fixed allowlisted developer endpoint reachability probes.
func (m *MachineOps) Network(ctx context.Context) (proto.MachineNetworkReport, error) {
	out, err := m.node.machineNetwork(ctx, nil)
	if err != nil {
		return proto.MachineNetworkReport{}, err
	}
	rep, ok := out.(proto.MachineNetworkReport)
	if !ok {
		return proto.MachineNetworkReport{}, fmt.Errorf("unexpected network report type %T", out)
	}
	return rep, nil
}

// Ports lists active listening TCP/UDP sockets with bind scopes and redacted command lines.
func (m *MachineOps) Ports(ctx context.Context, params proto.MachinePortsParams) (proto.MachinePortsResult, error) {
	out, err := m.node.machinePorts(ctx, params)
	if err != nil {
		return proto.MachinePortsResult{}, err
	}
	rep, ok := out.(proto.MachinePortsResult)
	if !ok {
		return proto.MachinePortsResult{}, fmt.Errorf("unexpected ports result type %T", out)
	}
	return rep, nil
}

// Processes lists running processes with orphan heuristics, protection flags, and redacted command lines.
func (m *MachineOps) Processes(ctx context.Context, params proto.MachineProcessesParams) (proto.MachineProcessesResult, error) {
	out, err := m.node.machineProcesses(ctx, params)
	if err != nil {
		return proto.MachineProcessesResult{}, err
	}
	rep, ok := out.(proto.MachineProcessesResult)
	if !ok {
		return proto.MachineProcessesResult{}, fmt.Errorf("unexpected processes result type %T", out)
	}
	return rep, nil
}

// Services lists user systemd/launchd services, Docker containers, and manager availability.
func (m *MachineOps) Services(ctx context.Context) (proto.MachineServicesReport, error) {
	out, err := m.node.machineServices(ctx, nil)
	if err != nil {
		return proto.MachineServicesReport{}, err
	}
	rep, ok := out.(proto.MachineServicesReport)
	if !ok {
		return proto.MachineServicesReport{}, fmt.Errorf("unexpected services report type %T", out)
	}
	return rep, nil
}

// ServiceLogs fetches bounded tail logs for a validated systemd unit, launchd label, or Docker container.
func (m *MachineOps) ServiceLogs(ctx context.Context, params proto.MachineServiceLogsParams) (proto.MachineServiceLogsResult, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return proto.MachineServiceLogsResult{}, err
	}
	out, err := m.node.machineServiceLogs(ctx, raw)
	if err != nil {
		return proto.MachineServiceLogsResult{}, err
	}
	rep, ok := out.(proto.MachineServiceLogsResult)
	if !ok {
		return proto.MachineServiceLogsResult{}, fmt.Errorf("unexpected service logs result type %T", out)
	}
	return rep, nil
}
