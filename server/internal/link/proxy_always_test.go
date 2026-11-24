package link

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Proxy-only mode (Config.ProxyAlways, #12299): active mode goes only through the outbound proxy. These tests keep the
// hub's direct address reachable and count every TLS dial to it, so "only through the proxy" is measured.

// proxyOnlySetup is a hub reachable directly (counted by direct) and through proxy, and a link.json at path paired
// with it, the proxy saved and proxy-only mode as on says.
func proxyOnlySetup(t *testing.T, on bool) (hub *sshTestHub, direct *gatedDirectListener, proxy *connectProxy, ep Endpoint, path string) {
	t.Helper()
	host := proxyableLocalhost(t)
	hub = newSSHTestHub(t, "")
	direct = newGatedDirectListener(t, hub.target, true)
	proxy = newConnectProxy(t, hub.target, nil)
	ep = Endpoint{URL: "https://" + host + ":" + portOf(direct.url), Kind: "name"}
	path = filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		*c = Config{Hub: "h1", Machine: "laptop", FP: hub.pin, Token: "jd_ssh", Proxy: proxy.url, ProxyAlways: on, Endpoints: []Endpoint{ep}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRouteOrderProxyOnly(t *testing.T) {
	eps := []Endpoint{{URL: "https://hub.example.com", Kind: "name"}}
	c := &Config{Endpoints: eps, ProxyAlways: true}
	for _, last := range []string{"", "name", KindProxy, KindSSH} {
		if got := routeOrder(c, true, last); !slices.Equal(got, []string{KindProxy}) {
			t.Errorf("proxy-only, last %q: %v", last, got)
		}
		// Without a proxy for the candidates the order stays proxy-only: dialActive says why rather than dialing direct.
		if got := routeOrder(c, false, last); !slices.Equal(got, []string{KindProxy}) {
			t.Errorf("proxy-only without a proxy, last %q: %v", last, got)
		}
	}
	// A tunnel it need not use is left out, with candidates or without.
	c.SSH = &SSH{Server: "user@example.com"}
	if got := routeOrder(c, true, KindSSH); !slices.Equal(got, []string{KindProxy}) {
		t.Errorf("proxy-only with SSH: %v", got)
	}
	noCandidates := &Config{SSH: &SSH{Server: "user@example.com"}, ProxyAlways: true}
	if got := routeOrder(noCandidates, false, ""); !slices.Equal(got, []string{KindProxy}) {
		t.Errorf("proxy-only with SSH and no candidates: %v", got)
	}
	// Always use SSH wins over a hand-edited link.json that has both.
	c.SSH.Always = true
	if got := routeOrder(c, true, KindProxy); !slices.Equal(got, []string{KindSSH}) {
		t.Errorf("both set: %v", got)
	}
	// Off: the order of today.
	off := &Config{Endpoints: eps, SSH: &SSH{Server: "user@example.com"}}
	if got := routeOrder(off, true, ""); !slices.Equal(got, []string{"", KindProxy, KindSSH}) {
		t.Errorf("off: %v", got)
	}
}

// The first dial, a stream on the link, and a reconnect after the hub restarts all go through the proxy, with the
// hub's direct address answering and never dialed.
func TestProxyOnlyCarriesTheLinkStreamsAndReconnects(t *testing.T) {
	hub, direct, proxy, ep, path := proxyOnlySetup(t, true)
	node := newFakeNode(t)
	ch, _ := runLink(t, Options{Path: path, Target: node.path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: time.Hour})

	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != KindProxy || st.Address != ep.URL || st.Proxy != RedactedProxy(proxy.url) {
		t.Fatalf("status %+v", st)
	}
	first := <-hub.links
	sendJSON(t, first.conn, `{"type":"open","id":"s1"}`)
	var stream *websocket.Conn
	select {
	case stream = <-hub.streams:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not reach the hub")
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, b, err := stream.Read(rctx)
	rcancel()
	if err != nil || string(b) != nodeHello {
		t.Fatalf("stream: %q %v", b, err)
	}
	_ = stream.CloseNow()
	if n := proxy.connects.Load(); n != 2 {
		t.Fatalf("CONNECTs for the link and its stream: %d", n)
	}

	_ = first.conn.Close(websocket.StatusGoingAway, "restart")
	again := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind == KindProxy && s.Conn > st.Conn })
	<-hub.links
	if n := proxy.connects.Load(); n != 3 {
		t.Fatalf("CONNECTs after the reconnect: %d", n)
	}

	// A new proxy URL saved while connected moves the link to the new proxy, still never dialing direct: serve
	// follows the proxy setting on a link through the proxy, not only on a fallback one.
	other := newConnectProxy(t, hub.target, nil)
	if err := SetProxy(path, other.url); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Conn > again.Conn })
	if moved.Kind != KindProxy || moved.Address != ep.URL || moved.Proxy != RedactedProxy(other.url) {
		t.Fatalf("after a new proxy URL: %+v", moved)
	}
	<-hub.links
	if n := other.connects.Load(); n == 0 {
		t.Fatal("the link did not go through the new proxy")
	}
	if n := direct.attempts.Load(); n != 0 {
		t.Fatalf("proxy-only dialed the hub directly %d times", n)
	}
}

