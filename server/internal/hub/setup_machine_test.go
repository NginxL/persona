package hub

import (
	"net"
	"net/netip"
	"testing"
)

// The LAN address setup writes into public_url and public.tls_listen (#11125): the default route's address when it is
// on a LAN interface, else the first LAN address, and never a VPN tunnel's, which a full-tunnel VPN puts on the default
// route and replaces on every reconnect.
func TestPickLANIPv4(t *testing.T) {
	v4 := func(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	wifi, ethernet, tunnel := v4(192, 168, 1, 20), v4(192, 168, 2, 30), v4(10, 8, 0, 5)
	loopback := localAddress{Addr: v4(127, 0, 0, 1), Interface: "lo0", Flags: net.FlagUp | net.FlagLoopback}
	vpn := localAddress{Addr: tunnel, Interface: "utun4", Flags: net.FlagUp | net.FlagPointToPoint | net.FlagMulticast}
	machine := []localAddress{
		loopback,
		{Addr: netip.MustParseAddr("fd00::20"), Interface: "en0", Flags: up},
		{Addr: wifi, Interface: "en0", Flags: up},
		{Addr: v4(172, 17, 0, 1), Interface: "docker0", Flags: up},
		{Addr: ethernet, Interface: "en5", Flags: up},
		vpn,
	}
	for _, c := range []struct {
		name  string
		route netip.Addr
		addrs []localAddress
		want  string
	}{
		{"the default route's interface", wifi, machine, wifi.String()},
		{"the default route's interface when it is not the first", ethernet, machine, ethernet.String()},
		{"a full-tunnel VPN: the first LAN address, not the tunnel's", tunnel, machine, wifi.String()},
		{"no default route", netip.Addr{}, machine, wifi.String()},
		{"a public address on the default route", v4(203, 0, 113, 9),
			[]localAddress{{Addr: v4(203, 0, 113, 9), Interface: "en0", Flags: up}, {Addr: ethernet, Interface: "en5", Flags: up}},
			ethernet.String()},
		{"an interface that is down", wifi,
			[]localAddress{{Addr: wifi, Interface: "en0", Flags: net.FlagBroadcast}, {Addr: ethernet, Interface: "en5", Flags: up}},
			ethernet.String()},
		{"only a VPN tunnel has a private address", tunnel, []localAddress{loopback, vpn}, ""},
	} {
		if got := pickLANIPv4(c.route, c.addrs); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
