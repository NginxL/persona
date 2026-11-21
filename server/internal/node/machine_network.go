package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	defaultNetworkProbeTimeout = 4 * time.Second
	defaultNetworkTotalTimeout = 12 * time.Second
	redactedProxyCredential    = "[redacted]"
)

type networkProbeTarget struct {
	ID       string
	Name     string
	Target   string
	Protocol string // "https" | "tcp"
}

// defaultNetworkProbeTargets is the fixed, safe allowlist of developer endpoints
// probed on demand by machine.network. Targets are never caller-supplied.
var defaultNetworkProbeTargets = []networkProbeTarget{
	{ID: "github_https", Name: "GitHub HTTPS", Target: "https://github.com", Protocol: "https"},
	{ID: "github_ssh_443", Name: "GitHub SSH (443)", Target: "ssh.github.com:443", Protocol: "tcp"},
	{ID: "npm_registry", Name: "npm Registry", Target: "https://registry.npmjs.org", Protocol: "https"},
	{ID: "go_proxy", Name: "Go Module Proxy", Target: "https://proxy.golang.org", Protocol: "https"},
	{ID: "pypi", Name: "PyPI", Target: "https://pypi.org", Protocol: "https"},
}

type machineNetworkOptions struct {
	getenv       func(string) string
	interfaces   func() ([]proto.MachineNetInterface, error)
	targets      []networkProbeTarget
	probeTimeout time.Duration
	totalTimeout time.Duration
	now          func() time.Time
	httpClient   func(proxyFn func(*http.Request) (*url.URL, error), timeout time.Duration) *http.Client
	tcpDial      func(ctx context.Context, addr string) (net.Conn, error)
}

func (n *Node) machineNetworkOptions() machineNetworkOptions {
	if n != nil && n.networkDiagAt != nil {
		return n.networkDiagAt()
	}
	return machineNetworkOptions{}
}

// machineNetwork handles the read-only on-demand machine.network RPC.
func (n *Node) machineNetwork(ctx context.Context, params json.RawMessage) (any, error) {
	if err := validateMachineNetworkParams(params); err != nil {
		return nil, err
	}
	return runMachineNetworkDiagnostics(ctx, n.machineNetworkOptions()), nil
}

func validateMachineNetworkParams(params json.RawMessage) error {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var p proto.MachineNetworkParams
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return errors.New("bad machine.network params")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("bad machine.network params")
	}
	return nil
}

type rawProxyEnv struct {
	httpProxy  string
	httpsProxy string
	allProxy   string
	noProxy    string
}

func runMachineNetworkDiagnostics(ctx context.Context, opts machineNetworkOptions) proto.MachineNetworkReport {
	getenv := opts.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	nowFn := opts.now
	if nowFn == nil {
		nowFn = time.Now
	}
	probeTimeout := opts.probeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultNetworkProbeTimeout
	}
	totalTimeout := opts.totalTimeout
	if totalTimeout <= 0 {
		totalTimeout = defaultNetworkTotalTimeout
	}
	targets := opts.targets
	if targets == nil {
		targets = defaultNetworkProbeTargets
	}
	ifaceFn := opts.interfaces
	if ifaceFn == nil {
		ifaceFn = collectNetworkInterfaces
	}

	raw := readProxyEnv(getenv)
	proxyCfg := sanitizeProxyEnv(raw)
	secrets := extractProxySecrets(raw)
	proxyLookup := buildProxyFunc(raw)

	ifaces, err := ifaceFn()
	if err != nil || ifaces == nil {
		ifaces = []proto.MachineNetInterface{}
	}

	totalCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	probes := make([]proto.MachineEndpointProbe, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, tgt networkProbeTarget) {
			defer wg.Done()
			probes[idx] = probeNetworkTarget(totalCtx, tgt, probeTimeout, proxyLookup, secrets, opts)
		}(i, target)
	}
	wg.Wait()

	return proto.MachineNetworkReport{
		CheckedAt:  nowFn().UTC().Unix(),
		Interfaces: ifaces,
		Proxy:      proxyCfg,
		Probes:     probes,
	}
}

