package link

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
)

// race tries the candidates best first, starting the next one stagger after the previous, and keeps the first that
// works; drop closes any that succeed after it.
func race[T any](ctx context.Context, eps []Endpoint, try func(context.Context, Endpoint) (T, error), drop func(T)) (T, Endpoint, error) {
	eps = ranked(eps)
	var zero T
	if len(eps) == 0 {
		return zero, Endpoint{}, newTextError("link-errors.no_candidates", nil)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		v   T
		ep  Endpoint
		err error
	}
	results := make(chan result, len(eps))
	for i, ep := range eps {
		go func() {
			select {
			case <-time.After(time.Duration(i) * stagger):
			case <-ctx.Done():
				results <- result{ep: ep, err: ctx.Err()}
				return
			}
			actx, acancel := context.WithTimeout(ctx, dialTimeout)
			defer acancel()
			v, err := try(actx, ep)
			results <- result{v, ep, err}
		}()
	}
	var errs []error
	for left := len(eps); left > 0; left-- {
		r := <-results
		if r.err == nil {
			go func(n int) {
				for ; n > 0; n-- {
					if late := <-results; late.err == nil {
						drop(late.v)
					}
				}
			}(left - 1)
			return r.v, r.ep, nil
		}
		if !errors.Is(r.err, context.Canceled) {
			errs = append(errs, &attemptError{r.ep, r.err})
		}
	}
	if len(errs) == 0 { // every attempt was called off: ctx ended (errors.Join of nothing would be no error)
		return zero, Endpoint{}, cmp.Or(ctx.Err(), context.Canceled)
	}
	return zero, Endpoint{}, errors.Join(errs...)
}

// attemptError is how one candidate failed in a race.
type attemptError struct {
	ep  Endpoint
	err error
}

func (e *attemptError) Error() string { return e.ep.URL + ": " + e.err.Error() }
func (e *attemptError) Unwrap() error { return e.err }
func (e *attemptError) Text(l i18n.Lang) string {
	return e.ep.URL + ": " + ErrorText(l, e.err)
}

// ErrorText renders err for a terminal without changing the canonical Error text kept for logs, saved statuses and
// older peers. Unknown errors stay exactly as they are; joined errors retain errors.Join's line order and separator.
func ErrorText(l i18n.Lang, err error) string {
	if err == nil {
		return ""
	}
	if text, ok := err.(interface{ Text(i18n.Lang) string }); ok {
		return text.Text(l)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := []string{}
		for _, part := range joined.Unwrap() {
			parts = append(parts, ErrorText(l, part))
		}
		return strings.Join(parts, "\n")
	}
	if l == i18n.Default {
		return err.Error()
	}
	return StatusErrorText(l, err.Error())
}

// kindText is an endpoint kind in the language of the report being rendered.
func kindText(l i18n.Lang, kind string) string {
	switch kind {
	case "lan":
		return i18n.T(l, "link-errors.kind_lan")
	case "ipv6":
		return i18n.T(l, "link-errors.kind_ipv6")
	case "name":
		return i18n.T(l, "link-errors.kind_name")
	case "ipv4":
		return i18n.T(l, "link-errors.kind_ipv4")
	case KindSSH:
		return "SSH"
	case KindProxy:
		return i18n.T(l, "link-errors.kind_proxy")
	default:
		return i18n.T(l, "link-errors.kind_address")
	}
}

// textKinds are the kinds kindText names ("other" for the rest).
var textKinds = []string{"lan", "ipv6", "name", "ipv4", KindSSH, KindProxy, "other"}

// summary says in one line why the link failed, in the canonical language saved for other processes.
func summary(err error) string { return summaryText(i18n.Default, err) }

func summaryText(l i18n.Lang, err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, e := range joined.Unwrap() {
			parts = append(parts, summaryText(l, e))
		}
		return strings.Join(parts, i18n.T(l, "link-errors.error_separator"))
	}
	var ae *attemptError
	if errors.As(err, &ae) {
		host := ae.ep.URL
		if u, err := url.Parse(ae.ep.URL); err == nil {
			host = u.Host + u.Path // a reverse proxy's path says which hub
		}
		return i18n.T(l, "link-errors.attempt", i18n.Args{
			"kind": kindText(l, ae.ep.Kind), "host": host, "error": describeText(l, ae.err),
		})
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusGoingAway:
		return i18n.T(l, "link-errors.hub_restarted")
	case CloseRefused:
		return i18n.T(l, "link-errors.hello_refused")
	}
	return ErrorText(l, err)
}

func describe(err error) string { return describeText(i18n.Default, err) }

