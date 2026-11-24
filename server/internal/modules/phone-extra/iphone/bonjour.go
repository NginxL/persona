package iphone

// Finding a paired phone on the network by its Bonjour advertisement, each time a session (re)connects: a phone's
// address on Wi-Fi changes with DHCP, so the address resolved once is never trusted for long. A one-shot multicast DNS
// query from an ephemeral port (RFC 6762 §5.1, answered by unicast), so it runs beside the system's own responder.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ServiceType is what a phone that can be reached by RemotePairing advertises.
const ServiceType = "_remotepairing._tcp.local."

var (
	mdnsV4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	mdnsV6 = net.ParseIP("ff02::fb")
)

// Service is one advertisement found.
type Service struct {
	Instance string            `json:"instance"`
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	TXT      map[string]string `json:"txt,omitempty"` // keys lowercased
	Addrs    []netip.Addr      `json:"addrs"`         // IPv4 first, link-local IPv6 last (with its zone)
}

// Identifier and AuthTag are the TXT values a phone is recognized by. The identifier changes; the tag is derived
// from it with a key kept in the pairing record, so only the bridge can tell whether an advertisement is the phone
// a record was made with (Matcher).
func (s Service) Identifier() string { return s.TXT["identifier"] }
func (s Service) AuthTag() string    { return s.TXT["authtag"] }

// usable: the service can be matched and connected to.
func (s Service) usable() bool {
	return s.Port > 0 && len(s.Addrs) > 0 && s.Identifier() != "" && s.AuthTag() != ""
}

// Browser finds the advertisements on the network. done, when not nil, ends the search early once it says the
// services so far are enough.
type Browser func(ctx context.Context, wait time.Duration, done func([]Service) bool) ([]Service, error)

// Browse is the Browser over multicast DNS on every multicast interface.
func Browse(ctx context.Context, wait time.Duration, done func([]Service) bool) ([]Service, error) {
	q, err := newQuerier()
	if err != nil {
		return nil, err
	}
	defer q.close()
	q.read()

	// First the instances (responders usually add SRV, TXT and addresses unasked); then what is still missing.
	q.send([]dnsmessage.Question{question(ServiceType, dnsmessage.TypePTR)})
	if q.collect(ctx, wait, done) {
		return q.c.services(), nil
	}
	if more := q.c.missing(); len(more) > 0 {
		q.send(more)
		q.collect(ctx, wait/2, done)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return q.c.services(), nil
}

// question asks name's type with the unicast-response bit, as a one-shot querier does.
func question(name string, t dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.ClassINET | 1<<15}
}

func packQuery(qs []dnsmessage.Question) ([]byte, error) {
	m := dnsmessage.Message{Questions: qs}
	return m.Pack()
}

type querier struct {
	v4  *ipv4.PacketConn
	v6  *ipv6.PacketConn
	ifs []net.Interface
	c   *cache
	got chan struct{} // a response arrived
	wg  sync.WaitGroup
}

func newQuerier() (*querier, error) {
	q := &querier{c: newCache(), got: make(chan struct{}, 1)}
	if c, err := net.ListenPacket("udp4", "0.0.0.0:0"); err == nil {
		q.v4 = ipv4.NewPacketConn(c)
	}
	if c, err := net.ListenPacket("udp6", "[::]:0"); err == nil {
		q.v6 = ipv6.NewPacketConn(c)
	}
	if q.v4 == nil && q.v6 == nil {
		return nil, errors.New("bonjour: no UDP socket")
	}
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		// Not loopback, and not point-to-point links (VPN tunnels): multicast DNS doesn't cross them.
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && ifi.Flags&(net.FlagLoopback|net.FlagPointToPoint) == 0 {
			q.ifs = append(q.ifs, ifi)
		}
	}
	return q, nil
}

func (q *querier) close() {
	if q.v4 != nil {
		q.v4.Close()
	}
	if q.v6 != nil {
		q.v6.Close()
	}
	q.wg.Wait()
}

// send sends the questions on every interface, both families. Failures on one interface don't matter.
func (q *querier) send(qs []dnsmessage.Question) {
	msg, err := packQuery(qs)
	if err != nil {
		return
	}
	for i := range q.ifs {
		ifi := &q.ifs[i]
		if q.v4 != nil && q.v4.SetMulticastInterface(ifi) == nil {
			_, _ = q.v4.WriteTo(msg, nil, mdnsV4)
		}
		if q.v6 != nil && q.v6.SetMulticastInterface(ifi) == nil {
			_, _ = q.v6.WriteTo(msg, nil, &net.UDPAddr{IP: mdnsV6, Port: 5353, Zone: ifi.Name})
		}
	}
}