func firstNonEmptyEnv(getenv func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func readProxyEnv(getenv func(string) string) rawProxyEnv {
	return rawProxyEnv{
		httpProxy:  firstNonEmptyEnv(getenv, "HTTP_PROXY", "http_proxy"),
		httpsProxy: firstNonEmptyEnv(getenv, "HTTPS_PROXY", "https_proxy"),
		allProxy:   firstNonEmptyEnv(getenv, "ALL_PROXY", "all_proxy"),
		noProxy:    firstNonEmptyEnv(getenv, "NO_PROXY", "no_proxy"),
	}
}

func sanitizeProxyEnv(raw rawProxyEnv) proto.MachineProxyConfig {
	cfg := proto.MachineProxyConfig{
		HTTPProxy:  redactProxyValue(raw.httpProxy),
		HTTPSProxy: redactProxyValue(raw.httpsProxy),
		ALLProxy:   redactProxyValue(raw.allProxy),
		NOProxy:    sanitizeNoProxy(raw.noProxy),
	}
	cfg.Active = cfg.HTTPProxy != "" || cfg.HTTPSProxy != "" || cfg.ALLProxy != ""
	return cfg
}

func sanitizeNoProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "@?#") {
			p = redactProxyValue(p)
		}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

func validURLScheme(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		isExtra := c == '+' || c == '-' || c == '.'
		if i == 0 {
			if !isLetter {
				return false
			}
		} else if !isLetter && !isDigit && !isExtra {
			return false
		}
	}
	return true
}

// redactProxyValue strips any embedded username/password (up to the last '@')
// as well as query/fragment components that could hold tokens. Works even when
// a password contains '/', '?', '#', or '@', or when the scheme is omitted.
func redactProxyValue(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	scheme := ""
	rest := s
	if idx := strings.Index(s, "://"); idx > 0 && validURLScheme(s[:idx]) {
		scheme = s[:idx+3]
		rest = s[idx+3:]
	}
	redactedAuth := false
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
		redactedAuth = true
	}
	if cut := strings.IndexAny(rest, "?#"); cut >= 0 {
		rest = rest[:cut]
	}
	rest = strings.TrimSpace(rest)
	if redactedAuth {
		if rest == "" {
			return scheme + redactedProxyCredential
		}
		return scheme + redactedProxyCredential + "@" + rest
	}
	return scheme + rest
}

var (
	schemeCredRE = lazyre.New(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s"'<>@]+@`)
	bareCredRE   = lazyre.New(`\b[^\s:/@"'<>]+:[^\s/@"'<>]+@`)
)

func extractProxySecrets(raw rawProxyEnv) []string {
	var secrets []string
	seen := map[string]bool{}
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		secrets = append(secrets, v)
	}
	for _, val := range []string{raw.httpProxy, raw.httpsProxy, raw.allProxy} {
		s := strings.TrimSpace(val)
		if s == "" {
			continue
		}
		rest := s
		if idx := strings.Index(s, "://"); idx > 0 && validURLScheme(s[:idx]) {
			rest = s[idx+3:]
		}
		if at := strings.LastIndex(rest, "@"); at > 0 {
			add(s)
			userinfo := rest[:at]
			add(userinfo + "@")
			add(userinfo)
			if colon := strings.Index(userinfo, ":"); colon >= 0 {
				user := userinfo[:colon]
				pass := userinfo[colon+1:]
				if len(pass) >= 3 {
					add(pass)
					add(url.QueryEscape(pass))
					add(url.UserPassword(user, pass).String())
				}
				if len(user) >= 4 {
					add(user)
				}
			} else if len(userinfo) >= 4 {
				add(userinfo)
			}
		}
		if cut := strings.IndexAny(rest, "?#"); cut >= 0 {
			add(s)
			if tail := rest[cut:]; len(tail) > 1 {
				add(tail)
			}
		}
	}
	// Replace longest secrets first so composite values are masked cleanly.
	slices.SortFunc(secrets, func(a, b string) int {
		if len(a) != len(b) {
			return cmp.Compare(len(b), len(a))
		}
		return cmp.Compare(a, b)
	})
	return secrets
}

func redactErrorText(errText string, secrets []string) string {
	out := strings.TrimSpace(errText)
	if out == "" {
		return ""
	}
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, redactedProxyCredential)
		}
	}
	out = schemeCredRE.ReplaceAllString(out, "${1}"+redactedProxyCredential+"@")
	out = bareCredRE.ReplaceAllString(out, redactedProxyCredential+"@")
	return out
}