func describeText(l i18n.Lang, err error) string {
	var he *httpError
	var ne net.Error
	var pe *proxyError
	switch {
	case errors.As(err, &pe): // before the network's words: the proxy, not the hub, failed
		return pe.Text(l)
	case errors.As(err, &he):
		return i18n.T(l, "link-errors.http_response", i18n.Args{"error": he.Error()})
	case errors.Is(err, errPin):
		return ErrorText(l, errPin)
	case errors.As(err, new(x509.HostnameError)):
		return i18n.T(l, "link-errors.hostname_mismatch")
	case errors.As(err, new(x509.UnknownAuthorityError)), errors.As(err, new(*tls.CertificateVerificationError)):
		return i18n.T(l, "link-errors.certificate_untrusted")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return i18n.T(l, "link-errors.timeout")
	case errors.Is(err, syscall.ECONNREFUSED):
		return i18n.T(l, "link-errors.connection_refused")
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EADDRNOTAVAIL):
		return i18n.T(l, "link-errors.unreachable")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return i18n.T(l, "link-errors.dns_failed")
	}
	return ErrorText(l, err)
}

// statusTemplateKeys are the only canonical strings StatusErrorText may recognize. The restriction matters: text
// received from a hub, an OS or a user is data, not something to reverse-translate by resemblance.
var statusTemplateKeys = []string{
	"link-errors.attempt",
	"link-errors.node_down_self",
	"link-errors.node_down_marker",
	"link-errors.node_how_windows",
	"link-errors.node_how_darwin",
	"link-errors.node_how_linux",
	"link-errors.node_how_other",
	"link-errors.pin_mismatch",
	"link-errors.proxy_invalid",
	"link-errors.proxy_unreachable",
	"link-errors.proxy_tls_failed",
	"link-errors.proxy_tls_handshake",
	"link-errors.proxy_connect_failed",
	"link-errors.proxy_rejected",
	"link-errors.proxy_only_none",
	"link-errors.manual_reconnect",
	"link-errors.repaired",
	"link-errors.direct_ready",
	"link-errors.credential_refused",
	"link-errors.replaced",
	"link-errors.credential_revoked",
	"link-errors.heartbeat_timeout",
	"link-errors.heartbeat_after_network_change",
	"link-errors.network_address_gone",
	"link-errors.stream_open_failed",
	"link-errors.no_candidates",
	"link-errors.hub_restarted",
	"link-errors.hello_refused",
	"link-errors.http_response",
	"link-errors.hostname_mismatch",
	"link-errors.certificate_untrusted",
	"link-errors.timeout",
	"link-errors.connection_refused",
	"link-errors.unreachable",
	"link-errors.dns_failed",
	"link-errors.pair_link_invalid",
	"link-errors.pair_token_missing",
	"link-errors.pair_pin_without_tls",
	"link-errors.pair_tls_required",
	"link-errors.pair_no_addresses",
	"link-errors.pair_machine_mismatch",
	"link-errors.pair_machine_required",
	"link-errors.hub_rejected",
	"link-errors.hub_no_credential",
	"link-errors.pair_failed",
	"link-errors.not_paired",
	"link-errors.address_port_missing",
	"link-errors.address_https_required",
	"link-errors.address_port_invalid",
	"link-errors.running",
	"link-errors.listen_invalid",
	"link-errors.listen_port_invalid",
	"link-errors.listen_host_invalid",
	"link-errors.address_port_required",
	"link-errors.address_invalid",
	"link-errors.address_zone",
	"link-errors.no_local_address",
	"link-errors.local_addresses_failed",
	"link-errors.tls_identity",
	"link-errors.connector_link_invalid",
	"link-errors.connector_link_incomplete",
	"link-errors.mode_invalid",
	"link-errors.listen_permission",
	"link-errors.listen_in_use",
	"link-errors.listen_address_missing",
	"link-errors.config_incomplete",
	"link-cli.systemd_unavailable_detail",
	"link-cli.systemd_not_linux",
	"link-cli.systemctl_missing",
	"link-cli.systemd_not_booted",
	"link-tray.ssh_error_no_ssh",
	"link-tray.ssh_error_auth_refused",
	"link-tray.ssh_error_host_key",
	"link-tray.ssh_error_forward_refused",
	"link-tray.ssh_error_target_unreachable",
	"link-tray.ssh_error_ssh_unreachable",
	"link-tray.ssh_error_invalid",
}

type statusTemplate struct {
	key     string
	pattern *regexp.Regexp
	names   []string
	fixed   int
}