// On a proxy-only link neither the periodic direct retry nor a network change dials the hub directly.
func TestProxyOnlyNeverProbesDirect(t *testing.T) {
	hub, direct, _, _, path := proxyOnlySetup(t, true)
	check := make(chan struct{}, 1)
	ch, _ := runLink(t, Options{Path: path, Check: check, PairCheck: 30 * time.Millisecond, DirectRetryEvery: 50 * time.Millisecond})
	if st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" }); st.Kind != KindProxy {
		t.Fatalf("status %+v", st)
	}
	over := <-hub.links
	// The network changed, three times: Check holds one, so the fourth send returns once the loop has taken three.
	for range 4 {
		check <- struct{}{}
	}
	time.Sleep(500 * time.Millisecond) // sleep-ok: no event marks a direct dial that never happens; ten retry periods
	if n := direct.attempts.Load(); n != 0 {
		t.Fatalf("proxy-only probed the hub directly %d times", n)
	}
	select {
	case <-over.ended:
		t.Fatal("the link through the proxy was dropped")
	default:
	}
}

// The backoff probe looks only through the proxy: a direct address that answers does not count, and without a proxy
// for the candidates there is no probe.
func TestHubProbeProxyOnly(t *testing.T) {
	hub, direct, proxy, ep, _ := proxyOnlySetup(t, true)
	c := &Config{FP: hub.pin, Proxy: proxy.url, ProxyAlways: true, Endpoints: []Endpoint{ep}}
	probe := hubProbe(c)
	if probe == nil {
		t.Fatal("no probe through the proxy")
	}
	if !probe(t.Context()) || len(proxy.asked()) == 0 {
		t.Fatalf("hub health did not answer through the proxy (asked %v)", proxy.asked())
	}
	proxy.refuse.Store(http.StatusBadGateway)
	if probe(t.Context()) {
		t.Fatal("the direct address counted while the proxy refused")
	}
	if n := direct.attempts.Load(); n != 0 {
		t.Fatalf("the probe dialed the hub directly %d times", n)
	}
	// With the switch off the same configuration probes direct too (today's probe).
	off := *c
	off.ProxyAlways = false
	if probe := hubProbe(&off); probe == nil || !probe(t.Context()) || direct.attempts.Load() == 0 {
		t.Fatalf("off: no direct probe (attempts %d)", direct.attempts.Load())
	}
	t.Setenv("NO_PROXY", strings.Split(strings.TrimPrefix(ep.URL, "https://"), ":")[0])
	if hubProbe(c) != nil {
		t.Fatal("a probe without a proxy for the candidates")
	}
}

