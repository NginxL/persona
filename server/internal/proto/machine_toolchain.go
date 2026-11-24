package proto

import "time"

const (
	// MethodMachineToolchain is the on-demand read-only node RPC that returns a
	// machine's local development toolchain, agent CLI, and hardware profile.
	MethodMachineToolchain = "machine.toolchain"
)

// MachineToolchainParams configures an on-demand machine.toolchain probe.
type MachineToolchainParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// MachineToolchainReport is the redacted local development toolchain and
// environment profile returned by a node on demand.
type MachineToolchainReport struct {
	CollectedAt time.Time            `json:"collected_at"`
	Cached      bool                 `json:"cached"`
	CacheTTLSec int                  `json:"cache_ttl_sec"`
	DurationMS  int64                `json:"duration_ms"`
	OS          string               `json:"os"`
	Arch        string               `json:"arch"`
	Tools       []ToolchainEntry     `json:"tools"`
	AgentCLIs   []ToolchainEntry     `json:"agent_clis"`
	Docker      *ToolchainDockerInfo `json:"docker,omitempty"`
	GitHubCLI   *ToolchainGHAuth     `json:"github_cli,omitempty"`
	Hardware    *ToolchainHardware   `json:"hardware,omitempty"`
	Environment ToolchainEnvSummary  `json:"environment"`
}

// ToolchainEntry describes one probed developer tool or agent CLI.
type ToolchainEntry struct {
	ID           string   `json:"id"`
	Category     string   `json:"category"` // "core" | "agent"
	Found        bool     `json:"found"`
	Version      string   `json:"version,omitempty"`
	Path         string   `json:"path,omitempty"`
	ResolvedPath string   `json:"resolved_path,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Status       string   `json:"status,omitempty"` // "ready" | "missing" | "unauthenticated" | "daemon_unreachable" | "permission_denied" | "error"
	Note         string   `json:"note,omitempty"`
}

// ToolchainDockerInfo summarizes local Docker CLI and daemon socket access.
type ToolchainDockerInfo struct {
	Installed        bool   `json:"installed"`
	ClientVersion    string `json:"client_version,omitempty"`
	ServerVersion    string `json:"server_version,omitempty"`
	SocketPath       string `json:"socket_path,omitempty"`
	SocketAccessible bool   `json:"socket_accessible"`
	Status           string `json:"status"` // "ready" | "daemon_unreachable" | "permission_denied" | "missing"
	Note             string `json:"note,omitempty"`
}

// ToolchainGHAuth summarizes GitHub CLI authentication without exposing tokens.
type ToolchainGHAuth struct {
	Installed     bool     `json:"installed"`
	Version       string   `json:"version,omitempty"`
	Authenticated bool     `json:"authenticated"`
	Hosts         []string `json:"hosts,omitempty"`
	Status        string   `json:"status"` // "authenticated" | "unauthenticated" | "missing" | "error"
	Note          string   `json:"note,omitempty"`
}

// ToolchainHardware describes optional GPU and laptop power capabilities.
type ToolchainHardware struct {
	Chip  string          `json:"chip,omitempty"`
	GPUs  []ToolchainGPU  `json:"gpus,omitempty"`
	Power *ToolchainPower `json:"power,omitempty"`
}

// ToolchainGPU describes one detected GPU device.
type ToolchainGPU struct {
	Name    string `json:"name"`
	Vendor  string `json:"vendor,omitempty"` // "nvidia" | "apple" | "amd" | "intel"
	VRAMMB  int64  `json:"vram_mb,omitempty"`
	Driver  string `json:"driver,omitempty"`
	Unified bool   `json:"unified,omitempty"`
}

// ToolchainPower describes laptop battery and sleep state where available.
type ToolchainPower struct {
	HasBattery     bool   `json:"has_battery"`
	BatteryPercent *int   `json:"battery_percent,omitempty"`
	Charging       *bool  `json:"charging,omitempty"`
	Source         string `json:"source,omitempty"`      // "ac" | "battery" | "unknown"
	SleepState     string `json:"sleep_state,omitempty"` // e.g. "normal", "prevent_sleep", "low_power", "s2idle", "deep"
}

// ToolchainEnvSummary describes safe, non-secret local environment facts.
type ToolchainEnvSummary struct {
	Shell           string   `json:"shell,omitempty"`
	PathEntries     []string `json:"path_entries,omitempty"`
	VersionManagers []string `json:"version_managers,omitempty"`
	HasCustomGoPath bool     `json:"has_custom_go_path,omitempty"`
	ProxyConfigured bool     `json:"proxy_configured,omitempty"`
}