func buildProxyFunc(raw rawProxyEnv) func(*url.URL) (*url.URL, error) {
	cfg := &httpproxy.Config{
		HTTPProxy:  raw.httpProxy,
		HTTPSProxy: raw.httpsProxy,
		NoProxy:    raw.noProxy,
	}
	base := cfg.ProxyFunc()
	allCfg := &httpproxy.Config{
		HTTPProxy:  raw.allProxy,
		HTTPSProxy: raw.allProxy,
		NoProxy:    raw.noProxy,
	}
	allBase := allCfg.ProxyFunc()
	return func(u *url.URL) (*url.URL, error) {
		if u == nil {
			return nil, nil
		}
		p, err := base(u)
		if err != nil || p != nil {
			return p, err
		}
		if raw.allProxy != "" {
			return allBase(u)
		}
		return nil, nil
	}
}

func probeNetworkTarget(
	ctx context.Context,
	tgt networkProbeTarget,
	timeout time.Duration,
	proxyLookup func(*url.URL) (*url.URL, error),
	secrets []string,
	opts machineNetworkOptions,
) proto.MachineEndpointProbe {
	res := proto.MachineEndpointProbe{
		ID:       tgt.ID,
		Name:     tgt.Name,
		Target:   tgt.Target,
		Protocol: tgt.Protocol,
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()

	switch tgt.Protocol {
	case "tcp":
		dial := opts.tcpDial
		if dial == nil {
			d := &net.Dialer{Timeout: timeout}
			dial = func(c context.Context, addr string) (net.Conn, error) {
				return d.DialContext(c, "tcp", addr)
			}
		}
		conn, err := dial(pctx, tgt.Target)
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			res.Error = redactErrorText(err.Error(), secrets)
			return res
		}
		_ = conn.Close()
		res.Reachable = true
		return res

	default: // "https"
		parsed, err := url.Parse(tgt.Target)
		if err != nil {
			res.LatencyMS = time.Since(start).Milliseconds()
			res.Error = redactErrorText(err.Error(), secrets)
			return res
		}
		if proxyURL, pErr := proxyLookup(parsed); pErr == nil && proxyURL != nil {
			res.ViaProxy = true
		}

		proxyFn := func(req *http.Request) (*url.URL, error) {
			return proxyLookup(req.URL)
		}
		var client *http.Client
		var transport *http.Transport
		if opts.httpClient != nil {
			client = opts.httpClient(proxyFn, timeout)
		} else {
			transport = &http.Transport{
				Proxy:                 proxyFn,
				DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
				TLSHandshakeTimeout:   timeout,
				ResponseHeaderTimeout: timeout,
				ForceAttemptHTTP2:     true,
				DisableKeepAlives:     true,
			}
			client = &http.Client{
				Transport: transport,
				Timeout:   timeout,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
		}
		if transport != nil {
			defer transport.CloseIdleConnections()
		}

		req, err := http.NewRequestWithContext(pctx, http.MethodHead, tgt.Target, nil)
		if err != nil {
			res.LatencyMS = time.Since(start).Milliseconds()
			res.Error = redactErrorText(err.Error(), secrets)
			return res
		}
		resp, err := client.Do(req)
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			res.Error = redactErrorText(err.Error(), secrets)
			return res
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		res.StatusCode = resp.StatusCode
		if resp.StatusCode == http.StatusProxyAuthRequired || resp.StatusCode >= 500 {
			res.Reachable = false
			res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			return res
		}
		res.Reachable = true
		return res
	}
}

func collectNetworkInterfaces() ([]proto.MachineNetInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]proto.MachineNetInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var ipv4s, ipv6s []string
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ipv4s = append(ipv4s, ip4.String())
			} else {
				ipv6s = append(ipv6s, ip.String())
			}
		}
		if len(ipv4s) == 0 && len(ipv6s) == 0 {
			continue
		}
		slices.Sort(ipv4s)
		slices.Sort(ipv6s)
		combined := slices.Concat(ipv4s, ipv6s)
		out = append(out, proto.MachineNetInterface{
			Name:      iface.Name,
			Up:        iface.Flags&net.FlagUp != 0,
			Addresses: combined,
		})
	}
	slices.SortFunc(out, func(a, b proto.MachineNetInterface) int {
		if a.Up != b.Up {
			if a.Up {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out, nil
}
