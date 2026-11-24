// Package machineops provides the opt-in, on-demand read-only machine diagnostics
// CLI and stdio MCP server (#13053).
//
// It wraps the five read-only diagnostic domains implemented on the node:
//   - #12349: per-mount disk/inode monitoring (storage) and bounded hotspot scans (hotspots)
//   - #12350: local development toolchain and redacted environment profiling (toolchain)
//   - #12351: network interfaces, redacted proxy configuration, and fixed endpoint probes (network)
//   - #12352: listening TCP/UDP sockets (ports) and processes with orphan heuristics (processes)
//   - #12353: user systemd/launchd services, Docker containers (services), and bounded logs (service-logs)
//
// Mutating machine actions (storage cleanup, process kill, service start/stop/restart)
// and human-only Web UI file management (#12354/#12403) are deliberately excluded.
package machineops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/proto"
)

// Read-only MCP tool names exposed by `personad ops mcp`.
const (
	ToolStorage         = "machine_ops_storage"
	ToolStorageHotspots = "machine_ops_storage_hotspots"
	ToolToolchain       = "machine_ops_toolchain"
	ToolNetwork         = "machine_ops_network"
	ToolPorts           = "machine_ops_ports"
	ToolProcesses       = "machine_ops_processes"
	ToolServices        = "machine_ops_services"
	ToolServiceLogs     = "machine_ops_service_logs"
)

// ReadOnlyToolNames is the closed, ordered allowlist of tools exposed by `personad ops mcp`.
var ReadOnlyToolNames = []string{
	ToolStorage,
	ToolStorageHotspots,
	ToolToolchain,
	ToolNetwork,
	ToolPorts,
	ToolProcesses,
	ToolServices,
	ToolServiceLogs,
}

const (
	storageTimeout         = 15 * time.Second
	storageHotspotsTimeout = 45 * time.Second
	toolchainTimeout       = 20 * time.Second
	networkTimeout         = 15 * time.Second
	portsTimeout           = 20 * time.Second
	processesTimeout       = 20 * time.Second
	servicesTimeout        = 25 * time.Second
	serviceLogsTimeout     = 20 * time.Second
)

// Provider is the strictly read-only diagnostic backend used by both the ops CLI
// and the ops MCP server.
type Provider interface {
	Storage(ctx context.Context) (proto.MachineStorageReport, error)
	StorageHotspots(ctx context.Context, params proto.StorageHotspotsParams) (proto.MachineStorageHotspotsReport, error)
	Toolchain(ctx context.Context, params proto.MachineToolchainParams) (proto.MachineToolchainReport, error)
	Network(ctx context.Context) (proto.MachineNetworkReport, error)
	Ports(ctx context.Context, params proto.MachinePortsParams) (proto.MachinePortsResult, error)
	Processes(ctx context.Context, params proto.MachineProcessesParams) (proto.MachineProcessesResult, error)
	Services(ctx context.Context) (proto.MachineServicesReport, error)
	ServiceLogs(ctx context.Context, params proto.MachineServiceLogsParams) (proto.MachineServiceLogsResult, error)
}

// NewLocalProvider returns the real read-only diagnostics provider for this machine.
func NewLocalProvider() Provider {
	return node.NewMachineOps()
}

func validateHotspotsParams(p proto.StorageHotspotsParams) error {
	if p.TimeoutMS < 0 {
		return errors.New("timeout_ms must be non-negative")
	}
	if p.MaxDepth < 0 {
		return errors.New("max_depth must be non-negative")
	}
	return nil
}

func validatePortsParams(p proto.MachinePortsParams) (proto.MachinePortsParams, error) {
	protocol := strings.ToLower(strings.TrimSpace(p.Protocol))
	switch protocol {
	case "", "tcp", "udp":
		p.Protocol = protocol
		return p, nil
	default:
		return p, fmt.Errorf("invalid protocol %q: expected tcp or udp", p.Protocol)
	}
}

func validateProcessesParams(p proto.MachineProcessesParams) (proto.MachineProcessesParams, error) {
	sortBy := strings.ToLower(strings.TrimSpace(p.Sort))
	switch sortBy {
	case "", "cpu", "memory", "uptime":
		p.Sort = sortBy
	default:
		return p, fmt.Errorf("invalid sort %q: expected cpu, memory, or uptime", p.Sort)
	}
	if p.Limit < 0 {
		return p, errors.New("limit must be non-negative")
	}
	p.Query = strings.TrimSpace(p.Query)
	return p, nil
}

func validateServiceLogsParams(p proto.MachineServiceLogsParams) (proto.MachineServiceLogsParams, error) {
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	p.ID = strings.TrimSpace(p.ID)
	if err := proto.ValidateServiceID(p.Kind, p.ID); err != nil {
		return p, err
	}
	if p.Lines < 0 {
		return p, errors.New("lines must be a positive integer")
	}
	if p.Lines > 0 {
		p.Lines = proto.ClampServiceLogLines(p.Lines)
	}
	return p, nil
}