// Without a usable proxy at run time (a hand-edited "off", NO_PROXY covering every candidate, or loopback-only
// candidates) proxy-only mode says so and never dials direct.
func TestProxyOnlyWithoutAProxy(t *testing.T) {
	hub, direct, proxy, ep, path := proxyOnlySetup(t, true)
	host := strings.Split(strings.TrimPrefix(ep.URL, "https://"), ":")[0]
	c := &Config{Hub: "h1", Machine: "laptop", FP: hub.pin, Token: "jd_ssh", Proxy: proxy.url, ProxyAlways: true, Endpoints: []Endpoint{ep}}
	try := func(name string, c *Config) {
		t.Helper()
		l, err := dialActive(context.Background(), c, "", "")
		if err == nil {
			l.closeNow()
			t.Fatalf("%s: connected (%s)", name, l.ep.Kind)
		}
		if !errors.Is(err, errProxyOnlyNone) || !strings.Contains(err.Error(), defaultText("link-errors.proxy_only_none")) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	off := *c
	off.Proxy = ProxyOff
	t.Setenv("HTTPS_PROXY", proxy.url) // off means not even the environment's
	try("off", &off)
	t.Setenv("HTTPS_PROXY", "")
	loopback := *c
	loopback.Endpoints = []Endpoint{{URL: direct.url, Kind: "lan"}}
	try("loopback", &loopback)
	t.Setenv("NO_PROXY", host)
	try("NO_PROXY", c)

	// Running: the clear error in the status (in other languages too), and still no direct dial.
	ch, _ := runLink(t, Options{Path: path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: 50 * time.Millisecond})
	st := waitFor(t, ch, func(s Status) bool { return s.State == "waiting" && s.Error != "" })
	if !strings.Contains(st.Error, defaultText("link-errors.proxy_only_none")) ||
		!strings.Contains(StatusErrorText(i18n.En, st.Error), i18n.T(i18n.En, "link-errors.proxy_only_none")) {
		t.Fatalf("status error %q (en %q)", st.Error, StatusErrorText(i18n.En, st.Error))
	}
	if direct.attempts.Load() != 0 || proxy.connects.Load() != 0 {
		t.Fatalf("direct %d, CONNECTs %d", direct.attempts.Load(), proxy.connects.Load())
	}
}

// Turning the switch on while connected directly moves the link to the proxy, which then stays; turning it off
// reconnects, and direct candidates take over again as before.
func TestProxyOnlyTurnedOnAndOffWhileConnected(t *testing.T) {
	hub, direct, proxy, ep, path := proxyOnlySetup(t, false)
	ch, _ := runLink(t, Options{Path: path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: 50 * time.Millisecond})
	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != "name" || st.Address != ep.URL || proxy.connects.Load() != 0 {
		t.Fatalf("off, direct first: %+v (CONNECTs %d)", st, proxy.connects.Load())
	}
	directLink := <-hub.links

	attempts := direct.attempts.Load()
	if err := SetProxyAlways(path, true); err != nil {
		t.Fatal(err)
	}
	on := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Conn > st.Conn })
	if on.Kind != KindProxy {
		t.Fatalf("on: %+v", on)
	}
	<-hub.links
	select {
	case <-directLink.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the direct link stayed open")
	}
	time.Sleep(500 * time.Millisecond) // sleep-ok: no event marks a direct dial that never happens; ten retry periods
	if got := direct.attempts.Load(); got != attempts {
		t.Fatalf("proxy-only dialed direct: %d → %d", attempts, got)
	}

	if err := SetProxyAlways(path, false); err != nil {
		t.Fatal(err)
	}
	back := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind != KindProxy })
	if back.Kind != "name" || back.Address != ep.URL || back.Proxy != "" {
		t.Fatalf("back direct: %+v", back)
	}
}

// Turned off while the hub's direct address is down, proxy-only mode leaves the link on the proxy as an ordinary
// fallback: the periodic direct retry runs again, and moves the link back once the direct address answers.
func TestProxyOnlyTurnedOffFallsBackThenReturnsDirect(t *testing.T) {
	hub, direct, proxy, ep, path := proxyOnlySetup(t, true)
	ch, _ := runLink(t, Options{Path: path, PairCheck: 30 * time.Millisecond, DirectRetryEvery: 50 * time.Millisecond})
	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != KindProxy {
		t.Fatalf("on: %+v", st)
	}
	<-hub.links

	direct.allowed.Store(false)
	if err := SetProxyAlways(path, false); err != nil {
		t.Fatal(err)
	}
	fallback := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Conn > st.Conn })
	if fallback.Kind != KindProxy || fallback.Proxy != RedactedProxy(proxy.url) {
		t.Fatalf("off, direct down: %+v", fallback)
	}
	overProxy := <-hub.links
	// The direct retry is back: it dials the hub directly, is refused, and the link stays on the proxy.
	tried := direct.attempts.Load()
	eventually(t, func() bool { return direct.attempts.Load() >= tried+2 })
	select {
	case <-overProxy.ended:
		t.Fatal("a refused direct retry dropped the link through the proxy")
	default:
	}

	direct.allowed.Store(true)
	back := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind != KindProxy })
	if back.Kind != "name" || back.Address != ep.URL || back.Proxy != "" {
		t.Fatalf("back direct: %+v", back)
	}
	<-hub.links
	select {
	case <-overProxy.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the link through the proxy stayed open")
	}
}

