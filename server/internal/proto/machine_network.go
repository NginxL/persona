package proto

// On-demand read-only network and proxy connectivity diagnostics for a machine
// (#12351). Probe targets are fixed on the node (never caller-supplied) to
// prevent SSRF, and any embedded proxy credentials are strictly redacted.

const (
	// MethodMachineNetwork runs an on-demand read-only network and proxy probe on the node.
	MethodMachineNetwork = "machine.network"
)

// MachineNetworkParams is machine.network's request parameters.
// Probe targets are fixed on the node (never caller-supplied) to prevent SSRF.
type MachineNetworkParams struct{}

// MachineNetworkReport is the on-demand network and proxy connectivity report
// returned by machine.network. Proxy credentials are always redacted.
type MachineNetworkReport struct {
	CheckedAt  int64                  `json:"checked_at"` // Unix seconds
	Interfaces []MachineNetInterface  `json:"interfaces"`
	Proxy      MachineProxyConfig     `json:"proxy"`
	Probes     []MachineEndpointProbe `json:"probes"`
}

// MachineNetInterface summarizes one non-loopback network interface with addresses.
type MachineNetInterface struct {
	Name      string   `json:"name"`
	Up        bool     `json:"up"`
	Addresses []string `json:"addresses"`
}

// MachineProxyConfig reports effective proxy environment variables with any
// embedded user/password strictly redacted.
type MachineProxyConfig struct {
	HTTPProxy  string `json:"http_proxy,omitempty"`
	HTTPSProxy string `json:"https_proxy,omitempty"`
	ALLProxy   string `json:"all_proxy,omitempty"`
	NOProxy    string `json:"no_proxy,omitempty"`
	Active     bool   `json:"active"`
}

// MachineEndpointProbe is the reachability and latency check of one allowlisted endpoint.
type MachineEndpointProbe struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Target     string `json:"target"`
	Protocol   string `json:"protocol"` // "https" | "tcp"
	ViaProxy   bool   `json:"via_proxy"`
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}
