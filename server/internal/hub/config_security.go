package hub

import "time"

// SecurityConfig controls ingress trust and limits failed authentication from public sources.
// Durations are policy defaults, not measured traffic characteristics.
type SecurityConfig struct {
	TrustedCIDRs         []string `json:"trusted_cidrs,omitempty"`
	AllowPublicPlaintext bool     `json:"allow_public_plaintext,omitempty"`
	// PlaintextLoopbackOnly refuses plaintext from the LAN too (plaintext_lan.go); it needs public.tls_listen.
	PlaintextLoopbackOnly bool `json:"plaintext_loopback_only,omitempty"`
	// PrivateNetworkOnly is office network mode (private_network.go): nothing of the hub reaches or answers the internet.
	PrivateNetworkOnly     bool `json:"private_network_only,omitempty"`
	AuthFailures           int  `json:"auth_failures,omitempty"`
	AuthWindowSeconds      int  `json:"auth_window_seconds,omitempty"`
	AuthBlockSeconds       int  `json:"auth_block_seconds,omitempty"`
	AuthMaxSources         int  `json:"auth_max_sources,omitempty"`
	AuthCacheEntries       int  `json:"auth_cache_entries,omitempty"`
	AuthLogIntervalSeconds int  `json:"auth_log_interval_seconds,omitempty"`
	// ProxyProtocol: the client addresses a proxy in front passes on (proxy_protocol.go); nil reads no header.
	ProxyProtocol *ProxyProtocolConfig `json:"proxy_protocol,omitempty"`
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func (s SecurityConfig) failures() int { return positiveOr(s.AuthFailures, 10) }
func (s SecurityConfig) window() time.Duration {
	return time.Duration(positiveOr(s.AuthWindowSeconds, 60)) * time.Second
}
func (s SecurityConfig) block() time.Duration {
	return time.Duration(positiveOr(s.AuthBlockSeconds, 900)) * time.Second
}
func (s SecurityConfig) maxSources() int   { return positiveOr(s.AuthMaxSources, 4096) }
func (s SecurityConfig) cacheEntries() int { return positiveOr(s.AuthCacheEntries, 2048) }
func (s SecurityConfig) logInterval() time.Duration {
	return time.Duration(positiveOr(s.AuthLogIntervalSeconds, 30)) * time.Second
}