// Proxy-only mode turned on just before the hub replaces the link (4001) cuts replaceWait short: after the link
// ends, a proxy_always that differs from the one it ran with counts as a change written while it was up, so the next
// look at link.json comes back at once, through the proxy.
func TestProxyOnlyTurnedOnBeforeAReplacedLinkComesBack(t *testing.T) {
	hub, _, _, _, path := proxyOnlySetup(t, false)
	// PairCheck paces both the connected link's look at link.json and the wait's: a second leaves the write below
	// to the wait after the link nearly always.
	ch, _ := runLink(t, Options{Path: path, PairCheck: time.Second, DirectRetryEvery: time.Hour})
	st := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if st.Kind != "name" {
		t.Fatalf("off, direct first: %+v", st)
	}
	first := <-hub.links
	if err := SetProxyAlways(path, true); err != nil {
		t.Fatal(err)
	}
	_ = first.conn.Close(CloseReplaced, "replaced")
	back := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Conn > st.Conn })
	if back.Kind != KindProxy {
		t.Fatalf("after the replaced link: %+v", back)
	}
}

func TestRepairedFollowsProxyAlways(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	ep := Endpoint{URL: unroutable, Kind: "ipv4"}
	c := &Config{Hub: "h1", Machine: "laptop", FP: "pin", Token: "jd_x", Proxy: "http://proxy.example:8080", Endpoints: []Endpoint{ep}}
	if err := Update(path, func(f *Config) error { *f = *c; return nil }); err != nil {
		t.Fatal(err)
	}
	if repaired(path, c, false) || directHandoffConfig(path, c, ep) == nil {
		t.Fatal("an unchanged link.json")
	}
	if err := SetProxyAlways(path, true); err != nil {
		t.Fatal(err)
	}
	// Turned on: every link moves, a direct one included; a direct candidate found on a fallback route is not taken.
	if !repaired(path, c, false) || !repaired(path, c, true) {
		t.Fatal("turning proxy-only mode on did not move the link")
	}
	if directHandoffConfig(path, c, ep) != nil {
		t.Fatal("a direct handoff under proxy-only mode")
	}
	// On, and on the proxy: a new proxy URL is followed.
	on := *c
	on.ProxyAlways = true
	if repaired(path, &on, true) {
		t.Fatal("an unchanged proxy-only link.json")
	}
	if err := SetProxy(path, "http://other.example:3128"); err != nil {
		t.Fatal(err)
	}
	if !repaired(path, &on, true) {
		t.Fatal("a proxy-only link kept the old proxy")
	}
	// Turned off: the link moves, and direct candidates are back.
	if err := SetProxyAlways(path, false); err != nil {
		t.Fatal(err)
	}
	on.Proxy = "http://other.example:3128"
	if !repaired(path, &on, true) {
		t.Fatal("turning proxy-only mode off did not move the link")
	}
}

// Pairing with proxy-only mode on goes only through the proxy, the hub's direct address answering, and keeps the mode.
func TestPairProxyOnly(t *testing.T) {
	host := proxyableLocalhost(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, true)
	proxy := newConnectProxy(t, hub.target, nil)
	path := filepath.Join(t.TempDir(), "link.json")
	if err := SetProxy(path, proxy.url); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyAlways(path, true); err != nil {
		t.Fatal(err)
	}
	rawLink := "persona://link?" + url.Values{
		"url": {"http://" + host + ":1"}, "tls": {portOf(direct.url)}, "fp": {hub.pin}, "hub": {"h1"}, "token": {"jp_pair"}, "machine": {"laptop"},
	}.Encode()
	// Always use SSH cannot come with it.
	if _, err := PairWithSSH(context.Background(), rawLink, "", "", path, &SSH{Server: "user@example.com", Always: true}); !errors.Is(err, ErrSSHAlwaysProxyAlways) {
		t.Fatalf("pair with Always use SSH: %v", err)
	}
	c, err := Pair(context.Background(), rawLink, "", "", path)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if c.Token != "jd_ssh" || direct.attempts.Load() != 0 || proxy.connects.Load() != 1 {
		t.Fatalf("paired %+v (direct %d, CONNECTs %d)", c, direct.attempts.Load(), proxy.connects.Load())
	}
	if f, err := Load(path); err != nil || f.Proxy != proxy.url || !f.ProxyAlways || f.Token != "jd_ssh" {
		t.Fatalf("link.json after pairing: %+v %v", f, err)
	}
}

