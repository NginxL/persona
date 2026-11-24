package node

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// loopbackNoProxy keeps companion <-> agy acp loopback RPC/HTTP and local gate proxies off any system proxy.
	loopbackNoProxy = "localhost,127.0.0.1,::1"
)

var (
	// systemProxyTimeout caps how long macOS `scutil --proxy` may take when starting a companion.
	systemProxyTimeout = 2 * time.Second
	// detectSystemProxy reads the OS-level proxy settings; tests replace it to avoid querying the host OS.
	detectSystemProxy = readSystemProxy
)

// systemProxy holds validated OS-level proxy URLs and NO_PROXY exceptions.
type systemProxy struct {
	HTTP                   string
	HTTPS                  string
	SOCKS                  string
	Exceptions             []string
	ExcludeSimpleHostnames bool
}

func (p systemProxy) hasProxy() bool {
	return p.HTTP != "" || p.HTTPS != "" || p.SOCKS != ""
}

// withSystemProxyEnv falls back to OS system proxy settings (macOS `scutil --proxy`, no-op on other OSes) when base
// does not explicitly set any proxy variable (including an explicit empty value such as `HTTPS_PROXY=`).
func withSystemProxyEnv(ctx context.Context, base []string) []string {
	if hasExplicitProxyEnv(base) {
		return base
	}
	return applySystemProxyEnv(base, detectSystemProxy(ctx))
}

// applySystemProxyEnv merges validated system proxy settings into base without mutating base.
func applySystemProxyEnv(base []string, p systemProxy) []string {
	if !p.hasProxy() || hasExplicitProxyEnv(base) {
		return base
	}
	env := slices.Clone(base)
	if p.HTTP != "" {
		env = setEnvVar(env, "HTTP_PROXY", p.HTTP)
		env = setEnvVar(env, "http_proxy", p.HTTP)
	}
	if p.HTTPS != "" {
		env = setEnvVar(env, "HTTPS_PROXY", p.HTTPS)
		env = setEnvVar(env, "https_proxy", p.HTTPS)
	}
	if p.SOCKS != "" {
		env = setEnvVar(env, "ALL_PROXY", p.SOCKS)
		env = setEnvVar(env, "all_proxy", p.SOCKS)
	}
	noProxy := mergeNoProxy(lastEnvValue(env, "NO_PROXY"), lastEnvValue(env, "no_proxy"))
	noProxy = mergeNoProxy(noProxy, loopbackNoProxy)
	if len(p.Exceptions) > 0 {
		noProxy = mergeNoProxy(noProxy, strings.Join(p.Exceptions, ","))
	}
	if p.ExcludeSimpleHostnames {
		if host := simpleGatewayHost(lastEnvValue(env, "GOOGLE_GEMINI_BASE_URL")); host != "" {
			noProxy = mergeNoProxy(noProxy, host)
		}
	}
	env = setEnvVar(env, "NO_PROXY", noProxy)
	env = setEnvVar(env, "no_proxy", noProxy)
	return env
}

// hasExplicitProxyEnv reports whether env explicitly names any HTTP/HTTPS/ALL proxy variable, even with an empty
// value (e.g. a launchd plist or managed launcher clearing a proxy with `HTTPS_PROXY=`).
func hasExplicitProxyEnv(env []string) bool {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(proxyVars, k) {
			return true
		}
	}
	return false
}

// lastEnvValue returns the last value set for exact variable name key in env, or "" if absent.
func lastEnvValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], prefix); ok {
			return v
		}
	}
	return ""
}

// setEnvVar returns a new slice with key set to value, deduplicating any repeated entries for key.
func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !replaced {
				out = append(out, prefix+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

// simpleGatewayHost extracts a validated single-label hostname (no dots, not an IP) from a configured gateway URL,
// used when macOS `ExcludeSimpleHostnames` is enabled so local single-label gateways stay direct.
func simpleGatewayHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, ".:") || !isValidHostname(host) {
		return ""
	}
	return host
}

