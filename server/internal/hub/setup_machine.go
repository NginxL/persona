package hub

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/pgdocker"
)

// --- the machine --------------------------------------------------------------------------------------------------

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // docker run may pull the image
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func runOnTerminal(ctx context.Context, name string, args ...string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	return cmd.Run()
}

// askTerminal asks a yes / no question on the terminal (install.sh runs as `curl | sh`: stdin is the script).
func askTerminal(question string) (bool, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [Y/n] ", question)
	var answer string
	_, _ = fmt.Fscanln(tty, &answer)
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return true, true
	}
	return false, true
}

func tcpPortFree(port int) bool {
	ln, err := osproc.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func pingPostgres(ctx context.Context, dbURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return conn.Ping(ctx)
}

// defaultLANIPv4 is this machine's private IPv4 address on the interface of the default route (several interfaces:
// the one traffic goes out of), else the first private one on an interface that is up and not virtual (pickLANIPv4).
func defaultLANIPv4() string {
	var route netip.Addr
	// A UDP "connect" only picks the route: nothing is sent (192.0.2.1 is TEST-NET-1).
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		route = c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
		c.Close()
	}
	addrs, err := allInterfaceAddresses()
	if err != nil {
		return ""
	}
	return pickLANIPv4(route, addrs)
}

// pickLANIPv4 is route when it is a private IPv4 address on an interface that is up and not virtual, else the first
// such address in addrs, else "". A full-tunnel VPN takes the default route, and its tunnel's address is no LAN address
// (#11125): many VPN clients give the tunnel a new one on every reconnect, and a public_url and a TLS listener bound to
// the old one reach nothing, not even from the LAN.
func pickLANIPv4(route netip.Addr, addrs []localAddress) string {
	var first netip.Addr
	for _, a := range addrs {
		ip := a.Addr.Unmap()
		if !ip.Is4() || !ip.IsPrivate() || a.Flags&net.FlagUp == 0 || a.Flags&net.FlagLoopback != 0 || virtualInterfaceName(a.Interface) {
			continue
		}
		if ip == route {
			return ip.String()
		}
		if !first.IsValid() {
			first = ip
		}
	}
	if !first.IsValid() {
		return ""
	}
	return first.String()
}

// allInterfaceAddresses lists the addresses of every interface, virtual ones included, in the system's order.
func allInterfaceAddresses() ([]localAddress, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []localAddress
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					out = append(out, localAddress{Addr: ip.Unmap(), Interface: ifc.Name, Flags: ifc.Flags})
				}
			}
		}
	}
	return out, nil
}

func virtualInterfaceName(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "bridge", "cni", "flannel", "podman"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return vpnInterface(name)
}

func setupFirstLine(s string) string { return pgdocker.FirstLine(s) }