func TestSetProxyAlways(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	read := func() *Config {
		t.Helper()
		c, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// A URL saved in link.json first: not set, off, or only in the environment are refused.
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	if err := SetProxyAlways(path, true); !errors.Is(err, ErrProxyAlwaysNeedsURL) {
		t.Fatalf("no proxy saved: %v", err)
	}
	if err := SetProxy(path, ProxyOff); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyAlways(path, true); !errors.Is(err, ErrProxyAlwaysNeedsURL) || read().ProxyAlways {
		t.Fatalf("proxy off: %v", err)
	}
	if err := SetProxy(path, "http://proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyAlways(path, true); err != nil || !read().ProxyAlways {
		t.Fatalf("on: %v", err)
	}
	// A new URL keeps it; --off and --env turn it off.
	if err := SetProxy(path, "http://other.example:8080"); err != nil || !read().ProxyAlways {
		t.Fatalf("a new URL: %v", err)
	}
	for _, raw := range []string{ProxyOff, ""} {
		if err := SetProxy(path, "http://proxy.example:8080"); err != nil {
			t.Fatal(err)
		}
		if err := SetProxyAlways(path, true); err != nil {
			t.Fatal(err)
		}
		if err := SetProxy(path, raw); err != nil || read().ProxyAlways {
			t.Fatalf("SetProxy(%q) kept proxy-only mode: %v", raw, err)
		}
	}
	// Always use SSH and proxy-only mode exclude each other; an SSH tunnel it need not use does not.
	if err := SetProxy(path, "http://proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyAlways(path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSSH(path, &SSH{Server: "user@example.com", Target: "127.0.0.1:18663", Always: true}); !errors.Is(err, ErrSSHAlwaysProxyAlways) {
		t.Fatalf("SetSSH Always under proxy-only mode: %v", err)
	}
	if c := read(); c.SSH != nil {
		t.Fatalf("a refused SetSSH saved %+v", c.SSH)
	}
	if _, err := SetSSH(path, &SSH{Server: "user@example.com", Target: "127.0.0.1:18663"}); err != nil {
		t.Fatalf("SetSSH without Always: %v", err)
	}
	if err := SetProxyAlways(path, false); err != nil || read().ProxyAlways {
		t.Fatalf("off: %v", err)
	}
	if _, err := SetSSH(path, &SSH{Server: "user@example.com", Target: "127.0.0.1:18663", Always: true}); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyAlways(path, true); !errors.Is(err, ErrProxyAlwaysSSHAlways) || read().ProxyAlways {
		t.Fatalf("proxy-only under Always use SSH: %v", err)
	}
	// Turning it off is always allowed.
	if err := SetProxyAlways(path, false); err != nil {
		t.Fatal(err)
	}
	// The refusals say what to do, in every language.
	for _, err := range []error{ErrProxyAlwaysNeedsURL, ErrProxyAlwaysSSHAlways, ErrSSHAlwaysProxyAlways} {
		for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			if txt := ErrorText(l, err); !strings.Contains(txt, "persona-link") {
				t.Errorf("%s: %q", l, txt)
			}
		}
	}
}

// The switch is part of the route fingerprint, so a hint saved with it on does not order routes once it is off;
// while off the fingerprint is the one saved before the switch existed.
func TestRouteFingerprintFollowsProxyAlways(t *testing.T) {
	c := &Config{Hub: "h1", Machine: "laptop", Token: "jd_x", FP: "pin", Proxy: "http://proxy.example:8080"}
	proxy, noProxy := proxySettings(c)
	before := routeFingerprint(struct {
		Hub, Machine, Token, FP, Trust, ConfigProxy, Proxy, NoProxy string
		SSH                                                         *SSH
	}{Hub: c.Hub, Machine: c.Machine, Token: c.Token, FP: c.FP, Trust: c.Trust, ConfigProxy: c.Proxy, Proxy: proxy, NoProxy: noProxy, SSH: c.SSH})
	if got := pairingFingerprint(c); got != before {
		t.Fatal("off: the fingerprint changed from the one saved before proxy-only mode")
	}
	on := *c
	on.ProxyAlways = true
	if pairingFingerprint(&on) == before {
		t.Fatal("on: the fingerprint did not change")
	}
}