// parseScutilProxy parses the text output of macOS `/usr/sbin/scutil --proxy`.
// Only top-level HTTP/HTTPS/SOCKS entries and top-level `ExceptionsList` entries are read; nested dictionaries such
// as `__SCOPED__` are ignored so per-interface overrides cannot clobber top-level values. When PAC or WPAD is
// enabled (`ProxyAutoConfigEnable` or `ProxyAutoDiscoveryEnable`), manual proxies are skipped because macOS routes
// through PAC first.
func parseScutilProxy(out string) systemProxy {
	top := map[string]string{}
	var rawExceptions []string

	depth := 0
	inExceptions := false
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.Trim(line, " \t")
		if trimmed == "" {
			continue
		}
		if trimmed == "}" {
			if depth > 0 {
				depth--
			}
			if depth < 2 {
				inExceptions = false
			}
			continue
		}
		if depth == 0 {
			if strings.HasSuffix(trimmed, "{") && !strings.HasSuffix(trimmed, "{}") {
				depth = 1
			}
			continue
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			if strings.HasSuffix(trimmed, "{") && !strings.HasSuffix(trimmed, "{}") {
				depth++
			}
			continue
		}
		key := strings.Trim(k, " \t")
		val := strings.Trim(v, " \t")
		if strings.HasSuffix(val, "{") && !strings.HasSuffix(val, "{}") {
			depth++
			if depth == 2 && key == "ExceptionsList" && strings.HasPrefix(val, "<array>") {
				inExceptions = true
			}
			continue
		}
		if depth == 1 {
			if !strings.HasSuffix(val, "{}") {
				top[key] = val
			}
			continue
		}
		if depth == 2 && inExceptions && isASCIIUint(key) && val != "" {
			rawExceptions = append(rawExceptions, val)
		}
	}

	if top["ProxyAutoConfigEnable"] == "1" || top["ProxyAutoDiscoveryEnable"] == "1" {
		return systemProxy{}
	}

	var p systemProxy
	if top["HTTPEnable"] == "1" {
		p.HTTP = formatProxyURL("http", top["HTTPProxy"], top["HTTPPort"])
	}
	if top["HTTPSEnable"] == "1" {
		// macOS "Secure Web Proxy (HTTPS)" uses HTTP CONNECT on the configured proxy host:port.
		p.HTTPS = formatProxyURL("http", top["HTTPSProxy"], top["HTTPSPort"])
	}
	if top["SOCKSEnable"] == "1" {
		p.SOCKS = formatProxyURL("socks5h", top["SOCKSProxy"], top["SOCKSPort"])
	}
	p.ExcludeSimpleHostnames = top["ExcludeSimpleHostnames"] == "1"
	if len(rawExceptions) > 0 {
		seen := make(map[string]bool, len(rawExceptions))
		for _, item := range rawExceptions {
			norm := normalizeNoProxyException(item)
			if norm == "" {
				continue
			}
			low := strings.ToLower(norm)
			if !seen[low] {
				seen[low] = true
				p.Exceptions = append(p.Exceptions, norm)
			}
		}
	}
	return p
}

// formatProxyURL validates host and port and builds `<scheme>://<host>:<port>` (bracketing IPv6 literals).
// Returns "" on any invalid host, invalid port, or control/URL-breaking character.
func formatProxyURL(scheme, rawHost, rawPort string) string {
	host := strings.Trim(rawHost, " \t")
	portStr, ok := parseValidPort(strings.Trim(rawPort, " \t"))
	if !ok || host == "" || hasUnsafeASCII(host) {
		return ""
	}
	bracketed := strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	if bracketed {
		host = host[1 : len(host)-1]
		if host == "" {
			return ""
		}
	}
	var formattedHost string
	if addr, err := netip.ParseAddr(host); err == nil {
		if bracketed && !addr.Is6() {
			return ""
		}
		if addr.Is6() && addr.Zone() != "" {
			if !isValidIPv6Zone(addr.Zone()) {
				return ""
			}
			formattedHost = addr.WithZone("").String() + "%25" + addr.Zone()
		} else {
			formattedHost = addr.String()
		}
	} else {
		if bracketed || !isValidHostname(host) {
			return ""
		}
		formattedHost = host
	}
	rawURL := scheme + "://" + net.JoinHostPort(formattedHost, portStr)
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != scheme || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return rawURL
}

