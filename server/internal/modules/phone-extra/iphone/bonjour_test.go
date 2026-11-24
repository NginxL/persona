package iphone

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func rrHeader(name string, t dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.ClassINET, TTL: 120}
}

func response(t *testing.T, answers, additionals []dnsmessage.Resource) []byte {
	t.Helper()
	m := dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}, Answers: answers, Additionals: additionals}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const testInstance = "Phone One._remotepairing._tcp.local."

func fullResponse(t *testing.T, identifier string, port uint16, a4 [4]byte, a6 [16]byte) []byte {
	t.Helper()
	return response(t,
		[]dnsmessage.Resource{{Header: rrHeader(ServiceType, dnsmessage.TypePTR), Body: &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(testInstance)}}},
		[]dnsmessage.Resource{
			{Header: rrHeader(testInstance, dnsmessage.TypeSRV), Body: &dnsmessage.SRVResource{Port: port, Target: dnsmessage.MustNewName("phone-one.local.")}},
			{Header: rrHeader(testInstance, dnsmessage.TypeTXT), Body: &dnsmessage.TXTResource{TXT: []string{"identifier=" + identifier, "authTag=tag-of-" + identifier, "ver=1"}}},
			{Header: rrHeader("phone-one.local.", dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: a6}},
			{Header: rrHeader("phone-one.local.", dnsmessage.TypeA), Body: &dnsmessage.AResource{A: a4}},
		})
}

func TestCacheAssemblesService(t *testing.T) {
	c := newCache()
	ll := netip.MustParseAddr("fe80::1").As16()
	if !c.add(fullResponse(t, "ABC-123", 49152, [4]byte{192, 0, 2, 10}, ll), "en0") {
		t.Fatal("response not taken")
	}
	got := c.services()
	if len(got) != 1 {
		t.Fatalf("services = %+v", got)
	}
	s := got[0]
	if s.Port != 49152 || s.Host != "phone-one.local." || s.Identifier() != "ABC-123" || s.AuthTag() != "tag-of-ABC-123" || !s.usable() {
		t.Fatalf("service = %+v", s)
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("fe80::1%en0")}
	if len(s.Addrs) != 2 || s.Addrs[0] != want[0] || s.Addrs[1] != want[1] {
		t.Fatalf("addrs = %v, want %v (IPv4 first, link-local with its zone)", s.Addrs, want)
	}
	if len(c.missing()) != 0 {
		t.Fatalf("nothing missing, asked %v", c.missing())
	}
}

func TestCacheDropsLinkLocalWithoutZone(t *testing.T) {
	c := newCache()
	c.add(fullResponse(t, "X", 1, [4]byte{192, 0, 2, 11}, netip.MustParseAddr("fe80::2").As16()), "")
	if s := c.services(); len(s[0].Addrs) != 1 || !s[0].Addrs[0].Is4() {
		t.Fatalf("addrs = %v", s[0].Addrs)
	}
}

func TestCacheAsksForWhatIsMissing(t *testing.T) {
	c := newCache()
	c.add(response(t, []dnsmessage.Resource{{Header: rrHeader(ServiceType, dnsmessage.TypePTR), Body: &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(testInstance)}}}, nil), "")
	qs := c.missing()
	types := map[dnsmessage.Type]bool{}
	for _, q := range qs {
		types[q.Type] = true
		if q.Class&(1<<15) == 0 {
			t.Errorf("%v asked without the unicast-response bit", q)
		}
	}
	if !types[dnsmessage.TypeSRV] || !types[dnsmessage.TypeTXT] {
		t.Fatalf("missing() = %v", qs)
	}
	if c.services()[0].usable() {
		t.Fatal("a service without address, port and TXT can't be used")
	}
}

func TestCacheIgnoresQueries(t *testing.T) {
	q, _ := packQuery([]dnsmessage.Question{question(ServiceType, dnsmessage.TypePTR)})
	if newCache().add(q, "") {
		t.Fatal("a query (someone else's) was taken as a response")
	}
}

func TestFindPhoneAsksTheBridgeOncePerAdvertisement(t *testing.T) {
	other := Service{Instance: "a", Port: 1, TXT: map[string]string{"identifier": "1", "authtag": "x"}, Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.1")}}
	mine := Service{Instance: "b", Port: 2, TXT: map[string]string{"identifier": "2", "authtag": "TAG-1"}, Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.2")}}
	incomplete := Service{Instance: "c", TXT: map[string]string{"identifier": "3", "authtag": "TAG-1"}}
	var asked []string
	match := func(_ context.Context, path string, s Service) (bool, error) {
		asked = append(asked, s.Instance)
		return s.AuthTag() == "TAG-1" && path == "/rec", nil
	}
	stoppedEarly := false
	browse := func(ctx context.Context, wait time.Duration, done func([]Service) bool) ([]Service, error) {
		if done([]Service{other, incomplete}) || done([]Service{other, incomplete}) {
			t.Fatal("no match yet")
		}
		stoppedEarly = done([]Service{other, incomplete, mine})
		return []Service{other, incomplete, mine}, nil
	}
	got, err := findPhone(context.Background(), browse, match, "/rec")
	if err != nil || got == nil || got.Instance != "b" || !stoppedEarly {
		t.Fatalf("findPhone = %+v, %v (stopped early: %v)", got, err, stoppedEarly)
	}
	if strings.Join(asked, ",") != "a,b" {
		t.Fatalf("asked %v: once per usable advertisement", asked)
	}
	// None matches, and the bridge fails: its error is the reason.
	match = func(context.Context, string, Service) (bool, error) { return false, errors.New("bridge broke") }
	if got, err := findPhone(context.Background(), browse, match, "/rec"); got != nil || err == nil {
		t.Fatalf("findPhone = %+v, %v", got, err)
	}
}
