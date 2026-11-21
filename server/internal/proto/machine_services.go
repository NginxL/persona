package proto

import (
	"errors"
	"strings"
)

// Machine service and container inspection, bounded logs, and confirmation-gated
// lifecycle control (#12353). Read-only methods (machine.services, machine.service_logs)
// inspect user systemd/launchd services and Docker containers; lifecycle writes
// (machine.service_action) require explicit confirmation and refuse Persona's own
// services.

const (
	MethodMachineServices      = "machine.services"       // MachineServicesParams -> MachineServicesReport
	MethodMachineServiceLogs   = "machine.service_logs"   // MachineServiceLogsParams -> MachineServiceLogsResult
	MethodMachineServiceAction = "machine.service_action" // MachineServiceActionParams -> MachineServiceActionResult
)

const (
	ServiceKindSystemd = "systemd"
	ServiceKindLaunchd = "launchd"
	ServiceKindDocker  = "docker"
)

const (
	ServiceStateRunning    = "running"
	ServiceStateStopped    = "stopped"
	ServiceStateFailed     = "failed"
	ServiceStateActivating = "activating"
	ServiceStateUnknown    = "unknown"
)

const (
	ServiceActionStart   = "start"
	ServiceActionStop    = "stop"
	ServiceActionRestart = "restart"
)

const (
	ServiceLogsDefaultLines = 100
	ServiceLogsMaxLines     = 500
	ServiceLogsMaxBytes     = 64 * 1024
	ServiceListMaxItems     = 200
)

const (
	CodeServiceProtected = "service_protected"
)

// MachineServicesParams is machine.services's params.
type MachineServicesParams struct{}

// ServiceManagerStatus describes whether a service or container manager is
// available on the node.
type ServiceManagerStatus struct {
	Kind        string `json:"kind"`                  // systemd | launchd | docker
	Available   bool   `json:"available"`             // true when the manager responded cleanly
	Unavailable string `json:"unavailable,omitempty"` // diagnostic reason when !Available
}

// MachineServiceItem represents one user service (systemd/launchd) or Docker container.
type MachineServiceItem struct {
	Kind        string `json:"kind"`                   // systemd | launchd | docker
	ID          string `json:"id"`                     // unit name, launchd label, or container ID
	Name        string `json:"name"`                   // display name
	State       string `json:"state"`                  // running | stopped | failed | activating | unknown
	SubState    string `json:"sub_state,omitempty"`    // raw sub-state or docker state
	Status      string `json:"status,omitempty"`       // human-readable status / uptime
	Description string `json:"description,omitempty"`  // unit description or container image
	PID         int    `json:"pid,omitempty"`          // process ID when reported
	Protected   bool   `json:"protected,omitempty"`    // true for Persona's own services
	ProtectNote string `json:"protect_note,omitempty"` // why lifecycle actions are guarded
}

// MachineServicesReport is machine.services's result.
type MachineServicesReport struct {
	At         int64                  `json:"at"`
	OS         string                 `json:"os"`
	Managers   []ServiceManagerStatus `json:"managers"`
	Services   []MachineServiceItem   `json:"services"`
	Containers []MachineServiceItem   `json:"containers"`
}

// MachineServiceLogsParams is machine.service_logs's params.
type MachineServiceLogsParams struct {
	Kind  string `json:"kind"`            // systemd | launchd | docker
	ID    string `json:"id"`              // validated unit name, launchd label, or container ID/name
	Lines int    `json:"lines,omitempty"` // default ServiceLogsDefaultLines, clamped to [1, ServiceLogsMaxLines]
}

// MachineServiceLogsResult is machine.service_logs's result.
type MachineServiceLogsResult struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated,omitempty"`
	Content   string `json:"content"`
	Source    string `json:"source,omitempty"` // journalctl | docker | file | log
	Note      string `json:"note,omitempty"`
}

// MachineServiceActionParams is machine.service_action's params.
type MachineServiceActionParams struct {
	Kind      string `json:"kind"`      // systemd | launchd | docker
	ID        string `json:"id"`        // validated unit name, launchd label, or container ID/name
	Action    string `json:"action"`    // start | stop | restart
	Confirmed bool   `json:"confirmed"` // must be true (explicit UI confirmation)
}

// MachineServiceActionResult is machine.service_action's result.
type MachineServiceActionResult struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
}

// ValidateServiceKind checks that kind is one of systemd, launchd, or docker.
func ValidateServiceKind(kind string) error {
	switch kind {
	case ServiceKindSystemd, ServiceKindLaunchd, ServiceKindDocker:
		return nil
	default:
		return errors.New("invalid service kind")
	}
}

// ValidateServiceAction checks that action is one of start, stop, or restart.
func ValidateServiceAction(action string) error {
	switch action {
	case ServiceActionStart, ServiceActionStop, ServiceActionRestart:
		return nil
	default:
		return errors.New("invalid service action")
	}
}

// ClampServiceLogLines clamps a requested line count into [1, ServiceLogsMaxLines].
func ClampServiceLogLines(lines int) int {
	if lines <= 0 {
		return ServiceLogsDefaultLines
	}
	if lines > ServiceLogsMaxLines {
		return ServiceLogsMaxLines
	}
	return lines
}

// ValidateServiceID strictly validates a systemd unit name, launchd label, or
// Docker container ID/name to prevent flag injection, path traversal, and shell
// metacharacters.
func ValidateServiceID(kind, id string) error {
	if err := ValidateServiceKind(kind); err != nil {
		return err
	}
	if id == "" {
		return errors.New("service id is required")
	}
	maxLen := 256
	if kind == ServiceKindDocker {
		maxLen = 128
	}
	if len(id) > maxLen {
		return errors.New("service id is too long")
	}
	if strings.Contains(id, "..") {
		return errors.New("invalid service id")
	}
	// Must start with an ASCII letter or digit (prevents flag injection like "-u").
	if !isASCIIAlnum(id[0]) {
		return errors.New("invalid service id")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if isASCIIAlnum(c) || c == '.' || c == '_' || c == '-' {
			continue
		}
		if kind == ServiceKindSystemd && (c == '@' || c == ':') {
			continue
		}
		return errors.New("invalid service id")
	}
	return nil
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