// normalizeNoProxyException converts one macOS scutil `ExceptionsList` item into a safe `NO_PROXY` token, or "" to
// drop macOS-specific tokens (`<local>`), bare exact domains (which Go's `NO_PROXY` would silently widen to match all
// subdomains), and malformed entries. Only wildcard suffixes (`*.domain` / `.domain`), `*`, IP literals/CIDRs, and
// `localhost` are exported.
func normalizeNoProxyException(raw string) string {
	s := strings.Trim(raw, " \t")
	if s == "" || strings.EqualFold(s, "<local>") {
		return ""
	}
	if s == "*" {
		return "*"
	}
	if hasUnsafeASCII(s) || strings.ContainsAny(s, ",;'\"\\<>@?=#") {
		return ""
	}
	if strings.Contains(s, "/") {
		if _, err := netip.ParsePrefix(s); err == nil {
			return s
		}
		if expanded, ok := expandShorthandIPv4CIDR(s); ok {
			return expanded
		}
		return ""
	}
	if strings.HasPrefix(s, "*.") {
		s = s[1:]
	}
	if strings.Contains(s, "*") {
		return ""
	}
	if strings.HasPrefix(s, "[") {
		if strings.HasSuffix(s, "]") {
			inner := s[1 : len(s)-1]
			if addr, err := netip.ParseAddr(inner); err == nil && addr.Is6() && addr.Zone() == "" {
				return addr.String()
			}
			return ""
		}
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return ""
		}
		portStr, ok := parseValidPort(port)
		if !ok {
			return ""
		}
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() && addr.Zone() == "" {
			return net.JoinHostPort(addr.String(), portStr)
		}
		return ""
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		if addr.Zone() != "" {
			return ""
		}
		return addr.String()
	}
	if strings.Contains(s, ":") {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return ""
		}
		portStr, ok := parseValidPort(port)
		if !ok {
			return ""
		}
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is4() {
			return net.JoinHostPort(addr.String(), portStr)
		}
		if isValidNoProxySuffixOrLocalhost(host) {
			return host + ":" + portStr
		}
		return ""
	}
	if isValidNoProxySuffixOrLocalhost(s) {
		return s
	}
	return ""
}

// expandShorthandIPv4CIDR converts macOS shorthand IPv4 CIDRs such as `192.0.2/24` into `192.0.2.0/24`.
func expandShorthandIPv4CIDR(raw string) (string, bool) {
	host, maskStr, ok := strings.Cut(raw, "/")
	if !ok || host == "" || !isASCIIUint(maskStr) {
		return "", false
	}
	mask, err := strconv.Atoi(maskStr)
	if err != nil || mask < 0 || mask > 32 {
		return "", false
	}
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return "", false
	}
	octets := make([]string, 0, 4)
	for _, part := range parts {
		if !isASCIIUint(part) || (len(part) > 1 && part[0] == '0') {
			return "", false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return "", false
		}
		octets = append(octets, strconv.Itoa(n))
	}
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	candidate := strings.Join(octets, ".") + "/" + strconv.Itoa(mask)
	if _, err := netip.ParsePrefix(candidate); err != nil {
		return "", false
	}
	return candidate, true
}

// isValidNoProxySuffixOrLocalhost accepts only explicit wildcard domain suffixes (`.domain`, converted from `*.domain`)
// and `localhost`; bare domains like `example.org` are rejected so Go's `NO_PROXY` does not widen exact macOS host
// exemptions into wildcard subdomain exemptions.
func isValidNoProxySuffixOrLocalhost(s string) bool {
	if strings.EqualFold(s, "localhost") {
		return true
	}
	if !strings.HasPrefix(s, ".") || s == "." {
		return false
	}
	return isValidHostname(s[1:])
}

func isValidHostname(host string) bool {
	h := strings.TrimSuffix(host, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if !isASCIIAlnum(label[0]) || !isASCIIAlnum(label[len(label)-1]) {
			return false
		}
		for i := 0; i < len(label); i++ {
			b := label[i]
			if !isASCIIAlnum(b) && b != '-' {
				return false
			}
		}
	}
	// Reject numeric-only TLDs / bogus dotted-quad IPs (valid IPv4 literals are handled via netip.ParseAddr).
	return !isASCIIUint(labels[len(labels)-1])
}

func isValidIPv6Zone(zone string) bool {
	if zone == "" {
		return false
	}
	for i := 0; i < len(zone); i++ {
		b := zone[i]
		if !isASCIIAlnum(b) && b != '.' && b != '-' && b != '_' {
			return false
		}
	}
	return true
}

func parseValidPort(s string) (string, bool) {
	if !isASCIIUint(s) || (len(s) > 1 && s[0] == '0') {
		return "", false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return strconv.Itoa(n), true
}

func isASCIIUint(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// hasUnsafeASCII reports whether s contains any control character, whitespace, or non-ASCII byte.
func hasUnsafeASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b <= 0x20 || b >= 0x7f {
			return true
		}
	}
	return false
}
