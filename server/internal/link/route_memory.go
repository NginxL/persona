package link

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"slices"
)

type routeBinding struct {
	Pairing string `json:"pairing"`
	Network string `json:"network"`
}

type routeHint struct {
	Kind string `json:"kind"`
	routeBinding
}

func pairingFingerprint(c *Config) string {
	proxy, noProxy := proxySettings(c)
	// Include the configured proxy as well as the selected one so switching between an explicit URL and the
	// same environment URL resets the hint. Hub-supplied names and addresses can change on every connection.
	return routeFingerprint(struct {
		Hub, Machine, Token, FP, Trust, ConfigProxy, Proxy, NoProxy string
		SSH                                                         *SSH
	}{
		Hub: c.Hub, Machine: c.Machine, Token: c.Token, FP: c.FP, Trust: c.Trust,
		ConfigProxy: c.Proxy, Proxy: proxy, NoProxy: noProxy, SSH: c.SSH,
	})
}

func routeFingerprint(value any) string {
	data, _ := json.Marshal(value) // only strings and SSH's string and bool fields
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func routeNetwork(addresses func() ([]net.Addr, error)) string {
	addrs, err := addresses()
	if err != nil {
		return ""
	}
	var ips []string
	for _, a := range addrs {
		ip := interfaceIP(a)
		if !ip.IsGlobalUnicast() {
			continue
		}
		if ip.Is6() {
			// Temporary IPv6 addresses rotate their interface ID without changing the network prefix.
			ips = append(ips, netip.PrefixFrom(ip, 64).Masked().String())
		} else {
			ips = append(ips, ip.String())
		}
	}
	if len(ips) == 0 {
		return ""
	}
	slices.Sort(ips)
	return routeFingerprint(slices.Compact(ips))
}

func interfaceIP(a net.Addr) netip.Addr {
	p, _ := netip.ParsePrefix(a.String())
	return p.Addr().Unmap().WithZone("")
}

func isFallbackKind(kind string) bool { return kind == KindProxy || kind == KindSSH }

// routeMemory belongs to the active loop. While serve runs, only its network watcher may invalidate the
// hint after the initial hello. serve joins that watcher before the loop starts its next dial.
type routeMemory struct {
	path      string
	addresses func() ([]net.Addr, error)
	loaded    bool
	hint      routeHint
	binding   routeBinding // the pairing and network at the start of this dial
}

func (m *routeMemory) beforeDial(c *Config) string {
	if !m.loaded {
		m.hint = readRouteHint(m.path)
		m.loaded = true
	}
	m.binding = routeBinding{Pairing: pairingFingerprint(c), Network: routeNetwork(m.addresses)}
	if m.binding.Pairing != m.hint.Pairing {
		m.clear()
	}
	m.invalidateNetwork(m.binding.Network)
	if m.binding.Network == "" && m.hint.Network != "" {
		return "" // use default order until the saved network can be checked
	}
	return m.hint.Kind
}

func (m *routeMemory) connected(kind string) {
	if !isFallbackKind(kind) {
		m.clear()
		return
	}
	network := routeNetwork(m.addresses)
	m.invalidateNetwork(network)
	if network != m.binding.Network || (network == "" && m.hint.Network != "") {
		return // the dial's network is unconfirmed; do not replace a saved hint
	}
	hint := routeHint{Kind: kind, routeBinding: m.binding}
	if hint == m.hint {
		return
	}
	m.hint = hint
	if hint.Network == "" {
		return // within this run only when there is no usable address to match at startup
	}
	// Pairing and route edits use this same lock. Cache updates may be skipped when an edit is in flight.
	release, err := LockFile(m.path+".lock", false)
	if err != nil {
		return
	}
	defer release()
	c, err := Load(m.path)
	if err != nil || c.CurrentMode() != ModeActive || pairingFingerprint(c) != hint.Pairing {
		m.clear()
		return
	}
	_ = writeRouteHint(m.path, hint)
}

func (m *routeMemory) networkChanged() {
	if m.hint.Kind != "" {
		m.invalidateNetwork(routeNetwork(m.addresses))
	}
}

func (m *routeMemory) invalidateNetwork(network string) {
	if network != "" && network != m.hint.Network {
		m.clear()
	}
}

func (m *routeMemory) clear() {
	if m.hint.Kind != "" {
		removeRouteHint(m.path)
		m.hint = routeHint{}
	}
}