var statusTemplates = sync.OnceValue(func() []statusTemplate {
	kinds := []string{}
	for _, kind := range textKinds {
		kinds = append(kinds, regexp.QuoteMeta(kindText(i18n.Default, kind)))
	}
	out := []statusTemplate{}
	for _, key := range statusTemplateKeys {
		text := i18n.T(i18n.Default, key)
		var pattern strings.Builder
		pattern.WriteString("(?s)^")
		names := []string{}
		at, fixed := 0, 0
		for {
			open := strings.IndexByte(text[at:], '{')
			if open < 0 {
				break
			}
			open += at
			close := strings.IndexByte(text[open:], '}')
			if close < 0 {
				break
			}
			close += open
			literal := text[at:open]
			pattern.WriteString(regexp.QuoteMeta(literal))
			fixed += len(literal)
			name := text[open+1 : close]
			names = append(names, name)
			if name == "kind" {
				pattern.WriteString("(" + strings.Join(kinds, "|") + ")")
			} else {
				pattern.WriteString("(.+?)")
			}
			at = close + 1
		}
		pattern.WriteString(regexp.QuoteMeta(text[at:]))
		fixed += len(text[at:])
		pattern.WriteString("$")
		out = append(out, statusTemplate{key: key, pattern: regexp.MustCompile(pattern.String()), names: names, fixed: fixed})
	}
	// A specific wrapper must be tried before a shorter, more open-ended one.
	slices.SortStableFunc(out, func(a, b statusTemplate) int { return b.fixed - a.fixed })
	return out
})

// StatusErrorText translates a recognized canonical status or saved error into l. It accepts old saved statuses too;
// unknown text (including text localized by a peer) passes through byte for byte.
func StatusErrorText(l i18n.Lang, text string) string { return statusErrorText(l, text, 0) }

func statusErrorText(l i18n.Lang, text string, depth int) string {
	if text == "" || l == i18n.Default || depth >= 8 {
		return text
	}
	if localized, ok := nodeMissingErrorText(l, text); ok {
		return localized
	}
	if localized, ok := listenErrorText(l, text); ok {
		return localized
	}
	for _, form := range statusTemplates() {
		match := form.pattern.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		if form.key == "link-errors.attempt" {
			host := ""
			for i, name := range form.names {
				if name == "host" {
					host = match[i+1]
				}
			}
			hostOnly, _, _ := strings.Cut(host, "/")
			if _, _, err := net.SplitHostPort(hostOnly); err != nil {
				continue
			}
		}
		args := i18n.Args{}
		for i, name := range form.names {
			value := match[i+1]
			switch name {
			case "error", "how":
				args[name] = statusErrorText(l, value, depth+1)
			case "kind":
				args[name] = localizedKindName(l, value)
			case "self":
				if value == NodeDownSelf {
					args[name] = i18n.T(l, "link-errors.node_down_self")
				} else {
					args[name] = value
				}
			case "marker":
				if value == NodeDownMarker {
					args[name] = i18n.T(l, "link-errors.node_down_marker")
				} else {
					args[name] = value
				}
			default:
				args[name] = value
			}
		}
		return i18n.T(l, form.key, args)
	}
	return text
}

// nodeMissingErrorText parses the exact canonical node-down shape. Its self and marker placeholders are adjacent,
// so a generic non-greedy template cannot separate them without risking unknown peer text.
func nodeMissingErrorText(l i18n.Lang, text string) (string, bool) {
	rest, found := strings.CutPrefix(text, NodeDownSelf+NodeDownMarker+"（") // i18n-ignore: parses the canonical node-down prefix
	if !found {
		return "", false
	}
	boundary := strings.LastIndex(rest, " 连不上）：") // i18n-ignore: parses the canonical node-down target suffix
	if boundary < 0 {
		return "", false
	}
	target, how := rest[:boundary], rest[boundary+len(" 连不上）："):] // i18n-ignore: parses the canonical node-down target suffix
	for _, key := range []string{"link-errors.node_how_windows", "link-errors.node_how_darwin", "link-errors.node_how_linux", "link-errors.node_how_other"} {
		if how == i18n.T(i18n.Default, key) {
			return i18n.T(l, "link-errors.node_missing", i18n.Args{
				"self": i18n.T(l, "link-errors.node_down_self"), "marker": i18n.T(l, "link-errors.node_down_marker"),
				"target": target, "how": i18n.T(l, key),
			}), true
		}
	}
	return "", false
}

// listenErrorText recognizes only a valid listen address followed by one of describeListen's three canonical reasons.
// An arbitrary peer string containing a full-width colon does not match it.
func listenErrorText(l i18n.Lang, text string) (string, bool) {
	for _, key := range []string{"link-errors.listen_permission", "link-errors.listen_in_use", "link-errors.listen_address_missing"} {
		reason := i18n.T(i18n.Default, key)
		listen, found := strings.CutSuffix(text, "："+reason) // i18n-ignore: parses the canonical listen-error separator
		if !found {
			continue
		}
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return "", false
		}
		return i18n.T(l, "link-errors.listen_failed", i18n.Args{"listen": listen, "error": i18n.T(l, key)}), true
	}
	return "", false
}

func localizedKindName(l i18n.Lang, canonical string) string {
	for _, kind := range textKinds {
		if canonical == kindText(i18n.Default, kind) {
			return kindText(l, kind)
		}
	}
	return canonical
}