// read takes responses from both sockets into the cache until they close.
func (q *querier) read() {
	if q.v4 != nil {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.readFrom(func(b []byte) (int, net.Addr, error) { n, _, src, err := q.v4.ReadFrom(b); return n, src, err })
		}()
	}
	if q.v6 != nil {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.readFrom(func(b []byte) (int, net.Addr, error) { n, _, src, err := q.v6.ReadFrom(b); return n, src, err })
		}()
	}
}

func (q *querier) readFrom(read func([]byte) (int, net.Addr, error)) {
	buf := make([]byte, 9000)
	for {
		n, src, err := read(buf)
		if err != nil {
			return
		}
		zone := ""
		if u, ok := src.(*net.UDPAddr); ok {
			zone = u.Zone
		}
		if q.c.add(buf[:n], zone) {
			select {
			case q.got <- struct{}{}:
			default:
			}
		}
	}
}

// collect waits wait (or for ctx), returning early with true once done says the services found are enough.
func (q *querier) collect(ctx context.Context, wait time.Duration, done func([]Service) bool) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-q.got:
			if done != nil && done(q.c.services()) {
				return true
			}
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// cache is every record seen, by lowercased name.
type cache struct {
	mu        sync.Mutex
	instances map[string]string // lowercased → as written
	srv       map[string]dnsmessage.SRVResource
	txt       map[string]map[string]string
	addrs     map[string][]netip.Addr
}

func newCache() *cache {
	return &cache{instances: map[string]string{}, srv: map[string]dnsmessage.SRVResource{}, txt: map[string]map[string]string{}, addrs: map[string][]netip.Addr{}}
}

// add takes the records of one response, zone being the interface a link-local sender came from. It says whether
// the response was one.
func (c *cache) add(packet []byte, zone string) bool {
	var m dnsmessage.Message
	if m.Unpack(packet) != nil || !m.Header.Response {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, group := range [][]dnsmessage.Resource{m.Answers, m.Authorities, m.Additionals} {
		for _, r := range group {
			name := strings.ToLower(r.Header.Name.String())
			switch b := r.Body.(type) {
			case *dnsmessage.PTRResource:
				if name == ServiceType {
					inst := b.PTR.String()
					c.instances[strings.ToLower(inst)] = inst
				}
			case *dnsmessage.SRVResource:
				c.srv[name] = *b
			case *dnsmessage.TXTResource:
				kv := map[string]string{}
				for _, s := range b.TXT {
					k, v, _ := strings.Cut(s, "=")
					kv[strings.ToLower(k)] = v
				}
				c.txt[name] = kv
			case *dnsmessage.AResource:
				c.addAddr(name, netip.AddrFrom4(b.A))
			case *dnsmessage.AAAAResource:
				a := netip.AddrFrom16(b.AAAA)
				if a.IsLinkLocalUnicast() {
					if zone == "" {
						continue // unusable without the interface it is on
					}
					a = a.WithZone(zone)
				}
				c.addAddr(name, a)
			}
		}
	}
	return true
}

func (c *cache) addAddr(host string, a netip.Addr) {
	for _, have := range c.addrs[host] {
		if have == a {
			return
		}
	}
	c.addrs[host] = append(c.addrs[host], a)
}

// missing is the questions for what the instances found still lack: their SRV and TXT, their hosts' addresses.
func (c *cache) missing() []dnsmessage.Question {
	c.mu.Lock()
	defer c.mu.Unlock()
	var qs []dnsmessage.Question
	for key, inst := range c.instances {
		if _, ok := c.srv[key]; !ok {
			qs = append(qs, question(inst, dnsmessage.TypeSRV))
		} else if host := strings.ToLower(c.srv[key].Target.String()); len(c.addrs[host]) == 0 {
			qs = append(qs, question(c.srv[key].Target.String(), dnsmessage.TypeA), question(c.srv[key].Target.String(), dnsmessage.TypeAAAA))
		}
		if _, ok := c.txt[key]; !ok {
			qs = append(qs, question(inst, dnsmessage.TypeTXT))
		}
	}
	return qs
}

// services is what the cache knows, by instance name.
func (c *cache) services() []Service {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Service
	for key, inst := range c.instances {
		s := Service{Instance: inst, TXT: c.txt[key]}
		if srv, ok := c.srv[key]; ok {
			s.Host, s.Port = srv.Target.String(), int(srv.Port)
			s.Addrs = append([]netip.Addr(nil), c.addrs[strings.ToLower(s.Host)]...)
			sort.SliceStable(s.Addrs, func(i, j int) bool { return addrRank(s.Addrs[i]) < addrRank(s.Addrs[j]) })
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

// addrRank orders addresses to try: IPv4, then routable IPv6, then link-local IPv6.
func addrRank(a netip.Addr) int {
	switch {
	case a.Is4():
		return 0
	case !a.IsLinkLocalUnicast():
		return 1
	default:
		return 2
	}
}
